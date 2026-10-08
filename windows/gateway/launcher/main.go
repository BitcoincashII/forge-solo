// Forge Gateway's Windows tray app: it runs forge-gateway.exe from its install folder with the
// config in %APPDATA%\ForgeGateway, keeps it running, and gives its status page and Settings
// password a place in the taskbar's notification area.
package main

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"fyne.io/systray"
)

// BCH2 logo, shown as the system-tray icon (Windows accepts .ico bytes). The same .ico is compiled
// into the exe's resources (rsrc.syso) for the taskbar and shortcut icon, and set as the installer
// icon.
//
//go:embed forge-gateway.ico
var trayIcon []byte

// version is set at build time: -ldflags "-X main.version=1.1.0".
var version = "dev"

const (
	gatewayExe = "forge-gateway.exe"  // the gateway, in the install folder
	configName = "forge-gateway.json" // its config, in the data folder
	gatewayKey = "gateway"            // the gateway's key in procs
)

var (
	installDir string
	dataDir    string
	mu         sync.Mutex
	procs      = map[string]*exec.Cmd{}
	exited     = map[string]chan struct{}{}  // closed when the process under the same key has exited
	stdins     = map[string]io.WriteCloser{} // write ends of the stdin pipes a clean stop closes
	stopping   bool                          // set under mu when the stop begins; nothing starts after it
	sec        secrets
)

// errStopping refuses a start once the stop has begun: a program started after the stop had passed
// it would be left running when Forge Gateway exits.
//
//lint:ignore ST1005 it starts with the product's name
var errStopping = errors.New("Forge Gateway is stopping")

// errStarted refuses a start of a program already running under the same key: two starts at once
// (a start tried again, and Restart Forge Gateway) would leave two, one of them no longer tracked.
var errStarted = errors.New("already running")

// secrets is what secrets.env holds: the password the status page's Settings asks for.
type secrets struct {
	Settings string
}

// genHex is n random bytes, in hex.
func genHex(n int) string { b := make([]byte, n); _, _ = rand.Read(b); return hex.EncodeToString(b) }

func md(p string)              { _ = os.MkdirAll(p, 0o755) }
func dpath(e ...string) string { return filepath.Join(append([]string{dataDir}, e...)...) }
func ipath(e ...string) string { return filepath.Join(append([]string{installDir}, e...)...) }

// hidden runs a program from the install folder with no console window.
func hidden(name string, args ...string) *exec.Cmd {
	c := exec.Command(ipath(name), args...)
	c.Dir = installDir
	c.SysProcAttr = noWindow(0)
	return c
}

// hiddenSystem runs a command from PATH rather than from the install directory.
func hiddenSystem(name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	c.SysProcAttr = noWindow(0)
	return c
}

// restrictDataDir locks the data directory to this user.
//
// secrets.env holds the Settings password and forge-gateway.json the node's RPC login, and Go's
// permission bits do not map to an ACL on Windows: protection would otherwise be whatever %APPDATA%
// happens to carry. Make the intent explicit instead of inheriting it: break inheritance and grant
// this user alone.
//
// Best effort. If icacls is unavailable or refuses, the app still runs: the folder is then no worse
// protected than it was before.
func restrictDataDir(dir string) {
	user := os.Getenv("USERNAME")
	if user == "" {
		return
	}
	_ = hiddenSystem("icacls", dir, "/inheritance:r", "/grant:r", user+":(OI)(CI)F").Run()
}

// runPiped starts c and tracks it under key, with stdin, if any, the write end of its stdin pipe,
// which a clean stop closes. It refuses once the stop has begun, or while key runs. The check, the
// start and the tracking are one step under mu, so the stop finds every program that started.
func runPiped(key string, c *exec.Cmd, stdin io.WriteCloser) error {
	_, err := startTracked(key, c, stdin)
	return err
}

// startTracked is runPiped, returning the channel closed once c has exited; c.ProcessState is then
// set.
func startTracked(key string, c *exec.Cmd, stdin io.WriteCloser) (chan struct{}, error) {
	mu.Lock()
	defer mu.Unlock()
	if stopping {
		closeIfAny(stdin)
		return nil, errStopping
	}
	if procs[key] != nil {
		closeIfAny(stdin)
		return nil, errStarted
	}
	if err := c.Start(); err != nil {
		closeIfAny(stdin)
		return nil, err
	}
	clearTrouble(key) // it runs: what the tray said about its start no longer holds
	// One wait per process, for everything that waits on it.
	done, since := make(chan struct{}), time.Now()
	go func() {
		st, _ := c.Process.Wait()
		c.ProcessState = st
		close(done)
		exitedOnItsOwn(key, c, st, since)
	}()
	procs[key], exited[key] = c, done
	if stdin != nil {
		stdins[key] = stdin
	}
	return done, nil
}

func closeIfAny(w io.WriteCloser) {
	if w != nil {
		_ = w.Close()
	}
}

// isStopping reports whether the stop has begun.
func isStopping() bool {
	mu.Lock()
	defer mu.Unlock()
	return stopping
}

// untrack takes the process under key out of what the tray tracks, so that its exit counts as the
// tray's doing and it is not started again, and returns it with its exit channel.
func untrack(key string) (*exec.Cmd, chan struct{}) {
	mu.Lock()
	defer mu.Unlock()
	c, done := procs[key], exited[key]
	delete(procs, key)
	delete(exited, key)
	return c, done
}

// kill ends c at once and waits a little for it to be gone: Windows ends a process only once its
// pending I/O is done, and one started in its place could otherwise still find its port taken.
func kill(c *exec.Cmd, done chan struct{}) {
	if c != nil && c.Process != nil {
		_ = c.Process.Kill()
	}
	waitDone(done, 10*time.Second)
}

// waitDone waits up to d for the exit channel done (none: nothing to wait for), and reports whether
// the process has exited.
func waitDone(done chan struct{}, d time.Duration) bool {
	if done == nil {
		return true
	}
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// tipMu and stopShown keep the stop's tray tooltip from being replaced by a start's.
var (
	tipMu     sync.Mutex
	stopShown bool
)

// setTooltip sets the tray tooltip (a stand-in in the tests). It waits on the taskbar, which can be
// slow to answer.
var setTooltip = systray.SetTooltip

// status shows s, one of the tray's texts (tips.go), as the tray tooltip, unless the stop has begun:
// its own tooltip stays. A program's own reason for not starting can make s long: it is cut short.
func status(s string) {
	tipMu.Lock()
	defer tipMu.Unlock()
	if !stopShown {
		setTooltip(trimTip(s))
	}
}

// showStopping shows the stop's tooltip, which no later status replaces.
func showStopping() {
	tipMu.Lock()
	defer tipMu.Unlock()
	stopShown = true
	setTooltip(tipStopping)
}

// openBrowser opens a page in the default browser (a stand-in in the tests).
var openBrowser = func(u string) { _ = exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start() }

// statusURL is the gateway's status page, at the address its config gives (readConfigPorts).
func statusURL() string { return "http://" + net.JoinHostPort(statusHost, statusPort) + "/" }

// settingsURL is the status page at its Settings.
func settingsURL() string { return statusURL() + "#settings" }

// pageToOpen is the page Open Status Page opens: Settings while the gateway last said it is not set
// up, the status page otherwise.
func pageToOpen() string {
	if a, known := lastAnswer(); known && !a.configured {
		return settingsURL()
	}
	return statusURL()
}

// alreadyRuns is alreadyRunning (a stand-in in the tests).
var alreadyRuns = alreadyRunning

// signInArg is what the sign-in start (the installer's Run value) passes: Windows started this
// copy on its own, and no one asked for a page.
const signInArg = "--at-sign-in"

// launchedAtSignIn reports whether the arguments are the sign-in start's.
func launchedAtSignIn(args []string) bool {
	for _, a := range args {
		if a == signInArg {
			return true
		}
	}
	return false
}

// secondLaunch reports whether Forge Gateway already runs on this PC, and if so opens its status
// page: a second launch (the shortcut, the Start menu) opens the page of the one running instead of
// starting another, which would only find the ports taken. The sign-in start (args) opens nothing:
// Windows runs it a while after sign-in, and right after an install that is after Setup's own
// launch, which has opened the page already. It writes nothing: this copy holds no mutex, and
// launcher.log is the running copy's.
func secondLaunch(args []string) bool {
	if !alreadyRuns() {
		return false
	}
	if launchedAtSignIn(args) {
		return true
	}
	_ = configPorts()
	openBrowser(statusURL())
	return true
}

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "-version" || os.Args[1] == "--version") {
		fmt.Println("forge-gateway-tray", version)
		return
	}
	dataDir = filepath.Join(os.Getenv("APPDATA"), "ForgeGateway")
	if secondLaunch(os.Args[1:]) {
		return
	}
	exe, _ := os.Executable()
	installDir = filepath.Dir(exe)
	md(dataDir)
	restrictDataDir(dataDir)
	rotateLog(dpath("launcher.log"), launcherLogLimit)
	// Read before the tray starts, so its menu never sees them half loaded.
	if prepErr = prepare(); prepErr != nil {
		logf("Forge Gateway cannot start: %v; %s", prepErr, tryAgainAdvice)
	}
	go watchTray(exe)
	// No exit callback: the tray calls it as it removes its icon, and the stop is shutdown's.
	systray.Run(onReady, nil)
	// The tray's loop ended without Quit: its window was closed (WM_CLOSE), by Windows or by the
	// installer or the uninstaller. Stop everything as Quit does, and exit.
	shutdown()
}

// prepErr is why Forge Gateway could not get ready to start; Try Again prepares again. It is read
// and written before the tray starts, and then only on the tray menu's loop.
var prepErr error

// prepare reads secrets.env, making the Settings password if there is none, writes a config to set
// up in Settings if there is none, and reads the ports the gateway listens on from the config.
func prepare() error {
	sec = secrets{}
	if err := setupSecrets(); err != nil {
		return err
	}
	if err := ensureConfig(); err != nil {
		return err
	}
	readConfigPorts()
	logf("Forge Gateway %s starting: miners on port %s, status page %s", version, stratumPort, statusURL())
	return nil
}

func onReady() {
	close(trayReady)
	systray.SetIcon(trayIcon)
	systray.SetTitle("Forge Gateway")
	systray.SetTooltip(tipStarting)
	// Shown only after a start that failed.
	mRetry := systray.AddMenuItem("Try Again", "Start Forge Gateway again")
	mRetry.Hide()
	showTryAgain = func(show bool) {
		if show {
			mRetry.Show()
		} else {
			mRetry.Hide()
		}
	}
	mOpen := systray.AddMenuItem("Open Status Page", "")
	mCopyPw := systray.AddMenuItem(copyPwTitle, "Copy the password the status page's Settings asks for")
	mRestart := systray.AddMenuItem("Restart Forge Gateway", "Stop the gateway cleanly and start it again")
	mData := systray.AddMenuItem("Open Data Folder", "")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit Forge Gateway", "")
	watchSessionEnd()
	if prepErr != nil {
		startFailed(tipCannotStart(startWhy(prepErr)))
	} else {
		startBoot()
	}
	go func() {
		for {
			select {
			case <-mRetry.ClickedCh:
				tryAgain()
			case <-mOpen.ClickedCh:
				openBrowser(pageToOpen())
			case <-mCopyPw.ClickedCh:
				copySettingsPassword(mCopyPw)
			case <-mRestart.ClickedCh:
				// Not on this loop: a click while it runs (up to half a minute) would be lost, Quit's
				// among them.
				go restartGateway()
			case <-mData.ClickedCh:
				_ = exec.Command("explorer", dataDir).Start()
			case <-mQuit.ClickedCh:
				go shutdown()
			}
		}
	}()
}

// trayReady is closed once the tray icon is up and its menu made.
var trayReady = make(chan struct{})

// trayWait is how long the tray icon may take to come up (shorter in the tests).
var trayWait = 90 * time.Second

// relaunch and exit stand in for the real ones in the tests.
var (
	relaunch = func(exe string, env ...string) error {
		c := exec.Command(exe)
		c.Env = append(os.Environ(), env...)
		return c.Start()
	}
	exit = os.Exit
)

// watchTray starts Forge Gateway again, once, if its tray icon never comes up. Windows can refuse
// the icon while it is still setting up the taskbar at sign-in, and the tray then never calls
// onReady: nothing starts, and this copy, holding the single-instance mutex, would turn every later
// launch into a status page that does not answer.
func watchTray(exe string) {
	select {
	case <-trayReady:
		return
	case <-time.After(trayWait):
	}
	if os.Getenv("FORGE_GATEWAY_RELAUNCHED") != "" {
		logf("the tray icon did not come up after a restart either: exiting")
		exit(1)
		return
	}
	logf("the tray icon did not come up in %v: starting Forge Gateway again", trayWait)
	releaseRunning()
	if err := relaunch(exe, "FORGE_GATEWAY_RELAUNCHED=1"); err != nil {
		logf("could not start Forge Gateway again: %v", err)
	}
	exit(0)
}

const copyPwTitle = "Copy Settings Password"

// copySettingsPassword puts the Settings password on the clipboard and says so on the menu item
// for a few seconds.
func copySettingsPassword(item *systray.MenuItem) {
	if sec.Settings != "" && copyText(sec.Settings) == nil {
		item.SetTitle("Settings Password Copied")
	} else {
		item.SetTitle("Could Not Copy the Password")
	}
	time.AfterFunc(4*time.Second, func() { item.SetTitle(copyPwTitle) })
}

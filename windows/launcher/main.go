// Forge Solo's Windows launcher: a tray app, with no console. It starts the BCH2 + 1175 nodes and
// the stratum + api services, which keep their data in forgesolo.db (moving an earlier version's
// PostgreSQL data into it once, migrate.go), serves the dashboard on 127.0.0.1, and opens the
// browser. All data + secrets live under %APPDATA%\ForgeSolo. Only the miner ports (3333, and 3335
// for rented hashpower) and the two nodes' P2P ports (8339, 25360) listen beyond this machine;
// everything else is on 127.0.0.1.
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
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/systray"
)

// BCH2 logo, shown as the system-tray icon (Windows accepts .ico bytes).
// The same .ico is compiled into the exe's resources (rsrc.syso) for the
// taskbar/shortcut icon, and set as the installer icon.
//
//go:embed forge-solo.ico
var trayIcon []byte

// Fixed, user-facing / internet-facing ports (matched by the installer's firewall rules).
const (
	minerPort  = "3333" // documented miner endpoint, fixed so users always point miners here
	rentalPort = "3335" // NiceHash / MiningRigRentals: one connection per order, high difficulty floor
	bch2P2P    = "8339" // BCH2 P2P (incoming peers), fixed so the installer firewall rule matches
)

// aux1175P2P is the 1175 node's P2P port (incoming peers), likewise fixed (a variable only so the
// tests can use a free one).
var aux1175P2P = "25360"

// webPort is the dashboard's port, fixed so its address stays the same across launches (a
// variable only so the tests can use a free one).
var webPort = "3080"

// version is set at build time: -ldflags "-X main.version=1.0.13". The status file records it.
var version = "dev"

// Loopback-only service ports. Chosen dynamically at startup (pickPort) so they can NEVER
// collide with other software or Windows reserved/excluded ranges: the root cause of the
// api-on-8080 (Apache/XAMPP) and 1175-RPC-on-25361 (WSAEACCES 10013) bind failures. Assigned
// in main() before anything binds; every conf/env/proxy reads these vars, and writeAlways
// regenerates the configs each launch, so a run is internally consistent. They have no default:
// a fixed one is a port another program may hold. pgPort, the old database's, is picked only for
// a move (from pgPortFrom), so a start that needs none never waits on its window.
var pgPort, bch2RPC, bch2ZMQ, aux1175RPC, stratumInt, apiPort string

// A portSlot is one service's loopback port, picked from the portWindow ports from `from`.
type portSlot struct {
	name string
	port *string
	from int
}

// portPlan gives each loopback port its own window, so two services never pick the same one. The
// old database's window, for a move, is 30000-30299 (pgPortFrom).
var portPlan = []portSlot{
	{"the BCH2 node", &bch2RPC, 30300},
	{"the BCH2 node's block notices", &bch2ZMQ, 30600},
	{"the 1175 node", &aux1175RPC, 30900},
	{"the miner's stats", &stratumInt, 31500},
	{"the dashboard's data", &apiPort, 31800},
}

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
// it would be left running when Forge Solo exits.
//
//lint:ignore ST1005 it starts with the product's name
var errStopping = errors.New("Forge Solo is stopping")

// errStarted refuses a start of a program already running under the same key: two starts at once
// (a start tried again, and Restart Mining) would leave two, one of them no longer tracked.
var errStarted = errors.New("already running")

type secrets struct {
	BCH2Pass, AuxPass, DBPass, Token, Settings string
}

func gen() string { return genHex(24) }

// genHex is n random bytes, in hex.
func genHex(n int) string { b := make([]byte, n); _, _ = rand.Read(b); return hex.EncodeToString(b) }

func md(p string)              { _ = os.MkdirAll(p, 0o755) }
func dpath(e ...string) string { return filepath.Join(append([]string{dataDir}, e...)...) }
func ipath(e ...string) string { return filepath.Join(append([]string{installDir}, e...)...) }

// belowNormal = BELOW_NORMAL_PRIORITY_CLASS. Run the heavy background processes
// (both nodes + Postgres) below normal so IBD can't starve the desktop on a laptop.
// A child process inherits its parent's priority class on Windows.
const belowNormal = 0x00004000

func hiddenPrio(extraFlags uint32, name string, args ...string) *exec.Cmd {
	c := exec.Command(ipath(name), args...)
	c.Dir = installDir
	c.SysProcAttr = noWindow(extraFlags)
	return c
}
func hidden(name string, args ...string) *exec.Cmd { return hiddenPrio(0, name, args...) }

// hiddenSystem runs a command from PATH rather than from the install directory.
func hiddenSystem(name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	c.SysProcAttr = noWindow(0)
	return c
}

// restrictDataDir locks the data directory to this user.
//
// secrets.env holds both node RPC passwords, the database password and the internal API
// token, and it is written with mode 0600 -- which Windows ignores, because Go's permission
// bits do not map to an ACL. Protection therefore comes from whatever %APPDATA% happens to
// carry, and the installer also adds a Defender exclusion for this same folder. Make the
// intent explicit instead of inheriting it: break inheritance and grant this user alone.
//
// Best effort. If icacls is unavailable or refuses, the app still runs -- the folder is then
// no worse protected than it was before.
func restrictDataDir(dir string) {
	user := os.Getenv("USERNAME")
	if user == "" {
		return
	}
	_ = hiddenSystem("icacls", dir, "/inheritance:r", "/grant:r", user+":(OI)(CI)F").Run()
}
func run(key string, c *exec.Cmd) error { return runPiped(key, c, nil) }

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

// runToEnd runs c to its end, unless the stop has begun. While it runs, *running is set, if given.
func runToEnd(c *exec.Cmd, running *atomic.Bool) error {
	mu.Lock()
	if stopping {
		mu.Unlock()
		return errStopping
	}
	err := c.Start()
	if err == nil && running != nil {
		running.Store(true)
	}
	mu.Unlock()
	if err != nil {
		return err
	}
	err = c.Wait()
	if running != nil {
		running.Store(false)
	}
	return err
}

// isStopping reports whether the stop has begun.
func isStopping() bool {
	mu.Lock()
	defer mu.Unlock()
	return stopping
}

// untrack takes the process under key out of what the launcher tracks, so that its exit counts as
// the launcher's doing and it is not started again, and returns it with its exit channel.
func untrack(key string) (*exec.Cmd, chan struct{}) {
	mu.Lock()
	defer mu.Unlock()
	c, done := procs[key], exited[key]
	delete(procs, key)
	delete(exited, key)
	return c, done
}

// stop ends the process under key at once, and waits a little for it to be gone.
func stop(key string) {
	c, done := untrack(key)
	kill(c, done)
}

// kill ends c at once and waits a little for it to be gone: Windows ends a process only once its
// pending I/O is done, and one started in its place could otherwise still find its port or its
// data folder taken.
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

// writeAlways rewrites generated config every boot so upgrades pick up new settings.
// Safe here: these files are machine-generated (the user's payout address lives in the DB).
func writeAlways(path, content string) { _ = os.WriteFile(path, []byte(content), 0o600) }
func waitTCP(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 2*time.Second); err == nil {
			_ = c.Close()
			return true
		}
		time.Sleep(time.Second)
	}
	return false
}

// openBrowser opens a page in the default browser (a stand-in in the tests).
var openBrowser = func(u string) { _ = exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start() }

// portWindow is how many ports pickPort tries, from its start.
const portWindow = 300

// pickPort returns the first free loopback TCP port at/above start. Scanning a fixed low
// range (not :0) keeps the port out of the OS ephemeral pool, so it won't be reused for an
// outbound socket between selection and bind; a reserved or in-use port simply fails to bind
// and we move on to the next. With none free it fails rather than fall back to one in use:
// the services would send the program holding it their passwords.
func pickPort(start int) (string, error) {
	for p := start; p < start+portWindow; p++ {
		if l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p)); err == nil {
			_ = l.Close()
			return strconv.Itoa(p), nil
		}
	}
	return "", fmt.Errorf("no free local port in %d-%d", start, start+portWindow-1)
}

// assignPorts picks every loopback port. If one cannot be had, nothing may start.
func assignPorts() error {
	for _, p := range portPlan {
		port, err := pickPort(p.from)
		if err != nil {
			return fmt.Errorf("no free local port (for %s, in %d-%d)", p.name, p.from, p.from+portWindow-1)
		}
		*p.port = port
	}
	return nil
}

func main() {
	// A second launch (the sign-in start, then the shortcut) opens the dashboard of the one already
	// running instead. Two shared one data folder, and quitting either stopped the database under
	// the other.
	if alreadyRunning() {
		openBrowser("http://127.0.0.1:" + webPort)
		return
	}
	exe, _ := os.Executable()
	installDir = filepath.Dir(exe)
	dataDir = filepath.Join(os.Getenv("APPDATA"), "ForgeSolo")
	md(dataDir)
	restrictDataDir(dataDir)
	rotateLog(dpath("launcher.log"), launcherLogLimit)
	// Read before the tray starts, so its menu never sees them half loaded.
	if prepErr = prepare(); prepErr != nil {
		logf("Forge Solo cannot start: %v; %s", prepErr, tryAgainAdvice)
	}
	go watchTray(exe)
	// No exit callback: the tray calls it as it removes its icon, and the stop is shutdown's.
	systray.Run(onReady, nil)
	// The tray's loop ended without Quit (Windows closed its window): stop everything and exit.
	shutdown()
}

// prepErr is why Forge Solo could not get ready to start; Try Again prepares again. It is read and
// written before the tray starts, and then only on the tray menu's loop.
var prepErr error

// prepare reads secrets.env, making what it lacks, and then picks the loopback ports,
// collision-proof, before any service binds.
func prepare() error {
	sec = secrets{}
	if err := setupSecrets(); err != nil {
		return err
	}
	if err := assignPorts(); err != nil {
		return err
	}
	logf("Forge Solo %s starting: BCH2 node %s (notices %s), 1175 node %s, miner stats %s, dashboard data %s",
		version, bch2RPC, bch2ZMQ, aux1175RPC, stratumInt, apiPort)
	return nil
}

func onReady() {
	close(trayReady)
	systray.SetIcon(trayIcon)
	systray.SetTitle("Forge Solo")
	systray.SetTooltip(tipStarting)
	// Shown only after a start that failed, or a dashboard another program's port kept closed.
	mRetry := systray.AddMenuItem("Try Again", "Start Forge Solo again")
	mRetry.Hide()
	showTryAgain = func(show bool) {
		if show {
			mRetry.Show()
		} else {
			mRetry.Hide()
		}
	}
	mOpen := systray.AddMenuItem("Open Dashboard", "")
	mCopyPw := systray.AddMenuItem(copyPwTitle, "Copy the password the Settings page asks for")
	mRestart := systray.AddMenuItem("Restart Mining", "Restart the miner")
	mData := systray.AddMenuItem("Open Data Folder", "")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit Forge Solo", "")
	watchSessionEnd()
	if prepErr != nil {
		startFailed(tipCannotStart(startWhy(prepErr)))
	} else {
		go boot()
	}
	go func() {
		for {
			select {
			case <-mRetry.ClickedCh:
				tryAgain()
			case <-mOpen.ClickedCh:
				openBrowser("http://127.0.0.1:" + webPort)
			case <-mCopyPw.ClickedCh:
				copySettingsPassword(mCopyPw)
			case <-mRestart.ClickedCh:
				// Not on this loop: a click while it runs (up to half a minute) would be lost, Quit's
				// among them.
				go restartMiner()
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

// watchTray starts Forge Solo again, once, if its tray icon never comes up. Windows can refuse the
// icon while it is still setting up the taskbar at sign-in, and the tray then never calls onReady:
// nothing starts, and this copy, holding the single-instance mutex, would turn every later launch
// into a dashboard that does not answer.
func watchTray(exe string) {
	select {
	case <-trayReady:
		return
	case <-time.After(trayWait):
	}
	if os.Getenv("FORGE_SOLO_RELAUNCHED") != "" {
		logf("the tray icon did not come up after a restart either: exiting")
		exit(1)
		return
	}
	logf("the tray icon did not come up in %v: starting Forge Solo again", trayWait)
	releaseRunning()
	if err := relaunch(exe, "FORGE_SOLO_RELAUNCHED=1"); err != nil {
		logf("could not start Forge Solo again: %v", err)
	}
	exit(0)
}

const copyPwTitle = "Copy Settings Password"

// copySettingsPassword puts the Settings page's password on the clipboard and says so on the menu
// item for a few seconds.
func copySettingsPassword(item *systray.MenuItem) {
	if sec.Settings != "" && copyText(sec.Settings) == nil {
		item.SetTitle("Settings Password Copied")
	} else {
		item.SetTitle("Could Not Copy the Password")
	}
	time.AfterFunc(4*time.Second, func() { item.SetTitle(copyPwTitle) })
}

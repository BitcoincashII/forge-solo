// Forge Solo's Windows launcher: a tray app, with no console. It boots a bundled Postgres,
// the BCH2 + 1175 nodes, and the stratum + api services, serves the dashboard on 127.0.0.1, and
// opens the browser. All data + secrets live under %APPDATA%\ForgeSolo. Only the miner port
// (3333) and the two nodes' P2P ports (8339, 25360) listen beyond this machine; everything else
// is on 127.0.0.1.
package main

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
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
	minerPort  = "3333"  // documented miner endpoint, fixed so users always point miners here
	webPort    = "3080"  // dashboard URL, fixed so it stays stable across launches
	bch2P2P    = "8339"  // BCH2 P2P (incoming peers), fixed so the installer firewall rule matches
	aux1175P2P = "25360" // 1175 P2P (incoming peers), likewise fixed; listen=1 needs a reachable port
)

// Loopback-only service ports. Chosen dynamically at startup (pickPort) so they can NEVER
// collide with other software or Windows reserved/excluded ranges: the root cause of the
// api-on-8080 (Apache/XAMPP) and 1175-RPC-on-25361 (WSAEACCES 10013) bind failures. Assigned
// in main() before anything binds; every conf/env/proxy reads these vars, and writeAlways
// regenerates the configs each launch, so a run is internally consistent. They have no default:
// a fixed one is a port another program may hold.
var pgPort, bch2RPC, bch2ZMQ, aux1175RPC, stratumInt, apiPort string

// A portSlot is one service's loopback port, picked from the portWindow ports from `from`.
type portSlot struct {
	name string
	port *string
	from int
}

// portPlan gives each loopback port its own window, so two services never pick the same one.
var portPlan = []portSlot{
	{"the database", &pgPort, 30000},
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
	stdins     = map[string]io.WriteCloser{} // write ends of the stdin pipes a clean stop closes
	sec        secrets
)

type secrets struct {
	BCH2Pass, AuxPass, DBPass, Token string
}

func gen() string              { b := make([]byte, 24); _, _ = rand.Read(b); return hex.EncodeToString(b) }
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
func run(key string, c *exec.Cmd) error {
	if err := c.Start(); err != nil {
		return err
	}
	mu.Lock()
	procs[key] = c
	mu.Unlock()
	return nil
}
func stop(key string) {
	mu.Lock()
	if c := procs[key]; c != nil && c.Process != nil {
		_ = c.Process.Kill()
		delete(procs, key)
	}
	mu.Unlock()
}
func writeAbsent(path, content string) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		_ = os.WriteFile(path, []byte(content), 0o600)
	}
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
func openBrowser(u string) { _ = exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start() }

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
			return fmt.Errorf("no free local port for %s (%d-%d)", p.name, p.from, p.from+portWindow-1)
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
	rotateLog(dpath("launcher.log"), 1<<20)
	// Assign collision-proof loopback ports before any service binds.
	portErr := assignPorts()
	if portErr != nil {
		logf("Forge Solo cannot start: %v", portErr)
	} else {
		logf("Forge Solo starting: database %s, BCH2 node %s (notices %s), 1175 node %s, miner stats %s, dashboard data %s",
			pgPort, bch2RPC, bch2ZMQ, aux1175RPC, stratumInt, apiPort)
	}
	systray.Run(func() { onReady(portErr) }, func() { shutdown() })
	// Quit runs shutdown on the menu's goroutine while the tray's loop ends, and returning from
	// main would end the process there, leaving the nodes and the database running with nothing
	// to stop them. This waits for that stop, or makes it, and exits when it is done.
	shutdown()
}

func onReady(portErr error) {
	systray.SetIcon(trayIcon)
	systray.SetTitle("Forge Solo")
	systray.SetTooltip("Forge Solo: starting…")
	mOpen := systray.AddMenuItem("Open Dashboard", "")
	mRestart := systray.AddMenuItem("Restart Mining", "Restart the miner after changing your payout address")
	mData := systray.AddMenuItem("Open Data Folder", "")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit Forge Solo", "")
	watchSessionEnd()
	if portErr != nil {
		systray.SetTooltip("Forge Solo cannot start: " + portErr.Error())
	} else {
		go boot()
	}
	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				openBrowser("http://127.0.0.1:" + webPort)
			case <-mRestart.ClickedCh:
				restartMiner()
			case <-mData.ClickedCh:
				_ = exec.Command("explorer", dataDir).Start()
			case <-mQuit.ClickedCh:
				systray.Quit()
			}
		}
	}()
}

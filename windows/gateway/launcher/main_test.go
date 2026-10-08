package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// helperDir holds a copy of this test binary named forge-gateway.exe: the gateway the tray starts
// in the tests. Started with GWL_HELPER set, the binary is that gateway (helperMain), not the tests.
var helperDir string

// TestMain runs the tests with programs that exit on their own left alone: most tests start
// stand-ins that exit at once, which would otherwise be started again under the next test. The
// tests of starting them again turn it on (startSupervising).
func TestMain(m *testing.M) {
	if mode := os.Getenv("GWL_HELPER"); mode != "" {
		os.Exit(helperMain(mode))
	}
	supervising.Store(false)
	dir, err := os.MkdirTemp("", "gwl-helper-")
	if err == nil {
		err = copyHelper(filepath.Join(dir, gatewayExe))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "setup: the helper gateway cannot be made:", err)
		os.Exit(1)
	}
	helperDir = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// copyHelper copies this test binary to path.
func copyHelper(path string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	in, err := os.Open(exe)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// The helper gateway's last line on stderr, in mode lastline.
const helperLastLine = `forge-gateway: settings mode: node.rpc_url "http://127.0.0.1:8342/": the node answers with something that is not JSON-RPC`

// helperMain is the gateway the tray starts in the tests, by mode. It notes each start in
// $GWL_HELPER_DIR/starts.
//
//	args      records its arguments, folder and environment, prints a line to stdout and one to
//	          stderr, and exits 0 when its stdin closes
//	eof       exits 0 when its stdin closes (after GWL_HELPER_DELAY), leaving the marker "clean"
//	stuck     ignores its stdin and writes a heartbeat until it is killed
//	exit1     says why on stderr and exits 1; exit3 likewise, with a config mistake, exit 3
//	lastline  writes lines to stderr and exits 1 at once
//	crash     exits 1 the first time, then runs as status
//	status    serves /api/status, the answer $GWL_HELPER_DIR/state.json holds, at the config's
//	          status.listen, until its stdin closes
//	port      exits 4 while $GWL_HELPER_DIR/held exists, otherwise runs as status
func helperMain(mode string) int {
	dir := os.Getenv("GWL_HELPER_DIR")
	appendTo(filepath.Join(dir, "starts"), "start\n")
	switch mode {
	case "args":
		cwd, _ := os.Getwd()
		rec := "args=" + strings.Join(os.Args[1:], "|") + "\ncwd=" + cwd +
			"\nSETTINGS_PASSWORD=" + os.Getenv("SETTINGS_PASSWORD") + "\nFORGE_STOP_ON_STDIN_EOF=" + os.Getenv("FORGE_STOP_ON_STDIN_EOF") + "\n"
		_ = os.WriteFile(filepath.Join(dir, "args.tmp"), []byte(rec), 0o600)
		_ = os.Rename(filepath.Join(dir, "args.tmp"), filepath.Join(dir, "args"))
		fmt.Println("the gateway's stdout")
		fmt.Fprintln(os.Stderr, "the gateway's stderr")
		_, _ = io.Copy(io.Discard, os.Stdin)
		return 0
	case "eof":
		_, _ = io.Copy(io.Discard, os.Stdin)
		if d, err := time.ParseDuration(os.Getenv("GWL_HELPER_DELAY")); err == nil {
			time.Sleep(d)
		}
		_ = os.WriteFile(filepath.Join(dir, "clean"), []byte("1"), 0o600)
		return 0
	case "stuck":
		for i := 0; ; i++ {
			_ = os.WriteFile(filepath.Join(dir, "beat"), []byte(strconv.Itoa(i)), 0o600)
			time.Sleep(100 * time.Millisecond)
		}
	case "exit1":
		fmt.Fprintln(os.Stderr, "forge-gateway: the gateway stopped")
		return 1
	case "exit3":
		fmt.Fprintln(os.Stderr, `forge-gateway: forge-gateway.json: unknown field "x"`)
		return 3
	case "lastline":
		fmt.Println("the gateway's stdout")
		fmt.Fprintln(os.Stderr, "an earlier line")
		fmt.Fprintln(os.Stderr, helperLastLine)
		return 1
	case "crash":
		if _, err := os.Stat(filepath.Join(dir, "crashed")); err != nil {
			_ = os.WriteFile(filepath.Join(dir, "crashed"), []byte("1"), 0o600)
			fmt.Fprintln(os.Stderr, "forge-gateway: it crashed")
			return 1
		}
		return serveStatus(dir)
	case "port":
		if _, err := os.Stat(filepath.Join(dir, "held")); err == nil {
			fmt.Fprintln(os.Stderr, "forge-gateway: stratum 0.0.0.0:3333: bind: the port is taken")
			return 4
		}
		return serveStatus(dir)
	case "status":
		return serveStatus(dir)
	}
	return 2
}

// serveStatus answers /api/status at the config's status.listen until stdin closes.
func serveStatus(dir string) int {
	cfg := ""
	for i, a := range os.Args {
		if a == "-config" && i+1 < len(os.Args) {
			cfg = os.Args[i+1]
		}
	}
	saved := dataDir
	dataDir = filepath.Dir(cfg)
	_ = configPorts()
	dataDir = saved
	l, err := net.Listen("tcp", net.JoinHostPort(statusHost, statusPort))
	if err != nil {
		fmt.Fprintln(os.Stderr, "forge-gateway: status:", err)
		return 4
	}
	go func() {
		_ = http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, err := os.ReadFile(filepath.Join(dir, "state.json"))
			if err != nil {
				b = []byte(`{"configured":false,"state":"unconfigured","mode":"off"}`)
			}
			if code, err := strconv.Atoi(string(b)); err == nil {
				w.WriteHeader(code)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(b)
		}))
	}()
	_, _ = io.Copy(io.Discard, os.Stdin)
	_ = os.WriteFile(filepath.Join(dir, "clean"), []byte("1"), 0o600)
	return 0
}

func appendTo(path, s string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.WriteString(s)
	_ = f.Close()
}

// testPassword stands in for the Settings password secrets.env holds.
const testPassword = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// A world is the tray with the helper gateway in mode, a data folder and ports of its own, and
// stand-ins for the browser, the tray and the system's tables, which say no one holds a port.
// Everything is stopped and put back after the test.
type world struct {
	dir      string // the helper's folder
	tips     *tips  // the tooltips shown
	opened   *tips  // the pages opened
	tryShown *tips  // Try Again shown (true) and hidden (false)
}

func gatewayWorld(t *testing.T, mode string) *world {
	t.Helper()
	w := &world{dir: t.TempDir(), tips: &tips{}, opened: &tips{}, tryShown: &tips{}}
	data := t.TempDir()
	t.Setenv("GWL_HELPER", mode)
	t.Setenv("GWL_HELPER_DIR", w.dir)
	savedInst, savedData, savedSec := installDir, dataDir, sec
	savedPorts := []string{stratumPort, statusHost, statusPort}
	savedBrowser, savedShow, savedPrep := openBrowser, showTryAgain, prepErr
	savedProgs, savedWait, savedKill := installedPrograms, waitPID, killPID
	savedTable, savedName, savedPath, savedExe, savedSolo, savedService, savedProbe := tcpListeners, processName, processPath, exeName, forgeSoloRuns, gatewayServicePID, listenProbe
	savedSaid, savedPoll, savedAsk, savedPage := gatewayStateSaid, statePollEvery, stateAskTimeout, statusPageWait
	savedGrace, savedEnd, savedOutput := gatewayStopGrace, stderrEndWait, gatewayOutput
	installDir, dataDir = helperDir, data
	sec = secrets{Settings: testPassword}
	ports := freePorts(t, 2)
	writeFile(t, dpath(configName), `{"stratum":{"listen":"127.0.0.1:`+ports[0]+`"},"status":{"listen":"127.0.0.1:`+ports[1]+`"}}`)
	readConfigPorts()
	openBrowser = w.opened.add
	tipMu.Lock()
	savedTip := setTooltip
	setTooltip = w.tips.add
	tipMu.Unlock()
	showTryAgain = func(show bool) { w.tryShown.add(strconv.FormatBool(show)) }
	installedPrograms = func() []runningProgram { return nil }
	tcpListeners = func() []tcpListener { return nil }
	processName, processPath, exeName = noName, noName, noName
	forgeSoloRuns = func() bool { return false }
	gatewayServicePID = func() int { return 0 }
	statePollEvery, stateAskTimeout, statusPageWait = 50*time.Millisecond, time.Second, 5*time.Second
	gatewayStopGrace = 10 * time.Second
	restartMu.Lock()
	a, b, q := restartFirstWait, restartMaxWait, restartQuick
	restartFirstWait, restartMaxWait, restartQuick = 50*time.Millisecond, 200*time.Millisecond, time.Hour
	restartMu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		stopping = true // nothing more is started
		mu.Unlock()
		bootsUnderWay.Wait()
		stateWatch.Wait()
		restartsUnderWay.Wait()
		stopNow(gatewayKey)
		closeServiceLogs()
		mu.Lock()
		stopping = false
		mu.Unlock()
		tipMu.Lock()
		stopShown = false
		setTooltip = savedTip
		tipMu.Unlock()
		resetStartState()
		restartMu.Lock()
		clear(restartWaits)
		restartFirstWait, restartMaxWait, restartQuick = a, b, q
		restartMu.Unlock()
		installDir, dataDir, sec = savedInst, savedData, savedSec
		stratumPort, statusHost, statusPort = savedPorts[0], savedPorts[1], savedPorts[2]
		openBrowser, showTryAgain, prepErr = savedBrowser, savedShow, savedPrep
		installedPrograms, waitPID, killPID = savedProgs, savedWait, savedKill
		tcpListeners, processName, processPath, exeName, forgeSoloRuns, gatewayServicePID, listenProbe = savedTable, savedName, savedPath, savedExe, savedSolo, savedService, savedProbe
		gatewayStateSaid, statePollEvery, stateAskTimeout, statusPageWait = savedSaid, savedPoll, savedAsk, savedPage
		gatewayStopGrace, stderrEndWait, gatewayOutput = savedGrace, savedEnd, savedOutput
	})
	return w
}

func noName(int) string { return "" }

// resetStartState forgets what a start left behind: the gateway it came to, what the tray said
// about it, what the gateway said, and Try Again.
func resetStartState() {
	gatewayDue.Store(false)
	restarting.Store(false)
	retryOffered.Store(noRetry)
	troubleMu.Lock()
	clear(trouble)
	clear(retrying)
	troubleMu.Unlock()
	stateMu.Lock()
	answer, answered, current = gatewayAnswer{}, false, false
	stateMu.Unlock()
	lastLines.Range(func(k, _ any) bool { lastLines.Delete(k); return true })
}

// stopNow ends the program under key at once, as the stop would not: it is the tests' own.
func stopNow(key string) {
	mu.Lock()
	if w := stdins[key]; w != nil {
		_ = w.Close()
		delete(stdins, key)
	}
	mu.Unlock()
	c, done := untrack(key)
	kill(c, done)
}

// closeServiceLogs closes the logs the tray keeps open, which Windows would not let the test's
// folder be removed with.
func closeServiceLogs() {
	serviceLogsMu.Lock()
	defer serviceLogsMu.Unlock()
	for p, l := range serviceLogs {
		l.mu.Lock()
		if l.f != nil {
			_ = l.f.Close()
			l.f = nil
		}
		l.mu.Unlock()
		delete(serviceLogs, p)
	}
}

// startSupervising turns starting programs again on for the test.
func startSupervising(t *testing.T) {
	t.Helper()
	supervising.Store(true)
	t.Cleanup(func() { supervising.Store(false) })
}

// starts is how many times the helper gateway has started.
func (w *world) starts() int {
	b, _ := os.ReadFile(filepath.Join(w.dir, "starts"))
	return strings.Count(string(b), "start\n")
}

// has reports whether the helper left the named marker.
func (w *world) has(marker string) bool {
	_, err := os.Stat(filepath.Join(w.dir, marker))
	return err == nil
}

// setState makes the helper's status page answer s: a JSON body, or an HTTP status code alone.
func (w *world) setState(t *testing.T, s string) {
	t.Helper()
	tmp := filepath.Join(w.dir, "state.tmp")
	writeFile(t, tmp, s)
	// Windows refuses the rename while the helper has the file open a moment to answer.
	var err error
	for i := 0; i < 100; i++ {
		if err = os.Rename(tmp, filepath.Join(w.dir, "state.json")); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(err)
}

// pid is the process id of the program under key, or 0.
func pid(key string) int {
	mu.Lock()
	defer mu.Unlock()
	if c := procs[key]; c != nil && c.Process != nil {
		return c.Process.Pid
	}
	return 0
}

// tips records what the tray was given, in order.
type tips struct {
	mu   sync.Mutex
	list []string
}

func (p *tips) add(s string) { p.mu.Lock(); p.list = append(p.list, s); p.mu.Unlock() }
func (p *tips) all() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.list...)
}
func (p *tips) last() string {
	if l := p.all(); len(l) > 0 {
		return l[len(l)-1]
	}
	return ""
}
func (p *tips) has(s string) bool {
	for _, x := range p.all() {
		if x == s {
			return true
		}
	}
	return false
}

func waitFor(d time.Duration, cond func() bool) bool {
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if cond() {
			return true
		}
	}
	return cond()
}

func launcherLog() string { b, _ := os.ReadFile(dpath("launcher.log")); return string(b) }

func logHas(s string) func() bool { return func() bool { return strings.Contains(launcherLog(), s) } }

func writeFile(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

// freePorts finds n ports no one listens on at 127.0.0.1, all different.
func freePorts(t *testing.T, n int) []string {
	t.Helper()
	var out []string
	for i := 0; i < n; i++ {
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		_, p, _ := net.SplitHostPort(l.Addr().String())
		out = append(out, p)
	}
	return out
}

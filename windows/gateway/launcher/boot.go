package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/systray"
)

// setupSecrets reads secrets.env, making the Settings password if it has none: other programs and
// accounts on this PC can reach the status page, so a change in its Settings needs it. It never
// replaces a file it could not read (another program may hold it a moment), and keeps every other
// line of the file as it is.
func setupSecrets() error {
	f := dpath("secrets.env")
	b, err := os.ReadFile(f)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("secrets.env cannot be read (%v)", err)
	}
	var others []string
	for _, line := range splitLines(string(b)) {
		if k, v := cut(line, '='); k == "SETTINGS" {
			sec.Settings = v
		} else {
			others = append(others, line)
		}
	}
	if sec.Settings != "" {
		return nil
	}
	sec.Settings = genHex(32)
	content := ""
	for _, line := range others {
		content += line + "\n"
	}
	content += "SETTINGS=" + sec.Settings + "\n"
	// Written aside, on the disk, and only then moved into place: a crash or a power cut part-way
	// must not leave the file half written.
	if err := writeDurably(f, content); err != nil {
		return fmt.Errorf("secrets.env cannot be written (%v)", err)
	}
	return nil
}

// writeDurably replaces path with content: written to path.tmp, flushed to the disk, then renamed
// over path, so the file is the old one or the new one, never a part.
func writeDurably(path, content string) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.WriteString(content); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// rotateLog moves a log past limit bytes to <name>.1, replacing the one before.
func rotateLog(path string, limit int64) {
	if st, err := os.Stat(path); err == nil && st.Size() > limit {
		_ = os.Remove(path + ".1")
		_ = os.Rename(path, path+".1")
	}
}

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

// gatewayStopGrace is how long the gateway gets to stop cleanly once asked: it closes its miners'
// connections, answers a Settings save under way and sends the pool the shares it still holds,
// usually in a few seconds (shorter in the tests).
var gatewayStopGrace = 30 * time.Second

// gatewayOutput is where the gateway's output goes: forge-gateway.log in the data folder (a
// stand-in in the tests).
var gatewayOutput = func() io.Writer { return serviceLog("forge-gateway") }

// lastLines holds, for each run of the gateway, the last line it wrote to its stderr.
var lastLines sync.Map // *exec.Cmd -> *lastLine

// startGateway starts forge-gateway.exe with its config in the data folder and the Settings
// password. Windows cannot signal it, so closing its stdin is how it is asked to stop cleanly
// (FORGE_STOP_ON_STDIN_EOF). Its stderr is read here, through a pipe of its own, so that its end is
// known: startTracked waits with Process.Wait, which does not wait for exec's own copy of an output,
// and the reason an exit gives, its last line, could otherwise still be on its way.
func startGateway() error {
	c := hidden(gatewayExe, "-config", dpath(configName))
	c.Dir = dataDir
	c.Env = append(os.Environ(), "SETTINGS_PASSWORD="+sec.Settings, "FORGE_STOP_ON_STDIN_EOF=1")
	out := gatewayOutput()
	c.Stdout = out
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	c.Stderr = pw
	w, err := c.StdinPipe()
	if err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return err
	}
	last := &lastLine{done: make(chan struct{})}
	lastLines.Store(c, last)
	err = runPiped(gatewayKey, c, w)
	_ = pw.Close() // the child has its own copy; on a failed start nothing writes to it
	if err != nil {
		lastLines.Delete(c)
	}
	go func() {
		_, _ = io.Copy(io.MultiWriter(out, last), pr)
		_ = pr.Close()
		close(last.done)
	}()
	return err
}

// lastLineMax is how much of a line lastLine keeps.
const lastLineMax = 500

// A lastLine keeps the last line that is not empty written to it, at most lastLineMax bytes, and is
// safe for writes from more than one goroutine. done is closed once the output has ended.
type lastLine struct {
	mu      sync.Mutex
	line    string
	partial []byte
	long    bool // the line being written is past lastLineMax: the rest of it is dropped
	done    chan struct{}
}

func (l *lastLine) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, b := range p {
		switch {
		case b == '\n':
			if s := strings.TrimSpace(string(l.partial)); s != "" {
				l.line = s
			}
			l.partial, l.long = l.partial[:0], false
		case l.long:
		case len(l.partial) == lastLineMax:
			l.long = true
		default:
			l.partial = append(l.partial, b)
		}
	}
	return len(p), nil
}

// text is the last line written: one that did not end in a new line counts too.
func (l *lastLine) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if s := strings.TrimSpace(string(l.partial)); s != "" {
		return s
	}
	return l.line
}

// gatewayDue is set once boot comes to start the gateway.
var gatewayDue atomic.Bool

// restarting is set while Restart Forge Gateway runs: a second click meanwhile does nothing.
var restarting atomic.Bool

// restartGateway is Restart Forge Gateway: the gateway is stopped cleanly and started again, or,
// when it is not running (its program could not start), started at once. Before boot comes to the
// gateway it does nothing: boot would then start a second.
func restartGateway() {
	if !restarting.CompareAndSwap(false, true) {
		return
	}
	defer restarting.Store(false)
	if started(gatewayKey) {
		logf("restarting the gateway")
		status(tipRestartingGW)
		stopGracefully(gatewayKey, gatewayStopGrace)
		time.Sleep(2 * time.Second)
	} else if gatewayDue.Load() {
		logf("starting the gateway, which is not running")
	} else {
		return
	}
	forgetState()
	if startOrKeepTrying(gatewayKey) {
		showRunning()
	}
}

// boot starts the gateway, once the ports it listens on are free, and opens its status page. Quit
// may come at any point of it: from then on nothing more starts.
func boot() {
	status(tipPreparing)
	stopLeftovers()
	if err := checkPorts(); err != nil {
		logf("Forge Gateway cannot start: %v; %s", err, err.advice())
		startFailed(tipCannotStart(startWhy(err)))
		return
	}
	if isStopping() {
		return
	}
	status(tipStarting)
	gatewayDue.Store(true)
	startOrKeepTrying(gatewayKey)
	if isStopping() {
		return
	}
	openStatusPage()
	watchGatewayState()
}

// statusPageWait is how long the first start waits for the status page to answer.
var statusPageWait = 60 * time.Second

// openStatusPage opens the status page once it answers, at Settings when the gateway says it is not
// set up. Every start of the tray opens it, as Forge Solo opens its dashboard; a start of the
// gateway alone (it stopped on its own, or Restart Forge Gateway) does not.
func openStatusPage() {
	if !waitTCP(net.JoinHostPort(statusHost, statusPort), statusPageWait) || isStopping() {
		if !isStopping() {
			openBrowser(statusURL())
		}
		return
	}
	if a, known := gatewayStateSaid(); known {
		noteState(a)
		if !a.configured {
			openBrowser(settingsURL())
			return
		}
	}
	openBrowser(statusURL())
}

// started reports whether this tray started the process under key and has not stopped it.
func started(key string) bool {
	mu.Lock()
	defer mu.Unlock()
	return procs[key] != nil
}

// stopGracefully asks a process to stop by closing its stdin, waits up to grace for it to exit,
// and kills it only if it has not.
func stopGracefully(key string, grace time.Duration) {
	mu.Lock()
	w := stdins[key]
	delete(stdins, key)
	mu.Unlock()
	c, done := untrack(key) // before it is asked: its exit is the tray's doing
	if w != nil {
		start := time.Now()
		_ = w.Close()
		if waitDone(done, grace) {
			logf("%s stopped in %v", key, time.Since(start).Round(time.Millisecond))
		} else {
			logf("%s did not stop in %v: killed", key, grace)
		}
	}
	kill(c, done)
}

// stopOnce runs the stop once: Quit and Windows ending the session can both ask for it.
var stopOnce sync.Once

// sessionEnding is set when Windows is ending the session, which leaves Forge Gateway about 5 s.
var sessionEnding atomic.Bool

// stopDone is closed when everything has stopped, just before the process exits.
var stopDone = make(chan struct{})

// shutdown stops everything cleanly and exits. The tray icon stays, showing the stop, until
// everything has stopped: gone at once, it looked as if Forge Gateway had exited, and a start
// meanwhile found it still running and only opened its status page. A second call waits for the
// first, which exits.
func shutdown() {
	stopOnce.Do(func() {
		stopForExit()
		close(stopDone)
		if relaunchAfterSessionStop() {
			// Windows asked to end the session, and then did not: someone cancelled the shutdown or
			// the restart that another program held up. Left stopped, the gateway would not mine
			// again until someone started Forge Gateway.
			logf("Windows did not end the session after all: starting Forge Gateway again")
			releaseRunning()
			exe, _ := os.Executable()
			if err := relaunch(exe); err != nil {
				logf("could not start Forge Gateway again: %v", err)
			}
		}
		systray.Quit() // removes the icon
		os.Exit(0)
	})
}

// sessionOutcome carries Windows's answer, after it asked to end the session: whether it ends.
var sessionOutcome = make(chan bool, 1)

// sessionOutcomeWait is how long the answer may take: Windows waits on the person at the screen
// when another program holds up the shutdown (shorter in the tests).
var sessionOutcomeWait = 15 * time.Minute

// noteSessionOutcome passes on Windows's answer, if Windows asked first: an answer to a question
// Forge Gateway was never asked (another program refused it before) must not be kept for a later
// one.
func noteSessionOutcome(ends bool) {
	if !sessionEnding.Load() {
		return
	}
	select {
	case sessionOutcome <- ends:
	default:
	}
}

// relaunchAfterSessionStop reports, after the stop Windows asked for, whether Forge Gateway must
// start again: when Windows then did not end the session, or never said.
func relaunchAfterSessionStop() bool {
	if !sessionEnding.Load() {
		return false
	}
	select {
	case ends := <-sessionOutcome:
		return !ends
	case <-time.After(sessionOutcomeWait):
		logf("Windows did not say in %v whether the session ends", sessionOutcomeWait)
		return true
	}
}

// stopForExit stops everything for the exit that follows. Nothing starts once it has begun: boot
// may still be starting the gateway, and one started after the stop had passed it would be left
// running.
func stopForExit() {
	mu.Lock()
	stopping = true
	mu.Unlock()
	if sessionEnding.Load() {
		// No one sees the tooltip while Windows ends the session, and the taskbar can be slow to
		// answer then: the gateway needs those seconds more.
		go showStopping()
	} else {
		showStopping()
	}
	start := time.Now()
	stopEverything()
	logf("everything stopped in %v", time.Since(start).Round(time.Millisecond))
}

// waitStopped waits up to max for the stop under way to finish, and reports whether it did.
func waitStopped(max time.Duration) bool {
	select {
	case <-stopDone:
		return true
	case <-time.After(max):
		return false
	}
}

// stopEverything is the stop, for Quit and for a closing Windows session alike: there is only the
// gateway to stop. It closes its miners' connections and sends the pool the shares it still holds.
func stopEverything() {
	logf("stopping: the gateway")
	stopGracefully(gatewayKey, gatewayStopGrace)
}

// small helpers to avoid extra imports
func splitLines(s string) []string {
	var out, cur = []string{}, ""
	for _, r := range s {
		if r == '\n' {
			out = append(out, cur)
			cur = ""
		} else if r != '\r' {
			cur += string(r)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
func cut(s string, sep byte) (string, string) {
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			return s[:i], s[i+1:]
		}
	}
	return s, ""
}

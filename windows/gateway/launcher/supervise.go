package main

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// What the gateway's exit code says (cmd/forge-gateway/exit.go): its config or SETTINGS_PASSWORD is
// wrong, or a port it listens on cannot be had.
const exitConfigMistake, exitPortTaken = 3, 4

// supervisedProgram is how to start the program under key again when it exits on its own, and what
// to call it; nil for one the tray does not start again.
func supervisedProgram(key string) (func() error, string) {
	extraMu.Lock()
	p, ok := extraSupervised[key]
	extraMu.Unlock()
	if ok {
		return p.start, p.what
	}
	if key == gatewayKey {
		return startGateway, gatewayExe
	}
	return nil, ""
}

// supervising is set while programs that exit on their own are started again: always, but in the
// tests, which turn it on only where they test it.
var supervising atomic.Bool

func init() { supervising.Store(true) }

// extraSupervised are the tests' own programs to start again.
var (
	extraMu         sync.Mutex
	extraSupervised = map[string]struct {
		start func() error
		what  string
	}{}
)

// The wait before a program that exited on its own is started again: restartFirstWait, doubled
// each time it exits within restartQuick of its start, up to restartMaxWait; back to the first once
// it has run longer (shorter in the tests).
var restartFirstWait, restartMaxWait, restartQuick = 2 * time.Second, time.Minute, time.Minute

var (
	restartMu        sync.Mutex // guards the waits above and below
	restartWaits     = map[string]time.Duration{}
	restartsUnderWay sync.WaitGroup // the restarts under way (the tests wait for them)
)

// stderrEndWait is how long the tray waits, after the gateway exited, for the end of what it wrote
// to its stderr: its last line says why it exited.
var stderrEndWait = 2 * time.Second

// exitedOnItsOwn follows the exit of c, started under key at since. One the tray did not ask for (a
// crash, or the gateway quitting on an error) is logged with the gateway's last words, shown in the
// tray, and the program is started again after a wait. There is no service manager behind the tray
// app to do it. Its exit code says what the tray shows: a config with a mistake, a port it cannot
// have, or a program that stopped.
func exitedOnItsOwn(key string, c *exec.Cmd, st *os.ProcessState, since time.Time) {
	var last *lastLine
	if l, ok := lastLines.LoadAndDelete(c); ok {
		last = l.(*lastLine)
	}
	if !supervising.Load() {
		return
	}
	mu.Lock()
	onItsOwn := !stopping && procs[key] == c
	if onItsOwn {
		delete(procs, key)
		delete(exited, key)
		if w := stdins[key]; w != nil {
			_ = w.Close()
			delete(stdins, key)
		}
	}
	mu.Unlock()
	start, what := supervisedProgram(key)
	if !onItsOwn || start == nil {
		return
	}
	restartsUnderWay.Add(1)
	defer restartsUnderWay.Done()
	if key == gatewayKey {
		forgetState()
	}
	ran := time.Since(since)
	restartMu.Lock()
	wait, quick := restartWaits[key], restartQuick
	if wait == 0 || ran >= restartQuick {
		wait = restartFirstWait
	} else {
		wait = min(2*wait, restartMaxWait)
	}
	restartWaits[key] = wait
	restartMu.Unlock()
	code := -1
	if st != nil {
		code = st.ExitCode()
	}
	logf("%s (%s) exited on its own after %v, exit code %d: starting it again in %v", what, key, ran.Round(time.Second), code, wait)
	if last != nil {
		waitDone(last.done, stderrEndWait)
		if said := last.text(); said != "" {
			logf("%s said: %s", what, said)
		}
	}
	switch code {
	case exitConfigMistake:
		// Fixed by hand, the config is read at the next start, within a minute.
		status(tipConfigMistake)
	case exitPortTaken:
		portsAfterExit(key, wait)
	default:
		status(tipRestarting(what))
	}
	time.Sleep(wait)
	if isStopping() {
		return
	}
	if err := start(); err != nil {
		if !errors.Is(err, errStopping) && !errors.Is(err, errStarted) {
			next := nextWait(wait)
			couldNotStart(key, what, err, next)
			keepStarting(key, what, start, next)
		}
		return
	}
	logf("%s started again", what)
	time.AfterFunc(quick, showRunning)
}

// portsAfterExit says why the gateway could not have a port it listens on. Another program holding
// one, or Windows keeping it, is said as at the start, and the tray names it; the gateway is
// started again all the same, with its waits, so that it starts once the port is free. With no one
// holding them, the ports are still closing the connections of the run before: Windows keeps a port
// that had connections, under exclusive address use, until they are gone, which can take minutes.
// The exit code does not say which port.
func portsAfterExit(key string, wait time.Duration) {
	if err := checkPorts(); err != nil {
		logf("Forge Gateway cannot start: %v; %s", err, err.byItself())
		setTrouble(key, tipCannotStart(startWhy(err)))
		return
	}
	logf("ports %s and %s are not free yet: the connections of the last run are still closing; trying again in %v", stratumPort, statusPort, wait)
	status(tipPortClosing)
}

// trouble is what the tray says about a program that is not running as it should (one that could
// not start), by its key. The tray does not say what the gateway is doing while there is any.
var (
	troubleMu sync.Mutex
	trouble   = map[string]string{}
	retrying  = map[string]bool{} // the programs tried again in the background
)

// runningKeys are the programs that run once Forge Gateway has started.
var runningKeys = []string{gatewayKey}

// showTrouble shows the first trouble in the tray, if there is one, and reports whether there was.
func showTrouble() bool {
	troubleMu.Lock()
	tip := ""
	for _, k := range runningKeys {
		if tip = trouble[k]; tip != "" {
			break
		}
	}
	troubleMu.Unlock()
	if tip != "" {
		status(tip)
	}
	return tip != ""
}

// showRunning shows what the gateway is doing, as it last said, while it runs; while it could not
// start, the tray says that instead. Before it has said anything, the tray stays as it is.
func showRunning() {
	if showTrouble() || !started(gatewayKey) {
		return
	}
	if a, known := currentState(); known {
		status(tipForState(a.state, a.mode))
	}
}

// startOrKeepTrying starts the program under key and reports whether it runs. One that cannot start
// (an antivirus holding or removing its program, say) is logged and named in the tray, and tried
// again in the background until it starts or the stop begins: nothing else on Windows would.
func startOrKeepTrying(key string) bool {
	start, what := supervisedProgram(key)
	err := start()
	if err == nil || errors.Is(err, errStarted) {
		return true
	}
	if errors.Is(err, errStopping) {
		return false
	}
	wait := nextWait(0)
	couldNotStart(key, what, err, wait)
	if supervising.Load() {
		restartsUnderWay.Add(1)
		go func() {
			defer restartsUnderWay.Done()
			keepStarting(key, what, start, wait)
		}()
	}
	return false
}

// couldNotStart logs that the program under key could not start, and when it is tried again, and
// names it in the tray with the reason.
func couldNotStart(key, what string, err error, wait time.Duration) {
	logf("%s could not start: %v; trying again in %v", what, err, wait)
	setTrouble(key, tipProgramCannotStart(what, startError(err)))
}

// setTrouble notes what is wrong with the program under key, and shows it in the tray.
func setTrouble(key, tip string) {
	troubleMu.Lock()
	trouble[key] = tip
	troubleMu.Unlock()
	status(tip)
}

// clearTrouble forgets what was wrong with the program under key.
func clearTrouble(key string) {
	troubleMu.Lock()
	delete(trouble, key)
	troubleMu.Unlock()
}

// keepStarting starts the program under key after wait, again after twice as long each time it
// cannot, up to restartMaxWait, until it starts or the stop begins. One loop per program: another
// start meanwhile (Restart Forge Gateway) ends it.
func keepStarting(key, what string, start func() error, wait time.Duration) {
	troubleMu.Lock()
	if retrying[key] {
		troubleMu.Unlock()
		return
	}
	retrying[key] = true
	troubleMu.Unlock()
	defer func() {
		troubleMu.Lock()
		delete(retrying, key)
		troubleMu.Unlock()
	}()
	for {
		if pause(wait) {
			return
		}
		err := start()
		switch {
		case err == nil || errors.Is(err, errStarted):
			if err == nil {
				logf("%s started", what)
			}
			showRunning()
			return
		case errors.Is(err, errStopping):
			return
		}
		wait = nextWait(wait)
		couldNotStart(key, what, err, wait)
	}
}

// pause waits d, or less once the stop has begun, and reports whether it has.
func pause(d time.Duration) bool {
	for end := time.Now().Add(d); time.Now().Before(end); {
		if isStopping() {
			return true
		}
		time.Sleep(min(50*time.Millisecond, time.Until(end)))
	}
	return isStopping()
}

// nextWait is the wait after wait: restartFirstWait at first, then twice the one before, up to
// restartMaxWait.
func nextWait(wait time.Duration) time.Duration {
	restartMu.Lock()
	defer restartMu.Unlock()
	if wait == 0 {
		return restartFirstWait
	}
	return min(2*wait, restartMaxWait)
}

// startError is why a program could not start, without its path, which the tray has no room for.
func startError(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	var ee *exec.Error
	if errors.As(err, &ee) {
		return ee.Err.Error()
	}
	return err.Error()
}

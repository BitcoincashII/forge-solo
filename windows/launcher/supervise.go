package main

import (
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// supervisedProgram is how to start the program under key again when it exits on its own, and what
// to call it; nil for one the launcher does not start again.
func supervisedProgram(key string) (func() error, string) {
	extraMu.Lock()
	p, ok := extraSupervised[key]
	extraMu.Unlock()
	if ok {
		return p.start, p.what
	}
	switch key {
	case "stratum":
		return startStratum, "the miner"
	case "api":
		return startAPI, "the dashboard's API"
	case "bch2":
		return startBCH2, "the BCH2 node"
	case "aux1175":
		return startAux, "the 1175 node"
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

// exitedOnItsOwn follows the exit of c, started under key at since. One the launcher did not ask
// for -- a crash, or a program quitting on an error -- is logged, shown in the tray, and the program
// is started again after a wait. There is no service manager on Windows to do it, and a miner that
// crashed left the tray saying "running" with nothing mining until someone noticed.
func exitedOnItsOwn(key string, c *exec.Cmd, st *os.ProcessState, since time.Time) {
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
	status("Forge Solo: " + what + " stopped on its own and is started again (see launcher.log)")
	time.Sleep(wait)
	if isStopping() {
		return
	}
	if err := start(); err != nil {
		logf("%s did not start again: %v", what, err)
		return
	}
	logf("%s started again", what)
	time.AfterFunc(quick, func() {
		if started(key) && dashboardOpen.Load() {
			status("Forge Solo: running")
		}
	})
}

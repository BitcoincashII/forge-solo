//go:build !windows

package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// startFailWorld is bootWorld with programs started again (short waits), RPC and API stand-ins that
// answer at once, and the tray's tooltips recorded. scripts are the programs in place; one left out
// cannot start. Everything started is stopped after the test, retries first.
func startFailWorld(t *testing.T, scripts map[string]string) *tips {
	t.Helper()
	bootWorld(t, scripts)
	startSupervising(t)
	restartMu.Lock()
	a, b, q := restartFirstWait, restartMaxWait, restartQuick
	restartFirstWait, restartMaxWait, restartQuick = 50*time.Millisecond, 200*time.Millisecond, time.Hour
	restartMu.Unlock()
	answer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":"stopping"}`))
	}))
	bch2RPC, bch2ZMQ, aux1175RPC, stratumInt, apiPort = portOf(answer.URL), freePort(t), portOf(answer.URL), freePort(t), portOf(answer.URL)
	bch2StopGrace, auxStopGrace = 300*time.Millisecond, 300*time.Millisecond
	tp := &tips{}
	savedTip := setTooltip
	setTooltip = tp.add
	t.Cleanup(func() {
		mu.Lock()
		stopping = true // nothing more is started
		mu.Unlock()
		minerStart.Wait() // the RPC stand-in answers: boot's start of the miner ends at once
		restartsUnderWay.Wait()
		for _, k := range []string{"stratum", "api", "bch2", "aux1175", "migrate"} {
			stop(k)
		}
		answer.Close()
		setTooltip = savedTip
		restartMu.Lock()
		for _, k := range []string{"stratum", "api", "bch2", "aux1175"} {
			delete(restartWaits, k)
		}
		restartFirstWait, restartMaxWait, restartQuick = a, b, q
		restartMu.Unlock()
		resetStartState()
	})
	return tp
}

// resetStartState forgets what a boot left behind: the miner it came to, and what the tray said
// about programs that could not start.
func resetStartState() {
	minerDue.Store(false)
	troubleMu.Lock()
	clear(trouble)
	clear(retrying)
	troubleMu.Unlock()
}

// tips records the tray's tooltips.
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
func (p *tips) has(prefix string) bool {
	for _, s := range p.all() {
		if strings.HasPrefix(s, prefix) {
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

const sleeper = "exec sleep 60"

// The BCH2 node's program cannot start (an antivirus holds or removed it): launcher.log and the tray
// say so, naming it and why, the tray never says "running" without it, and it is started once its
// program is back. It was dropped without a word: "nodes started", then "running", with nothing to
// mine on.
func TestANodeThatCannotStartIsTriedAgain(t *testing.T) {
	tp := startFailWorld(t, map[string]string{"elevenseventyfived.exe": sleeper, "api.exe": sleeper, "stratum.exe": sleeper})
	boot()
	if !waitFor(5*time.Second, func() bool { return started("stratum") }) {
		t.Fatalf("setup: the miner did not start; log:\n%s", launcherLog())
	}
	if !strings.Contains(launcherLog(), "the BCH2 node could not start: fork/exec ") {
		t.Errorf("WIN-START-LOGGED: launcher.log does not say the BCH2 node could not start, and why:\n%s", launcherLog())
	}
	if strings.Contains(launcherLog(), "nodes started") {
		t.Errorf("WIN-START-NODES-NOT-STARTED: launcher.log says the nodes started with no BCH2 node:\n%s", launcherLog())
	}
	if !tp.has("Forge Solo: the BCH2 node could not start: no such file or directory") {
		t.Errorf("WIN-START-TRAY: the tray never named the BCH2 node and why it could not start: %q", tp.all())
	}
	if tp.has("Forge Solo: running") {
		t.Errorf("WIN-START-NOT-RUNNING: the tray said running with no BCH2 node: %q", tp.all())
	}
	if err := os.WriteFile(ipath("bitcoincashIId.exe"), []byte("#!/bin/sh\n"+sleeper+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !waitFor(5*time.Second, func() bool { return started("bch2") }) {
		t.Fatalf("WIN-START-RETRIED: the BCH2 node was not started once its program was back; log:\n%s", launcherLog())
	}
	if !waitFor(2*time.Second, func() bool { return tp.last() == "Forge Solo: running" }) {
		t.Errorf("WIN-START-RUNNING-AFTER: with everything started the tray says %q", tp.last())
	}
}

// The API's program cannot start: it is logged and named in the tray, the dashboard opens and the
// miner starts all the same, and the API is started once its program is back. Boot used to stop
// there without a word, with the tray on "starting the nodes" for good and no miner.
func TestAnAPIThatCannotStartIsTriedAgain(t *testing.T) {
	tp := startFailWorld(t, map[string]string{"bitcoincashIId.exe": sleeper, "elevenseventyfived.exe": sleeper, "stratum.exe": sleeper})
	boot()
	if !waitFor(5*time.Second, func() bool { return started("stratum") }) {
		t.Fatalf("WIN-START-API-MINER: the miner did not start with the API missing; log:\n%s", launcherLog())
	}
	if !strings.Contains(launcherLog(), "the dashboard's API could not start") || !tp.has("Forge Solo: the dashboard's API could not start") {
		t.Errorf("WIN-START-API-SAID: the API's failure is not in launcher.log and the tray: %q\n%s", tp.all(), launcherLog())
	}
	if err := os.WriteFile(ipath("api.exe"), []byte("#!/bin/sh\n"+sleeper+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !waitFor(5*time.Second, func() bool { return started("api") }) {
		t.Fatalf("WIN-START-API-RETRIED: the API was not started once its program was back; log:\n%s", launcherLog())
	}
}

// The miner exits on its own and its program is gone (an antivirus took it): the restart that fails
// is logged and shown, and tried again until the program is back. One attempt was made, and the
// tray kept saying the miner "is started again" while nothing ever would.
func TestARestartThatFailsIsTriedAgain(t *testing.T) {
	tp := startFailWorld(t, map[string]string{"bitcoincashIId.exe": sleeper, "elevenseventyfived.exe": sleeper, "api.exe": sleeper,
		"stratum.exe": `rm -f "$0"; exit 3`})
	boot()
	if !waitFor(5*time.Second, func() bool { return strings.Contains(launcherLog(), "exited on its own") }) {
		t.Fatalf("setup: the miner did not exit; log:\n%s", launcherLog())
	}
	if !waitFor(3*time.Second, func() bool { return tp.has("Forge Solo: the miner could not start: no such file or directory") }) {
		t.Errorf("WIN-RESTART-TRAY: the tray does not say the miner could not start again: %q", tp.all())
	}
	// The first try came restartFirstWait (50 ms) after the exit; the next is 100 ms after it.
	if !strings.Contains(launcherLog(), "the miner could not start: fork/exec ") || !strings.Contains(launcherLog(), "; trying again in 100ms") {
		t.Errorf("WIN-RESTART-LOGGED: the restart that failed is not logged with when it is tried again:\n%s", launcherLog())
	}
	if err := os.WriteFile(ipath("stratum.exe"), []byte("#!/bin/sh\n"+sleeper+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !waitFor(5*time.Second, func() bool { return started("stratum") }) {
		t.Fatalf("WIN-RESTART-RETRIED: the miner was not started again once its program was back; log:\n%s", launcherLog())
	}
}

// The tray says "running" only once both nodes, the API and the miner run; while one could not
// start, it says that instead.
func TestRunningOnlyWithEverythingRunning(t *testing.T) {
	saved, savedTip := dataDir, setTooltip
	dataDir = t.TempDir()
	tp := &tips{}
	setTooltip = tp.add
	dashboardOpen.Store(true)
	t.Cleanup(func() {
		for _, k := range runningKeys {
			stop(k)
		}
		setTooltip, dataDir = savedTip, saved
		dashboardOpen.Store(false)
		resetStartState()
	})
	for _, k := range []string{"bch2", "aux1175", "api"} {
		startHelper(t, k, "stuck", t.TempDir())
	}
	showRunning()
	if tp.has("Forge Solo: running") {
		t.Fatalf("WIN-RUNNING-GATE: the tray said running with no miner: %q", tp.all())
	}
	startHelper(t, "stratum", "stuck", t.TempDir())
	showRunning()
	if tp.last() != "Forge Solo: running" {
		t.Fatalf("WIN-RUNNING-ALL: with everything running the tray says %q", tp.last())
	}
	setTrouble("database", moveFailedTip)
	tp.add("something else")
	showRunning()
	if tp.last() != moveFailedTip {
		t.Fatalf("WIN-RUNNING-DB: with the move of the old data failed, the miner idle, the tray says %q", tp.last())
	}
	clearTrouble("database")
	stop("bch2")
	couldNotStart("bch2", "the BCH2 node", errors.New("held"), time.Minute)
	tp.add("something else")
	showRunning()
	if tp.last() != "Forge Solo: the BCH2 node could not start: held" {
		t.Fatalf("WIN-RUNNING-SAYS-TROUBLE: with the BCH2 node unable to start the tray says %q", tp.last())
	}
}

// A program already running under a key is never started a second time beside it: a start tried
// again and Restart Mining at once would leave two miners, one no longer tracked.
func TestAProgramIsNeverStartedTwice(t *testing.T) {
	saved := dataDir
	dataDir = t.TempDir()
	t.Cleanup(func() { stop("twice"); dataDir = saved })
	startHelper(t, "twice", "stuck", t.TempDir())
	mu.Lock()
	first := procs["twice"]
	mu.Unlock()
	t.Cleanup(func() { _ = first.Process.Kill() }) // no longer tracked if the second start replaced it
	c := exec.Command(os.Args[0], "-test.run=TestHelperProcess")
	c.Env = append(os.Environ(), "FS_HELPER=stuck", "FS_HELPER_DIR="+t.TempDir())
	err := run("twice", c)
	if c.Process != nil {
		_ = c.Process.Kill()
		_, _ = c.Process.Wait()
	}
	if !errors.Is(err, errStarted) || c.Process != nil {
		t.Fatalf("WIN-START-ONCE: a second start of a running program gave %v and started it: %v", err, c.Process != nil)
	}
}

// A program that cannot start is tried again by one loop, however often it is asked to start
// meanwhile (Restart Mining clicked again and again).
func TestOneRetryLoopPerProgram(t *testing.T) {
	startSupervising(t)
	saved := dataDir
	dataDir = t.TempDir()
	restartMu.Lock()
	a, b := restartFirstWait, restartMaxWait
	restartFirstWait, restartMaxWait = 100*time.Millisecond, 100*time.Millisecond
	restartMu.Unlock()
	var tries atomic.Int32
	extraMu.Lock()
	extraSupervised["loopy"] = struct {
		start func() error
		what  string
	}{func() error { tries.Add(1); return errors.New("held by an antivirus") }, "the test program"}
	extraMu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		stopping = true
		mu.Unlock()
		restartsUnderWay.Wait()
		mu.Lock()
		stopping = false
		mu.Unlock()
		extraMu.Lock()
		delete(extraSupervised, "loopy")
		extraMu.Unlock()
		restartMu.Lock()
		restartFirstWait, restartMaxWait = a, b
		restartMu.Unlock()
		resetStartState()
		dataDir = saved
	})
	startOrKeepTrying("loopy")
	startOrKeepTrying("loopy")
	time.Sleep(time.Second)
	if n := tries.Load(); n > 15 {
		t.Fatalf("WIN-RETRY-ONE-LOOP: %d tries in 1 s at one every 100 ms: more than one loop", n)
	}
	if n := tries.Load(); n < 5 {
		t.Fatalf("WIN-RETRY-KEEPS-TRYING: %d tries in 1 s at one every 100 ms", n)
	}
}

// Restart Mining with no miner running, after boot came to start it (its program could not start):
// the miner is started at once, rather than the click doing nothing.
func TestRestartMiningStartsAMinerThatIsNotRunning(t *testing.T) {
	startFailWorld(t, map[string]string{"bitcoincashIId.exe": sleeper, "elevenseventyfived.exe": sleeper, "api.exe": sleeper})
	restartMu.Lock()
	restartFirstWait, restartMaxWait = time.Hour, time.Hour // no retry of its own in this test
	restartMu.Unlock()
	boot()
	if !waitFor(5*time.Second, func() bool { return strings.Contains(launcherLog(), "stratum.exe: no such file or directory") }) {
		t.Fatalf("setup: the miner's failure was not logged:\n%s", launcherLog())
	}
	if err := os.WriteFile(ipath("stratum.exe"), []byte("#!/bin/sh\n"+sleeper+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	restartMiner()
	if !started("stratum") {
		t.Fatalf("WIN-RESTART-MINER-UNTRACKED: Restart Mining did not start the miner that was not running; log:\n%s", launcherLog())
	}
}

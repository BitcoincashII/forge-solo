package main

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A gateway that stops on its own is started again, after a wait, and the log and the tray say so:
// there is no service manager behind the tray app to do it.
func TestAGatewayThatStopsIsStartedAgain(t *testing.T) {
	w := gatewayWorld(t, "crash")
	startSupervising(t)
	if !startOrKeepTrying(gatewayKey) {
		t.Fatal("setup: the gateway did not start")
	}
	if !waitFor(15*time.Second, func() bool { return w.starts() == 2 && started(gatewayKey) }) {
		t.Fatalf("GWL-SUPERVISE: a gateway that stopped on its own was started %d times; log:\n%s", w.starts(), launcherLog())
	}
	if !strings.Contains(launcherLog(), "forge-gateway.exe (gateway) exited on its own after ") || !strings.Contains(launcherLog(), ", exit code 1: starting it again in 50ms\n") ||
		!strings.Contains(launcherLog(), "forge-gateway.exe said: forge-gateway: it crashed\n") ||
		!strings.Contains(launcherLog(), "forge-gateway.exe started again\n") {
		t.Errorf("GWL-SUPERVISE-LOGGED: launcher.log does not say the gateway stopped, why and that it was started again:\n%s", launcherLog())
	}
	if !w.tips.has(tipRestarting("forge-gateway.exe")) {
		t.Errorf("GWL-SUPERVISE-TRAY: the tray never said the gateway stopped and is started again: %q", w.tips.all())
	}
	time.Sleep(300 * time.Millisecond)
	if w.starts() != 2 {
		t.Errorf("GWL-SUPERVISE-ONCE: a gateway that runs again was started %d times", w.starts())
	}
}

// A gateway that keeps stopping at once is started again less and less often, up to the longest
// wait, rather than in a tight loop.
func TestTheWaitGrowsForAGatewayThatKeepsStopping(t *testing.T) {
	w := gatewayWorld(t, "exit1")
	startSupervising(t)
	if !startOrKeepTrying(gatewayKey) {
		t.Fatal("setup: the gateway did not start")
	}
	waitNow := func() time.Duration { restartMu.Lock(); defer restartMu.Unlock(); return restartWaits[gatewayKey] }
	if !waitFor(15*time.Second, func() bool { return waitNow() == 200*time.Millisecond }) {
		t.Fatalf("GWL-SUPERVISE-BACKOFF: after %d starts the wait is %v, not grown to 200ms", w.starts(), waitNow())
	}
	// 50, 100, then 200 ms: after the third stop.
	if n := w.starts(); n < 3 || n > 5 {
		t.Fatalf("GWL-SUPERVISE-BACKOFF: the wait grew to 200ms after %d starts, want 3 to 5", n)
	}
}

// Quit while the gateway waits to be started again: nothing is started.
func TestQuitDuringTheWaitStartsNothing(t *testing.T) {
	w := gatewayWorld(t, "exit1")
	startSupervising(t)
	restartMu.Lock()
	restartFirstWait = time.Second
	restartMu.Unlock()
	if !startOrKeepTrying(gatewayKey) {
		t.Fatal("setup: the gateway did not start")
	}
	if !waitFor(10*time.Second, logHas("exited on its own")) {
		t.Fatalf("setup: the gateway did not stop; log:\n%s", launcherLog())
	}
	stopForExit()
	time.Sleep(1500 * time.Millisecond)
	if n := w.starts(); n != 1 || started(gatewayKey) {
		t.Fatalf("GWL-SUPERVISE-QUIT: the gateway was started %d times, once after Quit", n)
	}
}

// The gateway's config has a mistake (exit code 3): the tray says so, and launcher.log has what the
// gateway said. It is started again all the same: a config fixed by hand is read within a minute.
func TestAConfigMistakeIsSaid(t *testing.T) {
	w := gatewayWorld(t, "exit3")
	startSupervising(t)
	if !startOrKeepTrying(gatewayKey) {
		t.Fatal("setup: the gateway did not start")
	}
	if !waitFor(10*time.Second, logHas(`forge-gateway.exe said: forge-gateway: forge-gateway.json: unknown field "x"`+"\n")) {
		t.Fatalf("GWL-EXIT3-LOGGED: launcher.log does not have what the gateway said:\n%s", launcherLog())
	}
	if !waitFor(5*time.Second, func() bool { return w.tips.has(tipConfigMistake) }) {
		t.Errorf("GWL-EXIT3: the tray never said the config has a mistake: %q", w.tips.all())
	}
	if w.tips.has(tipRestarting("forge-gateway.exe")) {
		t.Errorf("GWL-EXIT3: the tray said the gateway stopped, not that its config has a mistake: %q", w.tips.all())
	}
	if !waitFor(5*time.Second, func() bool { return w.starts() >= 3 }) {
		t.Errorf("GWL-EXIT3-RESTARTS: a gateway with a config mistake was started %d times, not again and again", w.starts())
	}
}

// The gateway could not listen (exit code 4), and no one holds its ports: they are still closing
// the connections of the run before, which Windows keeps a port for, under exclusive address use,
// for minutes. The tray says it waits for them, never that another program has them, and the
// gateway starts by itself once they are free.
func TestPortsStillClosingAreWaitedFor(t *testing.T) {
	w := gatewayWorld(t, "port")
	startSupervising(t)
	writeFile(t, filepath.Join(w.dir, "held"), "1")
	if !startOrKeepTrying(gatewayKey) {
		t.Fatal("setup: the gateway did not start")
	}
	want := "ports " + stratumPort + " and " + statusPort + " are not free yet: the connections of the last run are still closing; trying again in "
	if !waitFor(10*time.Second, logHas(want)) || !waitFor(5*time.Second, func() bool { return w.tips.has(tipPortClosing) }) {
		t.Fatalf("GWL-EXIT4-CLOSING: the tray says %q; launcher.log:\n%s", w.tips.all(), launcherLog())
	}
	if strings.Contains(launcherLog(), "uses port") || strings.Contains(strings.Join(w.tips.all(), "\n"), "uses port") {
		t.Errorf("GWL-EXIT4-CLOSING: ports no one holds were said to be another program's:\n%s", launcherLog())
	}
	_ = os.Remove(filepath.Join(w.dir, "held"))
	if !waitFor(10*time.Second, func() bool { return started(gatewayKey) && askState(t) }) {
		t.Fatalf("GWL-EXIT4-STARTS: the gateway did not start by itself once its ports were free; log:\n%s", launcherLog())
	}
}

// askState reports whether the gateway's status page answers.
func askState(t *testing.T) bool {
	t.Helper()
	_, known := askGatewayState()
	return known
}

// The gateway could not listen (exit code 4) because Forge Solo holds its miner port: the tray says
// so, as at the start, and the gateway is started again with its waits, with no click, so that it
// starts once Forge Solo has quit.
func TestAPortForgeSoloTookIsSaidAndWaitedFor(t *testing.T) {
	w := gatewayWorld(t, "port")
	startSupervising(t)
	n, _ := strconv.Atoi(stratumPort)
	var held atomic.Bool
	held.Store(true)
	holdPort(stratumPort, holder{path: `C:\Users\you\AppData\Local\Programs\ForgeSolo\stratum.exe`, name: "stratum.exe", exe: "stratum.exe"})
	tcpListeners = func() []tcpListener { // until Forge Solo quits
		if held.Load() {
			return []tcpListener{{net.IPv4zero.To4(), n, 4242}}
		}
		return nil
	}
	writeFile(t, filepath.Join(w.dir, "held"), "1")
	if !startOrKeepTrying(gatewayKey) {
		t.Fatal("setup: the gateway did not start")
	}
	tray := "Forge Gateway cannot start: Forge Solo uses port " + stratumPort
	if !waitFor(10*time.Second, func() bool { return w.tips.has(tray) }) {
		t.Fatalf("GWL-EXIT4-SOLO: the tray says %q, not %q; log:\n%s", w.tips.all(), tray, launcherLog())
	}
	log := "Forge Gateway cannot start: Forge Solo uses port " + stratumPort + ", the miner port (stratum.exe, process 4242 listens on it at 0.0.0.0); " +
		"Forge Solo mines on this computer already, and its TIDES mode is the same gateway, built in: run one of the two. To run Forge Gateway, quit Forge Solo (right-click its tray icon, then Quit Forge Solo); Forge Gateway tries again by itself\n"
	if !strings.Contains(launcherLog(), log) {
		t.Errorf("GWL-EXIT4-SOLO-LOGGED: launcher.log does not say Forge Solo holds the port, and that the gateway tries again by itself:\n%s", launcherLog())
	}
	if !waitFor(5*time.Second, func() bool { return w.starts() >= 3 }) {
		t.Fatalf("GWL-EXIT4-RESTARTS: with Forge Solo on the port the gateway was started %d times, not again and again", w.starts())
	}
	if retryOffered.Load() != noRetry {
		t.Error("GWL-EXIT4-NO-CLICK: Try Again was offered for a gateway that is started again by itself")
	}
	held.Store(false)
	_ = os.Remove(filepath.Join(w.dir, "held"))
	if !waitFor(10*time.Second, func() bool { return started(gatewayKey) && askState(t) }) {
		t.Fatalf("GWL-EXIT4-STARTS: once Forge Solo let the port go, the gateway did not start by itself; log:\n%s", launcherLog())
	}
	troubleMu.Lock()
	left := trouble[gatewayKey]
	troubleMu.Unlock()
	if left != "" {
		t.Errorf("GWL-EXIT4-CLEARED: the gateway runs, and the tray still holds %q", left)
	}
}

// A gateway the tray stops (Quit, Restart) is not started again: that is not a stop on its own.
func TestAGatewayStoppedByTheTrayIsNotStartedAgain(t *testing.T) {
	w := gatewayWorld(t, "eof")
	startSupervising(t)
	if !startOrKeepTrying(gatewayKey) {
		t.Fatal("setup: the gateway did not start")
	}
	time.Sleep(300 * time.Millisecond)
	stopGracefully(gatewayKey, 5*time.Second)
	time.Sleep(time.Second)
	if w.starts() != 1 || strings.Contains(launcherLog(), "exited on its own") {
		t.Fatalf("GWL-SUPERVISE-STOPPED: a gateway the tray stopped was started again (%d starts); log:\n%s", w.starts(), launcherLog())
	}
}

// A gateway whose program cannot start (an antivirus holds or removed it) is named in the tray and
// tried again until it starts.
func TestAGatewayThatCannotStartIsTriedAgain(t *testing.T) {
	empty := t.TempDir() // no forge-gateway.exe there; removed after the world's clean-up
	w := gatewayWorld(t, "status")
	startSupervising(t)
	installDir = empty
	if startOrKeepTrying(gatewayKey) {
		t.Fatal("setup: a gateway that is not there started")
	}
	if !strings.Contains(launcherLog(), "forge-gateway.exe could not start: ") || !strings.HasPrefix(w.tips.last(), "Forge Gateway cannot start forge-gateway.exe: ") {
		t.Errorf("GWL-START-FAILS: the gateway's failure is not in launcher.log and the tray: %q\n%s", w.tips.all(), launcherLog())
	}
	if err := copyHelper(ipath(gatewayExe)); err != nil {
		t.Fatal(err)
	}
	if !waitFor(10*time.Second, func() bool { return started(gatewayKey) }) {
		t.Fatalf("GWL-START-RETRIED: the gateway was not started once its program was back; log:\n%s", launcherLog())
	}
}

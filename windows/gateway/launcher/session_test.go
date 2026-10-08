package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// watchWorld runs watchTray with a short wait and stand-ins for starting Forge Gateway again and
// for exiting, and reports what it did.
func watchWorld(t *testing.T, ready bool, relaunched string) (relaunches []string, exits []int) {
	t.Helper()
	savedReady, savedWait, savedRelaunch, savedExit, savedData := trayReady, trayWait, relaunch, exit, dataDir
	t.Cleanup(func() {
		trayReady, trayWait, relaunch, exit, dataDir = savedReady, savedWait, savedRelaunch, savedExit, savedData
	})
	dataDir = t.TempDir()
	t.Setenv("FORGE_GATEWAY_RELAUNCHED", relaunched)
	trayReady, trayWait = make(chan struct{}), 100*time.Millisecond
	if ready {
		close(trayReady)
	}
	relaunch = func(exe string, env ...string) error {
		relaunches = append(relaunches, exe+" "+strings.Join(env, " "))
		return nil
	}
	exit = func(code int) { exits = append(exits, code) }
	watchTray(`C:\fg\forge-gateway-tray.exe`)
	return relaunches, exits
}

// The tray icon came up: nothing to do.
func TestTrayUp(t *testing.T) {
	if r, e := watchWorld(t, true, ""); len(r) != 0 || len(e) != 0 {
		t.Fatalf("GWL-TRAY-WATCH-READY: relaunched %v, exited %v with the tray up", r, e)
	}
}

// The icon never came up (Windows can refuse it while it sets up the taskbar at sign-in), so
// nothing started and nothing will: Forge Gateway starts again, once, and this copy exits.
func TestTrayNeverUpStartsAgain(t *testing.T) {
	r, e := watchWorld(t, false, "")
	if len(r) != 1 || r[0] != `C:\fg\forge-gateway-tray.exe FORGE_GATEWAY_RELAUNCHED=1` || len(e) != 1 || e[0] != 0 {
		t.Fatalf("GWL-TRAY-WATCH-RELAUNCH: relaunched %v, exited %v; want one restart, then an exit", r, e)
	}
	if !strings.Contains(launcherLog(), "the tray icon did not come up in 100ms: starting Forge Gateway again") {
		t.Errorf("GWL-TRAY-WATCH-LOGGED: launcher.log:\n%s", launcherLog())
	}
}

// Started again and still no icon: it exits rather than start itself again and again.
func TestTrayNeverUpAfterARestartExits(t *testing.T) {
	r, e := watchWorld(t, false, "1")
	if len(r) != 0 || len(e) != 1 || e[0] != 1 {
		t.Fatalf("GWL-TRAY-WATCH-ONCE: relaunched %v, exited %v; want no second restart, and an exit", r, e)
	}
}

// sessionWorld resets what the session tests touch.
func sessionWorld(t *testing.T, ending bool) {
	t.Helper()
	savedData, savedWait := dataDir, sessionOutcomeWait
	dataDir = t.TempDir()
	sessionEnding.Store(ending)
	t.Cleanup(func() {
		sessionEnding.Store(false)
		select {
		case <-sessionOutcome:
		default:
		}
		dataDir, sessionOutcomeWait = savedData, savedWait
	})
}

// Windows asked to end the session, Forge Gateway stopped, and then the shutdown was cancelled
// (another program held it up, and someone chose Cancel): Forge Gateway starts again rather than
// leave the PC not mining. When the session does end, it does not.
func TestACancelledShutdownStartsForgeGatewayAgain(t *testing.T) {
	sessionWorld(t, true)
	noteSessionOutcome(false)
	if !relaunchAfterSessionStop() {
		t.Fatal("GWL-SESSION-CANCEL-RESTARTS: a cancelled shutdown left Forge Gateway stopped")
	}
	noteSessionOutcome(true)
	if relaunchAfterSessionStop() {
		t.Fatal("GWL-SESSION-END-EXITS: Forge Gateway would start again while Windows ends the session")
	}
}

// Windows never answering is taken as no shutdown: had the session ended, Forge Gateway would be
// gone.
func TestNoAnswerFromWindowsStartsForgeGatewayAgain(t *testing.T) {
	sessionWorld(t, true)
	sessionOutcomeWait = 100 * time.Millisecond
	if !relaunchAfterSessionStop() {
		t.Fatal("GWL-SESSION-OUTCOME-TIMEOUT: no answer from Windows left Forge Gateway stopped")
	}
}

// Quit is not a session end: nothing is waited for, and nothing starts again.
func TestQuitDoesNotStartForgeGatewayAgain(t *testing.T) {
	sessionWorld(t, false)
	sessionOutcomeWait = 200 * time.Millisecond
	start := time.Now()
	if relaunchAfterSessionStop() || time.Since(start) > 100*time.Millisecond {
		t.Fatal("GWL-SESSION-QUIT-EXITS: Quit started Forge Gateway again, or waited on Windows")
	}
}

// An answer to a question Forge Gateway was never asked is not kept for a later one: a stale "not
// ending" would start Forge Gateway again in the middle of a real shutdown.
func TestAnAnswerWithoutAQuestionIsNotKept(t *testing.T) {
	sessionWorld(t, false)
	noteSessionOutcome(false) // another program refused before Forge Gateway was asked
	sessionEnding.Store(true) // later, Windows asks Forge Gateway
	sessionOutcomeWait = 100 * time.Millisecond
	noteSessionOutcome(true) // and ends the session
	if relaunchAfterSessionStop() {
		t.Fatal("GWL-SESSION-OUTCOME-ONLY-AFTER-QUERY: a stale answer started Forge Gateway again in a real shutdown")
	}
}

// While Windows ends the session the stop does not wait on the tray's tooltip: the taskbar can be
// slow to answer then, and every second goes to the gateway's stop, which Windows ends a few
// seconds later.
func TestSessionEndStopDoesNotWaitOnTheTooltip(t *testing.T) {
	gatewayWorld(t, "eof")
	slow := make(chan struct{})
	tipMu.Lock()
	setTooltip = func(string) { <-slow }
	tipMu.Unlock()
	t.Cleanup(func() {
		close(slow)
		tipMu.Lock() // the stop's tooltip has been set once this is had: it read setTooltip before
		stopShown = false
		tipMu.Unlock()
		sessionEnding.Store(false)
	})
	sessionEnding.Store(true)
	done := make(chan struct{})
	go func() { stopForExit(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("GWL-SESSION-TOOLTIP-NO-WAIT: the stop waited on the tray's tooltip while Windows ended the session")
	}
}

// waitStopped gives up after its time when the stop has not finished.
func TestWaitStoppedGivesUp(t *testing.T) {
	start := time.Now()
	if waitStopped(200 * time.Millisecond) {
		t.Fatal("GWL-SESSION-WAIT-STOPPED: reported a stop that never ran as finished")
	}
	if took := time.Since(start); took < 200*time.Millisecond || took > 2*time.Second {
		t.Fatalf("GWL-SESSION-WAIT-STOPPED: gave up after %v, want about 200 ms", took)
	}
}

// The tray's log goes to launcher.log in the data folder, one timestamped line per entry.
func TestLauncherLog(t *testing.T) {
	saved := dataDir
	dataDir = t.TempDir()
	t.Cleanup(func() { dataDir = saved })
	logf("one %d", 1)
	logf("two")
	b, err := os.ReadFile(dpath("launcher.log"))
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if err != nil || len(lines) != 2 || !strings.HasSuffix(lines[0], " one 1") || !strings.HasSuffix(lines[1], " two") {
		t.Fatalf("GWL-LOG-WRITTEN: %v %q", err, b)
	}
	if _, err := time.Parse("2006-01-02T15:04:05.000Z", strings.SplitN(lines[0], " ", 2)[0]); err != nil {
		t.Errorf("GWL-LOG-TIME: %q does not start with the time: %v", lines[0], err)
	}
}

// Quit while boot still waits for a gateway a previous run left: once it has stopped, boot must not
// start the gateway after the stop has passed it, or Forge Gateway would exit and leave it running.
// Nothing starts once the stop has begun.
func TestQuitDuringBootLeavesNothingRunning(t *testing.T) {
	w := gatewayWorld(t, "eof")
	installedPrograms = func() []runningProgram { return []runningProgram{{101, gatewayExe}} }
	release := make(chan struct{})
	waitPID = func(int, time.Duration) bool { <-release; return true }
	booted := make(chan struct{})
	go func() { boot(); close(booted) }()
	if !waitFor(5*time.Second, logHas("waiting for it to stop")) {
		t.Fatal("setup: boot did not come to the leftover")
	}
	stopForExit() // what Quit runs, before the exit
	close(release)
	select {
	case <-booted:
	case <-time.After(10 * time.Second):
		t.Fatal("GWL-QUIT-BOOT: boot did not end after the stop")
	}
	time.Sleep(500 * time.Millisecond)
	if started(gatewayKey) || w.starts() != 0 || len(w.opened.all()) != 0 {
		t.Errorf("GWL-QUIT-BOOT: after the stop the gateway was started (%d) or the status page opened (%v): the exit would leave it behind", w.starts(), w.opened.all())
	}
	if err := runPiped("late", exec.Command(ipath(gatewayExe)), nil); err != errStopping {
		t.Errorf("GWL-QUIT-REFUSES-STARTS: a start after the stop gave %v, want errStopping", err)
	}
}

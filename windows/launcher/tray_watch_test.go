package main

import (
	"testing"
	"time"
)

// watchWorld runs watchTray with a short wait and stand-ins for starting Forge Solo again and for
// exiting, and reports what it did.
func watchWorld(t *testing.T, ready bool, relaunched string) (relaunches []string, exits []int) {
	t.Helper()
	savedReady, savedWait, savedRelaunch, savedExit, savedData := trayReady, trayWait, relaunch, exit, dataDir
	t.Cleanup(func() {
		trayReady, trayWait, relaunch, exit, dataDir = savedReady, savedWait, savedRelaunch, savedExit, savedData
	})
	dataDir = t.TempDir()
	t.Setenv("FORGE_SOLO_RELAUNCHED", relaunched)
	trayReady, trayWait = make(chan struct{}), 100*time.Millisecond
	if ready {
		close(trayReady)
	}
	relaunch = func(exe string) error { relaunches = append(relaunches, exe); return nil }
	exit = func(code int) { exits = append(exits, code) }
	watchTray(`C:\fs\forge-solo.exe`)
	return relaunches, exits
}

// The tray icon came up: nothing to do.
func TestTrayUp(t *testing.T) {
	if r, e := watchWorld(t, true, ""); len(r) != 0 || len(e) != 0 {
		t.Fatalf("TRAY-READY: relaunched %v, exited %v with the tray up", r, e)
	}
}

// The icon never came up, so nothing started and nothing will: Forge Solo starts again, once, and
// this copy exits.
func TestTrayNeverUpStartsAgain(t *testing.T) {
	r, e := watchWorld(t, false, "")
	if len(r) != 1 || r[0] != `C:\fs\forge-solo.exe` || len(e) != 1 || e[0] != 0 {
		t.Fatalf("TRAY-RELAUNCH: relaunched %v, exited %v; want one restart, then an exit", r, e)
	}
}

// Started again and still no icon: it exits rather than start itself again and again.
func TestTrayNeverUpAfterARestartExits(t *testing.T) {
	r, e := watchWorld(t, false, "1")
	if len(r) != 0 || len(e) != 1 || e[0] != 1 {
		t.Fatalf("TRAY-RELAUNCH-ONCE: relaunched %v, exited %v; want no second restart, and an exit", r, e)
	}
}

package main

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// leftovers stands in for the operating system: the programs running from the install folder,
// which of them stop by themselves, and what was ended.
type leftovers struct {
	mu     sync.Mutex
	exited map[int]bool
	killed []int
}

func leftoverWorld(t *testing.T, progs []runningProgram, stopsBy map[int]time.Duration) *leftovers {
	t.Helper()
	gatewayWorld(t, "eof")
	l := &leftovers{exited: map[int]bool{}}
	gatewayStopGrace = 300 * time.Millisecond
	start := time.Now()
	installedPrograms = func() []runningProgram { return progs }
	waitPID = func(pid int, d time.Duration) bool {
		for end := time.Now().Add(d); ; time.Sleep(10 * time.Millisecond) {
			l.mu.Lock()
			gone := l.exited[pid]
			l.mu.Unlock()
			if by, ok := stopsBy[pid]; gone || ok && time.Since(start) >= by {
				return true
			}
			if !time.Now().Before(end) {
				return false
			}
		}
	}
	killPID = func(pid int) error {
		l.mu.Lock()
		l.killed = append(l.killed, pid)
		l.exited[pid] = true
		l.mu.Unlock()
		return nil
	}
	return l
}

// A tray that crashed mid-start left forge-gateway.exe running from the install folder, holding the
// ports: the next start waits the time a stop has for it to stop, then ends it. Programs other than
// the gateway are left alone, and so is a gateway that stops by itself in time.
func TestLeftoversAreStopped(t *testing.T) {
	l := leftoverWorld(t, []runningProgram{{101, "forge-gateway.exe"}, {102, "forge-gateway-tray.exe"}, {103, "unins000.exe"}, {104, "forge-gateway.exe"}},
		map[int]time.Duration{104: 100 * time.Millisecond})
	start := time.Now()
	stopLeftovers()
	took := time.Since(start)
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.killed) != 1 || l.killed[0] != 101 {
		t.Errorf("GWL-LEFTOVER: ended %v, want only the leftover gateway that did not stop (101)", l.killed)
	}
	if took < 300*time.Millisecond {
		t.Errorf("GWL-LEFTOVER-WAITS: the leftover gateway was ended after %v, before the time a stop has", took)
	}
	for _, want := range []string{
		"a previous run left forge-gateway.exe running (process 101): waiting for it to stop\n",
		"the forge-gateway.exe a previous run left did not stop in 300ms: ended\n",
		"a previous run left forge-gateway.exe running (process 104): waiting for it to stop\n",
		"the forge-gateway.exe a previous run left stopped in ",
	} {
		if !strings.Contains(launcherLog(), want) {
			t.Errorf("GWL-LEFTOVER-LOGGED: launcher.log lacks %q:\n%s", want, launcherLog())
		}
	}
	if strings.Contains(launcherLog(), "process 102") || strings.Contains(launcherLog(), "process 103") {
		t.Errorf("GWL-LEFTOVER-OURS-ONLY: programs other than the gateway were taken for leftovers:\n%s", launcherLog())
	}
}

// With nothing left running, nothing is waited for, and the start goes on at once.
func TestNoLeftovers(t *testing.T) {
	l := leftoverWorld(t, nil, nil)
	start := time.Now()
	stopLeftovers()
	if took := time.Since(start); took > 100*time.Millisecond || len(l.killed) != 0 || launcherLog() != "" {
		t.Errorf("GWL-LEFTOVER-NONE: with no leftovers the start waited %v, ended %v, logged %q", took, l.killed, launcherLog())
	}
}

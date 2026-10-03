package main

import (
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

// fakePostmaster writes a postmaster.pid for pid (none when pid is 0) and puts a stand-in in
// signalPostgres that records the signal and, like the server, removes the file after `after`.
func fakePostmaster(t *testing.T, pid int, after time.Duration, fail error) *[]byte {
	t.Helper()
	saved, savedSignal, savedProgs := dataDir, signalPostgres, installedPrograms
	dataDir = t.TempDir()
	t.Cleanup(func() { dataDir, signalPostgres, installedPrograms = saved, savedSignal, savedProgs })
	installedPrograms = func() []runningProgram { return []runningProgram{{4242, "postgres.exe"}} }
	md(dpath("pgdata"))
	pidFile := dpath("pgdata", "postmaster.pid") // the stand-in's timer must not read dataDir after the test
	if pid != 0 {
		if err := os.WriteFile(pidFile, []byte("4242\nC:/x/pgdata\n1700000000\n30000\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var mu2 sync.Mutex
	sent := &[]byte{}
	signalPostgres = func(p int, sig byte) error {
		mu2.Lock()
		*sent = append(*sent, sig)
		mu2.Unlock()
		if p != 4242 {
			t.Errorf("DB-STOP-PID: signalled pid %d, want 4242 from postmaster.pid", p)
		}
		if fail != nil {
			return fail
		}
		time.AfterFunc(after, func() { _ = os.Remove(pidFile) })
		return nil
	}
	return sent
}

// The database is stopped as pg_ctl -m fast stops it, without starting pg_ctl: one SIGINT (2) to
// the server named in postmaster.pid, then a wait until the server has removed that file.
func TestStopDatabaseSignalsAndWaits(t *testing.T) {
	sent := fakePostmaster(t, 4242, 300*time.Millisecond, nil)
	stopDatabase()
	if len(*sent) != 1 || (*sent)[0] != 2 {
		t.Fatalf("DB-STOP-SIGNAL: sent %v, want one SIGINT (2), a fast shutdown", *sent)
	}
	if postmasterPID() != 0 {
		t.Fatal("DB-STOP-WAITS: stopDatabase returned while the server was still running")
	}
}

// With no postmaster.pid there is no server to stop, and nothing is signalled.
func TestStopDatabaseWithoutAServer(t *testing.T) {
	sent := fakePostmaster(t, 0, 0, nil)
	stopDatabase()
	if len(*sent) != 0 {
		t.Fatalf("DB-STOP-NONE: signalled %v with no server running", *sent)
	}
}

// postmaster.pid outlives a server that crashed, and its number can since have gone to another
// program: one that is not this install's database is not signalled.
func TestStopDatabaseLeavesAnotherProgram(t *testing.T) {
	sent := fakePostmaster(t, 4242, 0, nil)
	installedPrograms = func() []runningProgram { return []runningProgram{{4242, "notepad.exe"}} }
	stopDatabase()
	if len(*sent) != 0 {
		t.Fatalf("DB-STOP-OURS-ONLY: signalled process 4242, which is not this install's database")
	}
}

// A signal that cannot be delivered is not waited on.
func TestStopDatabaseSignalFails(t *testing.T) {
	fakePostmaster(t, 4242, 0, errors.New("no pipe"))
	start := time.Now()
	stopDatabase()
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("DB-STOP-FAILED: waited %v for a server that was never signalled", took)
	}
}

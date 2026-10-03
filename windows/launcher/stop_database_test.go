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
	saved, savedSignal := dataDir, signalPostgres
	dataDir = t.TempDir()
	t.Cleanup(func() { dataDir, signalPostgres = saved, savedSignal })
	md(dpath("pgdata"))
	if pid != 0 {
		if err := os.WriteFile(dpath("pgdata", "postmaster.pid"), []byte("4242\nC:/x/pgdata\n1700000000\n30000\n"), 0o600); err != nil {
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
		time.AfterFunc(after, func() { _ = os.Remove(dpath("pgdata", "postmaster.pid")) })
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

// A signal that cannot be delivered is not waited on.
func TestStopDatabaseSignalFails(t *testing.T) {
	fakePostmaster(t, 4242, 0, errors.New("no pipe"))
	start := time.Now()
	stopDatabase()
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("DB-STOP-FAILED: waited %v for a server that was never signalled", took)
	}
}

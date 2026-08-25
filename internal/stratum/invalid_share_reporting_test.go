package stratum

import (
	"sync"
	"sync/atomic"
	"testing"
)

// Every rejection path must report the reject onward, not just bump counters that
// nothing reads.
//
// The dashboard's "Reject %" tile is fed by stats.WorkerStats.InvalidShares, whose
// only increment site sits behind an already-validated share and so could never
// fire. Rejects landed in Client.InvalidShares instead, which no API surfaces. The
// tile was therefore a constant 0.00% -- including for stales and duplicates, the
// two things an overclocked miner actually produces, and the exact failure a solo
// miner watches that number to catch.
func TestNoteInvalidShareReportsToTheStatsHook(t *testing.T) {
	s := &Server{stats: &ServerStats{}}
	var mu sync.Mutex
	var got []string
	s.SetInvalidShareHandler(func(minerID, workerName, reason string) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, minerID+"/"+workerName+"/"+reason)
	})

	c := &Client{MinerID: "bitcoincashii:qreject00000000000000000000000000000000", WorkerName: "rig1"}
	s.noteInvalidShare(c, "duplicate")

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("a rejected share was counted but never reported to the stats hook "+
			"(%d calls); the Reject %% tile stays at 0.00%% no matter what the miner sends", len(got))
	}
	if want := "bitcoincashii:qreject00000000000000000000000000000000/rig1/duplicate"; got[0] != want {
		t.Fatalf("reject reported as %q, want %q", got[0], want)
	}
	if n := atomic.LoadInt64(&c.InvalidShares); n != 1 {
		t.Errorf("client counter = %d, want 1", n)
	}
	if n := atomic.LoadInt64(&s.stats.InvalidShares); n != 1 {
		t.Errorf("server counter = %d, want 1", n)
	}
}

// An unauthorized connection has no worker to charge, and must not invent one.
func TestNoteInvalidShareSkipsUnauthorizedClients(t *testing.T) {
	s := &Server{stats: &ServerStats{}}
	var calls int32
	s.SetInvalidShareHandler(func(_, _, _ string) { atomic.AddInt32(&calls, 1) })

	s.noteInvalidShare(&Client{}, "malformed_params") // no MinerID yet
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Fatalf("reported %d rejects for a client that has not authorized", n)
	}
	if n := atomic.LoadInt64(&s.stats.InvalidShares); n != 1 {
		t.Errorf("server total should still count it: got %d, want 1", n)
	}
}

// No handler set (the stratum used standalone in tests) must not panic.
func TestNoteInvalidShareWithoutAHandler(t *testing.T) {
	s := &Server{stats: &ServerStats{}}
	s.noteInvalidShare(&Client{MinerID: "m", WorkerName: "w"}, "invalid")
	if n := atomic.LoadInt64(&s.stats.InvalidShares); n != 1 {
		t.Fatalf("server total = %d, want 1", n)
	}
}

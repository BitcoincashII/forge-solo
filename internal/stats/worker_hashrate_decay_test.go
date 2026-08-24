package stats

import (
	"testing"
	"time"
)

// A worker's 5m/60m hashrate must reflect the present, not its last-ever share.
//
// UpdateWorker was the only writer of these fields, so a worker that stopped
// submitting kept advertising whatever it was doing when it last spoke -- forever.
// An unplugged 100 TH/s rig went on reporting "100.00 TH/s" under a column header
// that says "5m Hash", in a row the same call had already marked offline, directly
// beneath a tile reading 0 H/s.
func TestWorkerHashrateDecaysAfterTheMinerStops(t *testing.T) {
	m := &StatsManager{workers: make(map[string]*WorkerStats)}

	const (
		miner  = "bitcoincashii:qzeh9rcyyy8jlyalgh84e8fst6xh649hly2tfwgvwc"
		worker = "rig1"
		diff   = 1_000_000.0
	)
	m.UpdateWorker(miner, worker, true, diff, diff)

	live := m.GetAllWorkerStats()
	if len(live) != 1 {
		t.Fatalf("expected 1 worker, got %d", len(live))
	}
	if live[0].Hashrate5m <= 0 {
		t.Fatalf("a worker that just submitted reports %v for 5m", live[0].Hashrate5m)
	}
	if !live[0].Online {
		t.Fatal("a worker that just submitted is not online")
	}

	// Age the share out of the 5m window without touching the worker again --
	// exactly what an unplugged rig does.
	key := miner + ":" + worker
	w := m.workers[key]
	old := time.Now().Add(-30 * time.Minute)
	w.LastShareAt = old
	w.ShareBuffer = NewCircularShareBuffer(MaxSharesPerWorker)
	w.ShareBuffer.Add(ShareRecord{Time: old, Difficulty: diff})

	stale := m.GetAllWorkerStats()
	if stale[0].Online {
		t.Error("a worker silent for 30 minutes is still marked online")
	}
	if stale[0].Hashrate5m != 0 {
		t.Errorf("5m hashrate = %v after 30 minutes of silence, want 0 — the row "+
			"advertises a rate the miner is not producing", stale[0].Hashrate5m)
	}
	// 60m still sees the share, which is correct: it IS within the last hour.
	if stale[0].Hashrate60m <= 0 {
		t.Errorf("60m hashrate = %v; a share 30 minutes old is inside the 60m window",
			stale[0].Hashrate60m)
	}

	// Past the hour, both windows must be empty.
	older := time.Now().Add(-90 * time.Minute)
	w.ShareBuffer = NewCircularShareBuffer(MaxSharesPerWorker)
	w.ShareBuffer.Add(ShareRecord{Time: older, Difficulty: diff})
	gone := m.GetAllWorkerStats()
	if gone[0].Hashrate5m != 0 || gone[0].Hashrate60m != 0 {
		t.Errorf("after 90 minutes of silence: 5m=%v 60m=%v, want 0/0",
			gone[0].Hashrate5m, gone[0].Hashrate60m)
	}
}

// The read must not mutate the stored worker -- GetAllWorkerStats is a reader and
// runs under a read lock.
func TestGetAllWorkerStatsDoesNotMutateStoredWorkers(t *testing.T) {
	m := &StatsManager{workers: make(map[string]*WorkerStats)}
	m.UpdateWorker("miner", "w", true, 1000, 1000)

	stored := m.workers["miner:w"]
	before5, before60 := stored.Hashrate5m, stored.Hashrate60m

	stored.LastShareAt = time.Now().Add(-2 * time.Hour)
	stored.ShareBuffer = NewCircularShareBuffer(MaxSharesPerWorker)
	stored.ShareBuffer.Add(ShareRecord{Time: time.Now().Add(-2 * time.Hour), Difficulty: 1000})

	_ = m.GetAllWorkerStats()

	if stored.Hashrate5m != before5 || stored.Hashrate60m != before60 {
		t.Errorf("GetAllWorkerStats wrote through to the stored worker (%v/%v -> %v/%v); "+
			"it holds only a read lock", before5, before60, stored.Hashrate5m, stored.Hashrate60m)
	}
}

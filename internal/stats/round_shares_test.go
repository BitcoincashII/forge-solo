package stats

import "testing"

// "Round Shares" must mean shares since the last block.
//
// The tile was fed data.validShares -- the identical field the all-time "Valid
// Shares" tile uses -- so the two were byte-identical always, and this one sat
// inside the Round Effort card beside a Current Effort and a Best Difficulty that
// the round reset HAD cleared. On mainnet that is a lifetime total in the millions
// sitting next to "Current Effort 0.0%" immediately after a block is found.
func TestRoundSharesResetOnBlockFoundButValidSharesDoNot(t *testing.T) {
	m := &StatsManager{workers: make(map[string]*WorkerStats)}
	const miner, worker = "bitcoincashii:qzeh9rcyyy8jlyalgh84e8fst6xh649hly2tfwgvwc", "rig1"

	for i := 0; i < 5; i++ {
		m.UpdateWorker(miner, worker, true, 1000, 1000)
	}
	w := m.workers[miner+":"+worker]
	if w.ValidShares != 5 || w.RoundShares != 5 {
		t.Fatalf("before the block: valid=%d round=%d, want 5/5", w.ValidShares, w.RoundShares)
	}

	m.ResetWorkerRoundStats(miner)

	if w.RoundShares != 0 {
		t.Errorf("RoundShares = %d after a block was found, want 0 — the tile keeps "+
			"reporting the previous round's shares against a round with no work in it",
			w.RoundShares)
	}
	if w.ValidShares != 5 {
		t.Errorf("ValidShares = %d after a block, want 5 — the all-time count must survive",
			w.ValidShares)
	}
	// The round counter must agree with the other round fields the same reset clears.
	if w.TotalWork != 0 || w.BestDiff != 0 {
		t.Errorf("round reset left TotalWork=%v BestDiff=%v", w.TotalWork, w.BestDiff)
	}

	// And it must start counting again.
	m.UpdateWorker(miner, worker, true, 1000, 1000)
	if w.RoundShares != 1 {
		t.Errorf("RoundShares = %d after one share in the new round, want 1", w.RoundShares)
	}
	if w.ValidShares != 6 {
		t.Errorf("ValidShares = %d, want 6", w.ValidShares)
	}
}

// The all-miners reset (used when a block is found by anyone) must behave the same.
func TestResetAllWorkerRoundStatsClearsRoundShares(t *testing.T) {
	m := &StatsManager{workers: make(map[string]*WorkerStats)}
	m.UpdateWorker("a", "w1", true, 1000, 1000)
	m.UpdateWorker("b", "w2", true, 1000, 1000)

	m.ResetAllWorkerRoundStats()

	for key, w := range m.workers {
		if w.RoundShares != 0 {
			t.Errorf("%s: RoundShares = %d after the round reset, want 0", key, w.RoundShares)
		}
		if w.ValidShares != 1 {
			t.Errorf("%s: ValidShares = %d, want 1 (all-time must survive)", key, w.ValidShares)
		}
	}
}

// A rejected share must not count toward the round.
func TestRejectedSharesDoNotCountTowardTheRound(t *testing.T) {
	m := &StatsManager{workers: make(map[string]*WorkerStats)}
	m.UpdateWorker("a", "w", true, 1000, 1000)
	m.RecordInvalidShare("a", "w")
	m.RecordInvalidShare("a", "w")

	w := m.workers["a:w"]
	if w.RoundShares != 1 {
		t.Errorf("RoundShares = %d, want 1 — rejects are being credited as round work", w.RoundShares)
	}
	if w.InvalidShares != 2 {
		t.Errorf("InvalidShares = %d, want 2", w.InvalidShares)
	}
}

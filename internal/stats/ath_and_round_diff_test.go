package stats

import "testing"

// The Workers table shows two difficulty columns that mean different things, and nothing
// pinned the difference: "Round Best" is this round's best share and must reset when a block
// is found, while "ATH Diff" is titled "since the mining service last started" and must
// survive every round. Only ValidShares and RoundShares were covered, so a reset that also
// zeroed ATHDiff would have gone unnoticed -- and a miner's best-ever share silently
// disappearing is the kind of thing a user notices and cannot explain.
func TestATHSurvivesTheRoundResetButRoundBestDoesNot(t *testing.T) {
	m := &StatsManager{workers: make(map[string]*WorkerStats)}
	m.UpdateWorker("miner", "w1", true, 1000, 87_653_032) // a big one

	w := m.workers["miner:w1"]
	if w.ATHDiff != 87_653_032 || w.RoundBestDiff != 87_653_032 {
		t.Fatalf("before reset: ath=%v roundBest=%v, want both 87653032", w.ATHDiff, w.RoundBestDiff)
	}

	m.ResetAllWorkerRoundStats()

	if w.ATHDiff != 87_653_032 {
		t.Errorf("ATHDiff = %v after a block was found; the all-time high must survive the round", w.ATHDiff)
	}
	if w.RoundBestDiff != 0 {
		t.Errorf("RoundBestDiff = %v after the reset, want 0", w.RoundBestDiff)
	}
	if w.BestDiff != 0 {
		t.Errorf("BestDiff = %v after the reset, want 0", w.BestDiff)
	}

	// A smaller share now leads the new round without disturbing the all-time high.
	m.UpdateWorker("miner", "w1", true, 1000, 5_000)
	if w.RoundBestDiff != 5_000 {
		t.Errorf("RoundBestDiff = %v; the first share of a new round must set it", w.RoundBestDiff)
	}
	if w.ATHDiff != 87_653_032 {
		t.Errorf("ATHDiff = %v; a smaller share must not lower the all-time high", w.ATHDiff)
	}
}

// ATH only ever moves up, and only on a genuinely larger share.
func TestATHOnlyRisesAndTracksTheActualShareDifficulty(t *testing.T) {
	m := &StatsManager{workers: make(map[string]*WorkerStats)}
	// target difficulty stays 1000 throughout; only the ACTUAL share difficulty should matter.
	for _, actual := range []float64{500, 9_000, 1_200, 9_000, 40_000} {
		m.UpdateWorker("miner", "w1", true, 1000, actual)
	}
	w := m.workers["miner:w1"]
	if w.ATHDiff != 40_000 {
		t.Errorf("ATHDiff = %v, want 40000 (the largest actual share seen)", w.ATHDiff)
	}
	if w.RoundBestDiff != 40_000 {
		t.Errorf("RoundBestDiff = %v, want 40000", w.RoundBestDiff)
	}
}

// A rejected share must not set either figure: a miner cannot claim a best share the pool
// refused to credit.
func TestRejectedSharesDoNotSetBestOrATH(t *testing.T) {
	m := &StatsManager{workers: make(map[string]*WorkerStats)}
	m.UpdateWorker("miner", "w1", true, 1000, 2_000)
	before := m.workers["miner:w1"].ATHDiff

	m.UpdateWorker("miner", "w1", false, 1000, 999_999) // invalid, enormous
	w := m.workers["miner:w1"]
	if w.ATHDiff != before {
		t.Errorf("ATHDiff = %v after an INVALID share, want it unchanged at %v", w.ATHDiff, before)
	}
	if w.RoundBestDiff != before {
		t.Errorf("RoundBestDiff = %v after an INVALID share, want %v", w.RoundBestDiff, before)
	}
}

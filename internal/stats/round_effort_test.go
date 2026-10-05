package stats

import (
	"encoding/json"
	"math"
	"testing"
)

// The live run's shape: 3.1G of work at 1.4736G, then the next block's job at 0.9923G. Counted
// share by share the round goes 210.4% to 210.9%; the round's work over the new difficulty went
// from 236% to 313% on that one share.
func TestRoundEffortIsCountedShareByShare(t *testing.T) {
	m := &StatsManager{workers: make(map[string]*WorkerStats)}
	const d = 5e6
	// A share is credited at its target difficulty, whatever its hash happened to reach.
	for i := 0; i < 620; i++ {
		m.UpdateWorkerForJob("miner", "w1", true, d, 7*d, 1.4736e9)
	}
	before := m.workers["miner:w1"].RoundEffort
	if math.Abs(before-620*d/1.4736e9) > 1e-12 {
		t.Fatalf("STATS-EFFORT-SUM: 620 shares of 5M at 1.4736G make %v of a block, want %v", before, 620*d/1.4736e9)
	}
	m.UpdateWorkerForJob("miner", "w1", true, d, d, 0.9923e9)
	if got, want := m.workers["miner:w1"].RoundEffort-before, d/0.9923e9; math.Abs(got-want) > 1e-12 {
		t.Fatalf("STATS-EFFORT-RETARGET: one share on the next block's job added %v, want %v", got, want)
	}

	// A refused share is no work; a share whose job difficulty is not known adds nothing (no Inf).
	e := m.workers["miner:w1"].RoundEffort
	m.UpdateWorkerForJob("miner", "w1", false, d, d, 1e9)
	m.UpdateWorkerForJob("miner", "w1", true, d, d, 0)
	m.UpdateWorker("miner", "w1", true, d, d)
	if got := m.workers["miner:w1"].RoundEffort; got != e {
		t.Fatalf("STATS-EFFORT-NOT-WORK: a refused share or one with no job difficulty moved the round from %v to %v", e, got)
	}

	// The API reads it from /internal/workers.
	b, _ := json.Marshal(m.GetAllWorkerStats()[0])
	var wire map[string]any
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	if v, _ := wire["round_effort"].(float64); v != e {
		t.Fatalf("STATS-EFFORT-WIRE: /internal/workers sends round_effort %v, want %v", wire["round_effort"], e)
	}
}

// The round ends where TotalWork's does: at a block this miner found (solo or TIDES), and for
// every worker at a PPLNS block. A round's effort that outlived its round would start the next
// one at 200%.
func TestRoundEffortEndsWithTheRound(t *testing.T) {
	m := &StatsManager{workers: make(map[string]*WorkerStats)}
	m.UpdateWorkerForJob("a", "w1", true, 1000, 1000, 2000)
	m.UpdateWorkerForJob("b", "w2", true, 1000, 1000, 2000)
	m.ResetWorkerRoundStats("a")
	if a, b := m.workers["a:w1"], m.workers["b:w2"]; a.RoundEffort != 0 || a.TotalWork != 0 || b.RoundEffort != 0.5 {
		t.Fatalf("STATS-EFFORT-RESET-MINER: after a's block a=%v (work %v), b=%v; want 0, 0, 0.5", a.RoundEffort, a.TotalWork, b.RoundEffort)
	}
	m.UpdateWorkerForJob("a", "w1", true, 1000, 1000, 2000)
	m.ResetAllWorkerRoundStats()
	for k, w := range m.workers {
		if w.RoundEffort != 0 || w.TotalWork != 0 {
			t.Fatalf("STATS-EFFORT-RESET-ALL: %s kept effort %v (work %v) past a block for everyone", k, w.RoundEffort, w.TotalWork)
		}
	}
}

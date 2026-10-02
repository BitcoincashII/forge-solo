package stats

import (
	"fmt"
	"testing"
)

// A worker that has never had a share accepted costs a client nothing to invent, so only a few such
// entries are kept. Workers that do real work are never capped, and a real worker's refused shares
// still count against it.
func TestRejectOnlyWorkersAreCapped(t *testing.T) {
	m := &StatsManager{workers: make(map[string]*WorkerStats)}
	const miner = "bitcoincashii:qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzse6qye33q"
	m.UpdateWorker(miner, "real", true, 1, 1)
	for i := 0; i < 1000; i++ {
		m.RecordInvalidShare(miner, fmt.Sprintf("junk%d", i))
	}
	if n := len(m.workers); n != 1+MaxRejectOnlyWorkers {
		t.Fatalf("WORKER-CAP-REJECT-ONLY: %d worker entries after 1000 invented names, want %d", n, 1+MaxRejectOnlyWorkers)
	}
	m.RecordInvalidShare(miner, "real")
	if w := m.workers[miner+":real"]; w == nil || w.InvalidShares != 1 {
		t.Fatalf("WORKER-REAL-COUNTED: a real worker's refused share was not counted (%+v)", w)
	}
	m.UpdateWorker(miner, "new", true, 1, 1)
	if m.workers[miner+":new"] == nil {
		t.Fatal("WORKER-REAL-NEW: a new worker with an accepted share was not recorded")
	}
}

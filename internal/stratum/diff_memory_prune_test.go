package stratum

import (
	"testing"
	"time"
)

// diffMemory remembers a miner's last vardiff level so a reconnect does not restart at the
// floor. The read path ignores an entry past diffMemoryTTL, but nothing ever deleted one, so
// the map grew for the life of the process -- keyed by miner+worker name, which a marketplace
// order rotates. The staleness was invisible precisely because stale entries were never
// returned, which is what let it go unnoticed.
func TestDiffMemoryPrunesExpiredButKeepsLive(t *testing.T) {
	s := NewServerForTest()
	now := time.Now()

	fresh := diffMemoryKey("miner", "live")
	stale := diffMemoryKey("miner", "rotated-away")
	// Exactly at the TTL boundary must survive: only strictly older entries are junk.
	edge := diffMemoryKey("miner", "edge")

	s.diffMemory.Store(fresh, diffMem{diff: 1024, at: now})
	s.diffMemory.Store(stale, diffMem{diff: 2048, at: now.Add(-diffMemoryTTL - time.Minute)})
	s.diffMemory.Store(edge, diffMem{diff: 4096, at: now.Add(-diffMemoryTTL + time.Second)})

	s.cleanupDiffMemory()

	if _, ok := s.diffMemory.Load(stale); ok {
		t.Error("an entry past the TTL survived pruning; the map is still unbounded")
	}
	for _, k := range []string{fresh, edge} {
		if _, ok := s.diffMemory.Load(k); !ok {
			t.Errorf("pruning dropped a live entry (%q); a reconnecting miner would restart at the floor", k)
		}
	}

	// And the surviving value must be intact, not merely present.
	v, _ := s.diffMemory.Load(fresh)
	if m, ok := v.(diffMem); !ok || m.diff != 1024 {
		t.Errorf("live entry corrupted by pruning: %#v", v)
	}
}

// A map with nothing in it, and one holding only live entries, must both be no-ops.
func TestDiffMemoryPruneIsSafeWhenNothingIsStale(t *testing.T) {
	s := NewServerForTest()
	s.cleanupDiffMemory() // empty: must not panic

	s.diffMemory.Store(diffMemoryKey("m", "w"), diffMem{diff: 512, at: time.Now()})
	s.cleanupDiffMemory()
	n := 0
	s.diffMemory.Range(func(_, _ interface{}) bool { n++; return true })
	if n != 1 {
		t.Errorf("pruning a map with no stale entries left %d entries, want 1", n)
	}
}

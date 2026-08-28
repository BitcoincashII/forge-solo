package stats

import (
	"math/rand"
	"testing"
	"time"
)

// The ring grows on demand, so it must still behave exactly like the eager version it
// replaced. This compares it against a plain reference model -- keep the last cap records in
// arrival order -- across every fill state: empty, partially filled, exactly full, and
// wrapped many times over.
func TestShareBufferMatchesAReferenceModel(t *testing.T) {
	const cap = 7
	base := time.Unix(1_700_000_000, 0)
	rng := rand.New(rand.NewSource(1))

	for trial := 0; trial < 200; trial++ {
		b := NewCircularShareBuffer(cap)
		var model []ShareRecord

		n := rng.Intn(30)
		for i := 0; i < n; i++ {
			r := ShareRecord{Time: base.Add(time.Duration(i) * time.Second), Difficulty: float64(i)}
			b.Add(r)
			model = append(model, r)
			if len(model) > cap {
				model = model[len(model)-cap:]
			}
		}

		// Compare across cutoffs that fall before, inside and after the retained window.
		for _, off := range []int{-1, 0, 1, n / 2, n - 1, n, n + 5} {
			cutoff := base.Add(time.Duration(off) * time.Second)
			var want []ShareRecord
			for _, r := range model {
				if r.Time.After(cutoff) {
					want = append(want, r)
				}
			}
			got := b.GetRecordsAfter(cutoff)
			if len(got) != len(want) {
				t.Fatalf("trial %d n=%d cutoff=%ds: got %d records, want %d",
					trial, n, off, len(got), len(want))
			}
			for i := range want {
				if !got[i].Time.Equal(want[i].Time) || got[i].Difficulty != want[i].Difficulty {
					t.Fatalf("trial %d n=%d cutoff=%ds: record %d = %v/%v, want %v/%v",
						trial, n, off, i, got[i].Time, got[i].Difficulty, want[i].Time, want[i].Difficulty)
				}
			}
		}
	}
}

// The point of the change: a worker that submits a few shares must not cost a full ring.
func TestShareBufferAllocatesOnDemand(t *testing.T) {
	b := NewCircularShareBuffer(MaxSharesPerWorker)
	if got := len(b.records); got != 0 {
		t.Fatalf("a fresh buffer allocated %d records; it must allocate nothing until a share arrives", got)
	}
	now := time.Now()
	for i := 0; i < 5; i++ {
		b.Add(ShareRecord{Time: now, Difficulty: 1})
	}
	if got := len(b.records); got != 5 {
		t.Errorf("after 5 shares the backing array holds %d records, want 5", got)
	}
	if b.size != 5 {
		t.Errorf("size = %d, want 5", b.size)
	}
	// And it must still cap: filling past capacity may not grow beyond it.
	small := NewCircularShareBuffer(4)
	for i := 0; i < 50; i++ {
		small.Add(ShareRecord{Time: now.Add(time.Duration(i) * time.Second), Difficulty: float64(i)})
	}
	if len(small.records) != 4 || small.size != 4 {
		t.Errorf("bounded ring grew to len=%d size=%d, want 4/4", len(small.records), small.size)
	}
}

// A zero-capacity ring must not panic (% 0).
func TestShareBufferZeroCapacityIsInert(t *testing.T) {
	b := NewCircularShareBuffer(0)
	b.Add(ShareRecord{Time: time.Now(), Difficulty: 1})
	if got := b.GetRecordsAfter(time.Time{}); len(got) != 0 {
		t.Errorf("zero-capacity ring returned %d records", len(got))
	}
}

// Silent workers must leave the map, or rotating worker names grow it without bound.
func TestPruneStaleWorkersDropsOnlyLongSilentOnes(t *testing.T) {
	m := &StatsManager{workers: make(map[string]*WorkerStats)}
	now := time.Now()
	m.workers["m:live"] = &WorkerStats{Online: true, LastShareAt: now}
	m.workers["m:recent"] = &WorkerStats{Online: false, LastShareAt: now.Add(-time.Hour)}
	m.workers["m:ancient"] = &WorkerStats{Online: false, LastShareAt: now.Add(-48 * time.Hour)}
	// An online worker with an old timestamp must survive: only the offline flag retires it.
	m.workers["m:online-but-quiet"] = &WorkerStats{Online: true, LastShareAt: now.Add(-48 * time.Hour)}

	if pruned := m.PruneStaleWorkers(WorkerRetention); pruned != 1 {
		t.Fatalf("pruned %d workers, want 1", pruned)
	}
	for _, k := range []string{"m:live", "m:recent", "m:online-but-quiet"} {
		if _, ok := m.workers[k]; !ok {
			t.Errorf("%s was pruned but should have been kept", k)
		}
	}
	if _, ok := m.workers["m:ancient"]; ok {
		t.Error("a worker silent for 48h survived pruning")
	}
}

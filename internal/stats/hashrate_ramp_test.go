package stats

import (
	"math"
	"testing"
	"time"
)

// A rig's hashrate reads right soon after the mining service starts: averaged over the time the
// worker has been seen, not over the whole window it has not yet filled.
func TestHashrateReadsRightSoonAfterARestart(t *testing.T) {
	m := &StatsManager{}
	const diff = 1 << 20 // one share every 5 s at 900 TH/s
	rate := func(seen time.Duration) []ShareRecord {
		var out []ShareRecord
		now := time.Now()
		for at := seen; at > 0; at -= 5 * time.Second {
			out = append(out, ShareRecord{Time: now.Add(-at), Difficulty: diff})
		}
		return out
	}
	want := float64(diff) * 4294967296 / 5 / 1e12 // TH/s
	near := func(got float64) bool { return math.Abs(got-want)/want < 0.1 }

	// Seen for 2 minutes: both windows read the rig's rate, not a fraction of it.
	since := time.Now().Add(-2 * time.Minute)
	shares := rate(2 * time.Minute)
	if got := m.calculateHashrate(shares, 5*time.Minute, since); !near(got) {
		t.Fatalf("DATA11-5M: 2 minutes in, the 5-minute hashrate is %.1f TH/s, want about %.1f", got, want)
	}
	if got := m.calculateHashrate(shares, 60*time.Minute, since); !near(got) {
		t.Fatalf("DATA11-60M: 2 minutes in, the hour hashrate is %.1f TH/s, want about %.1f", got, want)
	}

	// Seen for 10 seconds: averaged over a minute at least, not over the 10 s of 2 shares.
	if got := m.calculateHashrate(rate(10*time.Second), 5*time.Minute, time.Now().Add(-10*time.Second)); got > want/2 {
		t.Fatalf("DATA11-MIN-SPAN: 10 seconds in, the hashrate is %.1f TH/s, averaged over too short a time", got)
	}

	// Seen for longer than the window: the window, as before -- a rig that has stopped decays.
	old := time.Now().Add(-3 * time.Hour)
	if got := m.calculateHashrate(rate(2*time.Minute), 60*time.Minute, old); math.Abs(got-want*2/60)/(want*2/60) > 0.1 {
		t.Fatalf("DATA11-WINDOW: an old worker's hour hashrate is %.2f TH/s, want the hour's average %.2f", got, want*2/60)
	}
}

// The same through the share path and the dashboard's worker list.
func TestAWorkersHashrateReadsRightThroughTheSharePath(t *testing.T) {
	m := &StatsManager{workers: map[string]*WorkerStats{}}
	const diff = 1 << 20
	now := time.Now()
	buf := NewCircularShareBuffer(MaxSharesPerWorker)
	for at := 2 * time.Minute; at >= 5*time.Second; at -= 5 * time.Second {
		buf.Add(ShareRecord{Time: now.Add(-at), Difficulty: diff})
	}
	m.workers["m:rig1"] = &WorkerStats{MinerID: "m", WorkerName: "rig1", ConnectedAt: now.Add(-2 * time.Minute), ShareBuffer: buf, Online: true}
	m.UpdateWorker("m", "rig1", true, diff, diff)
	want := float64(diff) * 4294967296 / 5 / 1e12
	near := func(got float64) bool { return math.Abs(got-want)/want < 0.1 }
	if w := m.workers["m:rig1"]; !near(w.Hashrate5m) || !near(w.Hashrate60m) {
		t.Fatalf("DATA11-SHARE-PATH: %.1f / %.1f TH/s, want about %.1f", w.Hashrate5m, w.Hashrate60m, want)
	}
	for _, w := range m.GetAllWorkerStats() {
		if !near(w.Hashrate5m) || !near(w.Hashrate60m) {
			t.Fatalf("DATA11-LIST: the worker list says %.1f / %.1f TH/s, want about %.1f", w.Hashrate5m, w.Hashrate60m, want)
		}
	}
}

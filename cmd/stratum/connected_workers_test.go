package main

import (
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/stats"
	"github.com/BitcoincashII/forge-solo/internal/stratum"
)

// A worker connected now is online on the dashboard, with or without a recent share, and is listed
// with the time it connected. One with no share yet said it connected in the year 1.
func TestConnectedWorkersAreOnline(t *testing.T) {
	const addr = "bitcoincashii:qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzse6qye33q"
	base := time.Date(2026, 10, 5, 23, 0, 0, 0, time.UTC)
	firstShare := base.Add(-3 * time.Hour) // counted by the stratum before the rig reconnected
	workers := []*stats.WorkerStats{
		{MinerID: addr, WorkerName: "s19", Online: true, ValidShares: 900, ConnectedAt: firstShare, LastShareAt: base},
		{MinerID: addr, WorkerName: "usb-stick", Online: false, ValidShares: 3}, // last share 8 minutes ago
		{MinerID: addr, WorkerName: "unplugged", Online: false, ValidShares: 50, ConnectedAt: firstShare},
	}
	ref := func(w string, at time.Time) stratum.WorkerRef {
		return stratum.WorkerRef{MinerID: addr, WorkerName: w, ConnectedAt: at}
	}
	connected := []stratum.WorkerRef{ref("s19", base.Add(-time.Minute)), ref("usb-stick", base),
		ref("nerdminer", base.Add(-10*time.Second)), ref("nerdminer", base.Add(-20*time.Second)),
		ref("bitaxe", base.Add(-5*time.Second)), ref("bitaxe", time.Time{})}
	got := map[string]*stats.WorkerStats{}
	for _, w := range withConnectedWorkers(workers, connected) {
		if got[w.WorkerName] != nil {
			t.Fatalf("DATA13-ONCE: %s listed twice", w.WorkerName)
		}
		got[w.WorkerName] = w
	}
	if w := got["usb-stick"]; w == nil || !w.Online || w.ValidShares != 3 {
		t.Fatalf("DATA13-SLOW: a connected miner between shares is %+v, want online with its counts", w)
	}
	if w := got["nerdminer"]; w == nil || !w.Online || w.Hashrate5m != 0 {
		t.Fatalf("DATA13-NO-SHARE-YET: a connected miner with no share yet is %+v, want listed, online, 0 H/s", w)
	}
	if w := got["unplugged"]; w == nil || w.Online {
		t.Fatalf("DATA13-GONE: a worker no longer connected is %+v, want offline", w)
	}
	if len(got) != 5 {
		t.Fatalf("DATA13-COUNT: %d workers listed, want 5", len(got))
	}

	if at := got["nerdminer"].ConnectedAt; at.IsZero() {
		t.Errorf("WORKERS-CONNECTED-AT: a worker with no share yet is listed as connected at %v", at)
	} else if !at.Equal(base.Add(-20 * time.Second)) {
		t.Errorf("WORKERS-EARLIEST: a worker connected at %v and %v is listed as connected at %v; want the first",
			base.Add(-20*time.Second), base.Add(-10*time.Second), at)
	}
	if at := got["bitaxe"].ConnectedAt; !at.Equal(base.Add(-5 * time.Second)) {
		t.Errorf("WORKERS-ZERO-REF: a worker connected at %v, and on a connection with no time, is listed as connected at %v", base.Add(-5*time.Second), at)
	}
	if at := got["s19"].ConnectedAt; !at.Equal(base.Add(-time.Minute)) {
		t.Errorf("WORKERS-CONNECTED-AT-LISTED: a worker connected at %v is listed as connected at %v", base.Add(-time.Minute), at)
	}
	if !got["s19"].LastShareAt.Equal(base) {
		t.Errorf("WORKERS-LAST-SHARE: the last share of a connected worker changed to %v", got["s19"].LastShareAt)
	}
	if at := got["unplugged"].ConnectedAt; !at.Equal(firstShare) {
		t.Errorf("WORKERS-OFFLINE-AT: a worker no longer connected is listed as connected at %v, want %v as before", at, firstShare)
	}
}

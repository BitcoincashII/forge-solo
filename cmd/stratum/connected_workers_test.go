package main

import (
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/stats"
	"github.com/BitcoincashII/forge-solo/internal/stratum"
)

// A worker connected now is online on the dashboard, with or without a recent share.
func TestConnectedWorkersAreOnline(t *testing.T) {
	const addr = "bitcoincashii:qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzse6qye33q"
	workers := []*stats.WorkerStats{
		{MinerID: addr, WorkerName: "s19", Online: true, ValidShares: 900},
		{MinerID: addr, WorkerName: "usb-stick", Online: false, ValidShares: 3}, // last share 8 minutes ago
		{MinerID: addr, WorkerName: "unplugged", Online: false, ValidShares: 50},
	}
	ref := func(w string) stratum.WorkerRef { return stratum.WorkerRef{MinerID: addr, WorkerName: w} }
	connected := []stratum.WorkerRef{ref("s19"), ref("usb-stick"), ref("nerdminer"), ref("nerdminer")}
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
	if len(got) != 4 {
		t.Fatalf("DATA13-COUNT: %d workers listed, want 4", len(got))
	}
}

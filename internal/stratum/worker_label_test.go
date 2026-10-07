package stratum

import (
	"strings"
	"testing"
	"time"
)

// A worker name is kept to letters, digits and . _ - @ +, so the dashboard shows a label, not a
// message: anyone who can reach the stratum chooses one.
func TestWorkerLabelsAreKeptToASafeCharset(t *testing.T) {
	for in, want := range map[string]string{
		"rig1":                 "rig1",
		"S19-01.rack_2@home+x": "S19-01.rack_2@home+x",
		"WARNING: payout moved! https://evil.example/x": "WARNING__payout_moved__https___evil.example_x",
		"rigé":        "rig__",
		"a\x00b\nc":   "a_b_c",
		"<b>bold</b>": "_b_bold__b_",
		"":            "default",
	} {
		if got := workerLabel(in); got != want {
			t.Errorf("WEB6-CHARSET: workerLabel(%q) = %q, want %q", in, got, want)
		}
	}
	if got := workerLabel(strings.Repeat("x", 500)); len(got) != maxWorkerLabel {
		t.Errorf("WEB6-LENGTH: a 500-byte name kept %d bytes", len(got))
	}

	// On every path a name reaches a worker: a label alone, an address and a label, the probe.
	s := newSoloServer(t, testPayout)
	for username, want := range map[string]string{
		"WARNING: payout moved":               "WARNING__payout_moved",
		testPayout + ".WARNING: payout moved": "WARNING__payout_moved",
		testPayout + ".rig 7":                 "rig_7",
	} {
		c, resp := authorize(t, s, username)
		c.mu.RLock()
		got := c.WorkerName
		c.mu.RUnlock()
		if resp.Result != true || got != want {
			t.Errorf("WEB6-AUTHORIZE: %q was kept as %q (%v), want %q", username, got, resp.Error, want)
		}
	}
	probe := NewServer(&ServerConfig{MinDiff: 1024, MaxDiff: 1e12}, s.logger, nil)
	c, _ := authorize(t, probe, "braiins probe: hi")
	c.mu.RLock()
	got := c.WorkerName
	c.mu.RUnlock()
	if got != "braiins_probe__hi" {
		t.Errorf("WEB6-PROBE: the probe's name was kept as %q", got)
	}
}

// AuthorizedWorkers names the authorized clients, not the probes or the ones still logging in, with
// the time each connected: the dashboard lists a worker with no share yet from it.
func TestAuthorizedWorkersNamesTheConnectedMiners(t *testing.T) {
	s := newSoloServer(t, testPayout)
	at := time.Date(2026, 10, 5, 23, 14, 48, 0, time.UTC)
	for id, c := range map[string]*Client{
		"a": {ID: "a", Authorized: true, MinerID: testPayout, WorkerName: "rig1", ConnectedAt: at},
		"b": {ID: "b", Authorized: false, MinerID: testPayout, WorkerName: "logging-in", ConnectedAt: at},
		"c": {ID: "c", Authorized: true, MinerID: "probe", WorkerName: "braiinstest", ConnectedAt: at},
	} {
		s.clients.Store(id, c)
	}
	got := s.AuthorizedWorkers()
	if len(got) != 1 || got[0].MinerID != testPayout || got[0].WorkerName != "rig1" {
		t.Fatalf("DATA13-AUTHORIZED: %+v", got)
	}
	if !got[0].ConnectedAt.Equal(at) {
		t.Fatalf("DATA13-AUTHORIZED-AT: the worker connected at %v is named with %v", at, got[0].ConnectedAt)
	}
}

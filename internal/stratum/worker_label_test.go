package stratum

import (
	"strings"
	"testing"
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
	probe := NewServer(&ServerConfig{MinDiff: 1024, MaxDiff: 1e12}, s.logger, nil, nil)
	c, _ := authorize(t, probe, "braiins probe: hi")
	c.mu.RLock()
	got := c.WorkerName
	c.mu.RUnlock()
	if got != "braiins_probe__hi" {
		t.Errorf("WEB6-PROBE: the probe's name was kept as %q", got)
	}
}

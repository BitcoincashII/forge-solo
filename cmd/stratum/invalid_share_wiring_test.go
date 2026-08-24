package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Both stratum servers must have the reject handler attached.
//
// The stratum counts rejects and offers them through SetInvalidShareHandler, and
// the stats manager behind the dashboard's Reject % tile only learns about them
// through that hook. A server constructed without it reports 0.00% forever no
// matter what its miners send -- and the rental port (3335) is exactly where
// stales show up, since rented hashpower is the least predictable.
//
// This is a source check because the wiring is the defect: the handler and the
// counter both work in isolation, and every unit test still passes with the
// hook left unattached.
func TestBothStratumServersGetTheInvalidShareHandler(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	lines := strings.Split(string(src), "\n")

	newServer := regexp.MustCompile(`^\s*(\w+) = stratum\.NewServer\(`)
	var constructed []struct {
		name string
		line int
	}
	for i, l := range lines {
		if m := newServer.FindStringSubmatch(l); m != nil {
			constructed = append(constructed, struct {
				name string
				line int
			}{m[1], i})
		}
	}
	if len(constructed) < 2 {
		t.Fatalf("expected both the miner and rental stratum servers to be constructed here, found %d", len(constructed))
	}

	for _, c := range constructed {
		// The handler must be attached in the few lines right after construction,
		// before the server can start taking submissions.
		found := false
		for j := c.line; j < c.line+6 && j < len(lines); j++ {
			if strings.Contains(lines[j], c.name+".SetInvalidShareHandler(") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("main.go:%d constructs %s without SetInvalidShareHandler; every share "+
				"it rejects is invisible to the dashboard, which will report Reject %% as "+
				"0.00%% no matter what the miner sends", c.line+1, c.name)
		}
	}
}

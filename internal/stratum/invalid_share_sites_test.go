package stratum

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Every rejection site must go through noteInvalidShare.
//
// The Reject % tile read 0.00% forever because eight rejection sites each wrote
// out the same two atomic increments by hand, and the third destination -- the
// stats manager the dashboard actually reads -- was missing from all eight. A
// ninth site written the old way would silently reintroduce exactly that.
func TestEveryRejectionSiteGoesThroughNoteInvalidShare(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}
	lines := strings.Split(string(src), "\n")

	// Where noteInvalidShare itself lives -- the one legitimate increment.
	body := regexp.MustCompile(`^func \(s \*Server\) noteInvalidShare\(`)
	start, end := -1, -1
	for i, l := range lines {
		if body.MatchString(l) {
			start = i
			continue
		}
		if start >= 0 && l == "}" {
			end = i
			break
		}
	}
	if start < 0 || end < 0 {
		t.Fatal("noteInvalidShare not found in server.go — did the reject accounting move?")
	}

	inc := regexp.MustCompile(`atomic\.AddInt64\(&(client|s)\.stats?\.?InvalidShares, 1\)`)
	for i, l := range lines {
		if i >= start && i <= end {
			continue // the helper's own increments
		}
		if inc.MatchString(l) {
			t.Errorf("server.go:%d increments an InvalidShares counter by hand:\n\t%s\n"+
				"Call s.noteInvalidShare(client, \"<reason>\") instead, or this reject "+
				"never reaches the stats manager and the dashboard reports it as 0.00%%.",
				i+1, strings.TrimSpace(l))
		}
	}

	// And the helper must still be reached from the submit path at all.
	if n := strings.Count(string(src), "s.noteInvalidShare(client,"); n < 8 {
		t.Errorf("only %d rejection sites route through noteInvalidShare; expected at "+
			"least the 8 that exist (malformed params x2, param range x2, duplicate, "+
			"stale job, invalid, low difficulty)", n)
	}
}

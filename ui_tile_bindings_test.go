package forgesolo

// The dashboard tiles are wired in plain JS, so nothing type-checks them: a tile bound to
// the wrong field renders a confident, wrong number and every Go test still passes. Each
// case below is a binding that was ACTUALLY WRONG in a shipped build, pinned so it cannot
// silently drift back.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func readDashboardJS(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("web/dist/js/pool-solo-inline.js")
	if err != nil {
		t.Fatalf("read dashboard JS: %v", err)
	}
	return string(b)
}

// assignmentFor returns the right-hand side assigned to document.getElementById('<id>').
func assignmentFor(t *testing.T, src, id string) string {
	t.Helper()
	re := regexp.MustCompile(`getElementById\('` + regexp.QuoteMeta(id) + `'\)\.textContent\s*=\s*([^;]+);`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("no textContent assignment found for #%s — did the tile move or get renamed?", id)
	}
	return strings.TrimSpace(m[1])
}

func TestRoundSharesTileIsNotTheAllTimeCount(t *testing.T) {
	src := readDashboardJS(t)

	round := assignmentFor(t, src, "roundShares")
	all := assignmentFor(t, src, "validShares")

	if round == all {
		t.Fatalf("#roundShares and #validShares are both assigned %s, so the two tiles are "+
			"byte-identical always — and 'Round Shares' sits in the Round Effort card beside "+
			"a Current Effort and Best Difficulty that the round reset DOES clear", round)
	}
	if !strings.Contains(round, "roundShares") {
		t.Errorf("#roundShares is assigned %s; it must come from the per-round counter "+
			"(data.roundShares), not an all-time field", round)
	}
}

// The Workers tile must count what is connected now, not every worker ever seen: the
// stats map is never pruned, so the all-time figure stays >= 1 for the life of the
// stratum process and reads 1 next to a hashrate of 0.
func TestWorkersTileCountsOnlineWorkers(t *testing.T) {
	src := readDashboardJS(t)
	got := assignmentFor(t, src, "workers")
	if !strings.Contains(got, "onlineWorkers") {
		t.Errorf("#workers is assigned %s; bind it to data.onlineWorkers, or an unplugged "+
			"rig leaves the tile reading 1 forever", got)
	}
}

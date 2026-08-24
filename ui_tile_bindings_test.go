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

// The Workers tile must count what is attached RIGHT NOW.
//
// Two ways it has been wrong: bound to data.workers, the never-pruned all-time count,
// which stays >= 1 for the life of the stratum process; and bound to data.onlineWorkers,
// where "online" means "submitted a share in the last 5 minutes", so a switched-off rig
// kept the tile at 1 for five minutes underneath a banner correctly reporting that no
// miner was connected.
func TestWorkersTileCountsWhatIsConnectedNow(t *testing.T) {
	src := readDashboardJS(t)
	got := assignmentFor(t, src, "workers")

	if strings.Contains(got, "data.workers") {
		t.Fatalf("#workers is assigned %s — data.workers counts every worker ever seen and "+
			"is never pruned, so an unplugged rig leaves the tile reading 1 forever", got)
	}
	if !strings.Contains(got, "workersConnectedNow") {
		t.Errorf("#workers is assigned %s; it must go through workersConnectedNow(), which "+
			"prefers the stratum's live authorized count over the 5-minute share window", got)
	}

	// The helper must actually prefer the live count, and must fall back rather than
	// reporting a confident zero when the status is missing or stale.
	helper := src[strings.Index(src, "function workersConnectedNow("):]
	if i := strings.Index(helper, "\n        }"); i > 0 {
		helper = helper[:i]
	}
	for _, must := range []string{"lastMiningStatus", "authorized", "onlineWorkers", "addressFromUrl"} {
		if !strings.Contains(helper, must) {
			t.Errorf("workersConnectedNow() does not mention %q; it needs the live count, a "+
				"fallback, and the guard against a ?address= override reading a process-wide "+
				"figure as if it described someone else's miner", must)
		}
	}
}

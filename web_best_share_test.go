package forgesolo

import (
	"regexp"
	"strings"
	"testing"
)

// The Workers table's best share is kept across restarts and updates now. Its column said "(since
// restart)", and its note that an update or a reboot resets it.
func TestWorkersBestDiffIsAllTime(t *testing.T) {
	page := readWebFile(t, "solo.html")
	th := regexp.MustCompile(`<th scope="col" title="([^"]*)">(Best Diff[^<]*)</th>`).FindStringSubmatch(page)
	if th == nil {
		t.Fatal("ATH-WEB-COLUMN: solo.html has no Best Diff column")
	}
	if th[2] != "Best Diff (all time)" {
		t.Errorf("ATH-WEB-LABEL: the column is headed %q, want \"Best Diff (all time)\"", th[2])
	}
	if strings.Contains(th[1], "resets") || !strings.Contains(th[1], "Restarts and updates keep it") {
		t.Errorf("ATH-WEB-NOTE: the column's note says %q; it must say restarts and updates keep the best", th[1])
	}
	js := readWebFile(t, "js/pool-solo-inline.js")
	if !strings.Contains(js, "${formatDiff(w.athDiff || w.bestDiff || 0)}") {
		t.Error("ATH-WEB-FIGURE: the column no longer shows the worker's athDiff")
	}
}

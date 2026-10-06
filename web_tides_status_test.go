package forgesolo

import (
	"os"
	"strings"
	"testing"
)

// TIDES falls back to solo when Forge Pool does not take the work, and also when this BCH2 node is
// not on the pool's block yet, mostly while it catches up with the chain after a restart. The
// dashboard blamed the pool for both: the badge said "Forge Pool is unavailable", and the banner and
// the TIDES card that TIDES resumes "when the pool answers again", next to a reason saying the node
// was behind. The gateway's status says which it is (node_behind), and each text follows it.
func TestTidesFallbackForANodeBehindIsNotBlamedOnThePool(t *testing.T) {
	gw, err := os.ReadFile("internal/tidesgw/gateway.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gw), "`json:\"node_behind,omitempty\"`") {
		t.Fatal("TIDES-WEB-FIELD: the gateway's status no longer says node_behind, which the dashboard reads")
	}
	js := readWebFile(t, "js/pool-solo-inline.js")

	line := textBetween(js, "function tidesStatusLine(ms) {", "\n        }\n")
	behind := textBetween(line, "if (t.state === 'fallback' && t.node_behind) {", "\n            }\n")
	if !strings.Contains(behind, "This BCH2 node is not on Forge Pool\\'s block yet") ||
		!strings.Contains(behind, "TIDES resumes by itself once it is.") || strings.Contains(behind, "pool answers") ||
		strings.Index(line, "if (t.state === 'fallback' && t.node_behind) {") > strings.Index(line, "if (t.state === 'fallback') {") {
		t.Errorf("TIDES-WEB-BANNER: the banner does not say this node is behind the pool when it is:\n%s", line)
	}

	badge := textBetween(js, "function updateModeBadge(ms) {", "\n        }\n")
	if !strings.Contains(badge, "ms.tides.node_behind ? 'TIDES chosen, but this BCH2 node is not on Forge Pool\\'s block yet: mining solo meanwhile'") {
		t.Errorf("TIDES-WEB-BADGE: the badge says the pool is unavailable while this node is behind it:\n%s", badge)
	}

	card := textBetween(js, "document.getElementById('tidesNote').textContent =", ";\n")
	if !strings.Contains(card, "t.state === 'fallback' && t.node_behind\n                ? 'This BCH2 node is not on Forge Pool\\'s block yet'") ||
		!strings.Contains(card, "so it is mining solo until it is.") {
		t.Errorf("TIDES-WEB-CARD: the TIDES card says the pool is not taking the work while this node is behind it:\n%s", card)
	}
}

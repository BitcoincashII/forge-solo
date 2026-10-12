package forgesolo

import (
	"os"
	"regexp"
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

// Forge Pool refuses every request of an install whose clock is more than 2 minutes off its own. The
// banner, the TIDES card and the badge showed the pool's answer as it came, JSON and all; they say
// now how far off the clock is and how to set it right on this system: on Windows and Linux in
// Forge Gateway's words, on Umbrel that umbrelOS sets its clock itself.
func TestTidesClockRefusalSaysHowToSetTheClock(t *testing.T) {
	gw := string(mustRead(t, "internal/tidesgw/gateway.go"))
	for _, field := range []string{"`json:\"clock_refused,omitempty\"`", "`json:\"clock_off,omitempty\"`"} {
		if !strings.Contains(gw, field) {
			t.Fatalf("WEB-CLOCK-FIELD: the gateway's status no longer has %s, which the dashboard reads", field)
		}
	}
	js := readWebFile(t, "js/pool-solo-inline.js")
	line := textBetween(js, "function tidesStatusLine(ms) {", "\n        }\n")
	behind := strings.Index(line, "if (t.state === 'fallback' && t.node_behind) {")
	clock := strings.Index(line, "if (t.state === 'fallback' && t.clock_refused) {")
	fallback := strings.Index(line, "if (t.state === 'fallback') {")
	if behind < 0 || clock < behind || fallback < clock {
		t.Errorf("WEB-CLOCK-BANNER: the clock is not checked after a node behind and before any other fall back:\n%s", line)
	}
	branch := textBetween(line, "if (t.state === 'fallback' && t.clock_refused) {", "\n            }\n")
	if !strings.Contains(branch, "sanitizeHTML(clockAdvice(t))") || strings.Contains(branch, "t.reason") ||
		!strings.Contains(branch, "TIDES resumes by itself once the clock is right.") {
		t.Errorf("WEB-CLOCK-BANNER: %s", branch)
	}
	card := textBetween(js, "document.getElementById('tidesNote').textContent =", ";\n")
	if !strings.Contains(card, "t.state === 'fallback' && t.clock_refused\n                ? clockAdvice(t) + ' Until then it is mining solo.") {
		t.Errorf("WEB-CLOCK-CARD: the TIDES card does not say how to set the clock right:\n%s", card)
	}
	badge := textBetween(js, "function updateModeBadge(ms) {", "\n        }\n")
	if !strings.Contains(badge, "ms.tides.clock_refused ? 'TIDES chosen, but Forge Pool refuses this install\\'s requests until its clock is right: mining solo meanwhile'") {
		t.Errorf("WEB-CLOCK-BADGE: %s", badge)
	}
	// Windows and Linux in Forge Gateway's words.
	fix := textBetween(string(mustRead(t, "cmd/forge-gateway/state.go")), "func clockFix(goos string) string {", "\n}\n")
	advice := textBetween(js, "const CLOCK_FIX = {", "};")
	for _, system := range []string{"windows", "linux"} {
		m := regexp.MustCompile(`case "` + system + `":\s+return "([^"]+)"`).FindStringSubmatch(fix)
		if m == nil {
			t.Fatalf("WEB-CLOCK-GATEWAY: Forge Gateway's clockFix has no text for %s", system)
		}
		if !strings.Contains(advice, system+": '"+m[1]+"'") {
			t.Errorf("WEB-CLOCK-WORDS: the dashboard's %s text is not Forge Gateway's %q:\n%s", system, m[1], advice)
		}
	}
	if !strings.Contains(advice, "umbrel: 'umbrelOS sets it by itself over the internet, so check that the Umbrel is online") {
		t.Errorf("WEB-CLOCK-UMBREL: %s", advice)
	}
	if !strings.Contains(readWebFile(t, "tides.html"), "Forge Pool also refuses the requests of an install whose clock is more than 2 minutes off its own") {
		t.Error("WEB-CLOCK-HELP: the TIDES help does not say the pool refuses a clock that is off")
	}
}

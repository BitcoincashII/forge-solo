package forgesolo

// The dashboard's figures, pinned as text like web_pages_test.go's rules: CI has no browser.

import (
	"regexp"
	"strings"
	"testing"
)

// Current Effort divided the round's work by the node's getdifficulty: the tip's difficulty, one
// block behind the one being mined. BCH2 retargets at every block, so each retarget rescaled the
// whole round (236%, 211%, then 316% across two blocks 11 s apart, with about 1% more work done).
// The tile shows the mining service's per-share figure, and the time to a block uses the
// difficulty of the block being mined.
func TestCurrentEffortIsCountedShareByShare(t *testing.T) {
	js := readWebFile(t, "js/pool-solo-inline.js")
	miner := textBetween(js, "async function fetchMinerData() {", "\n        // Connect & Network")
	if !strings.Contains(miner, `                const effort = typeof data.roundEffort === 'number'
                    ? data.roundEffort * 100
                    : (networkDiff > 0 ? (workDone / networkDiff * 100) : 0);`) {
		t.Error("EFFORT-JS-PER-SHARE: Current Effort is not the mining service's per-share figure (roundEffort)")
	}
	if !regexp.MustCompile(`const diffNow = difficultyNow\(\);\s*if \(hashrate > 0 && diffNow > 0\) \{[^}]*const hashesNeeded = diffNow \* 4294967296;`).MatchString(miner) {
		t.Error("EFFORT-JS-ETA: the time to a block does not use the difficulty of the block being mined")
	}
	const now = `        function difficultyNow() {
            const fresh = lastMiningStatus
                && (Date.now() - lastMiningStatusAt) < MINING_STATUS_MAX_AGE_MS;
            const d = fresh ? Number(lastMiningStatus.network_difficulty) : 0;
            return d > 0 ? d : networkDiff;
        }`
	if !strings.Contains(js, now) {
		t.Error("EFFORT-JS-DIFF-NOW: difficultyNow() does not take the newest job's difficulty from a fresh mining status, " +
			"falling back to getdifficulty")
	}
}

// In TIDES mode the balance card, the Blocks table and Payout History count solo blocks only: a
// TIDES block pays the TIDES window, and what it paid is in the TIDES card. The card read "Your
// Blocks Found 9, Includes 9 TIDES blocks, Total: 0 BCH2" and the tables "No blocks found yet" while
// the TIDES card showed the same address paid. In TIDES mode each figure is said to be solo and the
// empty tables point to the TIDES card; Solo mode reads as before.
func TestTidesModeSaysTheBalanceIsSolo(t *testing.T) {
	js := readWebFile(t, "js/pool-solo-inline.js")
	if !strings.Contains(textBetween(js, "function soloFiguresOnly() {", "\n        }\n"), "return tidesInEffect(lastMiningStatus);") {
		t.Error("BAL-SOLO-WHEN: the figures are not said to be solo exactly while TIDES is in effect")
	}
	labels := textBetween(js, "function renderBalanceLabels() {", "\n        }\n")
	for _, want := range []string{
		"const tides = soloFiguresOnly();",
		"label('matureLabel', tides ? 'Solo: matured (spendable)' : 'Matured (spendable)');",
		"label('immatureLabel', tides ? 'Solo, still maturing' : 'Still maturing');",
		"label('totalPaidLabel', tides ? 'Total Paid (solo)' : 'Total Paid');",
		"if (note) note.hidden = !tides;",
	} {
		if !strings.Contains(labels, want) {
			t.Errorf("BAL-SOLO-LABELS: renderBalanceLabels() lacks %q", want)
		}
	}
	// The labels follow the mode as the status banner learns it.
	if !strings.Contains(js, "updateModeBadge(lastMiningStatus);\n            renderBalanceLabels();\n") {
		t.Error("BAL-SOLO-LABELS-WIRED: the balance labels are not set when the mining status is read")
	}
	if !strings.Contains(textBetween(js, "function totalLabel() {", "\n        }\n"), "return soloFiguresOnly() ? 'Solo total' : 'Total';") {
		t.Error("BAL-SOLO-TOTAL: the Total line is not said to be solo in TIDES mode")
	}
	blocks := textBetween(js, "async function fetchBlocks() {", "\n        async function fetchPayouts")
	if strings.Contains(blocks, "'Total: ") || strings.Count(blocks, "totalLabel() + ': ") != 3 {
		t.Error("BAL-SOLO-TOTAL-LINE: a Total line in fetchBlocks() does not go through totalLabel()")
	}
	if !strings.Contains(blocks, "const none = soloFiguresOnly()\n                        ? 'No solo blocks yet. Your TIDES blocks, and what each paid you, are in the TIDES card above.'") {
		t.Error("BAL-EMPTY-BLOCKS: in TIDES mode the empty Blocks table does not point to the TIDES card")
	}
	payouts := textBetween(js, "async function fetchPayouts() {", "\n        function updateChart")
	if !strings.Contains(payouts, "const none = soloFiguresOnly()\n                        ? 'No solo payouts yet. Your TIDES payouts are in the TIDES card above.'") {
		t.Error("BAL-EMPTY-PAYOUTS: in TIDES mode the empty Payout History does not point to the TIDES card")
	}
	page := readWebFile(t, "solo.html")
	for _, want := range []string{`<h2 id="matureLabel"`, `<span id="immatureLabel">Still maturing</span>:`,
		`<span id="totalPaidLabel">Total Paid</span>:`,
		`id="balanceTidesNote" hidden>Your TIDES payouts are not counted here: they are in the TIDES card below.</div>`} {
		if !strings.Contains(page, want) {
			t.Errorf("BAL-SOLO-MARKUP: solo.html lacks %s", want)
		}
	}
}

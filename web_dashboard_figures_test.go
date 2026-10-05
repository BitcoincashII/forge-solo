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

// In TIDES mode the balance card's amounts, the Blocks table and Payout History count solo blocks
// only: a TIDES block pays the TIDES window, and what it paid is in the TIDES card. The card read
// "Your Blocks Found 9, Includes 9 TIDES blocks, Total: 0 BCH2" and the tables "No blocks found yet"
// while the TIDES card showed the same address paid. In TIDES mode each of these is said to be solo
// and the empty tables point to the TIDES card. Your Blocks Found counts TIDES blocks, so the table
// below it is headed Your Solo Blocks, and the help page says that the count includes them. Solo
// mode reads as before.
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
	if !strings.Contains(labels, "label('blocksTitle', tides ? 'Your Solo Blocks' : 'Your Blocks Found');") {
		t.Error("BAL-SOLO-BLOCKS-TITLE: in TIDES mode the Blocks table is not headed Your Solo Blocks")
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
	help := readWebFile(t, "tides.html")
	if !strings.Contains(help, "In TIDES mode the amounts in the balance card at the top of the dashboard (matured, still maturing and "+
		"the total), the Your Solo Blocks table and Payout History are solo, and say so: they count only the blocks that paid "+
		"your address in full") {
		t.Error("BAL-HELP: the TIDES help page does not say which of the dashboard's figures are solo in TIDES mode")
	}
	if !strings.Contains(help, "Your Blocks Found counts your TIDES blocks too.") {
		t.Error("BAL-HELP-FOUND: the TIDES help page does not say that Your Blocks Found counts TIDES blocks")
	}
	if !strings.Contains(page, `<h2 id="blocksTitle" data-i18n="p_solo_your_blocks">Your Blocks Found</h2>`) {
		t.Error("BAL-SOLO-BLOCKS-TITLE-ID: the Blocks table's heading has no id for renderBalanceLabels() to set")
	}
	for _, want := range []string{`<h2 id="matureLabel"`, `<span id="immatureLabel">Still maturing</span>:`,
		`<span id="totalPaidLabel">Total Paid</span>:`,
		`id="balanceTidesNote" hidden>Your TIDES payouts are not counted here: they are in the TIDES card below.</div>`} {
		if !strings.Contains(page, want) {
			t.Errorf("BAL-SOLO-MARKUP: solo.html lacks %s", want)
		}
	}
}

// The TIDES card's "Paid to you (confirmed)" counts payouts in blocks 2 deep, which is when the pool
// settles them. A coinbase output can be spent only 100 blocks after its block, and the same page
// calls a solo block "Confirmed" once it is 100 deep, so most of what the tile called confirmed was
// still maturing in the wallet. The tiles say how many confirmations they mean and when each payout
// is spendable, and the help page names them with the same words.
func TestTidesPaidTileSaysTwoConfirmations(t *testing.T) {
	page := readWebFile(t, "solo.html")
	grid := textBetween(page, `<div class="tides-grid">`, `<div class="tides-gw"`)
	tiles := map[string]string{
		"tidesPending": `<div class="tl">Pending (under 2 confirmations)</div></div>`,
		"tidesPaid":    `<div class="tl">Paid to you (2+ confirmations)</div><div class="ts">each spendable 100 blocks after its block</div></div>`,
	}
	for id, want := range tiles {
		if !strings.Contains(grid, `<div class="tv" id="`+id+`">--</div>`+want) {
			t.Errorf("TIDES-PAID-TILE: #%s is not labelled %s", id, want)
		}
	}
	if strings.Contains(grid, "(confirmed)") || strings.Contains(grid, "not yet confirmed") {
		t.Error("TIDES-PAID-OLD: a TIDES tile still says confirmed without saying how many confirmations")
	}
	if !regexp.MustCompile(`\.tides-grid \.ts\{[^}]*font-size:10px`).MatchString(page) {
		t.Error("TIDES-PAID-STYLE: the line under the Paid tile has no style of its own")
	}
	help := readWebFile(t, "tides.html")
	for _, label := range []string{"Pending (under 2 confirmations)", "Paid to you (2+ confirmations)"} {
		if !strings.Contains(help, "<dt>"+label+"</dt>") {
			t.Errorf("TIDES-HELP-LABELS: the help page does not name the tile %q", label)
		}
	}
	if strings.Contains(help, "(confirmed)") || strings.Contains(help, "not yet confirmed") {
		t.Error("TIDES-HELP-OLD: the help page still names the tiles by their old labels")
	}
	paid := textBetween(help, "<dt>Paid to you (2+ confirmations)</dt>", "</dd>")
	if !strings.Contains(paid, "2 or more confirmations") || !strings.Contains(paid, "can be spent 100 blocks after the block that paid it") {
		t.Errorf("TIDES-HELP-SPENDABLE: the help page's Paid entry does not say 2+ confirmations and spendable after 100 blocks: %s", paid)
	}
}

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

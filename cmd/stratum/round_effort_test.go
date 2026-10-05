package main

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/mining"
	"github.com/BitcoincashII/forge-solo/internal/stats"
	"github.com/BitcoincashII/forge-solo/internal/stratum"
	"go.uber.org/zap"
)

// BCH2 retargets at every block. The dashboard's Current Effort divided the round's work by the
// tip's difficulty, so every retarget rescaled the whole round: 236%, then 211%, then 316% across
// two blocks 11 s apart, while about 1% more work was done. Each share now counts against the
// difficulty of the job it was mined on, and against the newest template's when that job is gone.
func TestRoundEffortCountsEachShareAgainstItsJob(t *testing.T) {
	// Its own entry in the process-wide stats manager, and two jobs at difficulty 256 and 65,536.
	const miner = "round-effort-test-miner"
	jobs := map[string]string{"effort-a": "1c00ffff", "effort-b": "1b00ffff"}
	jobHistoryMu.Lock()
	for id, bits := range jobs {
		jobHistory[id] = &mining.Job{ID: id, NBits: bits}
	}
	jobHistoryMu.Unlock()
	t.Cleanup(func() {
		jobHistoryMu.Lock()
		for id := range jobs {
			delete(jobHistory, id)
		}
		jobHistoryMu.Unlock()
	})
	saved := getNetworkDifficulty()
	setNetworkDifficulty(1 << 20)
	t.Cleanup(func() { setNetworkDifficulty(saved) })

	p := &BlockFindingShareProcessor{logger: zap.NewNop()}
	share := func(job string, d float64) {
		t.Helper()
		s := &stratum.Share{JobID: job, MinerID: miner, WorkerName: "rig1", Difficulty: d, ActualDiff: d, IsSolo: true}
		if err := p.ProcessShare(context.Background(), s); err != nil {
			t.Fatal(err)
		}
	}
	// What /internal/workers sends the API for this worker.
	effort := func() float64 {
		t.Helper()
		for _, w := range stats.GetManager().GetAllWorkerStats() {
			if w.MinerID != miner {
				continue
			}
			b, _ := json.Marshal(w)
			var m map[string]any
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatal(err)
			}
			v, _ := m["round_effort"].(float64)
			return v
		}
		t.Fatal("the worker is not in the stats")
		return 0
	}

	for i := 0; i < 128; i++ {
		share("effort-a", 1) // 128 / 256
	}
	if got := effort(); math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("EFFORT-JOB-DIFF: 128 shares of 1 on a job at difficulty 256 make %v of a block, want 0.5", got)
	}
	share("effort-b", 32768) // the next block's job, at 65,536: half a block more
	if got := effort(); math.Abs(got-1) > 1e-9 {
		t.Fatalf("EFFORT-JOB-DIFF: after half a block on the next job the round is %v, want 1", got)
	}
	share("effort-gone", 1<<19) // its job is gone: the newest template's difficulty
	if got := effort(); math.Abs(got-1.5) > 1e-9 {
		t.Fatalf("EFFORT-JOB-GONE: a share whose job is gone made the round %v, want 1.5", got)
	}
}

// The dashboard's time to a block uses the difficulty of the block being mined, from the newest
// job. getdifficulty is the tip's, one block behind: 5 to 11% off after most retargets.
func TestMiningStatusGivesTheDifficultyBeingMined(t *testing.T) {
	saved := getCurrentJob()
	t.Cleanup(func() { setCurrentJob(saved) })
	diff := func() float64 {
		t.Helper()
		b, err := json.Marshal(buildMiningStatus())
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		v, _ := m["network_difficulty"].(float64)
		return v
	}
	setCurrentJob(nil)
	if d := diff(); d != 0 {
		t.Fatalf("ETA-DIFF-NO-JOB: with no job yet the status gives difficulty %v, want 0 (not known)", d)
	}
	setCurrentJob(&mining.Job{ID: "eta", NBits: "1b00ffff"})
	if d := diff(); d != 65536 {
		t.Fatalf("ETA-DIFF-JOB: the status gives difficulty %v for a job at 65,536", d)
	}
}

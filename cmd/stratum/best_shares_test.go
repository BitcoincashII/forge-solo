package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/stratum"
	"go.uber.org/zap"
)

// bestShare sends one accepted share, of actual difficulty diff, through the share path, for a miner
// of its own in the stats manager every test in this binary shares.
func bestShare(t *testing.T, worker string, diff float64) string {
	t.Helper()
	miner := fmt.Sprintf("best-share-test-%d", time.Now().UnixNano())
	saved := getNetworkDifficulty()
	setNetworkDifficulty(1e15) // no block candidate
	t.Cleanup(func() { setNetworkDifficulty(saved) })
	p := &BlockFindingShareProcessor{logger: zap.NewNop()}
	s := &stratum.Share{JobID: "best-share-gone", MinerID: miner, WorkerName: worker, Difficulty: 1000, ActualDiff: diff, IsSolo: true}
	if err := p.ProcessShare(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	return miner
}

// The api reads the miner's all-time best share from /internal/workers, also when none of the
// workers listed holds it; a worker connected again with no share yet in this run shows its own.
func TestWorkersAnswerHasTheKeptBest(t *testing.T) {
	miner := bestShare(t, "nerd", 7e9)
	b, err := json.Marshal(internalWorkers())
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		MinerATH map[string]float64 `json:"miner_ath_diff"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.MinerATH[miner] != 7e9 {
		t.Errorf("ATH-ANSWER-MINER: /internal/workers gives the miner's best as %v, want 7e9", got.MinerATH[miner])
	}

	listed := withConnectedWorkers(nil, []stratum.WorkerRef{{MinerID: miner, WorkerName: "nerd", ConnectedAt: time.Now()}})
	if len(listed) != 1 || listed[0].ATHDiff != 7e9 {
		t.Fatalf("ATH-CONNECTED-KEPT: a worker connected with no share in this run is listed as %+v, want its kept best 7e9", listed)
	}
}

// The stratum reads and writes the best shares while it runs, and writes the last ones once no
// share can come in any more, before the database closes. A source check: KeepBestShares and
// WriteBestShares work on their own, and every test above passes with them never called.
func TestStratumKeepsTheBestShares(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	body := src[strings.Index(src, "\nfunc main() {"):]
	body = body[:strings.Index(body, "\n}\n")]
	loop := strings.Index(body, "go stats.GetManager().KeepBestShares(workerTimeoutStop)")
	if loop < 0 || loop < strings.Index(body, "workerTimeoutStop := make(chan struct{})") {
		t.Error("ATH-WIRED-LOOP: main does not run KeepBestShares until it stops")
	}
	last := strings.LastIndex(body, "stats.GetManager().WriteBestShares()")
	if last < 0 || last < strings.Index(body, "stratumServer.Stop()") || last < strings.Index(body, "stopping.Wait()") {
		t.Error("ATH-WIRED-SHUTDOWN: main does not write the last best shares once both stratum servers stopped")
	}
	// The TIDES shares still queued are paid: they are sent first.
	if flush := strings.Index(body, "g.Flush()"); flush < 0 || last < flush {
		t.Error("ATH-WIRED-AFTER-FLUSH: main writes the last best shares before it sends the TIDES shares still queued")
	}
	if !strings.Contains(src, "json.NewEncoder(w).Encode(internalWorkers())") {
		t.Error("ATH-WIRED-ANSWER: /internal/workers does not give internalWorkers(), with the miners' best shares")
	}
}

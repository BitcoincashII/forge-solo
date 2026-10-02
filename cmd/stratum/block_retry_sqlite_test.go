//go:build sqlite

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/datum/wire"
	"github.com/BitcoincashII/forge-solo/internal/mining"
	"github.com/BitcoincashII/forge-solo/internal/stats"
	"github.com/BitcoincashII/forge-solo/internal/stratum"
	"github.com/BitcoincashII/forge-solo/internal/tides"
	"go.uber.org/zap"
)

// fakeNode answers submitblock and getblockhash the way the node does, after failing its first
// `down` requests as a restarting node would.
type fakeNode struct {
	mu        sync.Mutex
	down      int    // requests still to fail
	refuse    string // a submitblock verdict to give instead of accepting
	other     string // a hash another block holds the height with
	accepted  string // the hash of the block taken
	requests  int
	submitted int
}

func (n *fakeNode) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	n.mu.Lock()
	defer n.mu.Unlock()
	n.requests++
	if n.down > 0 {
		n.down--
		http.Error(w, "Work queue depth exceeded", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	json.Unmarshal(body, &req)
	switch req.Method {
	case "submitblock":
		n.submitted++
		if n.refuse != "" {
			json.NewEncoder(w).Encode(map[string]any{"result": n.refuse, "error": nil})
			return
		}
		var blockHex string
		json.Unmarshal(req.Params[0], &blockHex)
		header, _ := hex.DecodeString(blockHex[:160])
		h1 := sha256.Sum256(header)
		h2 := sha256.Sum256(h1[:])
		for i, j := 0, 31; i < j; i, j = i+1, j-1 {
			h2[i], h2[j] = h2[j], h2[i]
		}
		n.accepted = hex.EncodeToString(h2[:])
		json.NewEncoder(w).Encode(map[string]any{"result": nil, "error": nil})
	case "getblockhash":
		switch {
		case n.other != "":
			json.NewEncoder(w).Encode(map[string]any{"result": n.other, "error": nil})
		case n.accepted != "":
			json.NewEncoder(w).Encode(map[string]any{"result": n.accepted, "error": nil})
		default:
			json.NewEncoder(w).Encode(map[string]any{"result": nil, "error": map[string]any{"code": -8, "message": "Block height out of range"}})
		}
	default:
		json.NewEncoder(w).Encode(map[string]any{"result": nil, "error": nil})
	}
}

// shortRetries makes the block retries take milliseconds instead of minutes.
func shortRetries(t *testing.T) {
	t.Helper()
	a, b, c, d, e := blockRetryFirstWait, blockRetryMaxWait, blockSubmitRetryFor, blockRecordRetryFor, blockRecordMaxWait
	blockRetryFirstWait, blockRetryMaxWait, blockSubmitRetryFor, blockRecordRetryFor, blockRecordMaxWait =
		5*time.Millisecond, 20*time.Millisecond, 3*time.Second, 3*time.Second, 20*time.Millisecond
	t.Cleanup(func() {
		blockRetryFirstWait, blockRetryMaxWait, blockSubmitRetryFor, blockRecordRetryFor, blockRecordMaxWait = a, b, c, d, e
	})
}

// findBlock runs a solo block found at height through submitBlock against node.
func findBlock(t *testing.T, node *fakeNode, height int64) {
	t.Helper()
	findBlockPaying(t, node, height, "")
}

// findBlockPaying is findBlock on a job whose coinbase pays payTo and says so (blockTestPayout,
// unsaid, when payTo is ""), found by a miner credited to blockTestPayout.
func findBlockPaying(t *testing.T, node *fakeNode, height int64, payTo string) {
	t.Helper()
	srv := httptest.NewServer(node)
	t.Cleanup(srv.Close)
	savedURL, savedLogger := rpcURL, logger
	rpcURL, logger = srv.URL, zap.NewNop()
	t.Cleanup(func() { rpcURL, logger = savedURL, savedLogger })
	t.Setenv("RPC_USER", "u") // the stratum always has the node's credentials
	t.Setenv("RPC_PASSWORD", "p")
	coinbaseTo := payTo
	if coinbaseTo == "" {
		coinbaseTo = blockTestPayout
	}
	cb1, cb2, err := wire.BuildCoinbase(height, []byte("test"), []tides.Output{{Address: coinbaseTo, Sats: 50_0000_0000}})
	if err != nil {
		t.Fatal(err)
	}
	id := "job-" + hex.EncodeToString([]byte{byte(height)})
	jobHistoryMu.Lock()
	jobHistory[id] = &mining.Job{ID: id, Height: height, CoinBase1: cb1, CoinBase2: cb2, Version: "20000000", NBits: "207fffff",
		NTime: "66f8a1b2", PrevBlockHash: "00000000000000000000000000000000000000000000000000000000000000aa", CoinbaseValue: 50_0000_0000,
		PayTo: payTo}
	jobHistoryMu.Unlock()
	share := &stratum.Share{JobID: id, MinerID: blockTestPayout, WorkerName: "rig1", ExtraNonce1: "00000001",
		ExtraNonce2: "0000000000000001", NTime: "66f8a1b2", Nonce: "00000000", IsSolo: true}
	(&BlockFindingShareProcessor{logger: zap.NewNop()}).submitBlock(share)
}

const blockTestPayout = "bitcoincashii:qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzse6qye33q"

func recorded(height int64) bool { return recordedFor(blockTestPayout, height) }

func recordedFor(addr string, height int64) bool {
	for _, b := range stats.GetMinerBlocksDB(addr) {
		if b.Height == height {
			return true
		}
	}
	return false
}

// A node that is restarting or too busy to answer for longer than the old six seconds of retries
// still gets the block when it comes back, and the block is recorded.
func TestABlockOutlastsANodeThatIsDownAWhile(t *testing.T) {
	if err := stats.InitDB(filepath.Join(t.TempDir(), "b.db")); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()
	shortRetries(t)
	node := &fakeNode{down: 12} // the old loop gave up after 7 requests
	findBlock(t, node, 301)
	if node.accepted == "" || !recorded(301) {
		t.Fatalf("PAY2-DOWN-THEN-UP: the block was given up while the node was down (accepted %q, recorded %v, %d requests)", node.accepted, recorded(301), node.requests)
	}
}

// Another block at the height, or a refusal of the block itself, ends the retries at once.
func TestABlockLostOrRefusedIsNotRetried(t *testing.T) {
	if err := stats.InitDB(filepath.Join(t.TempDir(), "b.db")); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()
	shortRetries(t)

	lost := &fakeNode{refuse: "inconclusive", other: "00000000000000000000000000000000000000000000000000000000000000bb"}
	start := time.Now()
	findBlock(t, lost, 302)
	if recorded(302) || time.Since(start) > time.Second || lost.submitted > 1 {
		t.Fatalf("PAY2-OTHER-BLOCK: a block that lost its height was recorded=%v, or kept trying (%v, %d submits)", recorded(302), time.Since(start), lost.submitted)
	}

	refused := &fakeNode{refuse: "high-hash"}
	start = time.Now()
	findBlock(t, refused, 303)
	if recorded(303) || time.Since(start) > time.Second || refused.submitted > 2 {
		t.Fatalf("PAY2-REFUSED: a refused block was recorded=%v, or kept trying (%v, %d submits)", recorded(303), time.Since(start), refused.submitted)
	}
}

// A block found while the database is down is recorded once it is back: it used to be missing
// from the dashboard for good.
func TestABlockFoundWhileTheDatabaseIsDownIsRecordedLater(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.db")
	if err := stats.InitDB(path); err != nil {
		t.Fatal(err)
	}
	shortRetries(t)
	stats.CloseDB() // the database is down when the block is found
	findBlock(t, &fakeNode{}, 304)
	time.Sleep(100 * time.Millisecond)
	if err := stats.InitDB(path); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()
	deadline := time.Now().Add(2 * time.Second)
	for !recorded(304) {
		if time.Now().After(deadline) {
			t.Fatal("DATA2-DB-BACK: a block found while the database was down was never recorded once it was back")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A block found on a job from before an address change paid the old address, and is recorded
// under it: it was recorded under the address the miner was credited to by then.
func TestABlockIsRecordedUnderTheAddressItPaid(t *testing.T) {
	if err := stats.InitDB(filepath.Join(t.TempDir(), "b.db")); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()
	shortRetries(t)
	old := testAddr(7)
	findBlockPaying(t, &fakeNode{}, 305, old)
	if !recordedFor(old, 305) || recorded(305) {
		t.Fatalf("PAY3-RECORD: a block paying %s is recorded under it: %v, under the miner's new address: %v", old, recordedFor(old, 305), recorded(305))
	}
	for _, b := range stats.GetMinerBlocksDB(old) {
		if b.Height == 305 && b.PayoutTxid == "" {
			t.Fatal("PAY3-RECORD-PAYOUT: the block is under the address it paid, but its payout is under another")
		}
	}
}

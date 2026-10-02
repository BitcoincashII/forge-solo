//go:build sqlite

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/stats"
	"go.uber.org/zap"
)

// chainNode answers getblockhash from a map of height to hash, or fails while down.
type chainNode struct {
	mu     sync.Mutex
	hashes map[int64]string
	down   bool
}

func (c *chainNode) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.down {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Method string  `json:"method"`
		Params []int64 `json:"params"`
	}
	json.Unmarshal(body, &req)
	if h, ok := c.hashes[req.Params[0]]; ok && req.Method == "getblockhash" {
		json.NewEncoder(w).Encode(map[string]any{"result": h, "error": nil})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"result": nil, "error": map[string]any{"code": -8, "message": "Block height out of range"}})
}

func soloStatus(t *testing.T, height int64) string {
	t.Helper()
	for _, b := range stats.GetMinerSoloBlocksDB(blockTestPayout) {
		if b.Height == height {
			return b.Status
		}
	}
	t.Fatalf("no block recorded at %d", height)
	return ""
}

// Every pending solo block is checked against the chain from six blocks deep: an orphan is marked
// at once, our own block is confirmed only once mature, and no block is ever confirmed unchecked.
func TestPendingSoloBlocksAreJudgedByHash(t *testing.T) {
	if err := stats.InitDB(filepath.Join(t.TempDir(), "o.db")); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()
	h := func(c byte) string { return strings.Repeat(string(c), 64) }
	node := &chainNode{hashes: map[int64]string{100: h('b'), 101: h('c'), 103: h('b'), 197: h('e')}}
	srv := httptest.NewServer(node)
	defer srv.Close()
	savedURL, savedLogger := rpcURL, logger
	rpcURL, logger = srv.URL, zap.NewNop()
	t.Cleanup(func() { rpcURL, logger = savedURL, savedLogger })
	t.Setenv("RPC_USER", "u")
	t.Setenv("RPC_PASSWORD", "p")
	for height, hash := range map[int64]string{100: h('a'), 101: h('c'), 103: h('a'), 197: h('d')} {
		if err := stats.SaveSoloBlockCoinbaseDirect(blockTestPayout, height, 50, hash); err != nil {
			t.Fatal(err)
		}
	}

	node.down = true
	reconcilePendingSoloBlocks(200)
	if s := soloStatus(t, 100); s != "pending" {
		t.Fatalf("ORPH-NODE-DOWN: with the node down a block became %q", s)
	}
	node.down = false

	reconcilePendingSoloBlocks(107)
	if s := soloStatus(t, 100); s != "orphaned" {
		t.Fatalf("ORPH-EARLY: a block whose height another block holds, 7 deep, is %q; want orphaned", s)
	}
	if s := soloStatus(t, 103); s != "pending" {
		t.Fatalf("ORPH-TOO-SHALLOW: a block 4 deep was judged (%q)", s)
	}
	if s := soloStatus(t, 101); s != "pending" {
		t.Fatalf("ORPH-NO-EARLY-CONFIRM: our block 6 deep, still immature, is %q; want pending", s)
	}
	reconcilePendingSoloBlocks(202)
	if s := soloStatus(t, 101); s != "confirmed" {
		t.Fatalf("ORPH-CONFIRM-MATURE: our block 101 deep is %q; want confirmed", s)
	}
	// The node was away or catching up: the tip is now hundreds of blocks past the old check band,
	// and the block at 197 -- too shallow to judge at 202 -- was never checked before.
	if s := soloStatus(t, 197); s != "pending" {
		t.Fatalf("ORPH-JUMP-SETUP: the block at 197 was judged before the jump (%q)", s)
	}
	reconcilePendingSoloBlocks(900)
	if s := soloStatus(t, 197); s != "orphaned" {
		t.Fatalf("ORPH-JUMP: a block far below the old check band, whose height another block holds, is %q; want orphaned", s)
	}
}

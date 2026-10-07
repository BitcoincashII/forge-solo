package main

import (
	"path/filepath"
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/stats"
)

// A block found while the node restarts is kept trying through the node's warmup: every call is
// answered -28 "Loading block index…" until it has loaded the chain, and that is not the node
// refusing the block.
func TestBlockSurvivesNodeWarmup(t *testing.T) {
	if err := stats.InitDB(filepath.Join(t.TempDir(), "b.db")); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()
	shortRetries(t)
	node := &fakeNode{down: 1, warming: 3}
	findBlock(t, node, 401)
	if node.accepted == "" || !recorded(401) {
		t.Fatalf("BLOCK-WARMUP: block given up while the node was warming up (accepted %q, recorded %v, %d requests)", node.accepted, recorded(401), node.requests)
	}
}

// A block the node cannot even read (-22, "Block decode failed") is not tried for half an hour.
func TestBlockDecodeErrorIsFinal(t *testing.T) {
	if err := stats.InitDB(filepath.Join(t.TempDir(), "b.db")); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()
	shortRetries(t)
	node := &fakeNode{errCode: -22}
	findBlock(t, node, 402)
	if node.submitted > 2 || recorded(402) {
		t.Fatalf("BLOCK-DECODE-FINAL: a block the node cannot read was submitted %d times (recorded %v)", node.submitted, recorded(402))
	}
}

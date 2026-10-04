//go:build sqlite

package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/stats"
)

// recorded1175 records and credits a solo 1175 block.
func recorded1175(t *testing.T, height int64, hash, finder string) {
	t.Helper()
	if err := stats.Record1175Block(height, hash, 25, finder, true); err != nil {
		t.Fatal(err)
	}
	if err := stats.Distribute1175Block(height, 0); err != nil {
		t.Fatal(err)
	}
}

func pending1175(height int64) bool {
	blocks, _ := stats.UnconfirmedBlocks1175()
	for _, b := range blocks {
		if b[0].(int64) == height {
			return true
		}
	}
	return false
}

// A 1175 node that is rebuilding its chain (-reindex), or still syncing, answers -1 for a block on
// the chain it has not connected yet. A block judged then was marked orphaned for good and left
// out of the 1175 totals, though it matured on the chain. It is judged once the node has caught
// up.
func TestA1175BlockIsNotOrphanedWhileThe1175NodeCatchesUp(t *testing.T) {
	if err := stats.InitDB(filepath.Join(t.TempDir(), "aux.db")); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()
	node, _ := useAuxConfNode(t)
	const finder = "bitcoincashii:qfinder1175"
	hash := strings.Repeat("a", 64)
	recorded1175(t, 900, hash, finder)
	node.set(hash, -1)

	node.setChain(2582, 4500, true) // as v29.1.0 reports mid -reindex
	run1175PayoutCycle()
	if !pending1175(900) {
		t.Fatal("AUX-REINDEX-ORPHAN: a block the node had not connected yet, mid -reindex, was judged")
	}
	node.setChain(4499, 4500, false) // out of initial sync, a block still to connect
	run1175PayoutCycle()
	if !pending1175(900) {
		t.Fatal("AUX-BEHIND-ORPHAN: a block was judged while the node had a header it had not connected")
	}

	node.setChain(5000, 5000, false)
	node.set(hash, 106)
	run1175PayoutCycle()
	if pending1175(900) {
		t.Fatal("AUX-CAUGHT-UP: the caught-up node was not asked; the block stays pending")
	}
	if n, paid, _ := stats.Miner1175Totals(finder, true); n != 1 || paid != 25 {
		t.Fatalf("AUX-CAUGHT-UP-TOTALS: %d blocks, %v paid; want 1, 25", n, paid)
	}
}

// A block marked orphaned that the caught-up node has on its chain (a reorg that turned back, or
// a mark made before the check above) is put back with its credits, and confirmed at maturity.
// A block orphaned far below the tip is not asked about every cycle.
func TestA1175BlockOrphanedByMistakeIsPutBack(t *testing.T) {
	if err := stats.InitDB(filepath.Join(t.TempDir(), "aux.db")); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()
	node, _ := useAuxConfNode(t)
	const finder = "bitcoincashii:qfinder1175"
	back, gone, old := strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64)
	recorded1175(t, 4901, back, finder)
	recorded1175(t, 4902, gone, finder)
	recorded1175(t, 10, old, finder)
	for _, h := range []int64{4901, 4902, 10} {
		if err := stats.Orphan1175Block(h); err != nil {
			t.Fatal(err)
		}
	}
	node.set(back, 5)
	node.set(gone, -1)
	node.set(old, 4000)
	node.setChain(5000, 5000, false)

	run1175PayoutCycle()
	if !pending1175(4901) {
		t.Fatal("AUX-ORPHAN-BACK: a block on the 1175 chain stays marked orphaned")
	}
	if pending1175(4902) {
		t.Fatal("AUX-ORPHAN-STAYS: a block the node has off its chain was put back")
	}
	if n := node.askedAbout(old); n != 0 {
		t.Fatalf("AUX-ORPHAN-WINDOW: a block orphaned %d blocks below the tip was asked about %d times", 5000-10, n)
	}
	if err := stats.Confirm1175Block(4901); err != nil {
		t.Fatal(err)
	}
	if miners, _ := stats.ConfirmedPendingMiners1175(); len(miners) != 1 || miners[0] != finder {
		t.Fatalf("AUX-ORPHAN-BACK-CREDIT: the block is back, but its credit is not: %v", miners)
	}
}

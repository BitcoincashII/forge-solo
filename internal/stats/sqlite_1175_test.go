//go:build sqlite

package stats

import (
	"path/filepath"
	"testing"
)

// The 1175 ledger is shared code with postgres-shaped SQL, and Init1175Schema used to live
// only in the postgres backend. On SQLite -- every Windows build -- blocks_1175 therefore
// never existed, and a found aux block died in the handler with "no such table: blocks_1175",
// logged as "1175 record block FAILED (block may be lost — verify)". The block itself
// survived (the aux coinbase pays on-chain directly); the ledger row and the dashboard
// entry did not.
//
// This drives the REAL functions the stratum calls when it finds an aux block, in the order
// it calls them, against a real SQLite file.
func Test1175LedgerWorksOnSQLite(t *testing.T) {
	if err := InitDB(filepath.Join(t.TempDir(), "esf.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	defer CloseDB()

	const (
		height = 207
		hash   = "f88b171967ccfef0aebc417c77ebb3716792f64e2b03764b443463bb50901504"
		miner  = "bitcoincashii:qpvg5aehqc3mtrmf2say7tmn0t9cxcw0wsahs5d9r5"
		reward = 25.0
	)

	// cmd/stratum aux1175BlockHandler: record, then distribute.
	if err := Record1175Block(height, hash, reward, miner, true); err != nil {
		t.Fatalf("Record1175Block: %v", err)
	}
	// Same arguments the stratum passes: (height, pplnsWindow).
	if err := Distribute1175Block(height, 100000); err != nil {
		t.Fatalf("Distribute1175Block: %v", err)
	}

	// The reconciliation loop: find it undistributed/unconfirmed, then confirm it.
	if _, err := UndistributedBlocks1175(); err != nil {
		t.Fatalf("UndistributedBlocks1175: %v", err)
	}
	pending, err := UnconfirmedBlocks1175()
	if err != nil {
		t.Fatalf("UnconfirmedBlocks1175: %v", err)
	}
	var found bool
	for _, hb := range pending {
		if h, ok := hb[0].(int64); ok && h == height {
			found = true
		}
	}
	if !found {
		t.Fatalf("recorded block %d is not in UnconfirmedBlocks1175 (%v)", height, pending)
	}
	if err := Confirm1175Block(height); err != nil {
		t.Fatalf("Confirm1175Block: %v", err)
	}

	// Solo settle: coinbase-direct, DB-only.
	miners, err := ConfirmedPendingMiners1175()
	if err != nil {
		t.Fatalf("ConfirmedPendingMiners1175: %v", err)
	}
	if len(miners) == 0 {
		t.Fatal("no miners with confirmed-pending 1175 payouts")
	}
	n, err := Settle1175ByCoinbase(miner)
	if err != nil {
		t.Fatalf("Settle1175ByCoinbase: %v", err)
	}
	if n != 1 {
		t.Errorf("settled %d payouts, want 1", n)
	}

	// The dashboard read path: this is the query carrying EXTRACT(EPOCH ...)::bigint on
	// postgres, which SQLite cannot parse at all.
	blocks, err := Get1175BlocksForMiner(miner, true, 10)
	if err != nil {
		t.Fatalf("Get1175BlocksForMiner: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("dashboard shows %d 1175 blocks, want 1", len(blocks))
	}
	if blocks[0].Height != height {
		t.Errorf("height = %d, want %d", blocks[0].Height, height)
	}
	if blocks[0].Time <= 0 {
		t.Errorf("Time = %d; the epoch conversion produced nothing usable", blocks[0].Time)
	}

	// The stuck-batch sweep: interval arithmetic, also postgres-only in its original form.
	if _, err := StuckSending1175(600); err != nil {
		t.Fatalf("StuckSending1175: %v", err)
	}
}

// Init1175Schema must be safe to run again over an existing file, which is what every
// restart does.
func Test1175SchemaIsIdempotentOnSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "esf2.db")
	if err := InitDB(path); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	if err := Record1175Block(1, "h", 25, "m", true); err != nil {
		t.Fatalf("Record1175Block: %v", err)
	}
	CloseDB()

	if err := InitDB(path); err != nil {
		t.Fatalf("re-InitDB: %v", err)
	}
	defer CloseDB()
	heights, err := UnconfirmedBlocks1175()
	if err != nil {
		t.Fatalf("after reopen: %v", err)
	}
	if len(heights) != 1 {
		t.Errorf("after reopen got %d unconfirmed blocks, want the 1 recorded before", len(heights))
	}
}

// Winning the same aux height twice is the ordinary outcome of a small reorg on the aux
// chain. With ON CONFLICT DO NOTHING alone, the orphaned first block's row stayed forever:
// permanently 'pending', its credits never voided, and the real block never recorded at all.
// BCH2's recordBlockRow has always superseded; the 1175 ledger did not.
func Test1175BlockSupersedesAReorgedOutHeight(t *testing.T) {
	if err := InitDB(filepath.Join(t.TempDir(), "reorg.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	defer CloseDB()

	const (
		height  = 4242
		orphan  = "1111111111111111111111111111111111111111111111111111111111111111"
		winner  = "2222222222222222222222222222222222222222222222222222222222222222"
		miner   = "bitcoincashii:qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzse6qye33q"
		rewardA = 25.0
		rewardB = 26.5
	)

	if err := Record1175Block(height, orphan, rewardA, miner, true); err != nil {
		t.Fatalf("record orphan: %v", err)
	}
	if err := Distribute1175Block(height, 100000); err != nil {
		t.Fatalf("distribute orphan: %v", err)
	}

	// The chain reorgs and we win the same height with a different block.
	if err := Record1175Block(height, winner, rewardB, miner, true); err != nil {
		t.Fatalf("record winner: %v", err)
	}

	var gotHash, gotStatus string
	var gotReward float64
	var distributed bool
	if err := db.QueryRow(`SELECT hash, status, gross_reward, distributed FROM blocks_1175 WHERE height=?`, height).
		Scan(&gotHash, &gotStatus, &gotReward, &distributed); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if gotHash != winner {
		t.Errorf("hash = %s, want the block that actually won (%s); the orphan is still recorded", gotHash, winner)
	}
	if gotReward != rewardB {
		t.Errorf("gross_reward = %v, want %v", gotReward, rewardB)
	}
	if gotStatus != "pending" || distributed {
		t.Errorf("status=%q distributed=%v, want a fresh pending/undistributed row", gotStatus, distributed)
	}

	// The orphaned block's unpaid credits must be voided, not left payable.
	var pending int
	if err := db.QueryRow(`SELECT COUNT(*) FROM payouts_1175 WHERE block_height=? AND status='pending'`, height).Scan(&pending); err != nil {
		t.Fatalf("count pending: %v", err)
	}
	if pending != 0 {
		t.Errorf("%d payout row(s) at the reorged height are still pending; the orphaned block's credits were never voided", pending)
	}

	// Exactly one row per height, still.
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM blocks_1175 WHERE height=?`, height).Scan(&rows); err != nil {
		t.Fatalf("count blocks: %v", err)
	}
	if rows != 1 {
		t.Errorf("blocks_1175 has %d rows at height %d, want 1", rows, height)
	}
}

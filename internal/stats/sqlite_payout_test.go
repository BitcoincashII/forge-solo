//go:build sqlite

package stats

import (
	"path/filepath"
	"testing"
)

// Exercises the SQLite payout path against a real database file. Compiling was never the
// hard part -- the schema was. pool_config did not exist, blocks had no confirmed_at, and
// payouts had no UNIQUE(miner_address, block_height), so these calls would have linked
// and then failed at runtime on a user's Windows box.
func openTestDB(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	if err := InitDB(path); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(CloseDB)
}

// The reservation must be exactly-once. A second reserve for the same miner must return
// nothing, because the first claimed every eligible row -- this is what stops the auto
// processor and a manual request from both paying the same balance.
func TestSQLiteReserveMaturePayoutsIsExactlyOnce(t *testing.T) {
	openTestDB(t)

	const miner = "bitcoincashii:qtestminer000000000000000000000000000000"
	for _, h := range []int64{100, 101, 102} {
		if err := SavePayout(miner, h, 1.25); err != nil {
			t.Fatalf("SavePayout(%d): %v", h, err)
		}
	}

	pendingID, rows, total, err := ReserveMaturePayouts(miner, 200)
	if err != nil {
		t.Fatalf("first reserve: %v", err)
	}
	if pendingID == "" || len(rows) != 3 {
		t.Fatalf("first reserve: got id=%q rows=%d, want an id and 3 rows", pendingID, len(rows))
	}
	if total < 3.74 || total > 3.76 {
		t.Errorf("total = %v, want 3.75", total)
	}
	// Ordered by block_height.
	if rows[0].BlockHeight != 100 || rows[2].BlockHeight != 102 {
		t.Errorf("rows not ordered by height: %v", rows)
	}

	// THE PROPERTY: nothing left to claim.
	id2, rows2, total2, err := ReserveMaturePayouts(miner, 200)
	if err != nil {
		t.Fatalf("second reserve: %v", err)
	}
	if id2 != "" || len(rows2) != 0 || total2 != 0 {
		t.Fatalf("second reserve claimed rows already reserved -- double-pay: id=%q rows=%d total=%v",
			id2, len(rows2), total2)
	}

	// Reverting returns them to claimable.
	ids := []int64{rows[0].ID, rows[1].ID, rows[2].ID}
	if err := RevertPayoutRows(ids); err != nil {
		t.Fatalf("RevertPayoutRows: %v", err)
	}
	if _, again, _, err := ReserveMaturePayouts(miner, 200); err != nil || len(again) != 3 {
		t.Fatalf("after revert: rows=%d err=%v, want 3 claimable again", len(again), err)
	}

	// Finalizing stamps the real txid and refreshes the cached columns.
	if err := FinalizePayoutRows(ids, "realtxid123"); err != nil {
		t.Fatalf("FinalizePayoutRows: %v", err)
	}
	var paid int
	if err := db.QueryRow(`SELECT COUNT(*) FROM payouts WHERE txid='realtxid123' AND confirmed=1 AND status='paid'`).Scan(&paid); err != nil {
		t.Fatalf("verify finalize: %v", err)
	}
	if paid != 3 {
		t.Errorf("finalized %d rows, want 3", paid)
	}
}

// Maturity must bound the claim: rows above matureHeight are not reservable.
func TestSQLiteReserveRespectsMaturity(t *testing.T) {
	openTestDB(t)
	const miner = "bitcoincashii:qmaturity00000000000000000000000000000000"
	_ = SavePayout(miner, 50, 1)
	_ = SavePayout(miner, 500, 1)

	_, rows, _, err := ReserveMaturePayouts(miner, 100)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if len(rows) != 1 || rows[0].BlockHeight != 50 {
		t.Fatalf("got %d rows %v, want only height 50", len(rows), rows)
	}
}

// pool_config had no table in SQLite at all; a missing row must read as defaults.
func TestSQLitePoolConfigRoundTrips(t *testing.T) {
	openTestDB(t)

	if _, _, _, err := GetPoolConfig(); err != nil {
		t.Fatalf("GetPoolConfig on empty: %v", err)
	}

	if err := SavePoolConfig("bitcoincashii:qpool", "esf1payout", "/forge/"); err != nil {
		t.Fatalf("SavePoolConfig: %v", err)
	}
	addr, p1175, tag, err := GetPoolConfig()
	if err != nil || addr != "bitcoincashii:qpool" || p1175 != "esf1payout" || tag != "/forge/" {
		t.Fatalf("round trip: %q %q %q err=%v", addr, p1175, tag, err)
	}

	// Upsert, not a second row.
	if err := SavePoolConfig("bitcoincashii:qpool2", "", ""); err != nil {
		t.Fatalf("SavePoolConfig upsert: %v", err)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM pool_config`).Scan(&n)
	if n != 1 {
		t.Errorf("pool_config rows = %d, want 1", n)
	}
}

// Solo blocks settle coinbase-direct and must be idempotent on re-record -- that needs
// blocks.confirmed_at and the payouts uniqueness index, neither of which SQLite had.
func TestSQLiteSoloBlockLifecycle(t *testing.T) {
	openTestDB(t)
	const miner = "bitcoincashii:qsolo0000000000000000000000000000000000"

	if err := SaveSoloBlockCoinbaseDirect(miner, 900, 3.125, "hashaaa"); err != nil {
		t.Fatalf("SaveSoloBlockCoinbaseDirect: %v", err)
	}
	// Re-record of the SAME block must not create a second payout row.
	if err := SaveSoloBlockCoinbaseDirect(miner, 900, 3.125, "hashaaa"); err != nil {
		t.Fatalf("re-record: %v", err)
	}
	var rows int
	_ = db.QueryRow(`SELECT COUNT(*) FROM payouts WHERE block_height=900`).Scan(&rows)
	if rows != 1 {
		t.Fatalf("payout rows for height 900 = %d, want 1 (duplicate credit)", rows)
	}

	// A coinbase-direct row is already settled, so it must never be reservable.
	if _, r, _, err := ReserveMaturePayouts(miner, 1000); err != nil || len(r) != 0 {
		t.Fatalf("coinbase-direct row was reservable: rows=%d err=%v", len(r), err)
	}

	heights, err := PendingSoloHeights(1000, 0)
	if err != nil || len(heights) != 1 || heights[0] != 900 {
		t.Fatalf("PendingSoloHeights = %v err=%v, want [900]", heights, err)
	}

	if hash, ok := GetRecordedBlockHash(900); !ok || hash != "hashaaa" {
		t.Fatalf("GetRecordedBlockHash = %q,%v want hashaaa,true", hash, ok)
	}

	if err := ConfirmSoloBlock(900); err != nil {
		t.Fatalf("ConfirmSoloBlock: %v", err)
	}
	var status string
	_ = db.QueryRow(`SELECT status FROM blocks WHERE height=900`).Scan(&status)
	if status != "confirmed" {
		t.Errorf("status = %q, want confirmed", status)
	}
	// Confirmed is terminal for PendingSoloHeights.
	if h, _ := PendingSoloHeights(1000, 0); len(h) != 0 {
		t.Errorf("confirmed block still pending: %v", h)
	}
}

// A reorged-out solo block must be voided, never left overstating earnings.
func TestSQLiteOrphanSoloBlock(t *testing.T) {
	openTestDB(t)
	const miner = "bitcoincashii:qorphan000000000000000000000000000000000"

	if err := SaveSoloBlockCoinbaseDirect(miner, 950, 3.125, "hashbbb"); err != nil {
		t.Fatalf("save: %v", err)
	}
	n, err := OrphanSoloBlock(950)
	if err != nil || n != 1 {
		t.Fatalf("OrphanSoloBlock = %d,%v want 1,nil", n, err)
	}
	var bs, ps string
	_ = db.QueryRow(`SELECT status FROM blocks WHERE height=950`).Scan(&bs)
	_ = db.QueryRow(`SELECT status FROM payouts WHERE block_height=950`).Scan(&ps)
	if bs != "orphaned" || ps != "orphaned" {
		t.Errorf("block=%q payout=%q, want both orphaned", bs, ps)
	}
}

// Unpaid rows at an orphaned height must become permanently unpayable.
func TestSQLiteVoidOrphanedPayouts(t *testing.T) {
	openTestDB(t)
	const miner = "bitcoincashii:qvoid00000000000000000000000000000000000"
	_ = SavePayout(miner, 800, 2.0)

	heights, err := GetUnpaidMatureHeights(1000, 0)
	if err != nil || len(heights) != 1 || heights[0] != 800 {
		t.Fatalf("GetUnpaidMatureHeights = %v err=%v, want [800]", heights, err)
	}

	n, amount, err := VoidOrphanedPayouts(800)
	if err != nil || n != 1 {
		t.Fatalf("VoidOrphanedPayouts = %d,%v,%v", n, amount, err)
	}
	if amount < 1.99 || amount > 2.01 {
		t.Errorf("voided amount = %v, want 2.0", amount)
	}
	// Now unreservable and no longer reported unpaid.
	if _, r, _, _ := ReserveMaturePayouts(miner, 1000); len(r) != 0 {
		t.Errorf("voided row was reservable -- would pay for a block we never received")
	}
	if h, _ := GetUnpaidMatureHeights(1000, 0); len(h) != 0 {
		t.Errorf("voided height still reported unpaid: %v", h)
	}
}

//go:build sqlite

package stats

import (
	"path/filepath"
	"testing"
)

// A solo block pays its finder in its OWN coinbase. SaveSoloBlockCoinbaseDirect settles the
// payout row immediately with txid='coinbase-direct' and status='paid' -- there is no pool
// wallet to send from, and nothing to accumulate.
//
// This app therefore has no minimum payout and, as of this change, no wallet-send path at
// all: payMiner, sendPayout and /internal/trigger-payout are gone. ReserveMaturePayouts is
// what any future sender would have to go through, and it selects only rows with a NULL or
// empty txid -- so a settled solo reward can never be reserved for sending, at any balance.
//
// This is re-anchored on purpose. The previous version asserted against GetReadyPayoutsDB,
// which was NOT what fed the sender, and that mistake is why the sender was believed
// unreachable when it was not.
//
// If someone later changes the settle to leave txid NULL, this test fails. That is the
// warning that wants heeding: it would make a solo reward reservable for a send against a
// wallet this app does not have.
func TestSoloPayoutIsNeverReservableForSending(t *testing.T) {
	if err := InitDB(filepath.Join(t.TempDir(), "solo.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	defer CloseDB()

	const (
		miner  = "bitcoincashii:qzeh9rcyyy8jlyalgh84e8fst6xh649hly2tfwgvwc"
		height = 31
		reward = 50.0
	)
	if err := SaveSoloBlockCoinbaseDirect(miner, height, reward, "deadbeef"); err != nil {
		t.Fatalf("SaveSoloBlockCoinbaseDirect: %v", err)
	}

	// Far past coinbase maturity: there must still be nothing to reserve.
	current := int64(height) + COINBASE_MATURITY + 1000
	pendingID, rows, total, err := ReserveMaturePayouts(miner, current)
	if err != nil {
		t.Fatalf("ReserveMaturePayouts: %v", err)
	}
	if pendingID != "" || len(rows) != 0 || total != 0 {
		t.Errorf("solo payout was reserved for a wallet send (id=%q rows=%d total=%v). "+
			"It was already paid on-chain by the coinbase; sending it again would need a "+
			"pool wallet this app does not have.", pendingID, len(rows), total)
	}
}

// The row must be settled, not merely hidden: paid, confirmed, and stamped coinbase-direct.
func TestSoloPayoutIsSettledAsCoinbaseDirect(t *testing.T) {
	if err := InitDB(filepath.Join(t.TempDir(), "solo2.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	defer CloseDB()

	const miner = "bitcoincashii:qzeh9rcyyy8jlyalgh84e8fst6xh649hly2tfwgvwc"
	if err := SaveSoloBlockCoinbaseDirect(miner, 31, 50.0, "deadbeef"); err != nil {
		t.Fatalf("SaveSoloBlockCoinbaseDirect: %v", err)
	}

	var txid, status string
	var confirmed bool
	err := db.QueryRow(`SELECT COALESCE(txid,''), COALESCE(status,''), confirmed FROM payouts
		WHERE miner_address=? AND block_height=?`, miner, 31).Scan(&txid, &status, &confirmed)
	if err != nil {
		t.Fatalf("read back the payout row: %v", err)
	}
	if txid != "coinbase-direct" {
		t.Errorf("txid = %q, want %q — a NULL/empty txid puts this row back in the wallet-send queue",
			txid, "coinbase-direct")
	}
	if status != "paid" {
		t.Errorf("status = %q, want \"paid\"", status)
	}
	if !confirmed {
		t.Error("confirmed = false; the coinbase already paid this on-chain")
	}
}

// The balance card was fed from the pool-style ready-to-pay query, which cannot return a
// solo payout (see above), so it showed 0.00 matured and 0.00 maturing while blocks were
// genuinely maturing -- and asserted "waiting 100 confirms" underneath. SoloEarnings reads
// the blocks the miner actually found, which is the only thing that means anything when the
// coinbase has already paid them.
func TestSoloEarningsSplitsByMaturity(t *testing.T) {
	if err := InitDB(filepath.Join(t.TempDir(), "earn.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	defer CloseDB()

	const miner = "bitcoincashii:qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzse6qye33q"
	const tip = 1000
	// Two matured, one still maturing, one orphaned (paid nothing).
	for _, b := range []struct {
		height int64
		reward float64
		status string
	}{
		{100, 50, "confirmed"},
		{200, 50, "confirmed"},
		{tip - 10, 50, "pending"},
		{300, 50, "orphaned"},
	} {
		if _, err := db.Exec(`INSERT INTO blocks (height, hash, miner_address, reward, is_solo, status)
			VALUES (?, ?, ?, ?, 1, ?)`, b.height, "h", miner, b.reward, b.status); err != nil {
			t.Fatalf("seed %d: %v", b.height, err)
		}
	}

	mature, immature := SoloEarnings(miner, tip)
	if mature != 100 {
		t.Errorf("matured = %v, want 100 (two blocks past coinbase maturity, orphan excluded)", mature)
	}
	if immature != 50 {
		t.Errorf("still maturing = %v, want 50", immature)
	}
	if mature == 0 && immature == 0 {
		t.Error("both zero — this is the bug the card had: nothing owed is not the same as nothing mined")
	}
}

// An orphaned payout must not count as confirmed, and must not sit inside "Total Paid".
//
// The confirmation flag was derived by pattern-matching the txid STRING
// (CASE WHEN txid LIKE 'pending_%' THEN false ELSE true), which ignores the
// status='orphaned', confirmed=false that OrphanSoloBlock writes -- and the orphan txid is
// the literal 'orphaned', so it fell through to "confirmed". The dashboard therefore kept a
// voided reward inside Total Paid while the balance card beside it, which does filter
// orphans, disagreed.
func TestOrphanedPayoutIsNotCountedAsPaid(t *testing.T) {
	if err := InitDB(filepath.Join(t.TempDir(), "orphan.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	defer CloseDB()

	const miner = "bitcoincashii:qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzse6qye33q"

	// Two solo blocks settled coinbase-direct, then one of them orphaned.
	if err := SaveSoloBlockCoinbaseDirect(miner, 10, 50, "aaa"); err != nil {
		t.Fatalf("record 10: %v", err)
	}
	if err := SaveSoloBlockCoinbaseDirect(miner, 11, 50, "bbb"); err != nil {
		t.Fatalf("record 11: %v", err)
	}
	if _, err := db.Exec(`UPDATE payouts SET status='orphaned', txid='orphaned', confirmed=0
		WHERE miner_address=? AND block_height=?`, miner, 11); err != nil {
		t.Fatalf("orphan 11: %v", err)
	}

	rows, count, totalPaid := GetMinerSoloPayoutsDB(miner)
	if totalPaid != 50 {
		t.Errorf("totalPaid = %v, want 50 — the orphaned block's 50 is still being counted as paid", totalPaid)
	}
	// PayoutRecord carries no height, so identify the orphan by its txid.
	for _, r := range rows {
		if r.TxID == "orphaned" && r.Confirmed {
			t.Error("the orphaned payout reports confirmed; the chain discarded that block")
		}
	}
	if count == 0 {
		t.Fatal("no payout rows returned at all")
	}
}

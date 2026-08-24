//go:build sqlite

package stats

import (
	"path/filepath"
	"testing"
)

// A minimum payout is meaningless in this app, and these tests pin the reason so it stays
// that way.
//
// A solo block pays its finder in its OWN coinbase. SaveSoloBlockCoinbaseDirect therefore
// settles the payout row immediately with txid='coinbase-direct' and status='paid' -- there
// is no pool wallet to send from, and nothing to accumulate. GetReadyPayoutsDB, the only
// feed into payMiner (the wallet sendtoaddress path), considers only rows with a NULL or
// empty txid. So a solo payout can never be "ready" at ANY threshold, payMiner is
// unreachable, and the min-payout value it takes as an argument can never change an outcome.
//
// The settings UI used to expose a "Minimum payout" field on the strength of that dead
// path. It has been removed. If someone later changes the settle to leave txid NULL, these
// tests fail -- which is the warning that wants heeding, because it would point a
// wallet-send path at a wallet this app does not have.
//
// The postgres backend carries the identical WHERE (txid IS NULL OR txid = ”) filter and
// the identical 'coinbase-direct' stamp; this runs under the sqlite tag because it needs no
// container.
func TestSoloPayoutIsNeverReadyForSending(t *testing.T) {
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

	// Far past coinbase maturity, and every threshold from "none" to "more than the block
	// reward". None of them may produce anything to send.
	current := int64(height) + COINBASE_MATURITY + 1000
	for _, minPayout := range []float64{0, 0.00000001, 1, 2.75, reward, reward * 10} {
		ready := GetReadyPayoutsDB(current, minPayout)
		if len(ready) != 0 {
			t.Errorf("min_payout=%v: solo payout is queued for a wallet send (%v). "+
				"It was already paid on-chain by the coinbase; sending it again would need a "+
				"pool wallet this app does not have.", minPayout, ready)
		}
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

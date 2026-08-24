//go:build sqlite

package stats

import (
	"path/filepath"
	"testing"
)

// An orphaned solo block paid nothing and never will. Its payout row reaches the UI
// with confirmed=false and no real txid -- indistinguishable from a payout still on
// its way -- so the row rendered as "Pending <amount>", advertising money that will
// never arrive, on the same page whose blocks table already called it "Orphaned".
//
// The ledger's status column is what tells the two apart, so it must survive the
// query out to the API. This pins that it does.
func TestOrphanedSoloPayoutCarriesItsStatus(t *testing.T) {
	if err := InitDB(filepath.Join(t.TempDir(), "orphan.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	defer CloseDB()

	const (
		miner  = "bitcoincashii:qzeh9rcyyy8jlyalgh84e8fst6xh649hly2tfwgvwc"
		good   = 41
		orphan = 42
		reward = 50.0
	)
	if err := SaveSoloBlockCoinbaseDirect(miner, good, reward, "aaaa"); err != nil {
		t.Fatalf("save good block: %v", err)
	}
	if err := SaveSoloBlockCoinbaseDirect(miner, orphan, reward, "bbbb"); err != nil {
		t.Fatalf("save orphan block: %v", err)
	}
	if _, err := OrphanSoloBlock(orphan); err != nil {
		t.Fatalf("OrphanSoloBlock: %v", err)
	}

	payouts, total, totalPaid := GetMinerSoloPayoutsDB(miner)
	if total != 2 {
		t.Fatalf("expected both rows in the history, got %d", total)
	}

	var sawOrphan, sawGood bool
	for _, p := range payouts {
		switch p.Status {
		case "orphaned":
			sawOrphan = true
			if p.Confirmed {
				t.Error("an orphaned payout is marked confirmed")
			}
		case "":
			t.Errorf("payout row carries no status at all (txid=%q); the UI cannot "+
				"tell a voided reward from one still on its way and will render it "+
				"as Pending", p.TxID)
		default:
			sawGood = true
		}
	}
	if !sawOrphan {
		t.Error("no row came back with status 'orphaned'; the voided reward is " +
			"indistinguishable from a pending payment in the UI")
	}
	if !sawGood {
		t.Error("the settled payout lost its status too")
	}

	// The voided reward must not be counted as money received.
	if totalPaid != reward {
		t.Errorf("Total Paid = %v, want %v — an orphaned block's reward is inside the total",
			totalPaid, reward)
	}
}

package stats

import (
	"fmt"
	"math"
	"testing"
)

// checkDashboardTotals records 130 solo blocks for one miner, 10 of them orphaned, and 70 solo 1175
// blocks, 5 of them orphaned. The dashboard's figures must cover all of them while its lists hold
// only the latest; past 100 blocks they were the newest 100's. base keeps the heights clear of
// other tests' rows in a shared database. It closes the database at the end.
func checkDashboardTotals(t *testing.T, miner string, base int64) {
	t.Helper()
	const n, orphans = 130, 10
	var want float64
	for i := int64(0); i < n; i++ {
		h := base + i
		amount := 3.125 + float64(i)/1024
		if err := SaveSoloBlockCoinbaseDirect(miner, h, amount, fmt.Sprintf("%064x", h)); err != nil {
			t.Fatal(err)
		}
		if i < orphans {
			if _, err := OrphanSoloBlock(h); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want += amount
	}
	blocks, found, earned, err := SoloBlocksSummary(miner)
	if err != nil || len(blocks) != 100 || found != n || math.Abs(earned-want) > 1e-6 {
		t.Fatalf("DATA6-BLOCKS: %d rows, %d found (want %d), %.8f earned (want %.8f), %v", len(blocks), found, n, earned, want, err)
	}
	payouts, count, paid, err := SoloPayoutsSummary(miner)
	if err != nil || len(payouts) != 100 || count != n || math.Abs(paid-want) > 1e-6 {
		t.Fatalf("DATA6-PAYOUTS: %d rows, %d payouts (want %d), %.8f paid (want %.8f), %v", len(payouts), count, n, paid, want, err)
	}
	if _, c, p := GetMinerSoloPayoutsDB(miner); c != n || math.Abs(p-want) > 1e-6 {
		t.Fatalf("DATA6-OLD-ENTRY: GetMinerSoloPayoutsDB says %d payouts, %.8f paid", c, p)
	}

	const n1175, orphans1175 = 70, 5
	var want1175 float64
	for i := int64(0); i < n1175; i++ {
		h := base + i
		gross := 25 + float64(i)/512
		if err := Record1175Block(h, fmt.Sprintf("%064x", h+1), gross, miner, true); err != nil {
			t.Fatal(err)
		}
		if err := Distribute1175Block(h, 0); err != nil {
			t.Fatal(err)
		}
		if i < orphans1175 {
			if err := Orphan1175Block(h); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want1175 += gross
	}
	if got, err := Get1175BlocksForMiner(miner, true, 50); err != nil || len(got) != 50 {
		t.Fatalf("DATA6-1175-SETUP: %d rows, %v", len(got), err)
	}
	if c, p, err := Miner1175Totals(miner, true); err != nil || c != n1175-orphans1175 || math.Abs(p-want1175) > 1e-6 {
		t.Fatalf("DATA6-1175: %d blocks (want %d), %.8f paid (want %.8f), %v", c, n1175-orphans1175, p, want1175, err)
	}

	// With the database gone every figure says so, rather than reading as nothing found.
	CloseDB()
	if _, _, _, err := SoloBlocksSummary(miner); err == nil {
		t.Error("DATA5-BLOCKS: no database, and the blocks read as none")
	}
	if _, _, _, err := SoloPayoutsSummary(miner); err == nil {
		t.Error("DATA5-PAYOUTS: no database, and the payouts read as none")
	}
	if _, _, err := SoloEarningsErr(miner, base+500); err == nil {
		t.Error("DATA5-EARNINGS: no database, and the balance read as 0")
	}
	if _, _, err := Miner1175Totals(miner, true); err == nil {
		t.Error("DATA5-1175: no database, and the 1175 totals read as none")
	}
}

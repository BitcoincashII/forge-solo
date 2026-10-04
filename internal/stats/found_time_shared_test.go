package stats

import (
	"fmt"
	"testing"
	"time"
)

// checkFoundTimes records a solo block and a 1175 block at a find time hours ago, as a record
// retried after a database outage does, and checks that the dashboard lists both, and the solo
// block's payout, at that time. A block that replaces another at its height takes its own find
// time. base keeps the heights clear of other tests' rows in a shared database.
func checkFoundTimes(t *testing.T, miner string, base int64) {
	t.Helper()
	at := time.Now().Add(-3 * time.Hour).Truncate(time.Second)
	later := at.Add(time.Hour)
	// A shared database keeps an earlier run's rows.
	for _, q := range []string{
		`DELETE FROM payouts WHERE block_height = $1`, `DELETE FROM blocks WHERE height = $1`,
		`DELETE FROM payouts_1175 WHERE block_height = $1`, `DELETE FROM blocks_1175 WHERE height = $1`,
	} {
		if _, err := db.Exec(q, base); err != nil {
			t.Fatal(err)
		}
	}

	if err := SaveSoloBlockCoinbaseDirectAt(miner, base, 3.125, fmt.Sprintf("%064x", base), at); err != nil {
		t.Fatal(err)
	}
	blocks, _, _, err := SoloBlocksSummary(miner)
	if err != nil || len(blocks) != 1 {
		t.Fatalf("FOUND-TIME-SETUP: %+v, %v", blocks, err)
	}
	if blocks[0].Time != at.Unix() {
		t.Fatalf("FOUND-TIME-SOLO: found at %d, listed at %d", at.Unix(), blocks[0].Time)
	}
	payouts, _, _, err := SoloPayoutsSummary(miner)
	if err != nil || len(payouts) != 1 {
		t.Fatalf("FOUND-TIME-SETUP: %+v, %v", payouts, err)
	}
	if payouts[0].PaidAt.Unix() != at.Unix() {
		t.Fatalf("FOUND-TIME-SOLO-PAYOUT: found at %d, its payout at %d", at.Unix(), payouts[0].PaidAt.Unix())
	}
	// Another block at that height (the first was reorged out) is listed at its own find time.
	if err := SaveSoloBlockCoinbaseDirectAt(miner, base, 3.125, fmt.Sprintf("%064x", base+1), later); err != nil {
		t.Fatal(err)
	}
	if blocks, _, _, _ := SoloBlocksSummary(miner); len(blocks) != 1 || blocks[0].Time != later.Unix() {
		t.Fatalf("FOUND-TIME-SOLO-REPLACED: the replacing block, found at %d, is listed as %+v", later.Unix(), blocks)
	}

	if err := Record1175BlockAt(base, fmt.Sprintf("%064x", base+2), 25, miner, true, at); err != nil {
		t.Fatal(err)
	}
	if err := Distribute1175Block(base, 0); err != nil {
		t.Fatal(err)
	}
	aux, err := Get1175BlocksForMiner(miner, true, 10)
	if err != nil || len(aux) != 1 {
		t.Fatalf("FOUND-TIME-SETUP: %+v, %v", aux, err)
	}
	if aux[0].Time != at.Unix() {
		t.Fatalf("FOUND-TIME-1175: found at %d, listed at %d", at.Unix(), aux[0].Time)
	}
	if err := Record1175BlockAt(base, fmt.Sprintf("%064x", base+3), 25, miner, true, later); err != nil {
		t.Fatal(err)
	}
	if err := Distribute1175Block(base, 0); err != nil {
		t.Fatal(err)
	}
	if aux, _ := Get1175BlocksForMiner(miner, true, 10); len(aux) != 1 || aux[0].Time != later.Unix() {
		t.Fatalf("FOUND-TIME-1175-REPLACED: the replacing block, found at %d, is listed as %+v", later.Unix(), aux)
	}
}

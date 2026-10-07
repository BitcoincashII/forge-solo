package stats

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// minerPayoutsJSON is what /internal/miner-payouts answers for a miner: GetMinerPayoutsDB, encoded
// as the stratum encodes it. An orphaned payout's time is when it was found orphaned; it must be
// within a minute of now and is written as the zero time, so that every run gives one answer.
func minerPayoutsJSON(t *testing.T, miner string) string {
	t.Helper()
	payouts, total, totalPaid := GetMinerPayoutsDB(miner)
	for i, p := range payouts {
		if p.TxID != "orphaned" {
			continue
		}
		if d := time.Since(p.PaidAt); d < -time.Minute || d > time.Minute {
			t.Fatalf("PAYOUTS-ORPHAN-TIME: the orphaned payout of %s is dated %v, not about now", miner, p.PaidAt)
		}
		payouts[i].PaidAt = time.Time{}
	}
	b, err := json.Marshal(map[string]interface{}{"payouts": payouts, "total": total, "totalPaid": totalPaid})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// checkMinerPayouts records, for one miner, two solo blocks paid by their coinbase, a third found
// orphaned and an old payout with no time, and one block for another miner, and checks the payout
// list: the same JSON, byte for byte. In 1.0.12 the list failed on PostgreSQL with 42803 (the paid
// state read a column the query did not group by); on SQLite every row was dropped (the latest
// payout time came back as text, which a time.Time cannot be scanned from), so the list was always
// empty. A miner with no payouts gets an empty list, not null. base keeps the heights clear of other
// tests' rows in a shared database.
func checkMinerPayouts(t *testing.T, a, b, none string, base int64) {
	t.Helper()
	// A shared database keeps an earlier run's rows.
	for _, q := range []string{
		`DELETE FROM payouts WHERE miner_address IN ($1, $2, $3)`,
		`DELETE FROM blocks WHERE miner_address IN ($1, $2, $3)`,
	} {
		if _, err := db.Exec(q, a, b, none); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2021, 6, 1, 10, 0, 0, 0, time.UTC)
	for _, r := range []struct {
		miner  string
		height int64
		amount float64
		found  time.Time
	}{
		{a, base, 3.125, at},
		{a, base + 1, 1.5625, at.Add(time.Hour)},
		{a, base + 2, 0.78125, at.Add(2 * time.Hour)},
		{b, base + 3, 6.25, at},
	} {
		if err := SaveSoloBlockCoinbaseDirectAt(r.miner, r.height, r.amount, fmt.Sprintf("%064x", r.height), r.found); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := OrphanSoloBlock(base + 2); err != nil || n != 1 {
		t.Fatalf("PAYOUTS-SETUP: orphaning block %d: %d rows, %v", base+2, n, err)
	}
	// A payout of the old pool-style sender, which kept no time: listed last.
	if _, err := db.Exec(`INSERT INTO payouts (miner_address, block_height, amount, confirmed, txid, status) VALUES ($1, $2, 0.5, $3, $4, 'paid')`,
		a, base+4, true, strings.Repeat("ab", 32)); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		code, miner, want string
	}{
		{"PAYOUTS-LIST", a, `{"payouts":[` +
			`{"txid":"orphaned","amount":0.78125,"paidAt":"0001-01-01T00:00:00Z","blocks":1,"confirmed":false,"status":""},` +
			`{"txid":"coinbase-direct","amount":4.6875,"paidAt":"2021-06-01T11:00:00Z","blocks":2,"confirmed":true,"status":""},` +
			`{"txid":"` + strings.Repeat("ab", 32) + `","amount":0.5,"paidAt":"0001-01-01T00:00:00Z","blocks":1,"confirmed":true,"status":""}` +
			`],"total":3,"totalPaid":5.1875}`},
		{"PAYOUTS-OTHER-MINER", b, `{"payouts":[` +
			`{"txid":"coinbase-direct","amount":6.25,"paidAt":"2021-06-01T10:00:00Z","blocks":1,"confirmed":true,"status":""}` +
			`],"total":1,"totalPaid":6.25}`},
		{"PAYOUTS-NONE", none, `{"payouts":[],"total":0,"totalPaid":0}`},
	} {
		if got := minerPayoutsJSON(t, c.miner); got != c.want {
			t.Errorf("%s: the payouts of %s read\n%s\nwant\n%s", c.code, c.miner, got, c.want)
		}
	}
}

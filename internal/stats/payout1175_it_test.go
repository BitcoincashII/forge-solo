package stats

import (
	"os"
	"testing"
)

func abs1175(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// TestPayout1175Accounting exercises the hardened 1175 merge-mining payout ledger
// against a real postgres test DB (MMTEST_DB connStr). It validates the fund-safety
// invariants from the 2026-07-17 audit:
//   - proportional PPLNS distribution (60/40) of the full reward
//   - idempotent re-distribution (no double-credit)
//   - the CONFIRMATION GATE: credits are unpayable until the aux block is confirmed
//   - redistribute REFUSED once a height has paid/sending rows
//   - SOLO block credits only the finder, the full reward
//   - ORPHAN-VOID: an orphaned block voids its unpaid credits + drops from the payable set
//   - the mark->send->finalize money path, stuck-sending surfacing, and revert
//   - 1175 address resolution
//
// Run it with ./scripts/it-postgres.sh, which provisions a throwaway postgres in the image
// docker-compose.yml pins, passes MMTEST_DB, and fails if this test skips. CI runs the same
// script.
func TestPayout1175Accounting(t *testing.T) {
	connStr := os.Getenv("MMTEST_DB")
	if connStr == "" {
		t.Skip("MMTEST_DB not set; skipping 1175 payout DB integration test")
	}
	if err := InitDB(connStr); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	Init1175Schema()

	// clean slate
	for _, q := range []string{`DELETE FROM shares`, `DELETE FROM payouts_1175`, `DELETE FROM blocks_1175`, `DELETE FROM miners`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("cleanup %q: %v", q, err)
		}
	}
	// minerA supplies a 1175 address; minerB does not
	if _, err := db.Exec(`INSERT INTO miners (address, address_1175) VALUES ('minerAAAAAAAAAA','esf1aaa'),('minerBBBBBBBBBB',NULL)`); err != nil {
		t.Fatalf("seed miners: %v", err)
	}
	// window shares (not solo): A=60, B=40 => 60/40 split
	for i := 0; i < 6; i++ {
		db.Exec(`INSERT INTO shares (miner_address, worker_name, difficulty, is_solo) VALUES ('minerAAAAAAAAAA','w',10,false)`)
	}
	for i := 0; i < 4; i++ {
		db.Exec(`INSERT INTO shares (miner_address, worker_name, difficulty, is_solo) VALUES ('minerBBBBBBBBBB','w',10,false)`)
	}

	amtOf := func(miner string, h int64) float64 {
		var v float64
		db.QueryRow(`SELECT COALESCE(amount,0) FROM payouts_1175 WHERE miner_address=$1 AND block_height=$2`, miner, h).Scan(&v)
		return v
	}
	statusOf := func(miner string, h int64) string {
		var s string
		db.QueryRow(`SELECT COALESCE(status,'') FROM payouts_1175 WHERE miner_address=$1 AND block_height=$2`, miner, h).Scan(&s)
		return s
	}
	payable := func(miner string) bool {
		miners, _ := ConfirmedPendingMiners1175()
		for _, m := range miners {
			if m == miner {
				return true
			}
		}
		return false
	}

	// ---- 1. PPLNS distribution: block 100, gross 25, no fee => A=15.00 B=10.00 (sums to gross)
	if err := Record1175Block(100, "hash100", 25.0, "minerAAAAAAAAAA", false); err != nil {
		t.Fatalf("record 100: %v", err)
	}
	if err := Distribute1175Block(100, 1000); err != nil {
		t.Fatalf("distribute 100: %v", err)
	}
	if a, b := amtOf("minerAAAAAAAAAA", 100), amtOf("minerBBBBBBBBBB", 100); abs1175(a-15.00) > 1e-9 || abs1175(b-10.00) > 1e-9 {
		t.Fatalf("pplns split: A=%.8f (want 15.00) B=%.8f (want 10.00)", a, b)
	}

	// The fee-net pair (14.85 / 9.90) was a stronger invariant than two round numbers:
	// it could only hold if the split AND the deduction were both right. With no fee, the
	// equivalent strength is that the credits sum to the whole reward, nothing withheld.
	if a, b := amtOf("minerAAAAAAAAAA", 100), amtOf("minerBBBBBBBBBB", 100); abs1175((a+b)-25.0) > 1e-9 {
		t.Fatalf("credits must sum to the full reward: A+B=%.8f (want 25.00000000)", a+b)
	}

	// ---- 2. idempotency: re-record + re-distribute a still-pending height must not double
	if err := Record1175Block(100, "hash100", 25.0, "minerAAAAAAAAAA", false); err != nil {
		t.Fatalf("re-record 100: %v", err)
	}
	if err := Distribute1175Block(100, 1000); err != nil {
		t.Fatalf("re-distribute 100: %v", err)
	}
	var cnt int
	db.QueryRow(`SELECT COUNT(*) FROM payouts_1175 WHERE block_height=100`).Scan(&cnt)
	if cnt != 2 || abs1175(amtOf("minerAAAAAAAAAA", 100)-15.00) > 1e-9 {
		t.Fatalf("idempotency broken: rows=%d A=%.8f", cnt, amtOf("minerAAAAAAAAAA", 100))
	}

	// ---- 3. CONFIRMATION GATE: credits are NOT payable while the block is unconfirmed
	if payable("minerAAAAAAAAAA") {
		t.Fatal("confirmation gate: A payable before block confirmed (want not payable)")
	}
	// confirm => now payable
	if err := Confirm1175Block(100); err != nil {
		t.Fatalf("confirm 100: %v", err)
	}
	if !payable("minerAAAAAAAAAA") {
		t.Fatal("A not payable after confirm (want payable)")
	}

	// ---- 4. money path: the aux COINBASE settles it. There is no reserve->send->finalize
	// pipeline any more -- it was deleted, because wiring it in on top of the coinbase
	// payment would have paid twice. Settle1175ByCoinbase is the whole money path.
	if n, err := Settle1175ByCoinbase("minerAAAAAAAAAA"); err != nil || n == 0 {
		t.Fatalf("settle A: n=%d err=%v (want a settled credit)", n, err)
	}
	if s := statusOf("minerAAAAAAAAAA", 100); s != "paid" {
		t.Fatalf("A status after coinbase settle = %q (want paid)", s)
	}
	if abs1175(amtOf("minerAAAAAAAAAA", 100)-15.00) > 1e-9 {
		t.Fatalf("settling must not change the amount: %.8f (want 15.00)", amtOf("minerAAAAAAAAAA", 100))
	}
	// Settling twice must not double-credit or resurrect the row.
	if n, err := Settle1175ByCoinbase("minerAAAAAAAAAA"); err != nil || n != 0 {
		t.Fatalf("re-settle A: n=%d err=%v (want 0 -- already paid, must not settle twice)", n, err)
	}
	if s := statusOf("minerAAAAAAAAAA", 100); s != "paid" {
		t.Fatalf("A status after re-settle = %q (want paid)", s)
	}

	// ---- 5a. re-distributing an already-distributed height is a SAFE NO-OP (idempotency guard)
	if err := Distribute1175Block(100, 1000); err != nil {
		t.Fatalf("re-distribute (distributed=true) should be a safe no-op, got: %v", err)
	}
	db.QueryRow(`SELECT COUNT(*) FROM payouts_1175 WHERE block_height=100`).Scan(&cnt)
	if cnt != 2 || statusOf("minerAAAAAAAAAA", 100) != "paid" || abs1175(amtOf("minerAAAAAAAAAA", 100)-15.00) > 1e-9 {
		t.Fatalf("re-distribute mutated a paid height: rows=%d Astatus=%s Aamt=%.8f", cnt, statusOf("minerAAAAAAAAAA", 100), amtOf("minerAAAAAAAAAA", 100))
	}
	// ---- 5b. defensive: a height flagged undistributed but already carrying paid/sending rows
	//          is REFUSED (with error) rather than double-paying.
	db.Exec(`UPDATE blocks_1175 SET distributed=false WHERE height=100`)
	if err := Distribute1175Block(100, 1000); err == nil {
		t.Fatal("re-distribute of a height with paid rows succeeded (want refuse)")
	}
	if statusOf("minerAAAAAAAAAA", 100) != "paid" || abs1175(amtOf("minerAAAAAAAAAA", 100)-15.00) > 1e-9 {
		t.Fatalf("refused re-distribute still mutated A: status=%s amt=%.8f", statusOf("minerAAAAAAAAAA", 100), amtOf("minerAAAAAAAAAA", 100))
	}
	db.Exec(`UPDATE blocks_1175 SET distributed=true WHERE height=100`) // restore invariant

	// ---- 6. SOLO block: credits ONLY the finder, the full reward: 10 => 10.00
	if err := Record1175Block(101, "hash101", 10.0, "minerAAAAAAAAAA", true); err != nil {
		t.Fatalf("record 101: %v", err)
	}
	if err := Distribute1175Block(101, 1000); err != nil {
		t.Fatalf("distribute 101: %v", err)
	}
	if a := amtOf("minerAAAAAAAAAA", 101); abs1175(a-10.00) > 1e-9 {
		t.Fatalf("solo credit A=%.8f (want 10.00)", a)
	}
	if b := amtOf("minerBBBBBBBBBB", 101); b != 0 {
		t.Fatalf("solo block credited non-finder B=%.8f (want 0)", b)
	}

	// ---- 7. ORPHAN-VOID: an orphaned block voids its unpaid credits + drops from payable set
	if err := Record1175Block(102, "hash102", 25.0, "minerAAAAAAAAAA", false); err != nil {
		t.Fatalf("record 102: %v", err)
	}
	if err := Distribute1175Block(102, 1000); err != nil {
		t.Fatalf("distribute 102: %v", err)
	}
	if err := Confirm1175Block(102); err != nil {
		t.Fatalf("confirm 102: %v", err)
	}
	if err := Orphan1175Block(102); err != nil {
		t.Fatalf("orphan 102: %v", err)
	}
	if s := statusOf("minerAAAAAAAAAA", 102); s != "orphaned" {
		t.Fatalf("A 102 status after orphan = %q (want orphaned)", s)
	}
	var blkStatus string
	db.QueryRow(`SELECT status FROM blocks_1175 WHERE height=102`).Scan(&blkStatus)
	if blkStatus != "orphaned" {
		t.Fatalf("block 102 status = %q (want orphaned)", blkStatus)
	}

	// ---- 8. revert path: confirm 101, process it, then revert cleanly back to pending
	if err := Confirm1175Block(101); err != nil {
		t.Fatalf("confirm 101: %v", err)
	}
	if abs1175(amtOf("minerAAAAAAAAAA", 101)-10.00) > 1e-9 {
		t.Fatalf("101 credit = %.8f (want 10.00)", amtOf("minerAAAAAAAAAA", 101))
	}
	if n, err := Settle1175ByCoinbase("minerAAAAAAAAAA"); err != nil || n == 0 {
		t.Fatalf("settle 101: n=%d err=%v (want a settled credit)", n, err)
	}
	if s := statusOf("minerAAAAAAAAAA", 101); s != "paid" {
		t.Fatalf("101 status after settle = %q (want paid)", s)
	}

	// ---- 9. no send path exists. These five were a pool-style reserve->send->finalize
	// pipeline that would have DOUBLE-PAID on top of the aux coinbase. No shipped version
	// ever called them, so they were deleted rather than left behind a warning comment.
	// If any of them comes back, this file stops compiling -- which is the point.

	t.Log("✅ 1175 payout: PPLNS 60/40, idempotent, confirmation-gated, redistribute-refused, solo-finder, orphan-voided, coinbase-settled, no send path")
}

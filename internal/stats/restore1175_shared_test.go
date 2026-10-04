package stats

import (
	"fmt"
	"testing"
)

// checkRestore1175 orphans two 1175 blocks and puts one back: it is pending again with its credit,
// and only the orphans at or above the height asked for are listed. base keeps the heights clear
// of other tests' rows in a shared database.
func checkRestore1175(t *testing.T, miner string, base int64) {
	t.Helper()
	for _, q := range []string{`DELETE FROM payouts_1175 WHERE block_height IN ($1, $2)`, `DELETE FROM blocks_1175 WHERE height IN ($1, $2)`} {
		if _, err := db.Exec(q, base, base+1); err != nil {
			t.Fatal(err)
		}
	}
	for _, h := range []int64{base, base + 1} {
		if err := Record1175Block(h, fmt.Sprintf("%064x", h), 25, miner, true); err != nil {
			t.Fatal(err)
		}
		if err := Distribute1175Block(h, 0); err != nil {
			t.Fatal(err)
		}
		if err := Orphan1175Block(h); err != nil {
			t.Fatal(err)
		}
	}
	orphans, err := Orphaned1175Blocks(base + 1)
	if err != nil || len(orphans) != 1 || orphans[0][0].(int64) != base+1 {
		t.Fatalf("RESTORE1175-LIST: orphans from %d: %v, %v", base+1, orphans, err)
	}
	if ok, err := Restore1175Block(base+1, fmt.Sprintf("%064x", base)); ok || err != nil {
		t.Fatalf("RESTORE1175-HASH: another block's hash put this one back (%v, %v)", ok, err)
	}
	if ok, err := Restore1175Block(base+1, fmt.Sprintf("%064x", base+1)); !ok || err != nil {
		t.Fatalf("RESTORE1175: not put back (%v, %v)", ok, err)
	}
	var status, pstatus string
	var txid *string
	if err := db.QueryRow(`SELECT status FROM blocks_1175 WHERE height = $1`, base+1).Scan(&status); err != nil || status != "pending" {
		t.Fatalf("RESTORE1175-STATUS: %q, %v", status, err)
	}
	if err := db.QueryRow(`SELECT status, txid FROM payouts_1175 WHERE block_height = $1`, base+1).Scan(&pstatus, &txid); err != nil || pstatus != "pending" || txid != nil {
		t.Fatalf("RESTORE1175-CREDIT: %q %v, %v", pstatus, txid, err)
	}
	if ok, _ := Restore1175Block(base+1, fmt.Sprintf("%064x", base+1)); ok {
		t.Fatal("RESTORE1175-ONCE: a pending block was put back again")
	}
}

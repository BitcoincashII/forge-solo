package stats

import (
	"path/filepath"
	"testing"
)

func TestSoloSharesAreNotStoredAndOldOnesAreCleared(t *testing.T) {
	if err := InitDB(filepath.Join(t.TempDir(), "x.db")); err != nil {
		t.Fatal(err)
	}
	defer CloseDB()
	checkSoloShares(t, func(solo bool) {
		v := 0
		if solo {
			v = 1
		}
		if _, err := db.Exec(`INSERT INTO shares (miner_address, worker_name, difficulty, is_solo) VALUES ('bitcoincashii:qtestminer', 'rig1', 1024, ?)`, v); err != nil {
			t.Fatal(err)
		}
	})
}

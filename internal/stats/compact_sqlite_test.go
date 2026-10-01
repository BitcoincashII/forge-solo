//go:build sqlite

package stats

import (
	"os"
	"path/filepath"
	"testing"
)

// Clearing the solo shares an install stored must also shrink the file: SQLite keeps freed pages.
func TestClearedSharesGiveTheirSpaceBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	if err := InitDB(path); err != nil {
		t.Fatal(err)
	}
	defer CloseDB()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60000; i++ {
		if _, err := tx.Exec(`INSERT INTO shares (miner_address, worker_name, difficulty, is_solo) VALUES ('bitcoincashii:qtestminer', 'rig1', 1024, 1)`); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	size := func() int64 {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		return st.Size()
	}
	before := size()
	if _, err := ClearSoloShares(); err != nil {
		t.Fatal(err)
	}
	if err := Compact(); err != nil {
		t.Fatal(err)
	}
	if after := size(); after*2 > before {
		t.Errorf("the database file is %d bytes after clearing the shares, was %d: the space was not given back", after, before)
	}
	// Nothing to give back: Compact leaves the file alone.
	if err := Compact(); err != nil {
		t.Fatal(err)
	}
}

//go:build sqlite

package stats

import (
	"path/filepath"
	"testing"
	"time"
)

// A solo block is listed, with its time. Times were bound as time.Time, which the driver stored
// in Go's String() form ("2026-10-01 01:45:30.009992576 +0000 UTC"); SQLite's strftime cannot
// read that, the scan failed, and the dashboard of Forge Solo for Linux listed no solo blocks at
// all. A row already stored in that form must read too.
func TestSoloBlocksAreListedWithTheirTime(t *testing.T) {
	if err := InitDB(filepath.Join(t.TempDir(), "x.db")); err != nil {
		t.Fatal(err)
	}
	defer CloseDB()
	const miner = "bitcoincashii:qtestminer000000000000000000000000000000"
	before := time.Now().Add(-time.Minute).Unix()
	if err := SaveSoloBlockCoinbaseDirect(miner, 300, 50, "00000000000000000000000000000000000000000000000000000000000000aa"); err != nil {
		t.Fatal(err)
	}
	// A block recorded by an earlier build, which bound a time.Time.
	old := time.Date(2026, 10, 1, 1, 45, 30, 9992576, time.UTC)
	if _, err := db.Exec(`INSERT INTO blocks (height, hash, miner_address, reward, status, is_solo, created_at)
		VALUES (301, ?, ?, 50, 'pending', 1, ?)`,
		"00000000000000000000000000000000000000000000000000000000000000bb", miner, old); err != nil {
		t.Fatal(err)
	}
	blocks := GetMinerSoloBlocksDB(miner)
	if len(blocks) != 2 {
		t.Fatalf("listed %d solo blocks, want 2: %+v", len(blocks), blocks)
	}
	for _, b := range blocks {
		switch b.Height {
		case 300:
			if b.Time < before || b.Time > time.Now().Unix()+1 {
				t.Errorf("block 300 time %d, want about now", b.Time)
			}
		case 301:
			if want := old.Unix(); b.Time != want {
				t.Errorf("block 301 time %d, want %d", b.Time, want)
			}
		}
	}
	if got := len(listedPoolBlocks(t)); got != 2 {
		t.Fatalf("the pool block list has %d rows, want 2", got)
	}
}

func listedPoolBlocks(t *testing.T) []PoolBlock {
	t.Helper()
	blocks, _ := GetAllPoolBlocksDB(1, 50)
	return blocks
}

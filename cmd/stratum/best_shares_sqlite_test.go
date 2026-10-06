//go:build sqlite

package main

import (
	"path/filepath"
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/stats"
)

// A share's best reaches forgesolo.db through the share path and the stratum's write.
func TestShareBestIsWrittenToTheDatabase(t *testing.T) {
	if err := stats.InitDB(filepath.Join(t.TempDir(), "b.db")); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()
	if _, err := stats.GetManager().LoadBestShares(); err != nil {
		t.Fatal(err)
	}
	miner := bestShare(t, "s19", 9.5e9)
	if err := stats.GetManager().WriteBestShares(); err != nil {
		t.Fatalf("ATH-STRATUM-WRITE: %v", err)
	}
	rows, err := stats.LoadBestSharesDB()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Miner == miner && r.Worker == "s19" && r.Difficulty == 9.5e9 {
			return
		}
	}
	t.Fatalf("ATH-STRATUM-WRITTEN: the database holds %+v, want %s s19 at 9.5e9", rows, miner)
}

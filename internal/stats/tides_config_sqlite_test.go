//go:build sqlite

package stats

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestSQLiteTidesConfig(t *testing.T) {
	if err := InitDB(filepath.Join(t.TempDir(), "tides.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	defer CloseDB()
	checkTidesConfig(t)
}

// A Windows install that predates TIDES has a pool_config table without payout_mode and a row
// with its settings in it. Starting the new build must add the column, keep the row, and read
// the mode as solo.
func TestSQLiteTidesConfigUpgradesAnOldDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE pool_config (id INTEGER PRIMARY KEY CHECK (id = 1), pool_address TEXT DEFAULT '',
			payout_address_1175 TEXT DEFAULT '', coinbase_tag TEXT DEFAULT '', updated_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`INSERT INTO pool_config (id, pool_address, coinbase_tag) VALUES (1, 'bitcoincashii:qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzse6qye33q', 'Kept')`,
	} {
		if _, err := old.Exec(stmt); err != nil {
			t.Fatalf("old schema: %v", err)
		}
	}
	old.Close()

	if err := InitDB(path); err != nil {
		t.Fatalf("InitDB on an old database: %v", err)
	}
	defer CloseDB()
	if mode, err := GetPayoutMode(); err != nil || mode != PayoutModeSolo {
		t.Fatalf("TIDES-CFG-UPGRADE: upgraded payout mode = %q, %v; want solo", mode, err)
	}
	if err := SavePayoutMode(PayoutModeTides); err != nil {
		t.Fatalf("TIDES-CFG-UPGRADE: the migrated column cannot be written: %v", err)
	}
	if pool, _, tag, err := GetPoolConfig(); err != nil || tag != "Kept" || pool == "" {
		t.Fatalf("TIDES-CFG-UPGRADE: the old settings row did not survive: %q %q %v", pool, tag, err)
	}
}

//go:build !sqlite

package stats

import (
	"os"
	"testing"
)

// Postgres (the Umbrel build). Needs a disposable database: TIDES_PG_DB is a connection string
// to one, and the test drops pool_config and datum_identity in it to start clean.
func TestPostgresTidesConfig(t *testing.T) {
	connStr := os.Getenv("TIDES_PG_DB")
	if connStr == "" {
		t.Skip("TIDES_PG_DB not set; skipping the Postgres TIDES settings test")
	}
	if err := InitDB(connStr); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	defer CloseDB()
	for _, stmt := range []string{`DROP TABLE IF EXISTS pool_config`, `DROP TABLE IF EXISTS datum_identity`} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	// An install from before TIDES: pool_config without payout_mode, holding its settings.
	if _, err := db.Exec(`CREATE TABLE pool_config (id INT PRIMARY KEY DEFAULT 1, pool_address TEXT DEFAULT '',
		payout_address_1175 TEXT DEFAULT '', coinbase_tag TEXT DEFAULT '', updated_at TIMESTAMPTZ DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	CloseDB()
	if err := InitDB(connStr); err != nil {
		t.Fatalf("InitDB on an old database: %v", err)
	}
	checkTidesConfig(t)
}

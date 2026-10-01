//go:build !sqlite

package stats

import (
	"os"
	"testing"
)

// Postgres (the Umbrel build), as TestStoredOldDefaultTagReadsAsNoneChosen. Needs a disposable
// database: TIDES_PG_DB is a connection string to one.
func TestPostgresStoredOldDefaultTagReadsAsNoneChosen(t *testing.T) {
	connStr := os.Getenv("TIDES_PG_DB")
	if connStr == "" {
		t.Skip("TIDES_PG_DB not set; skipping the Postgres coinbase tag test")
	}
	if err := InitDB(connStr); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	defer CloseDB()
	const addr = "bitcoincashii:qtestminer000000000000000000000000000000"
	for stored, want := range map[string]string{"Forge": "", "MyRig": "MyRig", "": ""} {
		if err := SavePoolConfig(addr, "", stored); err != nil {
			t.Fatal(err)
		}
		pool, _, tag, err := GetPoolConfig()
		if err != nil {
			t.Fatal(err)
		}
		if tag != want || pool != addr {
			t.Errorf("stored tag %q: read pool %q tag %q, want %q %q", stored, pool, tag, addr, want)
		}
	}
}

//go:build !sqlite

package stats

import (
	"os"
	"testing"
)

// checkRestore1175 on Postgres; scripts/it-postgres.sh runs it. TIDES_PG_DB is a connection string
// to a disposable database.
func TestPostgresA1175BlockIsPutBack(t *testing.T) {
	connStr := os.Getenv("TIDES_PG_DB")
	if connStr == "" {
		t.Skip("TIDES_PG_DB not set; skipping the 1175 restore test on Postgres")
	}
	if err := InitDB(connStr); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(CloseDB)
	checkRestore1175(t, "bitcoincashii:qqrestore1175pg00000000000000000000000000", 9_300_000)
}

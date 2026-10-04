//go:build !sqlite

package stats

import (
	"os"
	"testing"
)

// checkFoundTimes on Postgres; scripts/it-postgres.sh runs it. TIDES_PG_DB is a connection string
// to a disposable database.
func TestPostgresFoundTimesAreKept(t *testing.T) {
	connStr := os.Getenv("TIDES_PG_DB")
	if connStr == "" {
		t.Skip("TIDES_PG_DB not set; skipping the found-time test on Postgres")
	}
	if err := InitDB(connStr); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(CloseDB)
	checkFoundTimes(t, "bitcoincashii:qqfoundtimespg0000000000000000000000000000", 9_200_000)
}

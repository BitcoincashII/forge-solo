//go:build !sqlite

package stats

import (
	"os"
	"testing"
)

// payout1175TestDB is the database TestPayout1175Accounting runs on: MMTEST_DB, a connection
// string to a disposable Postgres database, which scripts/it-postgres.sh sets.
func payout1175TestDB(t *testing.T) string {
	connStr := os.Getenv("MMTEST_DB")
	if connStr == "" {
		t.Skip("MMTEST_DB not set; skipping 1175 payout DB integration test")
	}
	return connStr
}

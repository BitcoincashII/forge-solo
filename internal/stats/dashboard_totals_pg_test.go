//go:build !sqlite

package stats

import (
	"os"
	"testing"
)

// checkDashboardTotals on Postgres; scripts/it-postgres.sh runs it. TIDES_PG_DB is a connection
// string to a disposable database.
func TestPostgresDashboardTotalsCoverEveryBlock(t *testing.T) {
	connStr := os.Getenv("TIDES_PG_DB")
	if connStr == "" {
		t.Skip("TIDES_PG_DB not set; skipping the dashboard totals test on Postgres")
	}
	if err := InitDB(connStr); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(CloseDB)
	checkDashboardTotals(t, "bitcoincashii:qqdashboardtotalspg00000000000000000000000", 9_100_000)
}

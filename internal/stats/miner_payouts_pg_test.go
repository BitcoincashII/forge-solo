//go:build !sqlite

package stats

import (
	"os"
	"strings"
	"testing"
)

// checkMinerPayouts on Postgres; scripts/it-postgres.sh runs it. TIDES_PG_DB is a connection string
// to a disposable database. The session runs in America/Chicago, as a Windows PC's server did, and
// the times must still read as the SQLite build gives them.
func TestPostgresMinerPayoutsList(t *testing.T) {
	connStr := os.Getenv("TIDES_PG_DB")
	if connStr == "" {
		t.Skip("TIDES_PG_DB not set; skipping the miner payouts test on Postgres")
	}
	switch {
	case !strings.Contains(connStr, "://"):
		connStr += " timezone=America/Chicago"
	case strings.Contains(connStr, "?"):
		connStr += "&timezone=America/Chicago"
	default:
		connStr += "?timezone=America/Chicago"
	}
	if err := InitDB(connStr); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(CloseDB)
	var zone string
	if err := db.QueryRow(`SHOW TimeZone`).Scan(&zone); err != nil || zone != "America/Chicago" {
		t.Fatalf("PAYOUTS-PG-ZONE: the session's time zone is %q (%v), want America/Chicago", zone, err)
	}
	checkMinerPayouts(t, "bitcoincashii:qqminerpayoutspga000000000000000000000000",
		"bitcoincashii:qqminerpayoutspgb000000000000000000000000",
		"bitcoincashii:qqminerpayoutspgc000000000000000000000000", 9_400_000)
}

//go:build !sqlite

package stats

import (
	"os"
	"testing"
)

// Postgres (the Umbrel build), as TestSoloSharesAreNotStoredAndOldOnesAreCleared. On TimescaleDB the
// shares table is a hypertable, and emptying it must take its chunks with it. Needs a disposable
// database: TIDES_PG_DB is a connection string to one.
func TestPostgresSoloSharesAreNotStoredAndOldOnesAreCleared(t *testing.T) {
	connStr := os.Getenv("TIDES_PG_DB")
	if connStr == "" {
		t.Skip("TIDES_PG_DB not set; skipping the Postgres solo-shares test")
	}
	if err := InitDB(connStr); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	defer CloseDB()
	if _, err := db.Exec(`DELETE FROM shares`); err != nil {
		t.Fatal(err)
	}
	hour := 0
	checkSoloShares(t, func(solo bool) {
		hour++ // a share an hour apart lands in its own hourly chunk on TimescaleDB
		if _, err := db.Exec(`INSERT INTO shares (time, miner_address, worker_name, difficulty, is_solo)
			VALUES (now() - make_interval(hours => $1), 'bitcoincashii:qtestminer', 'rig1', 1024, $2)`, hour, solo); err != nil {
			t.Fatal(err)
		}
	})
	var timescale bool
	if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'timescaledb')`).Scan(&timescale); err != nil {
		t.Fatal(err)
	}
	if timescale {
		var chunks int
		if err := db.QueryRow(`SELECT COUNT(*) FROM timescaledb_information.chunks WHERE hypertable_name = 'shares'`).Scan(&chunks); err != nil {
			t.Fatal(err)
		}
		if chunks != 0 {
			t.Errorf("%d chunks left after emptying the shares table", chunks)
		}
	}
}

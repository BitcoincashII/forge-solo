//go:build !sqlite

package stats

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/lib/pq"
)

// An Umbrel install that stored every solo share for weeks has one TimescaleDB chunk per hour of
// them, and on a small board its lock table is 128 locks per transaction. Emptying the table in
// one statement failed there with "out of shared memory", so the clear failed on every start and
// the space was never freed. So does one statement that only asks whether the table holds any
// share, once there are a few thousand chunks: an install that stays on 1.0.12 until December. scripts/it-postgres.sh runs this against the shipped database image
// with that lock limit. Needs a disposable database: TIDES_PG_DB is a connection string to one.
func TestPostgresClearsThousandsOfShareChunks(t *testing.T) {
	connStr := os.Getenv("TIDES_PG_DB")
	if connStr == "" {
		t.Skip("TIDES_PG_DB not set; skipping the many-chunks clear test")
	}
	if err := InitDB(connStr); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(CloseDB) // registered first, so it runs after the table is reset
	var timescale bool
	if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'timescaledb')`).Scan(&timescale); err != nil {
		t.Fatal(err)
	}
	if !timescale {
		t.Skip("no TimescaleDB here: the shares table has no chunks")
	}
	const hours = 4000
	count := func(q string) int {
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	chunks := func() int {
		return count(`SELECT COUNT(*) FROM timescaledb_information.chunks WHERE hypertable_name = 'shares'`)
	}
	// One share an hour back, for `hours` hours: one chunk each. A hundred chunks per statement, so
	// filling the table never needs more locks than the clear may take.
	fill := func(solo bool) {
		for from := 1; from <= hours; from += 100 {
			if _, err := db.Exec(`INSERT INTO shares (time, miner_address, worker_name, difficulty, is_solo)
				SELECT now() - make_interval(hours => g), 'bitcoincashii:qtestminer', 'rig1', 1024, $3
				FROM generate_series($1::int, $2::int) g`, from, min(from+99, hours), solo); err != nil {
				t.Fatal(err)
			}
		}
	}
	// A table left with thousands of chunks (an earlier run) cannot be emptied in one statement
	// either: drop them oldest first, a hundred at a time, then empty what is left.
	reset := func() {
		rows, err := db.Query(`SELECT range_end FROM timescaledb_information.chunks WHERE hypertable_name = 'shares' ORDER BY range_end`)
		if err != nil {
			t.Fatal(err)
		}
		var ends []time.Time
		for rows.Next() {
			var e time.Time
			if err := rows.Scan(&e); err != nil {
				t.Fatal(err)
			}
			ends = append(ends, e)
		}
		rows.Close()
		for i := 99; i < len(ends); i += 100 {
			if _, err := db.Exec(`SELECT drop_chunks('shares', older_than => $1::timestamptz)`, ends[i]); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Exec(`TRUNCATE shares`); err != nil {
			t.Fatal(err)
		}
	}
	reset()
	t.Cleanup(reset)

	fill(true)
	if n := chunks(); n < hours {
		t.Fatalf("PG-CLEAR-SETUP: %d chunks, want at least %d", n, hours)
	}
	// The setup must reproduce the failure, or this test proves nothing about it.
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(`TRUNCATE shares`)
	_ = tx.Rollback()
	var pqErr *pq.Error
	if !errors.As(err, &pqErr) || pqErr.Code != "53200" {
		t.Fatalf("PG-CLEAR-NOT-PROVING: one TRUNCATE over %d chunks gave %v, not \"out of shared memory\": run with max_locks_per_transaction=128 (scripts/it-postgres.sh)", hours, err)
	}
	var some, pplns bool
	err = db.QueryRow(`SELECT EXISTS (SELECT 1 FROM shares), EXISTS (SELECT 1 FROM shares WHERE NOT is_solo)`).Scan(&some, &pplns)
	if !errors.As(err, &pqErr) || pqErr.Code != "53200" {
		t.Fatalf("PG-CLEAR-NOT-PROVING-CHECK: one look over %d chunks gave %v, not \"out of shared memory\"", hours, err)
	}

	if n, err := ClearSoloShares(); err != nil || n != -1 {
		t.Fatalf("PG-CLEAR-MANY-CHUNKS: ClearSoloShares = %d, %v; want -1, nil", n, err)
	}
	if n, rows := chunks(), count(`SELECT COUNT(*) FROM shares`); n != 0 || rows != 0 {
		t.Fatalf("PG-CLEAR-CHUNKS-LEFT: %d chunks and %d rows left", n, rows)
	}

	// An install that once ran PPLNS: only the solo rows go, one chunk at a time.
	fill(true)
	if err := SaveShare("bitcoincashii:qtestminer", "rig1", 1024, false); err != nil {
		t.Fatal(err)
	}
	if n, err := ClearSoloShares(); err != nil || n != hours {
		t.Fatalf("PG-CLEAR-MIXED-MANY: ClearSoloShares = %d, %v; want %d, nil", n, err, hours)
	}
	if solo, pplns := count(`SELECT COUNT(*) FROM shares WHERE is_solo`), count(`SELECT COUNT(*) FROM shares WHERE NOT is_solo`); solo != 0 || pplns != 1 {
		t.Fatalf("PG-CLEAR-MIXED-KEPT: %d solo and %d PPLNS rows left; want 0 and 1", solo, pplns)
	}
	// Only the chunk holding the PPLNS share is left.
	if n := chunks(); n != 1 {
		t.Fatalf("PG-CLEAR-MIXED-EMPTY-CHUNKS: %d chunks left after clearing a mixed table; want 1 (the one with the PPLNS share)", n)
	}
}

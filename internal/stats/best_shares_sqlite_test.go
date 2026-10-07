package stats

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// restartOn is a stratum on a new forgesolo.db that mines (mine) until it stops, with its last write,
// and one started again on the same file, up to its first write, before any worker is back.
func restartOn(t *testing.T, mine func(m *StatsManager)) *StatsManager {
	t.Helper()
	path := filepath.Join(t.TempDir(), "forgesolo.db")
	if err := InitDB(path); err != nil {
		t.Fatal(err)
	}
	run1 := startRun(t)
	mine(run1)
	mustWrite(t, run1)
	CloseDB()

	if err := InitDB(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(CloseDB)
	run2 := startRun(t)
	mustWrite(t, run2)
	return run2
}

// More than 100 worker names on one payout address mine until a restart. The new run's first write
// comes before any of them is back: it drops none, and each comes back with its best.
func TestFarmBestsOutliveARestart(t *testing.T) {
	const farm = MaxKeptWorkers + 50
	name := func(i int) string { return fmt.Sprintf("rig%03d", i) }
	run2 := restartOn(t, func(m *StatsManager) {
		for i := 0; i < farm; i++ {
			m.UpdateWorker(bestMiner, name(i), true, 1000, 1e9+float64(i))
		}
	})
	rows, err := LoadBestSharesDB()
	if err != nil {
		t.Fatal(err)
	}
	lost := 0
	for i := 0; i < farm; i++ {
		run2.UpdateWorker(bestMiner, name(i), true, 1000, 1000)
		if run2.workers[bestMiner+":"+name(i)].ATHDiff != 1e9+float64(i) {
			lost++
		}
	}
	if len(rows) != farm || lost != 0 {
		t.Errorf("ATH-RESTART-FARM-DB: of %d names mining before a restart, %d rows are left after the first write and %d come back without their best; want all kept",
			farm, len(rows), lost)
	}
}

// More than 20 payout addresses mine until a restart: the new run's first write drops none of them.
func TestMinersBestsOutliveARestart(t *testing.T) {
	const miners = MaxKeptMiners + 5
	miner := func(i int) string { return fmt.Sprintf("miner%02d", i) }
	run2 := restartOn(t, func(m *StatsManager) {
		for i := 0; i < miners; i++ {
			m.UpdateWorker(miner(i), "rig", true, 1000, 5e5+float64(i))
		}
	})
	rows, err := LoadBestSharesDB()
	if err != nil {
		t.Fatal(err)
	}
	bests, lost := run2.MinerBests(), 0
	for i := 0; i < miners; i++ {
		if bests[miner(i)] != 5e5+float64(i) {
			lost++
		}
	}
	if len(rows) != miners || lost != 0 {
		t.Errorf("ATH-RESTART-MINERS-DB: of %d miners mining before a restart, %d rows are left after the first write and %d lost their best; want all kept",
			miners, len(rows), lost)
	}
}

// The bests in forgesolo.db itself: a stratum that stops and starts again on the same file reads
// what the last one kept.
func TestBestSharesInTheDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forgesolo.db")
	if err := InitDB(path); err != nil {
		t.Fatal(err)
	}
	run1 := newStatsManager()
	if _, err := run1.LoadBestShares(); err != nil {
		t.Fatalf("ATH-DB-TABLE: a new database's best shares cannot be read: %v", err)
	}
	run1.UpdateWorker(bestMiner, "s19", true, 1000, 5e9)
	run1.UpdateWorker(bestMiner, "bitaxe", true, 1000, 2e6)
	mustWrite(t, run1)
	seen := keptOf(t, run1, bestMiner, "s19").seen
	CloseDB()

	if err := InitDB(path); err != nil {
		t.Fatal(err)
	}
	defer CloseDB()
	run2 := newStatsManager()
	if n, err := run2.LoadBestShares(); err != nil || n != 2 {
		t.Fatalf("ATH-DB-READ: the second run read %d bests (%v), want 2", n, err)
	}
	run2.UpdateWorker(bestMiner, "s19", true, 1000, 3000)
	if got := run2.workers[bestMiner+":s19"].ATHDiff; got != 5e9 {
		t.Errorf("ATH-DB-RESTART: after a restart the worker's best is %v, want 5e9", got)
	}
	if got := run2.MinerBests()[bestMiner]; got != 5e9 {
		t.Errorf("ATH-DB-MINER: after a restart the miner's best is %v, want 5e9", got)
	}

	// Stored as every time is: UTC, to the second.
	raw, err := sql.Open("sqlite", SQLiteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var at string
	if err := raw.QueryRow(`SELECT CAST(seen_at AS TEXT) FROM best_shares WHERE worker_name = 's19'`).Scan(&at); err != nil || at != SQLiteTime(seen) {
		t.Errorf("ATH-DB-UTC: seen_at is %q (%v), want %q", at, err, SQLiteTime(seen))
	}

	// A write never lowers a kept best, nor puts its time back, and removes what it is given.
	early := seen.Add(-time.Hour)
	if err := SaveBestSharesDB([]BestShare{{Miner: bestMiner, Worker: "s19", Difficulty: 1, Seen: early}}, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := LoadBestSharesDB()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]BestShare{}
	for _, r := range rows {
		got[r.Worker] = r
	}
	if r := got["s19"]; r.Difficulty != 5e9 {
		t.Errorf("ATH-DB-NEVER-LOWER: after a lower write the best is %v, want 5e9", r.Difficulty)
	}
	if r := got["s19"]; !r.Seen.Equal(seen.Truncate(time.Second)) {
		t.Errorf("ATH-DB-SEEN-NEVER-BACK: after an earlier write the worker was last seen at %v, want %v", r.Seen, seen.Truncate(time.Second))
	}
	if err := SaveBestSharesDB(nil, []BestShare{{Miner: bestMiner, Worker: "bitaxe"}}); err != nil {
		t.Fatal(err)
	}
	if rows, _ := LoadBestSharesDB(); len(rows) != 1 || rows[0].Worker != "s19" {
		t.Errorf("ATH-DB-GONE: after removing bitaxe the database holds %+v", rows)
	}
}

// A damaged row, a difficulty that is not a number, does not stop the others from being read: it
// reads as 0, which the stratum drops.
func TestBestSharesDamagedRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forgesolo.db")
	if err := InitDB(path); err != nil {
		t.Fatal(err)
	}
	defer CloseDB()
	if err := SaveBestSharesDB([]BestShare{{Miner: bestMiner, Worker: "s19", Difficulty: 5e9, Seen: time.Now()}}, nil); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", SQLiteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`INSERT INTO best_shares (miner_address, worker_name, difficulty, seen_at) VALUES (?, 'odd', 'x', NULL)`, bestMiner); err != nil {
		t.Fatal(err)
	}
	m := newStatsManager()
	if n, err := m.LoadBestShares(); err != nil || n != 2 || m.KeptBest(bestMiner, "s19") != 5e9 || m.KeptBest(bestMiner, "odd") != 0 {
		t.Fatalf("ATH-DB-DAMAGED-ROW: with one damaged row, %d rows were read (%v), s19 %v, odd %v; want 2, s19 5e9, odd dropped",
			n, err, m.KeptBest(bestMiner, "s19"), m.KeptBest(bestMiner, "odd"))
	}
	mustWrite(t, m)
	if rows, err := LoadBestSharesDB(); err != nil || len(rows) != 1 {
		t.Errorf("ATH-DB-DAMAGED-GONE: after a write the database holds %+v (%v), want the damaged row removed", rows, err)
	}
}

// A database made before the table was there, 1.0.12's on Linux or an earlier 1.0.13's, gets it at
// the next start, with what it held left as it was.
func TestBestSharesTableAddedToAnExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forgesolo.db")
	if err := InitDB(path); err != nil {
		t.Fatal(err)
	}
	if err := SavePoolConfig(bestMiner, "", ""); err != nil {
		t.Fatal(err)
	}
	CloseDB()
	raw, err := sql.Open("sqlite", SQLiteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`DROP TABLE best_shares`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	if err := InitDB(path); err != nil {
		t.Fatalf("ATH-DB-ADDED: the database without the table did not open: %v", err)
	}
	defer CloseDB()
	if err := SaveBestSharesDB([]BestShare{{Miner: bestMiner, Worker: "s19", Difficulty: 5e9, Seen: time.Now()}}, nil); err != nil {
		t.Fatalf("ATH-DB-ADDED: a best could not be written: %v", err)
	}
	if rows, err := LoadBestSharesDB(); err != nil || len(rows) != 1 {
		t.Errorf("ATH-DB-ADDED: %d bests read back (%v), want 1", len(rows), err)
	}
	if pool, _, _, err := GetPoolConfig(); err != nil || pool != bestMiner {
		t.Errorf("ATH-DB-KEPT-DATA: the payout address is %q (%v) after the table was added", pool, err)
	}
}

// With no database, nothing is read or written, and the stratum is told so: it tries again.
func TestBestSharesWithoutADatabase(t *testing.T) {
	CloseDB()
	if _, err := LoadBestSharesDB(); !errors.Is(err, ErrDatabaseNotInitialized) {
		t.Errorf("ATH-DB-NONE-READ: %v", err)
	}
	if err := SaveBestSharesDB([]BestShare{{Miner: bestMiner, Worker: "s19", Difficulty: 1}}, nil); !errors.Is(err, ErrDatabaseNotInitialized) {
		t.Errorf("ATH-DB-NONE-WRITE: %v", err)
	}
}

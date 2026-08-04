//go:build sqlite

package stats

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// legacySchema is the payouts/blocks shape that shipped BEFORE the migration: no
// confirmed_at on blocks, and no UNIQUE(miner_address, block_height) on payouts. Any
// Windows install created by an earlier build has exactly this on disk.
const legacySchema = `
CREATE TABLE IF NOT EXISTS blocks (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	height INTEGER UNIQUE NOT NULL,
	hash TEXT NOT NULL,
	miner_address TEXT NOT NULL,
	reward REAL DEFAULT 50.0,
	status TEXT DEFAULT 'pending',
	is_solo INTEGER DEFAULT 0,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS payouts (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	miner_address TEXT NOT NULL,
	block_height INTEGER NOT NULL,
	amount REAL NOT NULL,
	confirmed INTEGER DEFAULT 0,
	status TEXT DEFAULT 'pending',
	txid TEXT,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	paid_at DATETIME
);`

func makeLegacyDB(t *testing.T, seed func(*sql.DB)) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	if _, err := raw.Exec(legacySchema); err != nil {
		t.Fatalf("legacy schema: %v", err)
	}
	if seed != nil {
		seed(raw)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close legacy: %v", err)
	}
	return path
}

// The ordinary upgrade: an existing, clean database gains confirmed_at and the payouts
// uniqueness index, and its data survives.
func TestMigrationUpgradesACleanLegacyDatabase(t *testing.T) {
	const miner = "bitcoincashii:qlegacy00000000000000000000000000000000"
	path := makeLegacyDB(t, func(raw *sql.DB) {
		raw.Exec(`INSERT INTO payouts (miner_address, block_height, amount) VALUES (?,?,?)`, miner, 10, 1.5)
		raw.Exec(`INSERT INTO blocks (height, hash, miner_address, reward, is_solo, status) VALUES (?,?,?,?,1,'pending')`, 10, "hashlegacy", miner, 50.0)
	})

	if err := InitDB(path); err != nil {
		t.Fatalf("InitDB on legacy database: %v", err)
	}
	defer CloseDB()

	// Pre-existing data survived.
	var amount float64
	if err := db.QueryRow(`SELECT amount FROM payouts WHERE miner_address=? AND block_height=10`, miner).Scan(&amount); err != nil {
		t.Fatalf("legacy payout row lost: %v", err)
	}
	if amount != 1.5 {
		t.Errorf("amount = %v, want 1.5", amount)
	}

	// confirmed_at was added, so ConfirmSoloBlock can write it.
	if err := ConfirmSoloBlock(10); err != nil {
		t.Fatalf("ConfirmSoloBlock after migration: %v", err)
	}
	var status string
	_ = db.QueryRow(`SELECT status FROM blocks WHERE height=10`).Scan(&status)
	if status != "confirmed" {
		t.Errorf("status = %q, want confirmed", status)
	}

	// The uniqueness index exists, so the coinbase-direct upsert is legal.
	if err := SaveSoloBlockCoinbaseDirect(miner, 11, 3.125, "hashnew"); err != nil {
		t.Fatalf("upsert after migration: %v", err)
	}

	// And the legacy row is still reservable -- migration must not strand balances.
	if _, rows, _, err := ReserveMaturePayouts(miner, 100); err != nil || len(rows) != 1 {
		t.Fatalf("legacy row not reservable after migration: rows=%d err=%v", len(rows), err)
	}
}

// Running the migration twice must be a no-op, not an error: every restart re-runs it.
func TestMigrationIsIdempotent(t *testing.T) {
	path := makeLegacyDB(t, nil)
	for i := 1; i <= 3; i++ {
		if err := InitDB(path); err != nil {
			t.Fatalf("InitDB pass %d: %v", i, err)
		}
		CloseDB()
	}
}

// THE CASE THAT MATTERS. An older build could create duplicate payout rows for the same
// (miner, height), because INSERT OR IGNORE ignores nothing without a unique constraint.
// The index cannot then be created. The app must still START -- refusing to boot would
// strand a user with a mining rig and no pool -- and must not silently destroy or
// deduplicate their ledger behind their back.
func TestMigrationSurvivesPreExistingDuplicates(t *testing.T) {
	const miner = "bitcoincashii:qdupes000000000000000000000000000000000"
	path := makeLegacyDB(t, func(raw *sql.DB) {
		// The exact shape the old INSERT OR IGNORE bug produced.
		raw.Exec(`INSERT INTO payouts (miner_address, block_height, amount) VALUES (?,?,?)`, miner, 20, 2.0)
		raw.Exec(`INSERT INTO payouts (miner_address, block_height, amount) VALUES (?,?,?)`, miner, 20, 2.0)
	})

	if err := InitDB(path); err != nil {
		t.Fatalf("app refused to start on a database with duplicates: %v", err)
	}
	defer CloseDB()

	// Both rows must still be there -- untouched, for the operator to reconcile.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM payouts WHERE miner_address=? AND block_height=20`, miner).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("duplicate rows = %d, want both 2 preserved (migration must not delete ledger data)", n)
	}

	// The index must NOT exist -- creating it would have required destroying data.
	var idx int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='uq_payouts_miner_height'`).Scan(&idx)
	if idx != 0 {
		t.Errorf("uniqueness index was created despite duplicates present")
	}

	// The pool still works for everything that does not need the index.
	pendingID, rows, total, err := ReserveMaturePayouts(miner, 100)
	if err != nil {
		t.Fatalf("reservation broken on a duplicate-bearing database: %v", err)
	}
	if pendingID == "" || len(rows) != 2 || total != 4.0 {
		t.Errorf("reserve = %q %d rows %v, want both rows totalling 4.0", pendingID, len(rows), total)
	}
}

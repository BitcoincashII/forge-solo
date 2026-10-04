//go:build sqlite

package stats

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/dblock"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

var (
	db   *sql.DB
	dbMu sync.RWMutex // CRITICAL FIX: Mutex protection for global db pointer
)

// ErrDatabaseNotInitialized is returned when database operations are attempted without initialization
var ErrDatabaseNotInitialized = fmt.Errorf("database not initialized")

// GetDBPath returns the SQLite database file path.
//
// The default file is forgesolo.db, but an existing forgepool.db is adopted in place.
// DB_PATH is set nowhere in this repo, so the default always wins and there was no legacy
// fallback: renaming outright would have booted an existing install against an empty
// database -- no blocks, no payouts, no settings, no payout address, and therefore mining
// paused -- with the real file sitting intact and unreachable beside it.
//
// The fallback makes the rename safe whichever way the Windows build is packaged, which is
// not something this repo can settle on its own: its own comments claim sqlite is the
// Windows path, while the shipped installer appears to bundle the postgres build.
func GetDBPath() string {
	if dbPath := os.Getenv("DB_PATH"); dbPath != "" {
		return dbPath
	}
	dir := "data"
	if exe, err := os.Executable(); err == nil {
		dir = filepath.Join(filepath.Dir(exe), "data")
	} else {
		log.Printf("Warning: could not get executable path: %v, using current directory", err)
	}
	if legacy := filepath.Join(dir, "forgepool.db"); fileExists(legacy) {
		return legacy
	}
	return filepath.Join(dir, "forgesolo.db")
}

// SQLiteTime is t as the SQLite build stores every time: UTC, to the second, "YYYY-MM-DD
// HH:MM:SS", the form SQLite's own CURRENT_TIMESTAMP writes. Bound as a time.Time, the driver
// stored Go's String() form in t's own zone, "2026-10-01 01:45:30.009992576 -0500 CDT m=+0.0058",
// which SQLite's date functions cannot read: strftime('%s', ...) returned NULL, the scan failed,
// and the dashboard listed no solo blocks at all. The queries read only the first 19 characters
// of a stored time, as UTC, so such a row reads too, off by its zone's offset.
func SQLiteTime(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05")
}

// sqliteNow is the current time as SQLiteTime stores it.
func sqliteNow() string {
	return SQLiteTime(time.Now())
}

// parseSQLiteTime reads a stored time as the queries do: its first 19 characters, as UTC. ok is
// false when they are not a time.
func parseSQLiteTime(s string) (time.Time, bool) {
	if len(s) < 19 {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", strings.Replace(s[:19], "T", " ", 1), time.UTC)
	return t, err == nil
}

// fileExists reports whether a path is present. Used only to adopt a pre-rename database.
func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// GetDBConnStr returns connection string (for compatibility)
func GetDBConnStr() string {
	return GetDBPath()
}

// SQLiteDSN is how every Forge Solo program opens the SQLite database at path. The api and the
// stratum share the file, so each connection:
//   - waits up to 10 s for the other's write lock (busy_timeout) instead of failing at once;
//   - starts every transaction with BEGIN IMMEDIATE (_txlock), taking the write lock where SQLite
//     waits for it. A plain BEGIN takes it at the transaction's first write, and when the other
//     program has written since the transaction's first read, SQLite fails that write at once
//     with SQLITE_BUSY: Distribute1175Block failed that way under load;
//   - uses WAL, so reads go on while the other program writes;
//   - syncs every commit to disk (synchronous FULL), as PostgreSQL did. It is SQLite's default
//     too; it is set here so that no change of default can loosen it.
func SQLiteDSN(path string) string {
	return path + "?_busy_timeout=10000&_journal_mode=WAL&_txlock=immediate&_pragma=synchronous(FULL)"
}

// sqliteConns is how many connections a program keeps open to the database. A write that waits
// for the other program's lock holds its connection while it waits; with one connection every
// dashboard read and the health ping waited behind it, and reported the database as down.
const sqliteConns = 4

// isBusy reports whether err is SQLITE_BUSY or one of its extended codes.
func isBusy(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code()&0xff == sqlite3.SQLITE_BUSY
}

// pingBusyFor is how long InitDB tries again while the database answers SQLITE_BUSY.
const pingBusyFor = 5 * time.Second

// ping opens the first connection. Two programs opening a database file that does not exist yet
// race to switch it to WAL, and SQLite fails the loser with SQLITE_BUSY at once, without waiting:
// on a fresh install the api or the stratum failed its first start that way. The switch takes
// moments, so a BUSY answer is tried again every 100 ms, for up to pingBusyFor.
func ping(d *sql.DB) error {
	deadline := time.Now().Add(pingBusyFor)
	for {
		err := d.Ping()
		if err == nil || !isBusy(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// inUse holds the in-use locks this program took on the databases it opened; CloseDB releases
// them. Guarded by dbMu.
var inUse []*dblock.Lock

// While the database is being moved, InitDB waits up to inUseWait, saying so every inUseLogEvery.
// Variables so that a test need not wait 15 minutes.
var (
	inUseWait     = 15 * time.Minute
	inUseLogEvery = 30 * time.Second
)

// holdInUse takes the shared in-use lock beside the database at dbPath (see internal/dblock),
// which the program then holds until CloseDB or its end. forge-solo-migrate holds the lock
// exclusively while it replaces the database file, and replaces it only while nobody else holds
// the lock, so a program never writes to a file that is being swapped out; one that starts
// meanwhile waits here and then opens the new file.
func holdInUse(dbPath string) (*dblock.Lock, error) {
	path := dblock.Path(dbPath)
	lock, err := dblock.Shared(path, 0)
	deadline := time.Now().Add(inUseWait)
	for errors.Is(err, dblock.ErrBusy) {
		left := time.Until(deadline)
		if left <= 0 {
			return nil, fmt.Errorf("the database %s is still being moved after %v", dbPath, inUseWait)
		}
		log.Printf("waiting: Forge Solo is moving its database")
		lock, err = dblock.Shared(path, min(inUseLogEvery, left))
	}
	if err != nil {
		return nil, fmt.Errorf("the database's in-use lock: %w", err)
	}
	return lock, nil
}

// InitDB initializes SQLite database
func InitDB(connStr string) error {
	dbPath := connStr
	if dbPath == "" {
		dbPath = GetDBPath()
	}

	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	// Before the database is opened. A wait for a move holds up nothing else: the rest of the
	// program finds no database meanwhile, as it does while a start is retried.
	lock, err := holdInUse(dbPath)
	if err != nil {
		return err
	}

	dbMu.Lock()
	defer dbMu.Unlock()

	if err = openSQLite(dbPath); err != nil {
		// The api and the stratum try again every few seconds while the database will not
		// open. A lock kept from each failed try would stay until the program ends, and the
		// move would find the database in use all that time.
		lock.Release()
		return err
	}
	inUse = append(inUse, lock)

	// 1175 merge-mining ledger tables. Mirrors the postgres backend; without it a found
	// aux block cannot be recorded (see Init1175Schema in dialect_sqlite.go).
	Init1175Schema()

	log.Printf("✅ Connected to SQLite database: %s", dbPath)
	return nil
}

// openSQLite opens the database at dbPath as db and makes its tables. When it fails, db is nil.
func openSQLite(dbPath string) error {
	var err error
	db, err = sql.Open("sqlite", SQLiteDSN(dbPath))
	if err != nil {
		return err
	}

	// The connections stay open for the life of the program (no maximum lifetime).
	db.SetMaxOpenConns(sqliteConns)
	db.SetMaxIdleConns(sqliteConns)
	db.SetConnMaxLifetime(0)

	if err = ping(db); err != nil {
		db.Close()
		db = nil
		return err
	}

	// Create tables
	if err = createTables(); err != nil {
		db.Close()
		db = nil
		return fmt.Errorf("failed to create tables: %w", err)
	}
	return nil
}

func createTables() error {
	schema := `
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
	);

	CREATE TABLE IF NOT EXISTS miners (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		address TEXT UNIQUE NOT NULL,
		solo_mining INTEGER DEFAULT 0,
		manual_diff REAL DEFAULT 0,
		address_1175 TEXT,
		settings_pin_hash TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS shares (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		miner_address TEXT NOT NULL,
		worker_name TEXT,
		difficulty REAL NOT NULL,
		is_solo INTEGER DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_blocks_height ON blocks(height);
	CREATE INDEX IF NOT EXISTS idx_blocks_miner ON blocks(miner_address);
	CREATE INDEX IF NOT EXISTS idx_blocks_status ON blocks(status);
	CREATE INDEX IF NOT EXISTS idx_blocks_created ON blocks(created_at);
	CREATE INDEX IF NOT EXISTS idx_payouts_miner ON payouts(miner_address);
	CREATE INDEX IF NOT EXISTS idx_payouts_height ON payouts(block_height);
	CREATE INDEX IF NOT EXISTS idx_payouts_txid ON payouts(txid);
	CREATE INDEX IF NOT EXISTS idx_payouts_status ON payouts(status);
	CREATE INDEX IF NOT EXISTS idx_shares_miner ON shares(miner_address);
	CREATE INDEX IF NOT EXISTS idx_shares_id_desc ON shares(id DESC);
	CREATE INDEX IF NOT EXISTS idx_miners_address ON miners(address);

	-- Dashboard-managed single-row pool configuration. Postgres has carried this
	-- table for as long as GetPoolConfig/SavePoolConfig have existed; SQLite never
	-- gained it, so those calls would have failed at runtime even once they linked.
	CREATE TABLE IF NOT EXISTS pool_config (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		pool_address TEXT DEFAULT '',
		payout_address_1175 TEXT DEFAULT '',
		coinbase_tag TEXT DEFAULT '',
		payout_mode TEXT DEFAULT 'solo',
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	-- The TIDES gateway's identity at Forge Pool: an Ed25519 seed made on first use.
	CREATE TABLE IF NOT EXISTS datum_identity (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		key_seed TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	`

	if _, err := db.Exec(schema); err != nil {
		return err
	}

	// Additive migrations. CREATE TABLE IF NOT EXISTS cannot add a column to a
	// database that already exists, and SQLite has no ADD COLUMN IF NOT EXISTS, so a
	// duplicate-column error here is the expected no-op on an already-migrated file.
	for _, stmt := range []string{
		`ALTER TABLE blocks ADD COLUMN confirmed_at DATETIME`,
		// TIDES mode (Forge Solo as a DATUM gateway to Forge Pool): which way blocks pay.
		`ALTER TABLE pool_config ADD COLUMN payout_mode TEXT DEFAULT 'solo'`,
		// A solo block pays its finder in its own coinbase: nothing accumulates and there is
		// no threshold to cross. These columns outlived the payout sender and kept reporting a
		// default nothing honours. SQLite has no DROP COLUMN IF EXISTS, so on an already
		// migrated file the "no such column" error below is the expected no-op.
		`ALTER TABLE miners DROP COLUMN min_payout`,
		`ALTER TABLE pool_config DROP COLUMN min_payout`,
		// Cached balance/total_paid: nothing ever read them, and the Postgres schema never
		// declared them at all, so the writes that maintained them had been failing silently
		// there. The dashboard sums payouts on read instead (GetMinerBalanceDB).
		`ALTER TABLE miners DROP COLUMN balance`,
		`ALTER TABLE miners DROP COLUMN total_paid`,
		// Matches postgres (payouts UNIQUE(miner_address, block_height) in corePostgresSchema).
		// Without it the INSERT OR IGNORE above ignores nothing and a re-recorded block
		// double-credits. Fails loudly-but-nonfatally if an existing file already holds
		// duplicates, which must then be reconciled by hand rather than silently indexed.
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_payouts_miner_height ON payouts(miner_address, block_height)`,
	} {
		if _, err := db.Exec(stmt); err != nil &&
			!strings.Contains(err.Error(), "duplicate column") &&
			!strings.Contains(err.Error(), "no such column") {
			log.Printf("Warning: schema migration %q: %v", stmt, err)
		}
	}
	return nil
}

// CloseDB closes the database, and then lets its in-use lock go.
func CloseDB() {
	dbMu.Lock()
	defer dbMu.Unlock()
	if db != nil {
		if err := db.Close(); err != nil {
			log.Printf("Warning: error closing database: %v", err)
		}
		db = nil
	}
	for _, l := range inUse {
		if err := l.Release(); err != nil {
			log.Printf("Warning: releasing the database's in-use lock: %v", err)
		}
	}
	inUse = nil
}

// IsDBInitialized reports whether a database handle was ever successfully created.
//
// Deliberately distinct from IsDBConnected, which also pings: a handle that exists but
// fails a ping is a transient outage, and database/sql reconnects that pool on its own.
// Re-running InitDB in that case would replace a self-healing pool with a new one every
// retry and leak the old, since InitDB overwrites the handle without closing it. Only a
// handle that was NEVER created needs to be built.
func IsDBInitialized() bool {
	dbMu.RLock()
	defer dbMu.RUnlock()
	return db != nil
}

func IsDBConnected() bool {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), PingTimeout)
	defer cancel()
	return db.PingContext(ctx) == nil
}

// SavePayout saves a payout to the database
func SavePayout(minerID string, blockHeight int64, amount float64) error {
	dbMu.RLock()
	defer dbMu.RUnlock()

	if db == nil {
		return ErrDatabaseNotInitialized // CRITICAL FIX: Return error instead of nil
	}

	_, err := db.Exec(`
		INSERT OR IGNORE INTO payouts (miner_address, block_height, amount, confirmed, created_at)
		VALUES (?, ?, ?, 0, ?)`,
		minerID, blockHeight, amount, sqliteNow())
	return err
}

// SaveBlockDBWithSolo saves a block to the database with solo flag
func SaveBlockDBWithSolo(minerID string, height int64, hash string, reward float64, isSolo bool) error {
	dbMu.RLock()
	defer dbMu.RUnlock()

	if db == nil {
		return ErrDatabaseNotInitialized // CRITICAL FIX: Return error instead of nil
	}

	solo := 0
	if isSolo {
		solo = 1
	}

	_, err := db.Exec(`
		INSERT OR IGNORE INTO blocks (height, hash, miner_address, reward, is_solo, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		height, hash, minerID, reward, solo, sqliteNow())
	return err
}

// SavePayoutAtomicWithSolo saves both block and payout with solo flag
func SavePayoutAtomicWithSolo(minerID string, blockHeight int64, amount float64, blockHash string, isSolo bool) error {
	dbMu.RLock()
	defer dbMu.RUnlock()

	if db == nil {
		return ErrDatabaseNotInitialized // CRITICAL FIX: Return error instead of nil
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	solo := 0
	if isSolo {
		solo = 1
	}

	_, err = tx.Exec(`
		INSERT OR IGNORE INTO blocks (height, hash, miner_address, reward, is_solo, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		blockHeight, blockHash, minerID, amount, solo, sqliteNow())
	if err != nil {
		return fmt.Errorf("failed to insert block: %w", err)
	}

	_, err = tx.Exec(`
		INSERT OR IGNORE INTO payouts (miner_address, block_height, amount, confirmed, created_at)
		VALUES (?, ?, ?, 0, ?)`,
		minerID, blockHeight, amount, sqliteNow())
	if err != nil {
		return fmt.Errorf("failed to insert payout: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	log.Printf("✅ Saved block %d and payout %.2f BCH2 for %s atomically", blockHeight, amount, minerID)
	return nil
}

// GetMinerBalanceDB gets balance from database
func GetMinerBalanceDB(minerID string, currentHeight int64) (mature float64, immature float64) {
	dbMu.RLock()
	defer dbMu.RUnlock()

	if db == nil {
		return GetMinerBalance(minerID, currentHeight)
	}

	matureHeight := currentHeight - COINBASE_MATURITY

	row := db.QueryRow(`
		SELECT COALESCE(SUM(amount), 0) FROM payouts
		WHERE miner_address = ? AND (txid IS NULL OR txid = '') AND block_height <= ?`,
		minerID, matureHeight)
	if err := row.Scan(&mature); err != nil {
		log.Printf("Warning: failed to scan mature balance for %s: %v", minerID, err)
		mature = 0
	}

	row = db.QueryRow(`
		SELECT COALESCE(SUM(amount), 0) FROM payouts
		WHERE miner_address = ? AND (txid IS NULL OR txid = '') AND block_height > ?`,
		minerID, matureHeight)
	if err := row.Scan(&immature); err != nil {
		log.Printf("Warning: failed to scan immature balance for %s: %v", minerID, err)
		immature = 0
	}

	return
}

// GetTotalBlocksDB returns total blocks in database
func GetTotalBlocksDB() int64 {
	dbMu.RLock()
	defer dbMu.RUnlock()

	if db == nil {
		return 0
	}

	var count int64
	if err := db.QueryRow("SELECT COUNT(*) FROM blocks").Scan(&count); err != nil {
		log.Printf("Warning: failed to count blocks: %v", err)
		return 0
	}
	return count
}

// GetAllPoolBlocksDB gets all blocks mined by the pool with pagination
func GetAllPoolBlocksDB(page, limit int) ([]PoolBlock, int64) {
	dbMu.RLock()
	defer dbMu.RUnlock()

	if db == nil {
		return []PoolBlock{}, 0
	}

	// CRITICAL FIX: Validate pagination parameters
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 1000 {
		limit = 1000
	}

	var total int64
	if err := db.QueryRow("SELECT COUNT(*) FROM blocks").Scan(&total); err != nil {
		log.Printf("Warning: failed to count blocks: %v", err)
		total = 0
	}

	offset := (page - 1) * limit
	rows, err := db.Query(`
		SELECT height, hash, reward, miner_address, status, strftime('%s', substr(created_at, 1, 19)), COALESCE(is_solo, 0)
		FROM blocks ORDER BY height DESC LIMIT ? OFFSET ?`,
		limit, offset)
	if err != nil {
		log.Printf("Warning: failed to query blocks: %v", err)
		return []PoolBlock{}, total
	}
	defer rows.Close()

	var blocks []PoolBlock
	for rows.Next() {
		var b PoolBlock
		var isSolo int
		if err := rows.Scan(&b.Height, &b.Hash, &b.Reward, &b.MinerAddr, &b.Status, &b.CreatedAt, &isSolo); err != nil {
			log.Printf("Warning: failed to scan block: %v", err)
			continue
		}
		b.IsSolo = isSolo == 1
		blocks = append(blocks, b)
	}

	if err := rows.Err(); err != nil {
		log.Printf("Warning: error iterating blocks: %v", err)
	}

	return blocks, total
}

// GetMinerSoloBlocksDB gets solo blocks found by a specific miner
func GetMinerSoloBlocksDB(minerID string) []SoloBlock {
	blocks, _, _, err := SoloBlocksSummary(minerID)
	if err != nil {
		log.Printf("Warning: failed to query solo blocks: %v", err)
		return []SoloBlock{}
	}
	return blocks
}

// SoloBlocksSummary is a miner's latest solo blocks (at most 100, newest first) and what all of
// them come to: how many were found, and what those not orphaned paid. The list is capped for the
// page; the figures are not -- past 100 blocks they were the newest 100's. An error means the
// database did not answer, which the dashboard must not show as "no blocks found".
func SoloBlocksSummary(minerID string) ([]SoloBlock, int, float64, error) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return nil, 0, 0, ErrDatabaseNotInitialized
	}
	ctx, cancel := context.WithTimeout(context.Background(), DashboardReadTimeout)
	defer cancel()
	var found int
	var earned float64
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(CASE WHEN COALESCE(status,'') = 'orphaned' THEN 0 ELSE reward END), 0)
		FROM blocks WHERE miner_address = ? AND is_solo = 1`, minerID).Scan(&found, &earned); err != nil {
		return nil, 0, 0, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT b.height, b.hash, b.reward, strftime('%s', substr(b.created_at, 1, 19)), b.status,
			COALESCE(p.txid, '') as payout_txid
		FROM blocks b
		LEFT JOIN payouts p ON p.block_height = b.height AND p.miner_address = b.miner_address
		WHERE b.miner_address = ? AND b.is_solo = 1
		ORDER BY b.height DESC LIMIT 100`,
		minerID)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()

	blocks := []SoloBlock{}
	for rows.Next() {
		var b SoloBlock
		var status, payoutTxid string
		if err := rows.Scan(&b.Height, &b.Hash, &b.Reward, &b.Time, &status, &payoutTxid); err != nil {
			return nil, 0, 0, err
		}
		b.Status = status
		b.Confirmed = (status == "confirmed")
		b.PayoutTxid = payoutTxid
		blocks = append(blocks, b)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, 0, err
	}
	return blocks, found, earned, nil
}

// SaveMinerSettings saves or updates miner settings
func SaveMinerSettings(settings *MinerSettings) error {
	dbMu.RLock()
	defer dbMu.RUnlock()

	if db == nil {
		return ErrDatabaseNotInitialized // CRITICAL FIX: Return error instead of nil
	}

	solo := 0
	if settings.SoloMining {
		solo = 1
	}

	// address_1175 is written here and read back in LoadAllMinerSettings and
	// GetMinerSettingsDB. The postgres backend has always persisted it; sqlite dropped it
	// at all three sites even though its own schema declares the column, so on the Windows
	// build a per-miner 1175 payout address was accepted by the API, never stored, and
	// blanked out of memory by the next settings reload.
	_, err := db.Exec(`
		INSERT INTO miners (address, solo_mining, manual_diff, address_1175, updated_at)
		VALUES (?, ?, ?, ?, datetime('now'))
		ON CONFLICT(address) DO UPDATE SET
			solo_mining = excluded.solo_mining,
			manual_diff = excluded.manual_diff,
			address_1175 = excluded.address_1175,
			updated_at = datetime('now')`,
		settings.Address, solo, settings.ManualDiff, settings.Address1175)
	return err
}

// LoadAllMinerSettings loads all miner settings from database
func LoadAllMinerSettings() map[string]*MinerSettings {
	dbMu.RLock()
	defer dbMu.RUnlock()

	result := make(map[string]*MinerSettings)
	if db == nil {
		return result
	}

	rows, err := db.Query(`SELECT address, solo_mining, manual_diff, COALESCE(address_1175, '') FROM miners`)
	if err != nil {
		log.Printf("Warning: failed to load miner settings: %v", err)
		return result
	}
	defer rows.Close()

	for rows.Next() {
		var s MinerSettings
		var solo int
		if err := rows.Scan(&s.Address, &solo, &s.ManualDiff, &s.Address1175); err != nil {
			log.Printf("Warning: failed to scan miner settings: %v", err)
			continue
		}
		s.SoloMining = solo == 1
		result[s.Address] = &s
	}

	if err := rows.Err(); err != nil {
		log.Printf("Warning: error iterating miner settings: %v", err)
	}

	if n := int64(len(result)) + 1; minerSettingsLogged.Swap(n) != n {
		log.Printf("✅ Loaded %d miner settings from database", len(result))
	}
	return result
}

// GetMinerPayoutsDB returns a miner's payouts grouped by txid, newest first, how many groups there
// are, and what the paid ones come to. A group is paid unless a row in it was orphaned or its txid
// is a reservation (pending_...). The Postgres build answers the same.
func GetMinerPayoutsDB(minerID string) ([]PayoutRecord, int, float64) {
	dbMu.RLock()
	defer dbMu.RUnlock()

	payouts := []PayoutRecord{}
	if db == nil {
		return payouts, 0, 0
	}

	rows, err := db.Query(`
		SELECT txid, SUM(amount) as amount, MAX(paid_at) as paid_at, COUNT(*) as blocks,
		       CASE WHEN MAX(CASE WHEN COALESCE(status,'') = 'orphaned' THEN 1 ELSE 0 END) = 1 THEN 0
		            WHEN txid LIKE 'pending_%' THEN 0 ELSE 1 END as is_confirmed
		FROM payouts
		WHERE miner_address = ?
		  AND txid IS NOT NULL
		  AND txid != ''
		GROUP BY txid
		ORDER BY MAX(paid_at) DESC, txid
		LIMIT 100`,
		minerID)
	if err != nil {
		log.Printf("Warning: failed to query payouts: %v", err)
		return payouts, 0, 0
	}
	defer rows.Close()

	var totalPaid float64

	for rows.Next() {
		var p PayoutRecord
		// MAX() drops the column's DATETIME type, so the driver returns the stored text, which a
		// time.Time cannot be scanned from: every row failed to scan and the list was always empty.
		var paidAt sql.NullString
		var confirmed int
		if err := rows.Scan(&p.TxID, &p.Amount, &paidAt, &p.Blocks, &confirmed); err != nil {
			log.Printf("Warning: failed to scan payout: %v", err)
			continue
		}
		p.Confirmed = confirmed == 1
		if t, ok := parseSQLiteTime(paidAt.String); paidAt.Valid && ok {
			p.PaidAt = t
		}
		if p.Confirmed {
			totalPaid += p.Amount
		}
		payouts = append(payouts, p)
	}

	if err := rows.Err(); err != nil {
		log.Printf("Warning: error iterating payouts: %v", err)
	}

	return payouts, len(payouts), totalPaid
}

// GetMinerSoloPayoutsDB returns payout history for solo blocks only
func GetMinerSoloPayoutsDB(minerID string) ([]PayoutRecord, int, float64) {
	payouts, count, paid, err := SoloPayoutsSummary(minerID)
	if err != nil {
		log.Printf("Warning: failed to query solo payouts: %v", err)
		return []PayoutRecord{}, 0, 0
	}
	return payouts, count, paid
}

// soloPayoutsFrom is the payouts of a miner's solo blocks: the ones with a txid. A payout counts as
// paid unless its block was orphaned or it is still pending.
const soloPayoutsFrom = `
		FROM payouts p
		JOIN blocks b ON b.height = p.block_height AND b.miner_address = p.miner_address
		WHERE p.miner_address = ?
		  AND b.is_solo = 1
		  AND p.txid IS NOT NULL
		  AND p.txid != ''`

// SoloPayoutsSummary is a miner's latest solo payouts (at most 100) and what all of them come to:
// how many, and how much was paid. The figures cover every payout; past 100 they were the newest
// 100's. An error means the database did not answer.
func SoloPayoutsSummary(minerID string) ([]PayoutRecord, int, float64, error) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return nil, 0, 0, ErrDatabaseNotInitialized
	}
	ctx, cancel := context.WithTimeout(context.Background(), DashboardReadTimeout)
	defer cancel()
	var count int
	var paid float64
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(CASE WHEN COALESCE(p.status,'') = 'orphaned' THEN 0
		                                   WHEN p.txid LIKE 'pending_%' THEN 0 ELSE p.amount END), 0)`+soloPayoutsFrom,
		minerID).Scan(&count, &paid); err != nil {
		return nil, 0, 0, err
	}

	rows, err := db.QueryContext(ctx, `
		SELECT p.txid, p.amount, p.paid_at, 1 as blocks, COALESCE(p.status,'') as status,
		       -- Read the ledger's own columns. Deriving this from the txid STRING treated an
		       -- orphaned payout (status='orphaned', confirmed=false, txid='orphaned') as
		       -- confirmed, so a voided reward stayed inside Total Paid -- contradicting the
		       -- balance card on the same screen, which does filter orphans out.
		       CASE WHEN COALESCE(p.status,'') = 'orphaned' THEN 0
		            WHEN p.txid LIKE 'pending_%' THEN 0 ELSE 1 END as is_confirmed`+soloPayoutsFrom+`
		ORDER BY p.paid_at DESC
		LIMIT 100`,
		minerID)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()

	payouts := []PayoutRecord{}
	for rows.Next() {
		var p PayoutRecord
		var paidAt sql.NullTime
		var confirmed int
		if err := rows.Scan(&p.TxID, &p.Amount, &paidAt, &p.Blocks, &p.Status, &confirmed); err != nil {
			return nil, 0, 0, err
		}
		p.Confirmed = confirmed == 1
		if paidAt.Valid {
			p.PaidAt = paidAt.Time
		}
		payouts = append(payouts, p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, 0, err
	}
	return payouts, count, paid, nil
}

// SaveShare saves a PPLNS share to the database
// SaveShare stores a share for the PPLNS window. A solo share is not stored: the PPLNS window
// (GetPPLNSShares) reads only non-solo shares, and nothing else reads them. Up to 1.0.12 every solo
// share was stored anyway, and trimmed only when a block was found, so a home miner's table grew by
// every share between blocks; ClearSoloShares removes those.
func SaveShare(minerAddress string, workerName string, difficulty float64, isSolo bool) error {
	if isSolo {
		return nil
	}
	dbMu.RLock()
	defer dbMu.RUnlock()

	if db == nil {
		return ErrDatabaseNotInitialized // CRITICAL FIX: Return error instead of nil
	}

	solo := 0
	if isSolo {
		solo = 1
	}

	_, err := db.Exec(`
		INSERT INTO shares (miner_address, worker_name, difficulty, is_solo)
		VALUES (?, ?, ?, ?)`,
		minerAddress, workerName, difficulty, solo)
	return err
}

// ClearSoloShares removes the solo shares stored before 1.0.13 (see SaveShare). With no PPLNS shares
// among them, one statement empties the table and frees its space at once (on TimescaleDB, every
// chunk with it); otherwise only the solo rows go, in batches that never hold a long transaction.
// It returns the rows removed, or -1 when the whole table was emptied without counting.
func ClearSoloShares() (int64, error) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return 0, ErrDatabaseNotInitialized
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var any, pplns bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM shares), EXISTS (SELECT 1 FROM shares WHERE NOT is_solo)`).Scan(&any, &pplns); err != nil {
		return 0, err
	}
	if !any {
		return 0, nil
	}
	if !pplns {
		_, err := db.ExecContext(ctx, `DELETE FROM shares`) // no WHERE: SQLite drops the pages at once
		return -1, err
	}
	var total int64
	for {
		res, err := db.ExecContext(ctx, `DELETE FROM shares WHERE id IN (SELECT id FROM shares WHERE is_solo LIMIT 50000)`)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		if total += n; n == 0 {
			return total, nil
		}
	}
}

// Compact gives back the space of rows deleted in bulk. SQLite keeps freed pages inside the file
// (auto_vacuum is off), so clearing the solo shares earlier versions stored would leave the file as
// large as before. It rewrites the file only when there is a lot to give back: at least 4 MB and a
// quarter of the file.
func Compact() error {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return ErrDatabaseNotInitialized
	}
	var free, total int64
	if err := db.QueryRow(`PRAGMA freelist_count`).Scan(&free); err != nil {
		return err
	}
	if err := db.QueryRow(`PRAGMA page_count`).Scan(&total); err != nil {
		return err
	}
	if free < 1024 || free*4 < total {
		return nil
	}
	if _, err := db.Exec(`VACUUM`); err != nil {
		return err
	}
	_, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	return err
}

// GetPPLNSShares returns the sum of difficulty per miner for the last N shares
func GetPPLNSShares(windowSize int) (map[string]float64, float64, error) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	return getPPLNSSharesLocked(windowSize)
}

// getPPLNSSharesLocked is GetPPLNSShares for a caller that already holds dbMu's read lock.
func getPPLNSSharesLocked(windowSize int) (map[string]float64, float64, error) {
	if db == nil {
		return nil, 0, ErrDatabaseNotInitialized
	}

	// CRITICAL FIX: Validate windowSize
	if windowSize < 1 {
		windowSize = 1
	}

	rows, err := db.Query(`
		WITH recent_shares AS (
			SELECT miner_address, difficulty
			FROM shares
			WHERE is_solo = 0
			ORDER BY id DESC
			LIMIT ?
		)
		SELECT miner_address, SUM(difficulty) as total_work
		FROM recent_shares
		GROUP BY miner_address`,
		windowSize)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to query PPLNS shares: %w", err)
	}
	defer rows.Close()

	result := make(map[string]float64)
	var totalWork float64

	for rows.Next() {
		var minerAddr string
		var work float64
		if err := rows.Scan(&minerAddr, &work); err != nil {
			log.Printf("Warning: failed to scan PPLNS share: %v", err)
			continue
		}
		result[minerAddr] = work
		totalWork += work
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("error iterating PPLNS shares: %w", err)
	}

	return result, totalWork, nil
}

// CleanupOldShares removes shares older than needed for PPLNS calculation
func CleanupOldShares(windowSize int) (int64, error) {
	dbMu.RLock()
	defer dbMu.RUnlock()

	if db == nil {
		return 0, ErrDatabaseNotInitialized // CRITICAL FIX: Return error instead of nil
	}

	// CRITICAL FIX: Validate windowSize
	if windowSize < 1 {
		windowSize = 1
	}

	result, err := db.Exec(`
		DELETE FROM shares
		WHERE id < (
			SELECT MIN(id) FROM (
				SELECT id FROM shares
				ORDER BY id DESC
				LIMIT ?
			)
		)`,
		windowSize*2)
	if err != nil {
		return 0, fmt.Errorf("failed to cleanup shares: %w", err)
	}

	deleted, err := result.RowsAffected()
	if err != nil {
		log.Printf("Warning: could not get deleted count: %v", err)
		return 0, nil
	}
	return deleted, nil
}

// Additional compatibility functions
func SaveBlock(height int64, hash, minerID string, reward float64) error {
	return SaveBlockDBWithSolo(minerID, height, hash, reward, false)
}

func SavePayoutAtomic(minerID string, blockHeight int64, amount float64, blockHash string) error {
	return SavePayoutAtomicWithSolo(minerID, blockHeight, amount, blockHash, false)
}

func GetMinerSettingsDB(address string) (*MinerSettings, error) {
	dbMu.RLock()
	defer dbMu.RUnlock()

	if db == nil {
		return nil, ErrDatabaseNotInitialized
	}

	var settings MinerSettings
	var solo int
	err := db.QueryRow(`
		SELECT address, solo_mining, manual_diff, COALESCE(address_1175, '')
		FROM miners WHERE address = ?`,
		address).Scan(&settings.Address, &solo, &settings.ManualDiff, &settings.Address1175)

	if err != nil {
		return nil, err
	}
	settings.SoloMining = solo == 1
	return &settings, nil
}

func GetMinerBlocksDB(minerID string) []MinerBlock {
	dbMu.RLock()
	defer dbMu.RUnlock()

	if db == nil {
		return GetMinerBlocks(minerID)
	}

	rows, err := db.Query(`
		SELECT b.height, b.hash, b.reward, b.created_at, b.status, COALESCE(p.txid, '')
		FROM blocks b
		LEFT JOIN payouts p ON p.block_height = b.height AND p.miner_address = b.miner_address
		WHERE b.miner_address = ? ORDER BY b.height DESC LIMIT 100`,
		minerID)
	if err != nil {
		log.Printf("Warning: failed to query miner blocks: %v", err)
		return []MinerBlock{}
	}
	defer rows.Close()

	var blocks []MinerBlock
	for rows.Next() {
		var b MinerBlock
		var status, payoutTxid string
		if err := rows.Scan(&b.Height, &b.Hash, &b.Reward, &b.Time, &status, &payoutTxid); err != nil {
			log.Printf("Warning: failed to scan miner block: %v", err)
			continue
		}
		b.MinerID = minerID
		b.Confirmed = (status == "confirmed")
		b.PayoutTxid = payoutTxid
		blocks = append(blocks, b)
	}

	if err := rows.Err(); err != nil {
		log.Printf("Warning: error iterating miner blocks: %v", err)
	}

	return blocks
}

func GetMinerBlockContributionsDB(minerID string) []MinerBlockContribution {
	dbMu.RLock()
	defer dbMu.RUnlock()

	if db == nil {
		return []MinerBlockContribution{}
	}

	rows, err := db.Query(`
		SELECT p.block_height, p.amount, COALESCE(b.reward, 0), strftime('%s', substr(p.created_at, 1, 19)),
			CASE WHEN p.txid IS NOT NULL AND p.txid != '' THEN 1 ELSE 0 END as is_paid
		FROM payouts p
		JOIN blocks b ON b.height = p.block_height
		WHERE p.miner_address = ? AND b.is_solo = 0
		ORDER BY p.block_height DESC LIMIT 50`,
		minerID)
	if err != nil {
		log.Printf("Warning: failed to query block contributions: %v", err)
		return []MinerBlockContribution{}
	}
	defer rows.Close()

	var contributions []MinerBlockContribution
	for rows.Next() {
		var c MinerBlockContribution
		var blockReward float64
		var isPaid int
		if err := rows.Scan(&c.Height, &c.Amount, &blockReward, &c.Time, &isPaid); err != nil {
			log.Printf("Warning: failed to scan block contribution: %v", err)
			continue
		}
		c.IsPaid = isPaid == 1
		// Against the block's OWN reward, read from the row beside it. This used to be
		// (amount / 49.5) * 100 -- a 1% fee frozen into a constant, on a public JSON route,
		// in an app that takes no fee, and wrong for any subsidy other than 50 so it would
		// have drifted at every halving.
		if blockReward > 0 {
			c.SharePct = (c.Amount / blockReward) * 100
		}
		contributions = append(contributions, c)
	}

	if err := rows.Err(); err != nil {
		log.Printf("Warning: error iterating block contributions: %v", err)
	}

	return contributions
}

func LoadAllPendingPayouts() {
	// Not needed for SQLite - query directly
}

// GetSettingsPinHash — sqlite variant (dev). See db.go for semantics.
func GetSettingsPinHash(address string) (string, error) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return "", ErrDatabaseNotInitialized
	}
	var h string
	err := db.QueryRow(`SELECT COALESCE(settings_pin_hash, '') FROM miners WHERE address = ?`, address).Scan(&h)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return h, err
}

// SetSettingsPinHash — sqlite variant (dev). See db.go for semantics.
func SetSettingsPinHash(address, hash string) error {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return ErrDatabaseNotInitialized
	}
	_, err := db.Exec(`
		INSERT INTO miners (address, settings_pin_hash, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(address) DO UPDATE SET settings_pin_hash = excluded.settings_pin_hash, updated_at = CURRENT_TIMESTAMP`,
		address, hash)
	return err
}

// ---------------------------------------------------------------------------
// Symbols the postgres backend (db.go, //go:build !sqlite) has and this file did
// not, which is why `go build -tags sqlite` -- i.e. every Windows build -- failed
// to link. Semantics match postgres; dialect and, for the reservation, the
// concurrency strategy differ. See ReserveMaturePayouts.
// ---------------------------------------------------------------------------

// PayoutRow is a single mature, reserved payout ledger row.
type PayoutRow struct {
	ID          int64
	Amount      float64
	BlockHeight int64
}

// idPlaceholders renders "?,?,?" plus the matching args for an IN clause.
// Postgres can say `id = ANY($1)` with pq.Array; SQLite has no array type.
func idPlaceholders(ids []int64) (string, []interface{}) {
	ph := make([]byte, 0, len(ids)*2)
	args := make([]interface{}, 0, len(ids))
	for i, id := range ids {
		if i > 0 {
			ph = append(ph, ',')
		}
		ph = append(ph, '?')
		args = append(args, id)
	}
	return string(ph), args
}

// NO PRODUCTION CALLER, AND THAT IS THE POINT. Every reward is paid by the block's own
// coinbase, so nothing sweeps the ledger: the sender was removed and no_wallet_send_test.go
// forbids its return. What survives here is the instrument that proves the property --
// TestSoloPayoutIsNeverReservableForSending reserves against a real database and asserts a
// solo payout is never handed back. Delete this and the proof goes with it; wire it to a
// sender and the guard test fails, which is the intended outcome.
//
// ReserveMaturePayouts atomically reserves every mature unpaid payout row for a
// miner, stamps them with a unique placeholder txid, and returns them ordered.
// Reserved rows carry a non-empty txid so GetReadyPayoutsDB no longer selects them --
// this is what stops the auto processor and a concurrent manual request from both
// paying the same balance.
//
// DIFFERS FROM POSTGRES BY DESIGN. Postgres reserves with SELECT ... FOR UPDATE then
// UPDATEs the same predicate in one transaction. SQLite has no row locks, and a plain
// BEGIN is DEFERRED -- it takes no write lock until the first write -- so a literal
// translation would leave a window where another writer claims the same rows between
// the SELECT and the UPDATE, and both callers would pay them. A faithful-looking port
// would have been the dangerous one.
//
// The order is inverted instead: CLAIM first with a single UPDATE (atomic on its own),
// then read back exactly the rows carrying our unique pendingID. Nothing else can
// produce that ID, so no row can be claimed twice and no lock is needed.
//
// A crash between the two steps leaves rows reserved with no in-flight payment: the
// same state postgres reaches if it dies after commit, and the safe direction --
// unpaid, never double-paid.
func ReserveMaturePayouts(minerID string, matureHeight int64) (pendingID string, rows []PayoutRow, total float64, err error) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return "", nil, 0, ErrDatabaseNotInitialized
	}
	ctx, cancel := context.WithTimeout(context.Background(), DBTimeout)
	defer cancel()

	suffix := minerID
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	pendingID = fmt.Sprintf("pending_%d_%s", time.Now().UnixNano(), suffix)

	res, err := db.ExecContext(ctx, `
		UPDATE payouts SET txid = ?, status = 'processing', paid_at = ?
		WHERE miner_address = ? AND (txid IS NULL OR txid = '') AND block_height <= ?`,
		pendingID, sqliteNow(), minerID, matureHeight)
	if err != nil {
		return "", nil, 0, err
	}
	if n, e := res.RowsAffected(); e == nil && n == 0 {
		return "", nil, 0, nil
	}

	r, err := db.QueryContext(ctx, `
		SELECT id, amount, block_height FROM payouts
		WHERE txid = ?
		ORDER BY block_height, id`, pendingID)
	if err != nil {
		// The rows ARE claimed but we cannot report them. Release rather than strand a
		// miner's balance behind a pendingID nobody holds.
		_, _ = db.ExecContext(ctx, `
			UPDATE payouts SET txid = NULL, status = 'pending', paid_at = NULL
			WHERE txid = ?`, pendingID)
		return "", nil, 0, err
	}
	defer r.Close()
	for r.Next() {
		var pr PayoutRow
		if err := r.Scan(&pr.ID, &pr.Amount, &pr.BlockHeight); err != nil {
			return "", nil, 0, err
		}
		rows = append(rows, pr)
		total += pr.Amount
	}
	if err := r.Err(); err != nil {
		return "", nil, 0, err
	}
	return pendingID, rows, total, nil
}

// RevertPayoutRows releases the given rows back to unpaid. Only ever used for rows
// reserved but definitively NOT broadcast.
func RevertPayoutRows(ids []int64) error {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return ErrDatabaseNotInitialized
	}
	if len(ids) == 0 {
		return nil
	}
	ph, args := idPlaceholders(ids)
	_, err := db.Exec(`
		UPDATE payouts SET txid = NULL, status = 'pending', paid_at = NULL
		WHERE id IN (`+ph+`)`, args...)
	return err
}

// FinalizePayoutRows stamps the given payout rows with the real broadcast txid, so the
// amount sent on-chain would always equal the amount marked paid in the ledger. Reached
// only from the reservation tests -- see the note on ReserveMaturePayouts.
func FinalizePayoutRows(ids []int64, actualTxid string) error {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return ErrDatabaseNotInitialized
	}
	if len(ids) == 0 {
		return nil
	}
	ph, args := idPlaceholders(ids)

	stampArgs := append([]interface{}{actualTxid}, args...)
	if _, err := db.Exec(`
		UPDATE payouts SET txid = ?, confirmed = 1, status = 'paid'
		WHERE id IN (`+ph+`)`, stampArgs...); err != nil {
		return err
	}

	// No cached-balance refresh: the balance the dashboard shows is summed from payouts on
	// read (GetMinerBalanceDB), so there is nothing here to keep in step. The columns it used
	// to write were never declared in the Postgres schema, so that write had always failed
	// silently, and nothing has ever read them in either dialect.
	return nil
}

// GetRecordedBlockHash returns the block hash this pool recorded for the given height.
// ok is false if no block is recorded there, in which case the caller must not make an
// orphan decision.
func GetRecordedBlockHash(height int64) (string, bool) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return "", false
	}
	var hash string
	err := db.QueryRow(`SELECT hash FROM blocks WHERE height = ?`, height).Scan(&hash)
	if err != nil || hash == "" {
		return "", false
	}
	return hash, true
}

// GetUnpaidMatureHeights returns the distinct block heights that still have unpaid
// mature payout rows, bounded to [minHeight, matureHeight].
func GetUnpaidMatureHeights(matureHeight, minHeight int64) ([]int64, error) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return nil, ErrDatabaseNotInitialized
	}
	rows, err := db.Query(`
		SELECT DISTINCT block_height FROM payouts
		WHERE (txid IS NULL OR txid = '')
		  AND block_height <= ? AND block_height >= ?
		ORDER BY block_height`, matureHeight, minHeight)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var heights []int64
	for rows.Next() {
		var h int64
		if err := rows.Scan(&h); err != nil {
			continue
		}
		heights = append(heights, h)
	}
	return heights, rows.Err()
}

// VoidOrphanedPayouts marks every unpaid payout row at the given height as orphaned so
// it is permanently excluded from payout selection: the pool never received that
// coinbase and must not pay miners for it. Returns rows voided and amount voided.
func VoidOrphanedPayouts(height int64) (int64, float64, error) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return 0, 0, ErrDatabaseNotInitialized
	}
	var amount float64
	_ = db.QueryRow(`SELECT COALESCE(SUM(amount),0) FROM payouts
		WHERE block_height = ? AND (txid IS NULL OR txid = '')`, height).Scan(&amount)
	res, err := db.Exec(`
		UPDATE payouts SET txid = 'orphaned', status = 'orphaned', paid_at = ?
		WHERE block_height = ? AND (txid IS NULL OR txid = '')`, sqliteNow(), height)
	if err != nil {
		return 0, 0, err
	}
	db.Exec(`UPDATE blocks SET status = 'orphaned' WHERE height = ?`, height)
	n, _ := res.RowsAffected()
	return n, amount, nil
}

// GetPoolConfig returns the single-row dashboard-managed pool configuration
// (pool_config id=1). A missing row yields empty strings and a nil error. tag is "" when no tag
// was chosen, the old default included (chosenCoinbaseTag).
func GetPoolConfig() (poolAddr, payout1175, tag string, err error) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return "", "", "", ErrDatabaseNotInitialized
	}
	row := db.QueryRow(`SELECT COALESCE(pool_address,''), COALESCE(payout_address_1175,''), COALESCE(coinbase_tag,'') FROM pool_config WHERE id = 1`)
	err = row.Scan(&poolAddr, &payout1175, &tag)
	if err == sql.ErrNoRows {
		return "", "", "", nil
	}
	if err != nil {
		return "", "", "", err
	}
	return poolAddr, payout1175, chosenCoinbaseTag(tag), nil
}

// SavePoolConfig upserts the single-row pool configuration (id=1). Empty strings are
// stored verbatim (an empty pool_address means "not configured -- mining paused").
func SavePoolConfig(poolAddr, payout1175, tag string) error {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return ErrDatabaseNotInitialized
	}
	_, err := db.Exec(`
		INSERT INTO pool_config (id, pool_address, payout_address_1175, coinbase_tag, updated_at)
		VALUES (1, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE
		SET pool_address = excluded.pool_address,
		    payout_address_1175 = excluded.payout_address_1175,
		    coinbase_tag = excluded.coinbase_tag,
		    updated_at = excluded.updated_at`,
		poolAddr, payout1175, tag, sqliteNow())
	return err
}

// InitDBWithRetry calls InitDB, retrying transient failures up to `attempts` times,
// `delay` apart. A briefly-unready database must not permanently disable DB-backed
// features for the whole process lifetime.
func InitDBWithRetry(connStr string, attempts int, delay time.Duration) error {
	if attempts < 1 {
		attempts = 1
	}
	var err error
	for i := 0; i < attempts; i++ {
		if err = InitDB(connStr); err == nil {
			return nil
		}
		if i < attempts-1 {
			log.Printf("DB init attempt %d/%d failed (%v); retrying in %s", i+1, attempts, err, delay)
			time.Sleep(delay)
		}
	}
	return err
}

// PPLNSShare represents a miner's share contribution in the PPLNS window
type PPLNSShare struct {
	MinerAddress string
	TotalWork    float64
}

// blockRowExecer is satisfied by both *sql.DB and *sql.Tx.
type blockRowExecer interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
}

// recordBlockRowAt records a block found at foundAt, idempotently and reorg-aware. blocks
// has UNIQUE(height), so if a DIFFERENT block already occupies this height it was reorged
// out (only possible while immature, since the reorg cap is far below coinbase
// maturity) and we replace it with the block the node just accepted as canonical. A
// re-record of the same hash is a no-op.
func recordBlockRowAt(ex blockRowExecer, height int64, hash, miner string, reward float64, isSolo bool, foundAt time.Time) error {
	solo := 0
	if isSolo {
		solo = 1
	}
	now := sqliteNow()
	found := SQLiteTime(foundAt)
	// Positional placeholders: postgres reuses $2 for hash in both SET and WHERE, so
	// hash is passed twice here.
	res, err := ex.Exec(`
		UPDATE blocks
		SET hash = ?, miner_address = ?, reward = ?, is_solo = ?, status = 'pending', created_at = ?
		WHERE height = ? AND hash <> ?`,
		hash, miner, reward, solo, found, height, hash)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		// Superseded a reorged-out block at this height. The orphan reconciler keys on
		// the now-overwritten recorded hash, so it can no longer void the prior block's
		// distribution. Void every still-unpaid payout row at this height HERE, before
		// the new distribution is credited: a contributor from the orphaned block who is
		// not in the new distribution stays orphaned/unpayable, while overlapping
		// contributors are re-credited by the payout upsert. Safe against double-pay
		// because an orphaned block is always immature, so these rows were never paid.
		_, err = ex.Exec(`
			UPDATE payouts SET status = 'orphaned', txid = 'orphaned', paid_at = ?
			WHERE block_height = ? AND (txid IS NULL OR txid = '')`, now, height)
		return err
	}
	_, err = ex.Exec(`
		INSERT INTO blocks (height, hash, miner_address, reward, is_solo, status, created_at)
		VALUES (?, ?, ?, ?, ?, 'pending', ?)
		ON CONFLICT DO NOTHING`,
		height, hash, miner, reward, solo, found)
	return err
}

// PendingSoloHeights returns the heights of still-pending solo blocks within
// [minHeight, matureHeight]. Solo blocks skip the payout-row orphan reconciler (their
// coinbase-direct payout row is already 'paid'), so they need their own pass.
func PendingSoloHeights(matureHeight, minHeight int64) ([]int64, error) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return nil, ErrDatabaseNotInitialized
	}
	rows, err := db.Query(`
		SELECT height FROM blocks
		WHERE is_solo = 1 AND status = 'pending'
		  AND height <= ? AND height >= ?
		ORDER BY height`, matureHeight, minHeight)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var heights []int64
	for rows.Next() {
		var h int64
		if err := rows.Scan(&h); err != nil {
			continue
		}
		heights = append(heights, h)
	}
	return heights, rows.Err()
}

// OrphanSoloBlock marks a solo block (and its coinbase-direct payout row) orphaned when
// the block recorded at that height is no longer on the active chain. Fund-safe: solo
// rewards are coinbase-direct, so nothing was ever sent -- this only stops a reorged-out
// block from overstating confirmed earnings. Atomic. Returns block rows voided.
func OrphanSoloBlock(height int64) (int64, error) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return 0, ErrDatabaseNotInitialized
	}
	ctx, cancel := context.WithTimeout(context.Background(), DBTimeout)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE blocks SET status = 'orphaned'
		WHERE height = ? AND is_solo = 1 AND status = 'pending'`, height)
	if err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE payouts SET status = 'orphaned', txid = 'orphaned', confirmed = 0, paid_at = ?
		WHERE block_height = ? AND txid = 'coinbase-direct'`, sqliteNow(), height); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ConfirmSoloBlock marks a single pending solo block confirmed. Called only after the
// stratum has verified the recorded block is the one on the active chain at that height.
func ConfirmSoloBlock(height int64) error {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return ErrDatabaseNotInitialized
	}
	_, err := db.Exec(`UPDATE blocks SET status = 'confirmed', confirmed_at = ?
		WHERE height = ? AND is_solo = 1 AND status = 'pending'`, sqliteNow(), height)
	return err
}

// SaveSoloBlockCoinbaseDirect records a solo-found block and settles its reward DB-only.
// In SOLO mode the block reward is paid on-chain DIRECTLY by the coinbase to
// POOL_ADDRESS, so there is NO secondary sendtoaddress (that path targets a nonexistent
// wallet, would fail forever, and risks a double-pay). The payout row is therefore
// inserted already settled (txid='coinbase-direct', status='paid'), which the payout
// processor -- selecting only rows WHERE txid IS NULL OR txid=” -- never touches.
// Reorg-aware via recordBlockRow.
func SaveSoloBlockCoinbaseDirect(minerID string, blockHeight int64, amount float64, blockHash string) error {
	return SaveSoloBlockCoinbaseDirectAt(minerID, blockHeight, amount, blockHash, time.Now())
}

// SaveSoloBlockCoinbaseDirectAt is SaveSoloBlockCoinbaseDirect for a block found at foundAt: a
// record retried after a database outage lists the block, and its payout, at the time it was found.
func SaveSoloBlockCoinbaseDirectAt(minerID string, blockHeight int64, amount float64, blockHash string, foundAt time.Time) error {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return ErrDatabaseNotInitialized
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	if err = recordBlockRowAt(tx, blockHeight, blockHash, minerID, amount, true, foundAt); err != nil {
		return fmt.Errorf("failed to insert solo block: %w", err)
	}

	// ON CONFLICT only overwrites an unpaid/orphaned row (never a genuinely paid one),
	// so re-records are idempotent. Requires uq_payouts_miner_height.
	found := SQLiteTime(foundAt)
	_, err = tx.Exec(`
		INSERT INTO payouts (miner_address, block_height, amount, confirmed, txid, status, created_at, paid_at)
		VALUES (?, ?, ?, 1, 'coinbase-direct', 'paid', ?, ?)
		ON CONFLICT (miner_address, block_height) DO UPDATE
		SET amount = excluded.amount, confirmed = 1, txid = 'coinbase-direct', status = 'paid', paid_at = excluded.paid_at
		WHERE payouts.txid IS NULL OR payouts.txid = '' OR payouts.status = 'orphaned'`,
		minerID, blockHeight, amount, found, found)
	if err != nil {
		return fmt.Errorf("failed to insert solo coinbase-direct payout: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit solo block: %w", err)
	}
	log.Printf("✅ Solo block %d recorded; %.8f BCH2 paid on-chain by coinbase to your configured payout address (settled DB-only)", blockHeight, amount)
	return nil
}

// SOLO_EARNINGS_QUERY groups a solo miner's own blocks by whether they have matured.
// Orphaned blocks are excluded: they paid nothing.
const SOLO_EARNINGS_QUERY = `
	SELECT height <= ? AS matured, COALESCE(SUM(reward), 0)
	FROM blocks
	WHERE miner_address = ? AND is_solo = 1 AND COALESCE(status,'') <> 'orphaned'
	GROUP BY height <= ?`

// SoloEarnings splits what a solo miner has actually mined into the part that is spendable
// and the part still maturing.
//
// The balance card used to be fed from the pool-style ready-to-pay query, which selects only
// payout rows with a NULL/empty txid. A solo payout is settled txid='coinbase-direct' the
// moment it is recorded, so that query can never return one and BOTH numbers were
// structurally always 0.00 -- while the card asserted, underneath, that 0.00 BCH2 was
// "waiting 100 confirms" with a hundred genuinely maturing.
//
// This reads the blocks the miner actually found instead, which is the only thing that means
// anything in solo: the coinbase already paid them, so the question is not what is owed but
// what has matured.
func SoloEarnings(minerID string, currentHeight int64) (mature, immature float64) {
	mature, immature, _ = SoloEarningsErr(minerID, currentHeight)
	return mature, immature
}

// SoloEarningsErr is SoloEarnings, saying when the database did not answer: the dashboard must
// not show that as nothing earned.
func SoloEarningsErr(minerID string, currentHeight int64) (mature, immature float64, err error) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return 0, 0, ErrDatabaseNotInitialized
	}
	ctx, cancel := context.WithTimeout(context.Background(), DashboardReadTimeout)
	defer cancel()
	matureBelow := currentHeight - COINBASE_MATURITY
	rows, err := db.QueryContext(ctx, SOLO_EARNINGS_QUERY, matureBelow, minerID, matureBelow)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var isMature bool
		var total float64
		if err := rows.Scan(&isMature, &total); err != nil {
			return 0, 0, err
		}
		if isMature {
			mature = total
		} else {
			immature = total
		}
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	return mature, immature, nil
}

const GET_1175_HASH_QUERY = `SELECT COALESCE(hash, '') FROM blocks_1175 WHERE height = ?`

// Get1175BlockHashAtHeight returns the aux block hash currently recorded at a height.
// Used to detect two candidates for the same height before deciding which one counts --
// a decision that belongs to the chain, not to whichever arrived second.
func Get1175BlockHashAtHeight(height int64) (string, bool) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return "", false
	}
	var hash string
	if err := db.QueryRow(GET_1175_HASH_QUERY, height).Scan(&hash); err != nil {
		return "", false
	}
	return hash, hash != ""
}

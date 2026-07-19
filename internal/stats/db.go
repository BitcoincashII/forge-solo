//go:build !sqlite

package stats

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/lib/pq"
)

var (
	db   *sql.DB
	dbMu sync.RWMutex // CRITICAL FIX: Mutex protection for global db pointer
)

// ErrDatabaseNotInitialized is returned when database operations are attempted without initialization
var ErrDatabaseNotInitialized = fmt.Errorf("database not initialized")

// GetDBConnStr returns the database connection string from environment variables
func GetDBConnStr() string {
	host := os.Getenv("DB_HOST")
	if host == "" {
		host = "localhost"
	}
	port := os.Getenv("DB_PORT")
	if port == "" {
		port = "5432"
	}
	user := os.Getenv("DB_USER")
	if user == "" {
		user = "forge"
	}
	password := os.Getenv("DB_PASSWORD")
	if password == "" {
		password = os.Getenv("FORGE_DB_PASSWORD")
	}
	dbname := os.Getenv("DB_NAME")
	if dbname == "" {
		dbname = "forgepool"
	}
	sslmode := os.Getenv("DB_SSLMODE")
	if sslmode == "" {
		sslmode = "disable"
	}

	return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		host, port, user, password, dbname, sslmode)
}

// corePostgresSchema is the canonical core schema for the Postgres (!sqlite) build.
// The sqlite build creates these tables in db_sqlite.go; the Postgres path must create
// them here so a fresh install is never schema-less (the mounted init-db.sql only
// guarantees the database/extension exist). Idempotent (CREATE ... IF NOT EXISTS), safe
// to run on every start; TimescaleDB features degrade gracefully on plain Postgres. Kept
// in sync with database/schema.sql and init-db.sql. Includes blocks.is_solo, which
// recordBlockRow and the solo/pool block queries require.
const corePostgresSchema = `
DO $$
BEGIN
    CREATE EXTENSION IF NOT EXISTS timescaledb;
EXCEPTION WHEN OTHERS THEN
    RAISE NOTICE 'TimescaleDB not available, using standard PostgreSQL';
END $$;

CREATE TABLE IF NOT EXISTS blocks (
    id BIGSERIAL PRIMARY KEY,
    height BIGINT NOT NULL UNIQUE,
    hash VARCHAR(64) NOT NULL UNIQUE,
    miner_address VARCHAR(255) NOT NULL,
    reward DECIMAL(20, 8) NOT NULL DEFAULT 50.0,
    difficulty DECIMAL(30, 8) DEFAULT 0,
    status VARCHAR(20) DEFAULT 'confirmed',
    confirmations INT DEFAULT 0,
    is_solo BOOLEAN DEFAULT FALSE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    confirmed_at TIMESTAMP WITH TIME ZONE
);

CREATE TABLE IF NOT EXISTS payouts (
    id BIGSERIAL PRIMARY KEY,
    miner_address VARCHAR(255) NOT NULL,
    block_height BIGINT NOT NULL,
    amount DECIMAL(20, 8) NOT NULL,
    confirmed BOOLEAN DEFAULT FALSE,
    txid VARCHAR(128),
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    paid_at TIMESTAMP WITH TIME ZONE,
    UNIQUE(miner_address, block_height)
);

CREATE TABLE IF NOT EXISTS miners (
    id BIGSERIAL PRIMARY KEY,
    address VARCHAR(255) UNIQUE NOT NULL,
    solo_mining BOOLEAN DEFAULT FALSE,
    manual_diff DECIMAL(20, 8) DEFAULT 0,
    min_payout DECIMAL(20, 8) DEFAULT 5.0,
    address_1175 TEXT,
    settings_pin_hash TEXT,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS shares (
    id BIGSERIAL,
    time TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    miner_address VARCHAR(255) NOT NULL,
    worker_name VARCHAR(255) NOT NULL,
    job_id VARCHAR(64),
    difficulty DECIMAL(20, 8) NOT NULL,
    is_valid BOOLEAN NOT NULL DEFAULT TRUE,
    is_block BOOLEAN DEFAULT FALSE,
    is_solo BOOLEAN DEFAULT FALSE,
    block_hash VARCHAR(64),
    PRIMARY KEY (id, time)
);

DO $$
BEGIN
    PERFORM create_hypertable('shares', 'time', chunk_time_interval => INTERVAL '1 hour', if_not_exists => TRUE);
EXCEPTION WHEN OTHERS THEN
    RAISE NOTICE 'Could not create hypertable for shares';
END $$;

CREATE TABLE IF NOT EXISTS pool_stats (
    time TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW() PRIMARY KEY,
    hashrate DECIMAL(30, 2) NOT NULL DEFAULT 0,
    workers INT NOT NULL DEFAULT 0,
    miners_online INT NOT NULL DEFAULT 0,
    valid_shares BIGINT NOT NULL DEFAULT 0,
    invalid_shares BIGINT NOT NULL DEFAULT 0,
    network_difficulty DECIMAL(30, 8) DEFAULT 0,
    block_height BIGINT DEFAULT 0
);

CREATE TABLE IF NOT EXISTS pool_config (
    id INT PRIMARY KEY DEFAULT 1,
    pool_address TEXT DEFAULT '',
    payout_address_1175 TEXT DEFAULT '',
    coinbase_tag TEXT DEFAULT '',
    min_payout DOUBLE PRECISION DEFAULT 1,
    updated_at TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_blocks_height ON blocks(height);
CREATE INDEX IF NOT EXISTS idx_blocks_miner ON blocks(miner_address);
CREATE INDEX IF NOT EXISTS idx_payouts_miner ON payouts(miner_address);
CREATE INDEX IF NOT EXISTS idx_payouts_unpaid ON payouts(miner_address) WHERE txid IS NULL OR txid = '';
CREATE INDEX IF NOT EXISTS idx_payouts_block ON payouts(block_height);
CREATE INDEX IF NOT EXISTS idx_shares_miner_time ON shares(miner_address, time DESC);
CREATE INDEX IF NOT EXISTS idx_miners_address ON miners(address);
`

func InitDB(connStr string) error {
	dbMu.Lock()
	defer dbMu.Unlock()

	var err error
	db, err = sql.Open("postgres", connStr)
	if err != nil {
		return err
	}

	// Set connection pool settings from environment or use sensible defaults
	maxOpen := 100 // Match config.yaml default
	maxIdle := 25
	if envMaxOpen := os.Getenv("DB_MAX_OPEN_CONNS"); envMaxOpen != "" {
		if v, err := strconv.Atoi(envMaxOpen); err == nil && v > 0 {
			maxOpen = v
		}
	}
	if envMaxIdle := os.Getenv("DB_MAX_IDLE_CONNS"); envMaxIdle != "" {
		if v, err := strconv.Atoi(envMaxIdle); err == nil && v > 0 {
			maxIdle = v
		}
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err = db.Ping(); err != nil {
		db.Close()
		db = nil
		return err
	}

	// Create the core schema (blocks/payouts/miners/shares/pool_stats). The !sqlite
	// build must create these itself; without this a fresh install is schema-less and
	// every block/share/payout write and read fails.
	if _, sErr := db.Exec(corePostgresSchema); sErr != nil {
		db.Close()
		db = nil
		return fmt.Errorf("core schema init failed: %w", sErr)
	}
	// Defensive additive migration for any pre-existing blocks table created before
	// is_solo existed (recordBlockRow / GetMinerSoloBlocksDB require it).
	if _, mErr := db.Exec(`ALTER TABLE blocks ADD COLUMN IF NOT EXISTS is_solo BOOLEAN DEFAULT FALSE`); mErr != nil {
		log.Printf("Warning: blocks.is_solo migration: %v", mErr)
	}

	// Idempotent additive migration: per-miner 1175 merge-mining payout address.
	if _, mErr := db.Exec(`ALTER TABLE miners ADD COLUMN IF NOT EXISTS address_1175 TEXT`); mErr != nil {
		log.Printf("Warning: address_1175 column migration: %v", mErr)
	}
	// Idempotent additive migration: optional per-miner settings PIN (bcrypt hash) —
	// proof-of-control for changing the redirectable 1175 payout address without a
	// stratum password (rental-friendly).
	if _, mErr := db.Exec(`ALTER TABLE miners ADD COLUMN IF NOT EXISTS settings_pin_hash TEXT`); mErr != nil {
		log.Printf("Warning: settings_pin_hash column migration: %v", mErr)
	}
	// Idempotent additive migration: payouts.status lifecycle column. The core schema and
	// init-db.sql predate it, yet several payout upserts/queries (and the solo coinbase-direct
	// settle) reference payouts.status, so a fresh Postgres install must have it.
	if _, mErr := db.Exec(`ALTER TABLE payouts ADD COLUMN IF NOT EXISTS status VARCHAR(20) DEFAULT 'pending'`); mErr != nil {
		log.Printf("Warning: payouts.status column migration: %v", mErr)
	}
	// 1175 merge-mining payout ledger tables.
	Init1175Schema()

	log.Printf("✅ Connected to PostgreSQL (pool: %d open, %d idle)", maxOpen, maxIdle)
	return nil
}

func CloseDB() {
	dbMu.Lock()
	defer dbMu.Unlock()
	if db != nil {
		if err := db.Close(); err != nil {
			log.Printf("Warning: error closing database: %v", err)
		}
		db = nil
	}
}

// IsDBConnected returns true if the database connection is active
func IsDBConnected() bool {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return false
	}
	return db.Ping() == nil
}

// InitDBWithRetry calls InitDB, retrying transient failures (e.g. a Postgres that is
// still starting up) up to `attempts` times, `delay` apart. A briefly-unready database
// must not permanently disable DB-backed features for the whole process lifetime.
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

// GetPoolConfig returns the single-row dashboard-managed pool configuration
// (pool_config id=1). A missing row yields empty strings + min_payout 1 and a nil error.
func GetPoolConfig() (poolAddr, payout1175, tag string, minPayout float64, err error) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return "", "", "", 0, ErrDatabaseNotInitialized
	}
	row := db.QueryRow(`SELECT COALESCE(pool_address,''), COALESCE(payout_address_1175,''), COALESCE(coinbase_tag,''), COALESCE(min_payout,1) FROM pool_config WHERE id = 1`)
	err = row.Scan(&poolAddr, &payout1175, &tag, &minPayout)
	if err == sql.ErrNoRows {
		return "", "", "", 1, nil
	}
	if err != nil {
		return "", "", "", 0, err
	}
	return poolAddr, payout1175, tag, minPayout, nil
}

// SavePoolConfig upserts the single-row pool configuration (id=1). Empty strings are
// stored verbatim (an empty pool_address means "not configured — mining paused").
func SavePoolConfig(poolAddr, payout1175, tag string, minPayout float64) error {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return ErrDatabaseNotInitialized
	}
	_, err := db.Exec(`
		INSERT INTO pool_config (id, pool_address, payout_address_1175, coinbase_tag, min_payout, updated_at)
		VALUES (1, $1, $2, $3, $4, now())
		ON CONFLICT (id) DO UPDATE
		SET pool_address = EXCLUDED.pool_address,
		    payout_address_1175 = EXCLUDED.payout_address_1175,
		    coinbase_tag = EXCLUDED.coinbase_tag,
		    min_payout = EXCLUDED.min_payout,
		    updated_at = now()`,
		poolAddr, payout1175, tag, minPayout)
	return err
}

// SaveSoloBlockCoinbaseDirect records a solo-found block and settles its reward DB-only.
// In SOLO mode the block reward is paid on-chain DIRECTLY by the coinbase to POOL_ADDRESS,
// so there is NO secondary sendtoaddress (that path targets a nonexistent wallet, would
// fail forever, and risks a double-pay). The payout row is therefore inserted already
// settled (txid='coinbase-direct', status='paid'), which the BCH2 payout processor —
// selecting only rows WHERE txid IS NULL OR txid='' — never touches. Mirrors
// Settle1175ByCoinbase; reorg-aware via recordBlockRow.
func SaveSoloBlockCoinbaseDirect(minerID string, blockHeight int64, amount float64, blockHash string) error {
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

	// Record the block (reorg-aware: a superseded height voids its prior unpaid rows).
	if err = recordBlockRow(tx, blockHeight, blockHash, minerID, amount, true); err != nil {
		return fmt.Errorf("failed to insert solo block: %w", err)
	}

	// Insert the payout row already settled as coinbase-direct. ON CONFLICT only overwrites
	// an unpaid/orphaned row (never a genuinely paid one), so re-records are idempotent.
	_, err = tx.Exec(`
		INSERT INTO payouts (miner_address, block_height, amount, confirmed, txid, status, created_at, paid_at)
		VALUES ($1, $2, $3, true, 'coinbase-direct', 'paid', NOW(), NOW())
		ON CONFLICT (miner_address, block_height) DO UPDATE
		SET amount = EXCLUDED.amount, confirmed = true, txid = 'coinbase-direct', status = 'paid', paid_at = NOW()
		WHERE payouts.txid IS NULL OR payouts.txid = '' OR payouts.status = 'orphaned'`,
		minerID, blockHeight, amount)
	if err != nil {
		return fmt.Errorf("failed to insert solo coinbase-direct payout: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit solo block: %w", err)
	}
	log.Printf("✅ Solo block %d recorded; %.8f BCH2 paid on-chain by coinbase to POOL_ADDRESS (settled DB-only)", blockHeight, amount)
	return nil
}

// ConfirmMatureSoloBlocks marks pending solo BCH2 blocks at height <= confirmHeight as
// confirmed. The stratum passes a confirmHeight BELOW the reorg-plausible band, so these
// blocks are buried too deep to reorganize and are safely on the active chain with no RPC
// check. Blocks still inside the reorg band are reconciled by reconcileSoloBlocks (an
// active-chain hash check) which confirms or orphans each, so an orphaned solo block is
// never blindly confirmed (which would overstate earnings). Rewards are already delivered
// on-chain by the coinbase; this is purely a status/display transition.
func ConfirmMatureSoloBlocks(confirmHeight int64) error {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return ErrDatabaseNotInitialized
	}
	_, err := db.Exec(`UPDATE blocks SET status = 'confirmed', confirmed_at = NOW()
		WHERE is_solo = true AND status = 'pending' AND height <= $1`, confirmHeight)
	return err
}

// PendingSoloHeights returns the heights of still-pending solo blocks within
// [minHeight, matureHeight]. The stratum active-chain-checks each one before it is
// confirmed: solo blocks skip the payout-row orphan reconciler (their coinbase-direct
// payout row is already 'paid'), so they need their own reconciliation pass.
func PendingSoloHeights(matureHeight, minHeight int64) ([]int64, error) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return nil, ErrDatabaseNotInitialized
	}
	rows, err := db.Query(`
		SELECT height FROM blocks
		WHERE is_solo = true AND status = 'pending'
		  AND height <= $1 AND height >= $2
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

// ConfirmSoloBlock marks a single pending solo block confirmed. Called only after the
// stratum has verified the recorded block is the one on the active chain at that height.
func ConfirmSoloBlock(height int64) error {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return ErrDatabaseNotInitialized
	}
	_, err := db.Exec(`UPDATE blocks SET status = 'confirmed', confirmed_at = NOW()
		WHERE height = $1 AND is_solo = true AND status = 'pending'`, height)
	return err
}

// OrphanSoloBlock marks a solo block (and its coinbase-direct payout row) orphaned when the
// block the pool recorded at that height is no longer on the active chain. Fund-safe: solo
// rewards are coinbase-direct, so nothing was ever sent — this only stops a reorged-out
// block from overstating confirmed earnings. Atomic. Returns the number of block rows voided.
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
		WHERE height = $1 AND is_solo = true AND status = 'pending'`, height)
	if err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE payouts SET status = 'orphaned', txid = 'orphaned', confirmed = false, paid_at = NOW()
		WHERE block_height = $1 AND txid = 'coinbase-direct'`, height); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// SavePayout saves a payout to the database
func SavePayout(minerID string, blockHeight int64, amount float64) error {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return ErrDatabaseNotInitialized // CRITICAL FIX: Return error instead of nil
	}

	_, err := db.Exec(`
		INSERT INTO payouts (miner_address, block_height, amount, confirmed, created_at)
		VALUES ($1, $2, $3, false, $4)
		ON CONFLICT (miner_address, block_height) DO UPDATE
		SET amount = EXCLUDED.amount, txid = NULL, status = 'pending', confirmed = false, paid_at = NULL, created_at = NOW()
		WHERE payouts.txid IS NULL OR payouts.txid = '' OR payouts.status = 'orphaned'`,
		minerID, blockHeight, amount, time.Now())
	return err
}

// blockRowExecer is satisfied by both *sql.DB and *sql.Tx.
type blockRowExecer interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
}

// recordBlockRow records a found block idempotently and reorg-aware. The blocks
// table has UNIQUE(height) and UNIQUE(hash), so a single ON CONFLICT cannot cover
// both. If a different block already occupies this height it was reorged out (only
// possible while immature, since the reorg cap is far below coinbase maturity), so
// we replace it with the block we just found, which the node accepted as canonical.
// A re-record of the same hash is a no-op.
func recordBlockRow(ex blockRowExecer, height int64, hash, miner string, reward float64, isSolo bool) error {
	res, err := ex.Exec(`
		UPDATE blocks
		SET hash = $2, miner_address = $3, reward = $4, is_solo = $5, status = 'pending', created_at = NOW()
		WHERE height = $1 AND hash <> $2`,
		height, hash, miner, reward, isSolo)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		// Superseded a reorged-out block at this height. The orphan reconciler keys on
		// the now-overwritten recorded hash, so it can no longer void the prior block's
		// distribution. Void every still-unpaid payout row at this height here, BEFORE
		// the new distribution is credited: a contributor from the orphaned block who is
		// not in the new distribution stays orphaned/unpayable, while overlapping
		// contributors are re-credited by the payout upsert. Safe against double-pay
		// because an orphaned block is always immature (reorg cap << coinbase maturity),
		// so these rows were never paid.
		_, err = ex.Exec(`
			UPDATE payouts SET status = 'orphaned', txid = 'orphaned', paid_at = NOW()
			WHERE block_height = $1 AND (txid IS NULL OR txid = '')`, height)
		return err
	}
	_, err = ex.Exec(`
		INSERT INTO blocks (height, hash, miner_address, reward, is_solo, status, created_at)
		VALUES ($1, $2, $3, $4, $5, 'pending', $6)
		ON CONFLICT DO NOTHING`,
		height, hash, miner, reward, isSolo, time.Now())
	return err
}

// SaveBlock saves a block to the database
func SaveBlock(height int64, hash, minerID string, reward float64) error {
	return SaveBlockDBWithSolo(minerID, height, hash, reward, false)
}

// SaveBlockDBWithSolo saves a block to the database with solo flag
func SaveBlockDBWithSolo(minerID string, height int64, hash string, reward float64, isSolo bool) error {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return ErrDatabaseNotInitialized // CRITICAL FIX: Return error instead of nil
	}

	return recordBlockRow(db, height, hash, minerID, reward, isSolo)
}

// SavePayoutAtomic saves both block and payout in a single transaction
// This prevents double-payout bugs and ensures consistency
func SavePayoutAtomic(minerID string, blockHeight int64, amount float64, blockHash string) error {
	return SavePayoutAtomicWithSolo(minerID, blockHeight, amount, blockHash, false)
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
	defer tx.Rollback() // Rollback if not committed

	// Record the block (reorg-aware: replaces a superseded row at this height).
	if err = recordBlockRow(tx, blockHeight, blockHash, minerID, 50.0, isSolo); err != nil {
		return fmt.Errorf("failed to insert block: %w", err)
	}

	// Insert or re-credit the payout. A re-mined block at a height whose prior
	// payout was orphaned (never paid -- orphans are always immature) is re-credited;
	// a row already paid (real txid) is never touched.
	_, err = tx.Exec(`
		INSERT INTO payouts (miner_address, block_height, amount, confirmed, created_at)
		VALUES ($1, $2, $3, false, $4)
		ON CONFLICT (miner_address, block_height) DO UPDATE
		SET amount = EXCLUDED.amount, txid = NULL, status = 'pending', confirmed = false, paid_at = NULL, created_at = NOW()
		WHERE payouts.txid IS NULL OR payouts.txid = '' OR payouts.status = 'orphaned'`,
		minerID, blockHeight, amount, time.Now())
	if err != nil {
		return fmt.Errorf("failed to insert payout: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	log.Printf("✅ Saved block %d and payout %.2f BCH2 for %s atomically", blockHeight, amount, minerID)
	return nil
}

// ProcessPayoutAtomic handles the entire payout process in a single transaction
// with proper row locking to prevent race conditions
func ProcessPayoutAtomic(minerID string, currentHeight int64, minPayout float64) (string, float64, error) {
	dbMu.RLock()
	defer dbMu.RUnlock()

	if db == nil {
		return "", 0, ErrDatabaseNotInitialized
	}

	// CRITICAL FIX: Validate minerID length before slicing
	if len(minerID) < 8 {
		return "", 0, fmt.Errorf("invalid miner ID: too short (minimum 8 characters)")
	}

	// Use context with timeout to prevent hung transactions
	ctx, cancel := context.WithTimeout(context.Background(), DBTimeout)
	defer cancel()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Lock and sum mature unpaid payouts for this miner
	var matureAmount float64
	matureHeight := currentHeight - COINBASE_MATURITY

	err = tx.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(amount), 0)
		FROM (
			SELECT amount FROM payouts
			WHERE miner_address = $1
			  AND (txid IS NULL OR txid = '')
			  AND block_height <= $2
			FOR UPDATE
		) AS locked_payouts`,
		minerID, matureHeight).Scan(&matureAmount)
	if err != nil {
		return "", 0, fmt.Errorf("failed to get mature balance: %w", err)
	}

	if matureAmount < minPayout {
		return "", 0, fmt.Errorf("insufficient mature balance: %.2f < %.2f", matureAmount, minPayout)
	}

	// Generate a placeholder txid that will be updated after actual send
	// CRITICAL FIX: Safe slice after validation
	pendingTxid := fmt.Sprintf("pending_%d_%s", time.Now().UnixNano(), minerID[:8])

	// Mark all mature payouts as being processed (with pending txid)
	_, err = tx.ExecContext(ctx, `
		UPDATE payouts
		SET txid = $1, paid_at = $2
		WHERE miner_address = $3
		  AND (txid IS NULL OR txid = '')
		  AND block_height <= $4`,
		pendingTxid, time.Now(), minerID, matureHeight)
	if err != nil {
		return "", 0, fmt.Errorf("failed to mark payouts: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return "", 0, fmt.Errorf("failed to commit: %w", err)
	}

	return pendingTxid, matureAmount, nil
}

// FinalizePayoutAtomic updates the pending txid to the actual txid after successful send
func FinalizePayoutAtomic(pendingTxid, actualTxid string) error {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return ErrDatabaseNotInitialized // CRITICAL FIX: Return error instead of nil
	}

	_, err := db.Exec(`
		UPDATE payouts
		SET txid = $1, confirmed = true, status = 'paid'
		WHERE txid = $2`,
		actualTxid, pendingTxid)
	if err != nil {
		return err
	}

	// Update miners table for affected miners
	db.Exec(`
		UPDATE miners SET
			balance = COALESCE((
				SELECT SUM(amount) FROM payouts
				WHERE miner_address = miners.address AND (txid IS NULL OR txid = '')
			), 0),
			total_paid = COALESCE((
				SELECT SUM(amount) FROM payouts
				WHERE miner_address = miners.address AND confirmed = true
			), 0),
			updated_at = NOW()
		WHERE address IN (SELECT DISTINCT miner_address FROM payouts WHERE txid = $1)`,
		actualTxid)

	return nil
}

// RevertPendingPayout reverts a failed payout attempt
func RevertPendingPayout(pendingTxid string) error {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return ErrDatabaseNotInitialized // CRITICAL FIX: Return error instead of nil
	}

	_, err := db.Exec(`
		UPDATE payouts
		SET txid = NULL, paid_at = NULL
		WHERE txid = $1`,
		pendingTxid)
	return err
}

// LoadMinerPayouts loads payouts from database into memory
func LoadMinerPayouts(minerID string) {
	if db == nil {
		log.Printf("Warning: LoadMinerPayouts called but database not initialized")
		return
	}

	rows, err := db.Query(`
		SELECT block_height, amount, confirmed, txid, created_at, paid_at
		FROM payouts WHERE miner_address = $1 AND confirmed = false`,
		minerID)
	if err != nil {
		log.Printf("Error loading payouts for %s: %v", minerID, err)
		return
	}
	defer rows.Close()

	pendingPayoutsMu.Lock()
	defer pendingPayoutsMu.Unlock()

	for rows.Next() {
		var p PendingPayout
		var txid sql.NullString
		var paidAt sql.NullTime

		if err := rows.Scan(&p.BlockHeight, &p.Amount, &p.Confirmed, &txid, &p.CreatedAt, &paidAt); err != nil {
			log.Printf("Warning: failed to scan payout for %s: %v", minerID, err)
			continue
		}

		p.MinerID = minerID
		if txid.Valid {
			p.TxID = txid.String
		}
		if paidAt.Valid {
			p.PaidAt = paidAt.Time
		}

		pendingPayouts[minerID] = append(pendingPayouts[minerID], p)
	}

	if err := rows.Err(); err != nil {
		log.Printf("Warning: error iterating payouts for %s: %v", minerID, err)
	}
}

// LoadAllPendingPayouts loads all unpaid payouts from database
func LoadAllPendingPayouts() {
	if db == nil {
		log.Printf("Warning: LoadAllPendingPayouts called but database not initialized")
		return
	}

	rows, err := db.Query(`
		SELECT miner_address, block_height, amount, confirmed, txid, created_at, paid_at
		FROM payouts WHERE confirmed = false OR txid IS NULL`)
	if err != nil {
		log.Printf("Error loading all payouts: %v", err)
		return
	}
	defer rows.Close()

	pendingPayoutsMu.Lock()
	defer pendingPayoutsMu.Unlock()

	// Clear existing
	pendingPayouts = make(map[string][]PendingPayout)

	for rows.Next() {
		var p PendingPayout
		var txid sql.NullString
		var paidAt sql.NullTime

		if err := rows.Scan(&p.MinerID, &p.BlockHeight, &p.Amount, &p.Confirmed, &txid, &p.CreatedAt, &paidAt); err != nil {
			log.Printf("Warning: failed to scan pending payout: %v", err)
			continue
		}

		if txid.Valid {
			p.TxID = txid.String
		}
		if paidAt.Valid {
			p.PaidAt = paidAt.Time
		}

		pendingPayouts[p.MinerID] = append(pendingPayouts[p.MinerID], p)
	}

	if err := rows.Err(); err != nil {
		log.Printf("Warning: error iterating pending payouts: %v", err)
	}

	log.Printf("✅ Loaded %d miners with pending payouts from database", len(pendingPayouts))
}

// MarkPayoutPaidDB marks payout as paid in database
func MarkPayoutPaidDB(minerID string, blockHeight int64, txid string) error {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return ErrDatabaseNotInitialized // CRITICAL FIX: Return error instead of nil
	}

	_, err := db.Exec(`
		UPDATE payouts SET confirmed = true, status = 'paid', txid = $1, paid_at = $2
		WHERE miner_address = $3 AND block_height = $4`,
		txid, time.Now(), minerID, blockHeight)
	return err
}

// GetMinerBalanceDB gets balance from database
func GetMinerBalanceDB(minerID string, currentHeight int64) (mature float64, immature float64) {
	if db == nil {
		return GetMinerBalance(minerID, currentHeight)
	}

	// Mature: blocks with 100+ confirmations and not paid
	row := db.QueryRow(`
		SELECT COALESCE(SUM(amount), 0) FROM payouts
		WHERE miner_address = $1 AND (txid IS NULL OR txid = '') AND block_height <= $2`,
		minerID, currentHeight-COINBASE_MATURITY)
	if err := row.Scan(&mature); err != nil {
		log.Printf("Warning: failed to scan mature balance for %s: %v", minerID, err)
		mature = 0
	}

	// Immature: blocks with < 100 confirmations
	row = db.QueryRow(`
		SELECT COALESCE(SUM(amount), 0) FROM payouts
		WHERE miner_address = $1 AND (txid IS NULL OR txid = '') AND block_height > $2`,
		minerID, currentHeight-COINBASE_MATURITY)
	if err := row.Scan(&immature); err != nil {
		log.Printf("Warning: failed to scan immature balance for %s: %v", minerID, err)
		immature = 0
	}

	return
}

// GetMinerBlocksDB gets blocks from database
func GetMinerBlocksDB(minerID string) []MinerBlock {
	if db == nil {
		return GetMinerBlocks(minerID)
	}

	// Join with payouts table to get the payout txid for each block
	rows, err := db.Query(`
		SELECT b.height, b.hash, b.reward, b.created_at, b.status, COALESCE(p.txid, '')
		FROM blocks b
		LEFT JOIN payouts p ON p.block_height = b.height AND p.miner_address = b.miner_address
		WHERE b.miner_address = $1 ORDER BY b.height DESC LIMIT 100`,
		minerID)
	if err != nil {
		log.Printf("Warning: failed to query miner blocks for %s: %v", minerID, err)
		return []MinerBlock{}
	}
	defer rows.Close()

	var blocks []MinerBlock
	for rows.Next() {
		var b MinerBlock
		var status string
		var payoutTxid string
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
		log.Printf("Warning: error iterating miner blocks for %s: %v", minerID, err)
	}

	return blocks
}

// GetTotalBlocksDB returns total blocks in database
func GetTotalBlocksDB() int64 {
	if db == nil {
		log.Printf("Warning: GetTotalBlocksDB called but database not initialized")
		return 0
	}

	var count int64
	if err := db.QueryRow("SELECT COUNT(*) FROM blocks").Scan(&count); err != nil {
		log.Printf("Warning: failed to count total blocks: %v", err)
		return 0
	}
	return count
}

// GetAllPoolBlocksDB gets all blocks mined by the pool with pagination
func GetAllPoolBlocksDB(page, limit int) ([]PoolBlock, int64) {
	if db == nil {
		log.Printf("Warning: GetAllPoolBlocksDB called but database not initialized")
		return []PoolBlock{}, 0
	}

	// Get total count
	var total int64
	if err := db.QueryRow("SELECT COUNT(*) FROM blocks").Scan(&total); err != nil {
		log.Printf("Warning: failed to count blocks: %v", err)
		total = 0
	}

	// Get paginated blocks
	offset := (page - 1) * limit
	rows, err := db.Query(`
		SELECT height, hash, reward, miner_address, status, EXTRACT(EPOCH FROM created_at)::bigint, COALESCE(is_solo, false)
		FROM blocks ORDER BY height DESC LIMIT $1 OFFSET $2`,
		limit, offset)
	if err != nil {
		log.Printf("Warning: failed to query pool blocks: %v", err)
		return []PoolBlock{}, total
	}
	defer rows.Close()

	var blocks []PoolBlock
	for rows.Next() {
		var b PoolBlock
		if err := rows.Scan(&b.Height, &b.Hash, &b.Reward, &b.MinerAddr, &b.Status, &b.CreatedAt, &b.IsSolo); err != nil {
			log.Printf("Warning: failed to scan pool block: %v", err)
			continue
		}
		blocks = append(blocks, b)
	}

	if err := rows.Err(); err != nil {
		log.Printf("Warning: error iterating pool blocks: %v", err)
	}

	return blocks, total
}

// GetMinerSoloBlocksDB gets solo blocks found by a specific miner
func GetMinerSoloBlocksDB(minerID string) []SoloBlock {
	if db == nil {
		log.Printf("Warning: GetMinerSoloBlocksDB called but database not initialized")
		return []SoloBlock{}
	}

	rows, err := db.Query(`
		SELECT b.height, b.hash, b.reward, EXTRACT(EPOCH FROM b.created_at)::bigint, b.status,
			COALESCE(p.txid, '') as payout_txid
		FROM blocks b
		LEFT JOIN payouts p ON p.block_height = b.height AND p.miner_address = b.miner_address
		WHERE b.miner_address = $1 AND b.is_solo = true
		ORDER BY b.height DESC LIMIT 100`,
		minerID)
	if err != nil {
		log.Printf("Warning: failed to query solo blocks for %s: %v", minerID, err)
		return []SoloBlock{}
	}
	defer rows.Close()

	var blocks []SoloBlock
	for rows.Next() {
		var b SoloBlock
		var status string
		var payoutTxid string
		if err := rows.Scan(&b.Height, &b.Hash, &b.Reward, &b.Time, &status, &payoutTxid); err != nil {
			log.Printf("Warning: failed to scan solo block: %v", err)
			continue
		}
		b.Status = status
		b.Confirmed = (status == "confirmed")
		b.PayoutTxid = payoutTxid
		blocks = append(blocks, b)
	}

	if err := rows.Err(); err != nil {
		log.Printf("Warning: error iterating solo blocks for %s: %v", minerID, err)
	}

	return blocks
}

// GetMinerBlockContributionsDB gets all block contributions for a miner from payouts table
func GetMinerBlockContributionsDB(minerID string) []MinerBlockContribution {
	if db == nil {
		log.Printf("Warning: GetMinerBlockContributionsDB called but database not initialized")
		return []MinerBlockContribution{}
	}

	rows, err := db.Query(`
		SELECT p.block_height, p.amount, EXTRACT(EPOCH FROM p.created_at)::bigint,
			CASE WHEN p.txid IS NOT NULL AND p.txid != '' THEN true ELSE false END as is_paid
		FROM payouts p
		JOIN blocks b ON b.height = p.block_height
		WHERE p.miner_address = $1 AND b.is_solo = false
		ORDER BY p.block_height DESC LIMIT 50`,
		minerID)
	if err != nil {
		log.Printf("Warning: failed to query block contributions for %s: %v", minerID, err)
		return []MinerBlockContribution{}
	}
	defer rows.Close()

	var contributions []MinerBlockContribution
	for rows.Next() {
		var c MinerBlockContribution
		if err := rows.Scan(&c.Height, &c.Amount, &c.Time, &c.IsPaid); err != nil {
			log.Printf("Warning: failed to scan block contribution: %v", err)
			continue
		}
		// Calculate share percentage (50 BCH2 * 0.99 fee = 49.5 max reward)
		c.SharePct = (c.Amount / 49.5) * 100
		contributions = append(contributions, c)
	}

	if err := rows.Err(); err != nil {
		log.Printf("Warning: error iterating block contributions for %s: %v", minerID, err)
	}

	return contributions
}

// MarkMaturePaidInDB marks all mature payouts as paid directly in database
// Uses a transaction with row locking to prevent race conditions
func MarkMaturePaidInDB(minerID string, currentHeight int64, txid string) error {
	return MarkMaturePaidInDBWithAmount(minerID, currentHeight, txid, 0)
}

// MarkMaturePaidInDBWithAmount marks mature payouts as paid with partial payment support
// If paidAmount > 0, only marks payouts up to that amount; otherwise marks all mature
func MarkMaturePaidInDBWithAmount(minerID string, currentHeight int64, txid string, paidAmount float64) error {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return ErrDatabaseNotInitialized // CRITICAL FIX: Return error instead of nil
	}

	// Use context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), DBTimeout)
	defer cancel()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	matureHeight := currentHeight - COINBASE_MATURITY
	now := time.Now()

	var result sql.Result

	if paidAmount == 0 {
		// Full payment mode - mark all mature as paid
		result, err = tx.ExecContext(ctx, `
			UPDATE payouts SET confirmed = true, status = 'paid', txid = $1, paid_at = $2
			WHERE miner_address = $3 AND block_height <= $4 AND (txid IS NULL OR txid = '')`,
			txid, now, minerID, matureHeight)
	} else {
		// Partial payment mode - mark payouts up to paidAmount
		// Use a CTE to select payouts in order and mark only up to the paid amount
		result, err = tx.ExecContext(ctx, `
			WITH to_pay AS (
				SELECT id, amount,
					SUM(amount) OVER (ORDER BY block_height, id) as running_total
				FROM payouts
				WHERE miner_address = $1 AND block_height <= $2 AND (txid IS NULL OR txid = '')
			)
			UPDATE payouts SET confirmed = true, status = 'paid', txid = $3, paid_at = $4
			WHERE id IN (
				SELECT id FROM to_pay WHERE running_total <= $5
			)`,
			minerID, matureHeight, txid, now, paidAmount)
	}

	if err != nil {
		log.Printf("DB update error: %v", err)
		return err
	}

	// Also update block status to confirmed for paid blocks
	tx.ExecContext(ctx, `
		UPDATE blocks SET status = 'confirmed'
		WHERE height IN (
			SELECT block_height FROM payouts
			WHERE miner_address = $1 AND txid = $2
		) AND status = 'pending'`,
		minerID, txid)

	// Update miners table balance and total_paid
	tx.ExecContext(ctx, `
		UPDATE miners SET
			balance = COALESCE((
				SELECT SUM(amount) FROM payouts
				WHERE miner_address = miners.address AND (txid IS NULL OR txid = '')
			), 0),
			total_paid = COALESCE((
				SELECT SUM(amount) FROM payouts
				WHERE miner_address = miners.address AND confirmed = true
			), 0),
			updated_at = NOW()
		WHERE address = $1`,
		minerID)

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit: %w", err)
	}

	rows, _ := result.RowsAffected()
	if paidAmount > 0 {
		log.Printf("Marked %d payouts as paid in DB for %s (txid: %s, amount: %.8f)",
			rows, minerID, txid, paidAmount)
	} else {
		log.Printf("Marked %d payouts as paid in DB for %s (txid: %s)", rows, minerID, txid)
	}
	return nil
}

// SaveMinerSettings saves or updates miner settings in the database
func SaveMinerSettings(settings *MinerSettings) error {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return ErrDatabaseNotInitialized // CRITICAL FIX: Return error instead of nil
	}

	_, err := db.Exec(`
		INSERT INTO miners (address, solo_mining, manual_diff, min_payout, address_1175, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (address) DO UPDATE SET
			solo_mining = EXCLUDED.solo_mining,
			manual_diff = EXCLUDED.manual_diff,
			min_payout = EXCLUDED.min_payout,
			address_1175 = EXCLUDED.address_1175,
			updated_at = NOW()`,
		settings.Address, settings.SoloMining, settings.ManualDiff, settings.MinPayout, settings.Address1175)
	return err
}

// GetMinerSettingsDB retrieves miner settings from the database
func GetMinerSettingsDB(address string) (*MinerSettings, error) {
	if db == nil {
		return nil, ErrDatabaseNotInitialized
	}

	var settings MinerSettings
	err := db.QueryRow(`
		SELECT address, solo_mining, manual_diff, min_payout, COALESCE(address_1175, '')
		FROM miners WHERE address = $1`,
		address).Scan(&settings.Address, &settings.SoloMining, &settings.ManualDiff, &settings.MinPayout, &settings.Address1175)

	if err != nil {
		return nil, err
	}
	return &settings, nil
}

// LoadAllMinerSettings loads all miner settings from database
func LoadAllMinerSettings() map[string]*MinerSettings {
	result := make(map[string]*MinerSettings)
	if db == nil {
		log.Printf("Warning: LoadAllMinerSettings called but database not initialized")
		return result
	}

	rows, err := db.Query(`SELECT address, solo_mining, manual_diff, min_payout, COALESCE(address_1175, '') FROM miners`)
	if err != nil {
		log.Printf("Error loading miner settings: %v", err)
		return result
	}
	defer rows.Close()

	for rows.Next() {
		var s MinerSettings
		if err := rows.Scan(&s.Address, &s.SoloMining, &s.ManualDiff, &s.MinPayout, &s.Address1175); err != nil {
			log.Printf("Warning: failed to scan miner settings: %v", err)
			continue
		}
		result[s.Address] = &s
	}

	if err := rows.Err(); err != nil {
		log.Printf("Warning: error iterating miner settings: %v", err)
	}

	log.Printf("✅ Loaded %d miner settings from database", len(result))
	return result
}

// GetMinerPayoutsDB returns payout history from database
func GetMinerPayoutsDB(minerID string) ([]PayoutRecord, int, float64) {
	if db == nil {
		log.Printf("Warning: GetMinerPayoutsDB called but database not initialized")
		return []PayoutRecord{}, 0, 0
	}

	// Get unique payouts grouped by txid (include pending_ prefixed txids for in-progress payouts)
	rows, err := db.Query(`
		SELECT txid, SUM(amount) as amount, MAX(paid_at) as paid_at, COUNT(*) as blocks,
		       CASE WHEN txid LIKE 'pending_%' THEN false ELSE true END as is_confirmed
		FROM payouts
		WHERE miner_address = $1
		  AND txid IS NOT NULL
		  AND txid != ''
		GROUP BY txid
		ORDER BY MAX(paid_at) DESC
		LIMIT 100`,
		minerID)
	if err != nil {
		log.Printf("Error getting payouts for %s: %v", minerID, err)
		return []PayoutRecord{}, 0, 0
	}
	defer rows.Close()

	var payouts []PayoutRecord
	var totalPaid float64

	for rows.Next() {
		var p PayoutRecord
		var paidAt sql.NullTime
		if err := rows.Scan(&p.TxID, &p.Amount, &paidAt, &p.Blocks, &p.Confirmed); err != nil {
			log.Printf("Warning: failed to scan payout record: %v", err)
			continue
		}
		if paidAt.Valid {
			p.PaidAt = paidAt.Time
		}
		// Only count confirmed payouts in totalPaid
		if p.Confirmed {
			totalPaid += p.Amount
		}
		payouts = append(payouts, p)
	}

	if err := rows.Err(); err != nil {
		log.Printf("Warning: error iterating payout records for %s: %v", minerID, err)
	}

	return payouts, len(payouts), totalPaid
}

// GetMinerSoloPayoutsDB returns payout history for solo blocks only
func GetMinerSoloPayoutsDB(minerID string) ([]PayoutRecord, int, float64) {
	if db == nil {
		log.Printf("Warning: GetMinerSoloPayoutsDB called but database not initialized")
		return []PayoutRecord{}, 0, 0
	}

	// Get payouts only for solo blocks
	rows, err := db.Query(`
		SELECT p.txid, p.amount, p.paid_at, 1 as blocks,
		       CASE WHEN p.txid LIKE 'pending_%' THEN false ELSE true END as is_confirmed
		FROM payouts p
		JOIN blocks b ON b.height = p.block_height AND b.miner_address = p.miner_address
		WHERE p.miner_address = $1
		  AND b.is_solo = true
		  AND p.txid IS NOT NULL
		  AND p.txid != ''
		ORDER BY p.paid_at DESC NULLS LAST
		LIMIT 100`,
		minerID)
	if err != nil {
		log.Printf("Error getting solo payouts for %s: %v", minerID, err)
		return []PayoutRecord{}, 0, 0
	}
	defer rows.Close()

	var payouts []PayoutRecord
	var totalPaid float64

	for rows.Next() {
		var p PayoutRecord
		var paidAt sql.NullTime
		if err := rows.Scan(&p.TxID, &p.Amount, &paidAt, &p.Blocks, &p.Confirmed); err != nil {
			log.Printf("Warning: failed to scan solo payout record: %v", err)
			continue
		}
		if paidAt.Valid {
			p.PaidAt = paidAt.Time
		}
		if p.Confirmed {
			totalPaid += p.Amount
		}
		payouts = append(payouts, p)
	}

	return payouts, len(payouts), totalPaid
}

// SaveShare saves a PPLNS share to the database for reward distribution
func SaveShare(minerAddress string, workerName string, difficulty float64, isSolo bool) error {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return ErrDatabaseNotInitialized // CRITICAL FIX: Return error instead of nil
	}

	_, err := db.Exec(`
		INSERT INTO shares (miner_address, worker_name, difficulty, is_solo)
		VALUES ($1, $2, $3, $4)`,
		minerAddress, workerName, difficulty, isSolo)
	return err
}

// PPLNSShare represents a miner's share contribution in the PPLNS window
type PPLNSShare struct {
	MinerAddress string
	TotalWork    float64
}

// GetPPLNSShares returns the sum of difficulty per miner for the last N shares
// Returns a map of minerAddress -> total difficulty contributed
func GetPPLNSShares(windowSize int) (map[string]float64, float64, error) {
	if db == nil {
		return nil, 0, fmt.Errorf("database not initialized")
	}

	// Use context with timeout to prevent hung queries
	ctx, cancel := context.WithTimeout(context.Background(), DBTimeout)
	defer cancel()

	// Get the last N PPLNS shares (not solo) and sum by miner
	rows, err := db.QueryContext(ctx, `
		WITH recent_shares AS (
			SELECT miner_address, difficulty
			FROM shares
			WHERE is_solo = false
			ORDER BY id DESC
			LIMIT $1
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
// Keeps 2x the window size as buffer
func CleanupOldShares(windowSize int) (int64, error) {
	if db == nil {
		return 0, ErrDatabaseNotInitialized
	}

	// Use context with timeout to prevent hung queries
	ctx, cancel := context.WithTimeout(context.Background(), DBTimeout)
	defer cancel()

	// Keep 2x window as buffer, delete older shares
	result, err := db.ExecContext(ctx, `
		DELETE FROM shares
		WHERE id < (
			SELECT MIN(id) FROM (
				SELECT id FROM shares
				ORDER BY id DESC
				LIMIT $1
			) AS recent
		)`,
		windowSize*2)
	if err != nil {
		return 0, fmt.Errorf("failed to cleanup old shares: %w", err)
	}

	deleted, _ := result.RowsAffected()
	return deleted, nil
}

// GetReadyPayoutsDB queries database directly for miners with mature unpaid balances
// This is more reliable than in-memory state, especially after restarts
func GetReadyPayoutsDB(currentHeight int64, minPayout float64) map[string]float64 {
	if db == nil {
		log.Printf("Warning: GetReadyPayoutsDB called but database not initialized")
		return make(map[string]float64)
	}

	matureHeight := currentHeight - COINBASE_MATURITY

	rows, err := db.Query(`
		SELECT miner_address, SUM(amount) as total
		FROM payouts
		WHERE (txid IS NULL OR txid = '')
		  AND block_height <= $1
		GROUP BY miner_address
		HAVING SUM(amount) >= $2`,
		matureHeight, minPayout)
	if err != nil {
		log.Printf("Error querying ready payouts from DB: %v", err)
		return nil
	}
	defer rows.Close()

	ready := make(map[string]float64)
	for rows.Next() {
		var addr string
		var amount float64
		if err := rows.Scan(&addr, &amount); err != nil {
			log.Printf("Warning: failed to scan ready payout: %v", err)
			continue
		}
		ready[addr] = amount
	}

	if err := rows.Err(); err != nil {
		log.Printf("Warning: error iterating ready payouts: %v", err)
	}

	return ready
}

// GetRecordedBlockHash returns the block hash this pool recorded for the given
// height (from the blocks table). ok is false if no block is recorded at that
// height, in which case the caller must not make an orphan decision.
func GetRecordedBlockHash(height int64) (string, bool) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return "", false
	}
	var hash string
	err := db.QueryRow(`SELECT hash FROM blocks WHERE height = $1`, height).Scan(&hash)
	if err != nil || hash == "" {
		return "", false
	}
	return hash, true
}

// GetUnpaidMatureHeights returns the distinct block heights that still have unpaid
// mature payout rows, restricted to [minHeight, matureHeight] so callers can bound
// how many heights they reconcile per pass.
func GetUnpaidMatureHeights(matureHeight, minHeight int64) ([]int64, error) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return nil, ErrDatabaseNotInitialized
	}
	rows, err := db.Query(`
		SELECT DISTINCT block_height FROM payouts
		WHERE (txid IS NULL OR txid = '')
		  AND block_height <= $1 AND block_height >= $2
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

// VoidOrphanedPayouts marks every unpaid payout row at the given height as
// orphaned so it is permanently excluded from payout selection. Used when the
// block the pool recorded at that height is no longer on the active chain, i.e.
// the pool never actually received that coinbase and must not pay miners for it.
// Returns the number of rows voided and the total amount voided.
func VoidOrphanedPayouts(height int64) (int64, float64, error) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return 0, 0, ErrDatabaseNotInitialized
	}
	var amount float64
	// Capture the amount before voiding so callers can log the forfeited total.
	_ = db.QueryRow(`SELECT COALESCE(SUM(amount),0) FROM payouts
		WHERE block_height = $1 AND (txid IS NULL OR txid = '')`, height).Scan(&amount)
	res, err := db.Exec(`
		UPDATE payouts SET txid = 'orphaned', status = 'orphaned', paid_at = NOW()
		WHERE block_height = $1 AND (txid IS NULL OR txid = '')`, height)
	if err != nil {
		return 0, 0, err
	}
	// Mark the block row itself so it is visibly reconciled.
	db.Exec(`UPDATE blocks SET status = 'orphaned' WHERE height = $1`, height)
	n, _ := res.RowsAffected()
	return n, amount, nil
}

// PayoutRow is a single mature, reserved payout ledger row.
type PayoutRow struct {
	ID          int64
	Amount      float64
	BlockHeight int64
}

// ReserveMaturePayouts atomically reserves (under FOR UPDATE) every mature unpaid
// payout row for a miner, stamps them with a unique placeholder txid, and returns
// them ordered. Reserved rows carry a non-empty txid so GetReadyPayoutsDB no longer
// selects them — this is what stops the auto processor and a concurrent manual
// request from both paying the same balance. The caller must, for each row it
// actually broadcasts, call FinalizePayoutRows with the real txid, and release any
// rows it did not send via RevertPendingPayout(pendingID). Rows left carrying the
// placeholder after an interrupted run are safely excluded from payment (never
// double-paid) until reconciled.
func ReserveMaturePayouts(minerID string, matureHeight int64) (pendingID string, rows []PayoutRow, total float64, err error) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return "", nil, 0, ErrDatabaseNotInitialized
	}
	ctx, cancel := context.WithTimeout(context.Background(), DBTimeout)
	defer cancel()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", nil, 0, err
	}
	defer tx.Rollback()

	r, err := tx.QueryContext(ctx, `
		SELECT id, amount, block_height FROM payouts
		WHERE miner_address = $1 AND (txid IS NULL OR txid = '') AND block_height <= $2
		ORDER BY block_height, id
		FOR UPDATE`, minerID, matureHeight)
	if err != nil {
		return "", nil, 0, err
	}
	for r.Next() {
		var pr PayoutRow
		if err := r.Scan(&pr.ID, &pr.Amount, &pr.BlockHeight); err != nil {
			r.Close()
			return "", nil, 0, err
		}
		rows = append(rows, pr)
		total += pr.Amount
	}
	r.Close()
	if err := r.Err(); err != nil {
		return "", nil, 0, err
	}
	if len(rows) == 0 {
		_ = tx.Commit()
		return "", nil, 0, nil
	}

	suffix := minerID
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	pendingID = fmt.Sprintf("pending_%d_%s", time.Now().UnixNano(), suffix)
	// Same predicate as the locked SELECT, inside the same transaction — marks
	// exactly the rows we just locked.
	if _, err := tx.ExecContext(ctx, `
		UPDATE payouts SET txid = $1, status = 'processing', paid_at = $2
		WHERE miner_address = $3 AND (txid IS NULL OR txid = '') AND block_height <= $4`,
		pendingID, time.Now(), minerID, matureHeight); err != nil {
		return "", nil, 0, err
	}
	if err := tx.Commit(); err != nil {
		return "", nil, 0, err
	}
	return pendingID, rows, total, nil
}

// FinalizePayoutRows stamps the given payout rows with the real broadcast txid.
// Called immediately after a chunk's sendtoaddress succeeds, so the amount sent
// on-chain always equals the amount marked paid in the ledger.
func FinalizePayoutRows(ids []int64, actualTxid string) error {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return ErrDatabaseNotInitialized
	}
	if len(ids) == 0 {
		return nil
	}
	_, err := db.Exec(`
		UPDATE payouts SET txid = $1, confirmed = true, status = 'paid'
		WHERE id = ANY($2)`, actualTxid, pq.Array(ids))
	if err != nil {
		return err
	}
	// Refresh affected miners' balance/total_paid.
	db.Exec(`
		UPDATE miners SET
			balance = COALESCE((SELECT SUM(amount) FROM payouts
				WHERE miner_address = miners.address AND (txid IS NULL OR txid = '')), 0),
			total_paid = COALESCE((SELECT SUM(amount) FROM payouts
				WHERE miner_address = miners.address AND confirmed = true), 0),
			updated_at = NOW()
		WHERE address IN (SELECT DISTINCT miner_address FROM payouts WHERE id = ANY($1))`,
		pq.Array(ids))
	return nil
}

// RevertPayoutRows releases the given rows back to unpaid. Only ever used for rows
// that were reserved but definitively NOT broadcast.
func RevertPayoutRows(ids []int64) error {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return ErrDatabaseNotInitialized
	}
	if len(ids) == 0 {
		return nil
	}
	_, err := db.Exec(`
		UPDATE payouts SET txid = NULL, status = 'pending', paid_at = NULL
		WHERE id = ANY($1)`, pq.Array(ids))
	return err
}

// GetSettingsPinHash returns the miner's bcrypt settings-PIN hash, or "" if none set.
func GetSettingsPinHash(address string) (string, error) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return "", ErrDatabaseNotInitialized
	}
	var h string
	err := db.QueryRow(`SELECT COALESCE(settings_pin_hash, '') FROM miners WHERE address = $1`, address).Scan(&h)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return h, err
}

// SetSettingsPinHash stores (or clears, when hash == "") the miner's settings-PIN hash.
// Upserts so a brand-new miner claiming a PIN before any settings row exists still works.
func SetSettingsPinHash(address, hash string) error {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return ErrDatabaseNotInitialized
	}
	_, err := db.Exec(`
		INSERT INTO miners (address, settings_pin_hash, updated_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (address) DO UPDATE SET settings_pin_hash = EXCLUDED.settings_pin_hash, updated_at = NOW()`,
		address, hash)
	return err
}

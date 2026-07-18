//go:build sqlite

package stats

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

var (
	db   *sql.DB
	dbMu sync.RWMutex // CRITICAL FIX: Mutex protection for global db pointer
)

// ErrDatabaseNotInitialized is returned when database operations are attempted without initialization
var ErrDatabaseNotInitialized = fmt.Errorf("database not initialized")

// GetDBPath returns the SQLite database file path
func GetDBPath() string {
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		// Default to data directory next to executable
		exe, err := os.Executable()
		if err != nil {
			log.Printf("Warning: could not get executable path: %v, using current directory", err)
			return filepath.Join("data", "forgepool.db")
		}
		dbPath = filepath.Join(filepath.Dir(exe), "data", "forgepool.db")
	}
	return dbPath
}

// GetDBConnStr returns connection string (for compatibility)
func GetDBConnStr() string {
	return GetDBPath()
}

// InitDB initializes SQLite database
func InitDB(connStr string) error {
	dbMu.Lock()
	defer dbMu.Unlock()

	dbPath := connStr
	if dbPath == "" {
		dbPath = GetDBPath()
	}

	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	var err error
	db, err = sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return err
	}

	// SQLite settings for reliability
	db.SetMaxOpenConns(1) // SQLite works best with single connection
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err = db.Ping(); err != nil {
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

	log.Printf("✅ Connected to SQLite database: %s", dbPath)
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
		min_payout REAL DEFAULT 5.0,
		balance REAL DEFAULT 0,
		total_paid REAL DEFAULT 0,
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
	`

	_, err := db.Exec(schema)
	return err
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

func IsDBConnected() bool {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return false
	}
	return db.Ping() == nil
}

// getDB returns the database connection with read lock held
// Caller must call dbMu.RUnlock() when done
func getDB() *sql.DB {
	dbMu.RLock()
	return db
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
		minerID, blockHeight, amount, time.Now())
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
		height, hash, minerID, reward, solo, time.Now())
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
		blockHeight, blockHash, minerID, amount, solo, time.Now())
	if err != nil {
		return fmt.Errorf("failed to insert block: %w", err)
	}

	_, err = tx.Exec(`
		INSERT OR IGNORE INTO payouts (miner_address, block_height, amount, confirmed, created_at)
		VALUES (?, ?, ?, 0, ?)`,
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
		SELECT height, hash, reward, miner_address, status, strftime('%s', created_at), COALESCE(is_solo, 0)
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
	dbMu.RLock()
	defer dbMu.RUnlock()

	if db == nil {
		return []SoloBlock{}
	}

	rows, err := db.Query(`
		SELECT b.height, b.hash, b.reward, strftime('%s', b.created_at), b.status,
			COALESCE(p.txid, '') as payout_txid
		FROM blocks b
		LEFT JOIN payouts p ON p.block_height = b.height AND p.miner_address = b.miner_address
		WHERE b.miner_address = ? AND b.is_solo = 1
		ORDER BY b.height DESC LIMIT 100`,
		minerID)
	if err != nil {
		log.Printf("Warning: failed to query solo blocks: %v", err)
		return []SoloBlock{}
	}
	defer rows.Close()

	var blocks []SoloBlock
	for rows.Next() {
		var b SoloBlock
		var status, payoutTxid string
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
		log.Printf("Warning: error iterating solo blocks: %v", err)
	}

	return blocks
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

	_, err := db.Exec(`
		INSERT INTO miners (address, solo_mining, manual_diff, min_payout, updated_at)
		VALUES (?, ?, ?, ?, datetime('now'))
		ON CONFLICT(address) DO UPDATE SET
			solo_mining = excluded.solo_mining,
			manual_diff = excluded.manual_diff,
			min_payout = excluded.min_payout,
			updated_at = datetime('now')`,
		settings.Address, solo, settings.ManualDiff, settings.MinPayout)
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

	rows, err := db.Query(`SELECT address, solo_mining, manual_diff, min_payout FROM miners`)
	if err != nil {
		log.Printf("Warning: failed to load miner settings: %v", err)
		return result
	}
	defer rows.Close()

	for rows.Next() {
		var s MinerSettings
		var solo int
		if err := rows.Scan(&s.Address, &solo, &s.ManualDiff, &s.MinPayout); err != nil {
			log.Printf("Warning: failed to scan miner settings: %v", err)
			continue
		}
		s.SoloMining = solo == 1
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
	dbMu.RLock()
	defer dbMu.RUnlock()

	if db == nil {
		return []PayoutRecord{}, 0, 0
	}

	rows, err := db.Query(`
		SELECT txid, SUM(amount) as amount, MAX(paid_at) as paid_at, COUNT(*) as blocks,
		       CASE WHEN txid LIKE 'pending_%' THEN 0 ELSE 1 END as is_confirmed
		FROM payouts
		WHERE miner_address = ?
		  AND txid IS NOT NULL
		  AND txid != ''
		GROUP BY txid
		ORDER BY MAX(paid_at) DESC
		LIMIT 100`,
		minerID)
	if err != nil {
		log.Printf("Warning: failed to query payouts: %v", err)
		return []PayoutRecord{}, 0, 0
	}
	defer rows.Close()

	var payouts []PayoutRecord
	var totalPaid float64

	for rows.Next() {
		var p PayoutRecord
		var paidAt sql.NullTime
		var confirmed int
		if err := rows.Scan(&p.TxID, &p.Amount, &paidAt, &p.Blocks, &confirmed); err != nil {
			log.Printf("Warning: failed to scan payout: %v", err)
			continue
		}
		p.Confirmed = confirmed == 1
		if paidAt.Valid {
			p.PaidAt = paidAt.Time
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
	dbMu.RLock()
	defer dbMu.RUnlock()

	if db == nil {
		return []PayoutRecord{}, 0, 0
	}

	rows, err := db.Query(`
		SELECT p.txid, p.amount, p.paid_at, 1 as blocks,
		       CASE WHEN p.txid LIKE 'pending_%' THEN 0 ELSE 1 END as is_confirmed
		FROM payouts p
		JOIN blocks b ON b.height = p.block_height AND b.miner_address = p.miner_address
		WHERE p.miner_address = ?
		  AND b.is_solo = 1
		  AND p.txid IS NOT NULL
		  AND p.txid != ''
		ORDER BY p.paid_at DESC
		LIMIT 100`,
		minerID)
	if err != nil {
		log.Printf("Warning: failed to query solo payouts: %v", err)
		return []PayoutRecord{}, 0, 0
	}
	defer rows.Close()

	var payouts []PayoutRecord
	var totalPaid float64

	for rows.Next() {
		var p PayoutRecord
		var paidAt sql.NullTime
		var confirmed int
		if err := rows.Scan(&p.TxID, &p.Amount, &paidAt, &p.Blocks, &confirmed); err != nil {
			log.Printf("Warning: failed to scan solo payout: %v", err)
			continue
		}
		p.Confirmed = confirmed == 1
		if paidAt.Valid {
			p.PaidAt = paidAt.Time
		}
		if p.Confirmed {
			totalPaid += p.Amount
		}
		payouts = append(payouts, p)
	}

	if err := rows.Err(); err != nil {
		log.Printf("Warning: error iterating solo payouts: %v", err)
	}

	return payouts, len(payouts), totalPaid
}

// MarkMaturePaidInDBWithAmount marks mature payouts as paid
func MarkMaturePaidInDBWithAmount(minerID string, currentHeight int64, txid string, paidAmount float64) error {
	dbMu.RLock()
	defer dbMu.RUnlock()

	if db == nil {
		return ErrDatabaseNotInitialized // CRITICAL FIX: Return error instead of nil
	}

	matureHeight := currentHeight - COINBASE_MATURITY
	now := time.Now()

	_, err := db.Exec(`
		UPDATE payouts SET confirmed = 1, status = 'paid', txid = ?, paid_at = ?
		WHERE miner_address = ? AND block_height <= ? AND (txid IS NULL OR txid = '')`,
		txid, now, minerID, matureHeight)

	return err
}

// GetReadyPayoutsDB queries database for miners with mature unpaid balances
func GetReadyPayoutsDB(currentHeight int64, minPayout float64) map[string]float64 {
	dbMu.RLock()
	defer dbMu.RUnlock()

	if db == nil {
		return nil
	}

	matureHeight := currentHeight - COINBASE_MATURITY

	rows, err := db.Query(`
		SELECT miner_address, SUM(amount) as total
		FROM payouts
		WHERE (txid IS NULL OR txid = '')
		  AND block_height <= ?
		GROUP BY miner_address
		HAVING SUM(amount) >= ?`,
		matureHeight, minPayout)
	if err != nil {
		log.Printf("Warning: failed to query ready payouts: %v", err)
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

// ProcessPayoutAtomic handles the entire payout process in a single transaction
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

	ctx, cancel := context.WithTimeout(context.Background(), DBTimeout)
	defer cancel()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	var matureAmount float64
	matureHeight := currentHeight - COINBASE_MATURITY

	err = tx.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(amount), 0)
		FROM payouts
		WHERE miner_address = ?
		  AND (txid IS NULL OR txid = '')
		  AND block_height <= ?`,
		minerID, matureHeight).Scan(&matureAmount)
	if err != nil {
		return "", 0, fmt.Errorf("failed to get mature balance: %w", err)
	}

	if matureAmount < minPayout {
		return "", 0, fmt.Errorf("insufficient mature balance: %.2f < %.2f", matureAmount, minPayout)
	}

	// CRITICAL FIX: Safe slice after validation
	pendingTxid := fmt.Sprintf("pending_%d_%s", time.Now().UnixNano(), minerID[:8])

	_, err = tx.ExecContext(ctx, `
		UPDATE payouts
		SET txid = ?, paid_at = ?
		WHERE miner_address = ?
		  AND (txid IS NULL OR txid = '')
		  AND block_height <= ?`,
		pendingTxid, time.Now(), minerID, matureHeight)
	if err != nil {
		return "", 0, fmt.Errorf("failed to mark payouts: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return "", 0, fmt.Errorf("failed to commit: %w", err)
	}

	return pendingTxid, matureAmount, nil
}

// FinalizePayoutAtomic updates the pending txid to the actual txid
func FinalizePayoutAtomic(pendingTxid, actualTxid string) error {
	dbMu.RLock()
	defer dbMu.RUnlock()

	if db == nil {
		return ErrDatabaseNotInitialized // CRITICAL FIX: Return error instead of nil
	}

	_, err := db.Exec(`
		UPDATE payouts
		SET txid = ?, confirmed = 1, status = 'paid'
		WHERE txid = ?`,
		actualTxid, pendingTxid)
	return err
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
		WHERE txid = ?`,
		pendingTxid)
	return err
}

// SaveShare saves a PPLNS share to the database
func SaveShare(minerAddress string, workerName string, difficulty float64, isSolo bool) error {
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

// GetPPLNSShares returns the sum of difficulty per miner for the last N shares
func GetPPLNSShares(windowSize int) (map[string]float64, float64, error) {
	dbMu.RLock()
	defer dbMu.RUnlock()

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

func MarkMaturePaidInDB(minerID string, currentHeight int64, txid string) error {
	return MarkMaturePaidInDBWithAmount(minerID, currentHeight, txid, 0)
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
		SELECT address, solo_mining, manual_diff, min_payout
		FROM miners WHERE address = ?`,
		address).Scan(&settings.Address, &solo, &settings.ManualDiff, &settings.MinPayout)

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
		SELECT p.block_height, p.amount, strftime('%s', p.created_at),
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
		var isPaid int
		if err := rows.Scan(&c.Height, &c.Amount, &c.Time, &isPaid); err != nil {
			log.Printf("Warning: failed to scan block contribution: %v", err)
			continue
		}
		c.IsPaid = isPaid == 1
		c.SharePct = (c.Amount / 49.5) * 100
		contributions = append(contributions, c)
	}

	if err := rows.Err(); err != nil {
		log.Printf("Warning: error iterating block contributions: %v", err)
	}

	return contributions
}

func MarkPayoutPaidDB(minerID string, blockHeight int64, txid string) error {
	dbMu.RLock()
	defer dbMu.RUnlock()

	if db == nil {
		return ErrDatabaseNotInitialized // CRITICAL FIX: Return error instead of nil
	}

	_, err := db.Exec(`
		UPDATE payouts SET confirmed = 1, status = 'paid', txid = ?, paid_at = ?
		WHERE miner_address = ? AND block_height = ?`,
		txid, time.Now(), minerID, blockHeight)
	return err
}

func LoadMinerPayouts(minerID string) {
	// Not needed for SQLite - query directly
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

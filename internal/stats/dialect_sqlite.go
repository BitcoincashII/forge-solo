//go:build sqlite

package stats

import (
	"log"
	"strings"
	"time"
)

// SQLite counterparts of the fragments in dialect.go. See that file for why the split is
// this narrow.
//
// Probed against modernc.org/sqlite before writing: $N placeholders bind correctly,
// ON CONFLICT upserts work, and partial indexes (INDEX ... WHERE) are supported, so those
// stay in the shared file. What SQLite rejects is BIGSERIAL, TIMESTAMPTZ, NOW(),
// EXTRACT(...)::bigint, interval arithmetic and ADD COLUMN IF NOT EXISTS.

// epochSecondsExpr renders a timestamp column as an integer Unix epoch.
func epochSecondsExpr(col string) string {
	return "CAST(strftime('%s', substr(" + col + ", 1, 19)) AS INTEGER)"
}

// dbTime is t as a query parameter for a timestamp column in the files both builds share:
// SQLiteTime, the form CURRENT_TIMESTAMP writes and epochSecondsExpr reads. A time.Time is stored
// in a form strftime cannot read.
func dbTime(t time.Time) interface{} {
	return SQLiteTime(t)
}

// Init1175Schema creates the 1175 merge-mining ledger tables; InitDB runs it.
//
// blocks_1175.status:  pending | confirmed | orphaned   (+ distributed bool)
// payouts_1175.status: pending | sending | paid
func Init1175Schema() {
	if db == nil {
		return
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS blocks_1175 (
			height        INTEGER PRIMARY KEY,
			hash          TEXT NOT NULL,
			gross_reward  REAL NOT NULL,
			is_solo       BOOLEAN DEFAULT 0,
			finder        TEXT,
			distributed   BOOLEAN DEFAULT 0,
			status        TEXT DEFAULT 'pending',
			created_at    DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS payouts_1175 (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			miner_address TEXT NOT NULL,
			block_height  INTEGER NOT NULL,
			amount        REAL NOT NULL,
			txid          TEXT,
			status        TEXT DEFAULT 'pending',
			batch         TEXT,
			paid_at       DATETIME,
			created_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(miner_address, block_height)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_payouts_1175_pending ON payouts_1175 (miner_address) WHERE status = 'pending'`,
		// Additive migrations. SQLite has no ADD COLUMN IF NOT EXISTS, so a
		// duplicate-column error is the expected no-op on an already-migrated file.
		`ALTER TABLE blocks_1175 ADD COLUMN is_solo BOOLEAN DEFAULT 0`,
		`ALTER TABLE blocks_1175 ADD COLUMN finder TEXT`,
		`ALTER TABLE blocks_1175 ADD COLUMN distributed BOOLEAN DEFAULT 0`,
		`ALTER TABLE payouts_1175 ADD COLUMN batch TEXT`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			log.Printf("Warning: 1175 payout schema: %v", err)
		}
	}
}

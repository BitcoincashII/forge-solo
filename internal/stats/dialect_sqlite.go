//go:build sqlite

package stats

import (
	"log"
	"strings"
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
	return "CAST(strftime('%s', " + col + ") AS INTEGER)"
}

// olderThanSecondsExpr is true when col is further in the past than the seconds bound in
// the given placeholder.
func olderThanSecondsExpr(col, placeholder string) string {
	return col + " < datetime('now', '-' || " + placeholder + " || ' seconds')"
}

// Init1175Schema creates the 1175 merge-mining ledger tables.
//
// This existed only in the postgres backend, so on SQLite -- i.e. every Windows build --
// blocks_1175 was never created and a found aux block died in the handler with
// "no such table: blocks_1175", logged as "1175 record block FAILED (block may be lost)".
// The block was not actually lost, because the aux coinbase pays on-chain directly, but the
// ledger row and the dashboard entry were.
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

//go:build !sqlite

package stats

import "log"

// SQL that differs between the two backends.
//
// The 1175 ledger lives in payout1175.go, which is SHARED between both builds. Most of its
// SQL is portable -- $N placeholders, ON CONFLICT upserts and partial indexes all work in
// both -- so only the genuinely dialect-specific fragments live here. Keeping the split this
// narrow is deliberate: two copies of a whole query file drift, and the 1175 path is the one
// place in this app where a drift means a found block goes unrecorded.

// epochSecondsExpr renders a timestamp column as an integer Unix epoch.
func epochSecondsExpr(col string) string {
	return "EXTRACT(EPOCH FROM " + col + ")::bigint"
}

// Init1175Schema creates the 1175 merge-mining ledger tables.
//
// blocks_1175.status:  pending | confirmed | orphaned   (+ distributed bool)
// payouts_1175.status: pending | sending | paid
func Init1175Schema() {
	if db == nil {
		return
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS blocks_1175 (
			height        BIGINT PRIMARY KEY,
			hash          TEXT NOT NULL,
			gross_reward  DOUBLE PRECISION NOT NULL,
			is_solo       BOOLEAN DEFAULT false,
			finder        TEXT,
			distributed   BOOLEAN DEFAULT false,
			status        TEXT DEFAULT 'pending',
			created_at    TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS payouts_1175 (
			id           BIGSERIAL PRIMARY KEY,
			miner_address TEXT NOT NULL,
			block_height BIGINT NOT NULL,
			amount       DOUBLE PRECISION NOT NULL,
			txid         TEXT,
			status       TEXT DEFAULT 'pending',
			batch        TEXT,
			paid_at      TIMESTAMPTZ,
			created_at   TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(miner_address, block_height)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_payouts_1175_pending ON payouts_1175 (miner_address) WHERE status = 'pending'`,
		// additive migrations for pre-existing tables
		`ALTER TABLE blocks_1175 ADD COLUMN IF NOT EXISTS is_solo BOOLEAN DEFAULT false`,
		`ALTER TABLE blocks_1175 ADD COLUMN IF NOT EXISTS finder TEXT`,
		`ALTER TABLE blocks_1175 ADD COLUMN IF NOT EXISTS distributed BOOLEAN DEFAULT false`,
		`ALTER TABLE payouts_1175 ADD COLUMN IF NOT EXISTS batch TEXT`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			log.Printf("Warning: 1175 payout schema: %v", err)
		}
	}
}

package stats

import (
	"context"
	"fmt"
	"log"
	"time"
)

// 1175 merge-mining payout ledger — a reorg-safe parallel of the BCH2 payout
// subsystem. Hardened per the 2026-07-17 fund-safety audit:
//   - Payout is gated on the aux node's ACTIVE-CHAIN CONFIRMATIONS (blocks_1175.status
//     = 'confirmed'), never on height arithmetic — so orphaned aux blocks are never paid.
//   - Distribution is idempotent + reorg-aware per height (refuse if any row is already
//     paid; otherwise recompute the whole unpaid set) so a height can never over-distribute.
//   - Solo-found blocks credit the finder; a degenerate empty PPLNS window falls back to
//     the finder so a won coinbase is never stranded.
//   - Block record is durable BEFORE distribution and re-distributed by a retry sweep, so a
//     transient DB failure never permanently underpays.
//   - Payout rows use a status lifecycle (pending -> sending -> paid | orphaned); a stuck
//     'sending' row is surfaced for reconciliation, never silently frozen or blindly reverted.
//
// payouts_1175.status: pending | sending | paid | orphaned
// blocks_1175.status:  pending | confirmed | orphaned   (+ distributed bool)

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
			created_at    TIMESTAMPTZ DEFAULT NOW()
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
			created_at   TIMESTAMPTZ DEFAULT NOW(),
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

// Record1175Block durably records a found aux block (idempotent). Must be committed
// BEFORE distribution so a distribution failure is retryable and never loses the block.
func Record1175Block(height int64, hash string, grossReward float64, finder string, isSolo bool) error {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return ErrDatabaseNotInitialized
	}
	_, err := db.Exec(`
		INSERT INTO blocks_1175 (height, hash, gross_reward, finder, is_solo, distributed, status, created_at)
		VALUES ($1, $2, $3, $4, $5, false, 'pending', NOW())
		ON CONFLICT (height) DO NOTHING`,
		height, hash, grossReward, finder, isSolo)
	return err
}

// Distribute1175Block credits the block's reward (net of fee) to miners, idempotently
// and reorg-aware. No-op if already distributed. If ANY row for this height is already
// paid it refuses (never double-pays); otherwise it deletes the still-unpaid rows and
// recomputes the full set, so the aggregate for a height can never exceed one reward.
// Solo blocks credit the finder; an empty PPLNS window falls back to the finder.
func Distribute1175Block(height int64, windowSize int, poolFeePct, soloFeePct float64) error {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return ErrDatabaseNotInitialized
	}
	ctx, cancel := context.WithTimeout(context.Background(), DBTimeout)
	defer cancel()

	var hash, finder string
	var gross float64
	var isSolo, distributed bool
	var status string
	if err := db.QueryRowContext(ctx,
		`SELECT hash, gross_reward, COALESCE(finder,''), is_solo, distributed, status FROM blocks_1175 WHERE height=$1`,
		height).Scan(&hash, &gross, &finder, &isSolo, &distributed, &status); err != nil {
		return fmt.Errorf("load 1175 block %d: %w", height, err)
	}
	if distributed || status == "orphaned" {
		return nil
	}

	feePct := poolFeePct
	if isSolo {
		feePct = soloFeePct
	}
	payoutAmount := gross * (1 - feePct/100.0)

	// shares only needed for the PPLNS path
	var shares map[string]float64
	var totalWork float64
	if !isSolo {
		var err error
		if shares, totalWork, err = GetPPLNSShares(windowSize); err != nil {
			return fmt.Errorf("1175 pplns shares: %w", err)
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Refuse if a prior distribution was already (partly) paid — never double-pay.
	var paidCount int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM payouts_1175 WHERE block_height=$1 AND status IN ('sending','paid')`, height).Scan(&paidCount); err != nil {
		return err
	}
	if paidCount > 0 {
		return fmt.Errorf("1175 block %d already has paid/sending rows; refusing to redistribute", height)
	}
	// Clear any stale unpaid rows, then recompute the full set.
	if _, err = tx.ExecContext(ctx, `DELETE FROM payouts_1175 WHERE block_height=$1 AND status='pending'`, height); err != nil {
		return err
	}

	credit := func(miner string, amount float64) error {
		if amount <= 0 || miner == "" {
			return nil
		}
		_, e := tx.ExecContext(ctx, `
			INSERT INTO payouts_1175 (miner_address, block_height, amount, status, created_at)
			VALUES ($1,$2,$3,'pending',NOW())
			ON CONFLICT (miner_address, block_height) DO UPDATE
			SET amount = EXCLUDED.amount, status='pending', txid=NULL, batch=NULL, paid_at=NULL
			WHERE payouts_1175.status='pending'`, miner, height, amount)
		return e
	}

	if isSolo || totalWork <= 0 || len(shares) == 0 {
		// solo block, or degenerate empty window: pay the finder so the coinbase is never stranded.
		if finder == "" {
			return fmt.Errorf("1175 block %d has no finder and no PPLNS work; holding (manual reconcile)", height)
		}
		if isSolo {
			// finder already accounted for via soloFee
		} else {
			log.Printf("Warning: 1175 block %d empty PPLNS window; crediting finder %s", height, finder)
		}
		if err = credit(finder, payoutAmount); err != nil {
			return err
		}
	} else {
		for miner, work := range shares {
			if err = credit(miner, payoutAmount*(work/totalWork)); err != nil {
				return err
			}
		}
	}

	if _, err = tx.ExecContext(ctx, `UPDATE blocks_1175 SET distributed=true WHERE height=$1`, height); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	log.Printf("✅ 1175 block %d distributed %.8f (solo=%v)", height, payoutAmount, isSolo)
	return nil
}

// Miner1175Block is one aux (1175 / ESF) block a miner earned from, for the miner
// dashboard. Reward is the miner's own credited ESF amount (post fee/share).
type Miner1175Block struct {
	Height   int64   `json:"height"`
	Hash     string  `json:"hash"`
	Reward   float64 `json:"reward"`
	SharePct float64 `json:"share_pct"`
	Time     int64   `json:"time"`
	Status   string  `json:"status"` // block status: pending|confirmed|orphaned
}

// Get1175BlocksForMiner returns the 1175 blocks a miner earned from for the given mode
// (isSolo true = solo blocks it found; false = PPLNS blocks it shared in). Keyed by the
// BCH2 mining address, which is exactly payouts_1175.miner_address (the 1175 payout
// address is only resolved at send time), so the caller passes the same normalized
// address the rest of the dashboard uses.
func Get1175BlocksForMiner(minerID string, isSolo bool, limit int) ([]Miner1175Block, error) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return nil, ErrDatabaseNotInitialized
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	ctx, cancel := context.WithTimeout(context.Background(), DBTimeout)
	defer cancel()
	rows, err := db.QueryContext(ctx, `
		SELECT b.height, b.hash, p.amount, b.gross_reward,
		       EXTRACT(EPOCH FROM b.created_at)::bigint, b.status
		FROM payouts_1175 p
		JOIN blocks_1175 b ON p.block_height = b.height
		WHERE p.miner_address = $1 AND b.is_solo = $2
		ORDER BY b.height DESC
		LIMIT $3`, minerID, isSolo, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Miner1175Block, 0, limit)
	for rows.Next() {
		var mb Miner1175Block
		var gross float64
		if err := rows.Scan(&mb.Height, &mb.Hash, &mb.Reward, &gross, &mb.Time, &mb.Status); err != nil {
			return nil, err
		}
		if gross > 0 {
			mb.SharePct = mb.Reward / gross * 100
		}
		out = append(out, mb)
	}
	return out, rows.Err()
}

// UndistributedBlocks1175 returns heights recorded but not yet distributed and not
// orphaned — the retry set for a distribution that failed transiently.
func UndistributedBlocks1175() ([]int64, error) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return nil, ErrDatabaseNotInitialized
	}
	rows, err := db.Query(`SELECT height FROM blocks_1175 WHERE distributed=false AND status<>'orphaned' ORDER BY height`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var h int64
		if rows.Scan(&h) == nil {
			out = append(out, h)
		}
	}
	return out, rows.Err()
}

// UnconfirmedBlocks1175 returns (height, hash) of blocks still 'pending' confirmation —
// the processor checks each against the aux node's active chain and calls Confirm/Orphan.
func UnconfirmedBlocks1175() ([][2]interface{}, error) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return nil, ErrDatabaseNotInitialized
	}
	rows, err := db.Query(`SELECT height, hash FROM blocks_1175 WHERE status='pending' ORDER BY height`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][2]interface{}
	for rows.Next() {
		var h int64
		var hash string
		if rows.Scan(&h, &hash) == nil {
			out = append(out, [2]interface{}{h, hash})
		}
	}
	return out, rows.Err()
}

// Confirm1175Block marks a block confirmed (mature on the active chain) so its credits
// become payable.
func Confirm1175Block(height int64) error {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return ErrDatabaseNotInitialized
	}
	_, err := db.Exec(`UPDATE blocks_1175 SET status='confirmed' WHERE height=$1 AND status='pending'`, height)
	return err
}

// Settle1175ByCoinbase marks a miner's confirmed-pending 1175 credits as paid. 1175 is
// aux-coinbase-direct: getauxblock builds the aux coinbase to pay PAYOUT_ADDRESS_1175
// on-chain, so the reward is already delivered — there is no secondary send (that would
// be a double-pay). Only credits on CONFIRMED blocks are settled (matching
// ConfirmedPendingMiners1175), so an orphaned block's credits are voided first and never
// marked paid. Returns the number of credits settled.
func Settle1175ByCoinbase(miner string) (int64, error) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return 0, ErrDatabaseNotInitialized
	}
	res, err := db.Exec(`
		UPDATE payouts_1175 SET status='paid', txid='coinbase-direct', paid_at=NOW()
		WHERE miner_address=$1 AND status='pending'
		  AND block_height IN (SELECT height FROM blocks_1175 WHERE status='confirmed')`, miner)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// Orphan1175Block marks a block orphaned and voids its still-unpaid credits (never touches
// sending/paid rows). Atomic.
func Orphan1175Block(height int64) error {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return ErrDatabaseNotInitialized
	}
	ctx, cancel := context.WithTimeout(context.Background(), DBTimeout)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE blocks_1175 SET status='orphaned' WHERE height=$1`, height); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE payouts_1175 SET status='orphaned', txid='orphaned' WHERE block_height=$1 AND status='pending'`, height); err != nil {
		return err
	}
	log.Printf("⚠️ 1175 block %d orphaned; voided its unpaid credits", height)
	return tx.Commit()
}

// ConfirmedPendingMiners1175 lists miners with payable (pending, on a confirmed block) credits.
func ConfirmedPendingMiners1175() ([]string, error) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return nil, ErrDatabaseNotInitialized
	}
	rows, err := db.Query(`
		SELECT DISTINCT p.miner_address FROM payouts_1175 p
		JOIN blocks_1175 b ON b.height = p.block_height
		WHERE p.status='pending' AND b.status='confirmed'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if rows.Scan(&m) == nil {
			out = append(out, m)
		}
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────────────────
// QUARANTINE — the send pipeline below (Process1175PayoutAtomic, Finalize1175Payout,
// Revert1175PayoutMark, StuckSending1175, Get1175PayoutAddress) is a pool-style
// reserve→send→finalize path that is NOT used by the SOLO app. Solo 1175 is COINBASE-DIRECT:
// getauxblock builds the aux coinbase paying PAYOUT_ADDRESS_1175 on-chain, and run1175PayoutCycle
// settles the ledger via Settle1175ByCoinbase ONLY — there is no sendtoaddress for 1175.
// ⛔ DO NOT wire these into the 1175 cycle: a secondary send on top of the coinbase payment would
// DOUBLE-PAY. Any 1175 send path must go through an explicit design review first.
// ─────────────────────────────────────────────────────────────────────────────────────────
// Process1175PayoutAtomic locks and sums a miner's payable credits (pending rows on
// CONFIRMED blocks — the confirmation gate replaces the old height arithmetic) and, if
// they meet minPayout, marks them 'sending' under a batch id. Returns (batch, amount).
func Process1175PayoutAtomic(minerID string, minPayout float64) (string, float64, error) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return "", 0, ErrDatabaseNotInitialized
	}
	if len(minerID) < 8 {
		return "", 0, fmt.Errorf("invalid miner ID: too short")
	}
	ctx, cancel := context.WithTimeout(context.Background(), DBTimeout)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", 0, err
	}
	defer tx.Rollback()

	var amount float64
	if err = tx.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(amount),0) FROM (
			SELECT p.amount FROM payouts_1175 p
			JOIN blocks_1175 b ON b.height = p.block_height
			WHERE p.miner_address=$1 AND p.status='pending' AND b.status='confirmed'
			FOR UPDATE OF p
		) t`, minerID).Scan(&amount); err != nil {
		return "", 0, fmt.Errorf("sum 1175 payable: %w", err)
	}
	if amount < minPayout {
		return "", 0, fmt.Errorf("insufficient 1175 payable: %.8f < %.8f", amount, minPayout)
	}
	batch := fmt.Sprintf("batch_%d_%s", time.Now().UnixNano(), minerID[:8])
	if _, err = tx.ExecContext(ctx, `
		UPDATE payouts_1175 SET status='sending', batch=$1, paid_at=$2
		WHERE miner_address=$3 AND status='pending'
		  AND block_height IN (SELECT height FROM blocks_1175 WHERE status='confirmed')`,
		batch, time.Now(), minerID); err != nil {
		return "", 0, fmt.Errorf("mark 1175 sending: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return "", 0, err
	}
	return batch, amount, nil
}

// Finalize1175Payout records the real txid and marks a batch paid.
func Finalize1175Payout(batch, actualTxid string) error {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return ErrDatabaseNotInitialized
	}
	_, err := db.Exec(`UPDATE payouts_1175 SET txid=$1, status='paid' WHERE batch=$2 AND status='sending'`, actualTxid, batch)
	return err
}

// Revert1175PayoutMark returns a batch to 'pending' so it is retried (call ONLY when the
// send is provably not broadcast — never on a staleness heuristic).
func Revert1175PayoutMark(batch string) error {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return ErrDatabaseNotInitialized
	}
	_, err := db.Exec(`UPDATE payouts_1175 SET status='pending', batch=NULL, paid_at=NULL WHERE batch=$1 AND status='sending'`, batch)
	return err
}

// StuckSending1175 lists batches stuck in 'sending' longer than graceSeconds — an
// ambiguous send that must be reconciled against the wallet manually (surfaced, not
// silently frozen, and never auto-reverted to avoid a crash-after-send double-pay).
func StuckSending1175(graceSeconds int) ([]string, error) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return nil, ErrDatabaseNotInitialized
	}
	rows, err := db.Query(`SELECT DISTINCT batch FROM payouts_1175 WHERE status='sending' AND paid_at < NOW() - ($1 || ' seconds')::interval`, graceSeconds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var b string
		if rows.Scan(&b) == nil {
			out = append(out, b)
		}
	}
	return out, rows.Err()
}

// Get1175PayoutAddress returns the miner's supplied esf1... 1175 address, or "".
func Get1175PayoutAddress(minerID string) (string, error) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return "", ErrDatabaseNotInitialized
	}
	var addr string
	err := db.QueryRow(`SELECT COALESCE(address_1175, '') FROM miners WHERE address = $1`, minerID).Scan(&addr)
	return addr, err
}

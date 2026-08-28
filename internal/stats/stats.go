package stats

import (
	"crypto/rand"
	"fmt"
	"sync"
	"time"
)

type ShareRecord struct {
	Time       time.Time
	Difficulty float64
}

// CircularShareBuffer is a fixed-size ring buffer for share records
// This prevents unbounded memory growth while maintaining O(1) insertion
// Thread-safe with internal mutex
type CircularShareBuffer struct {
	mu      sync.RWMutex
	records []ShareRecord
	head    int // Next write position
	size    int // Number of valid records
	cap     int // Maximum capacity
}

// NewCircularShareBuffer creates a share ring bounded to capacity records.
//
// The backing array grows on demand instead of being allocated up front. At
// MaxSharesPerWorker a full ring is ~320 KB (10000 * a 32-byte ShareRecord), and the worker
// map is keyed by miner+worker name, so every distinct name used to cost that much from its
// very first share -- a marketplace order that rotates worker names could pin hundreds of
// megabytes of buffers holding a handful of shares each.
func NewCircularShareBuffer(capacity int) *CircularShareBuffer {
	return &CircularShareBuffer{cap: capacity}
}

// Add adds a share record to the buffer, overwriting oldest if full
func (b *CircularShareBuffer) Add(r ShareRecord) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cap <= 0 {
		return // a zero-capacity ring has nowhere to write; % 0 would panic
	}
	if len(b.records) < b.cap {
		// Still filling. head == len(records) here, so appending keeps the
		// oldest-at-index-0 layout GetRecordsAfter walks, and head lands back on 0
		// exactly when the ring fills and overwriting must begin.
		b.records = append(b.records, r)
		b.size = len(b.records)
		b.head = b.size % b.cap
		return
	}
	b.records[b.head] = r
	b.head = (b.head + 1) % b.cap
	if b.size < b.cap {
		b.size++
	}
}

// GetRecordsAfter returns all records after the given time
func (b *CircularShareBuffer) GetRecordsAfter(cutoff time.Time) []ShareRecord {
	b.mu.RLock()
	defer b.mu.RUnlock()
	result := make([]ShareRecord, 0, b.size)
	for i := 0; i < b.size; i++ {
		// Read from oldest to newest
		idx := (b.head - b.size + i + b.cap) % b.cap
		if b.records[idx].Time.After(cutoff) {
			result = append(result, b.records[idx])
		}
	}
	return result
}

type WorkerStats struct {
	MinerID     string  `json:"miner_id"`
	WorkerName  string  `json:"worker_name"`
	Online      bool    `json:"online"`
	Hashrate5m  float64 `json:"hashrate_5m"`
	Hashrate60m float64 `json:"hashrate_60m"`
	ValidShares int64   `json:"valid_shares"`
	// RoundShares counts accepted shares SINCE THE LAST BLOCK. ValidShares is
	// all-time and is never reset, so the "Round Shares" tile -- which was fed
	// straight from it -- showed the same number as the all-time "Valid Shares"
	// tile beside it, and sat next to a Current Effort and Best Difficulty that
	// the round reset HAD cleared to zero.
	RoundShares   int64                `json:"round_shares"`
	InvalidShares int64                `json:"invalid_shares"`
	BestDiff      float64              `json:"best_diff"`       // Best share this round (resets on block found)
	RoundBestDiff float64              `json:"round_best_diff"` // Alias for best_diff for UI compatibility
	ATHDiff       float64              `json:"ath_diff"`        // All-time high share difficulty
	TotalWork     float64              `json:"total_work"`      // Cumulative share difficulty for round effort
	BlocksFound   int64                `json:"blocks_found"`    // Number of blocks found by this worker
	LastShareAt   time.Time            `json:"last_share_at"`
	ConnectedAt   time.Time            `json:"connected_at"`
	ShareBuffer   *CircularShareBuffer `json:"-"` // Use circular buffer instead of slice
}

type PoolStats struct {
	Hashrate      float64   `json:"hashrate"`
	Workers       int       `json:"workers"`
	BlocksFound   int64     `json:"blocks_found"`
	LastBlockAt   time.Time `json:"last_block_at"`
	LastBlockHash string    `json:"last_block_hash"`
	RoundEffort   float64   `json:"round_effort"` // Cumulative share difficulty this round
	AvgLuck       float64   `json:"avg_luck"`     // Average luck over recent blocks (0-1 scale, <1 is lucky)
}

type StatsManager struct {
	workers     map[string]*WorkerStats
	poolStats   PoolStats
	roundEffort float64   // Cumulative share difficulty for current round
	luckHistory []float64 // Recent block luck values (capped at 100)
	mu          sync.RWMutex
}

var (
	manager *StatsManager
	once    sync.Once
)

func GetManager() *StatsManager {
	once.Do(func() {
		manager = &StatsManager{
			workers: make(map[string]*WorkerStats),
		}
	})
	return manager
}

func (m *StatsManager) UpdateWorker(minerID, workerName string, valid bool, targetDiff, actualDiff float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := minerID + ":" + workerName
	w, exists := m.workers[key]
	if !exists {
		w = &WorkerStats{
			MinerID:     minerID,
			WorkerName:  workerName,
			ConnectedAt: time.Now(),
			ShareBuffer: NewCircularShareBuffer(MaxSharesPerWorker), // Fixed-size buffer
		}
		m.workers[key] = w
	}

	w.Online = true
	w.LastShareAt = time.Now()

	// Add share to the ring: O(1), and the backing array grows only until it reaches cap.
	// Use target difficulty for hashrate calculation (consistent work credit)
	w.ShareBuffer.Add(ShareRecord{
		Time:       time.Now(),
		Difficulty: targetDiff,
	})

	if valid {
		w.ValidShares++
		w.RoundShares++
		w.TotalWork += targetDiff   // Accumulate work for round effort calculation
		m.roundEffort += targetDiff // Track pool-wide round effort for luck calculation
		// Use ACTUAL share difficulty for best share tracking
		if actualDiff > w.BestDiff {
			w.BestDiff = actualDiff
			w.RoundBestDiff = actualDiff // Keep in sync for UI compatibility
		}
		if actualDiff > w.ATHDiff {
			w.ATHDiff = actualDiff
		}
	} else {
		w.InvalidShares++
	}

	// Calculate hashrates using shares from the last hour
	shares := w.ShareBuffer.GetRecordsAfter(time.Now().Add(-ShareHistoryDuration))
	w.Hashrate5m = m.calculateHashrate(shares, 5*time.Minute)
	w.Hashrate60m = m.calculateHashrate(shares, 60*time.Minute)
}

// RecordInvalidShare counts a rejected share against a worker.
//
// Deliberately NOT UpdateWorker(valid=false): that path also pushes the share
// into the hashrate buffer and stamps LastShareAt, so rejects would inflate the
// worker's reported hashrate and keep a miner that is producing nothing but
// stales looking healthy. A reject is not work. It is counted, and nothing else.
func (m *StatsManager) RecordInvalidShare(minerID, workerName string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := minerID + ":" + workerName
	w, exists := m.workers[key]
	if !exists {
		w = &WorkerStats{
			MinerID:     minerID,
			WorkerName:  workerName,
			ConnectedAt: time.Now(),
			ShareBuffer: NewCircularShareBuffer(MaxSharesPerWorker),
		}
		m.workers[key] = w
	}
	w.Online = true
	w.InvalidShares++
}

func (m *StatsManager) calculateHashrate(shares []ShareRecord, window time.Duration) float64 {
	cutoff := time.Now().Add(-window)

	var totalWork float64
	for _, s := range shares {
		if s.Time.After(cutoff) {
			totalWork += s.Difficulty
		}
	}

	if totalWork == 0 {
		return 0
	}

	seconds := window.Seconds()
	// Hashrate = total_difficulty * 2^32 / seconds
	// Result in TH/s
	hashrate := totalWork * 4294967296.0 / seconds / 1e12
	return hashrate
}

// MarkStaleWorkersOffline marks workers as offline if they haven't submitted shares recently
func (m *StatsManager) MarkStaleWorkersOffline() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	cutoff := time.Now().Add(-WorkerOfflineThreshold)
	count := 0

	for _, w := range m.workers {
		if w.Online && w.LastShareAt.Before(cutoff) {
			w.Online = false
			count++
		}
	}

	return count
}

// PruneStaleWorkers drops workers silent for longer than maxAge, releasing their share
// buffers. Nothing else ever removes from the worker map -- MarkStaleWorkersOffline only
// flips a bool -- so without this a renamed rig, or a marketplace order that rotates worker
// names, pins one buffer per name for the life of the stratum process.
func (m *StatsManager) PruneStaleWorkers(maxAge time.Duration) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	pruned := 0
	for key, w := range m.workers {
		if !w.Online && w.LastShareAt.Before(cutoff) {
			delete(m.workers, key)
			pruned++
		}
	}
	return pruned
}

// StartWorkerTimeoutChecker starts a background goroutine to mark stale workers offline
func (m *StatsManager) StartWorkerTimeoutChecker(stopCh <-chan struct{}) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			m.MarkStaleWorkersOffline()
			m.PruneStaleWorkers(WorkerRetention)
		}
	}
}

func (m *StatsManager) ResetWorkerRoundStats(minerID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for key, w := range m.workers {
		if w.MinerID == minerID {
			m.workers[key].BestDiff = 0
			m.workers[key].RoundBestDiff = 0
			m.workers[key].TotalWork = 0 // Reset round effort tracking
			m.workers[key].RoundShares = 0
		}
	}
}

// ResetAllWorkerRoundStats resets round stats for all workers (called after block found)
func (m *StatsManager) ResetAllWorkerRoundStats() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for key := range m.workers {
		m.workers[key].BestDiff = 0
		m.workers[key].RoundBestDiff = 0
		m.workers[key].TotalWork = 0
		m.workers[key].RoundShares = 0
	}
}

func (m *StatsManager) GetAllWorkerStats() []*WorkerStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	now := time.Now()
	cutoff := now.Add(-5 * time.Minute)

	var result []*WorkerStats
	for _, w := range m.workers {
		wCopy := *w
		wCopy.Online = w.LastShareAt.After(cutoff)

		// Recompute the hashrate windows at READ time, not just when a share arrives.
		//
		// UpdateWorker is the only writer of these fields, so a worker that stopped
		// submitting kept advertising whatever it was doing at its last share --
		// forever. An unplugged 100 TH/s rig went on reporting "100.00 TH/s" under a
		// column header that says "5m Hash", in a row this same function had already
		// marked offline, directly beneath a tile (which sums only online workers)
		// reading 0 H/s. Recomputing here lets both windows decay to 0 on their own
		// as the shares age out of the buffer.
		if w.ShareBuffer != nil {
			shares := w.ShareBuffer.GetRecordsAfter(now.Add(-ShareHistoryDuration))
			wCopy.Hashrate5m = m.calculateHashrate(shares, 5*time.Minute)
			wCopy.Hashrate60m = m.calculateHashrate(shares, 60*time.Minute)
		}

		// Get block count for this worker
		wCopy.BlocksFound = GetWorkerBlockCount(w.MinerID, w.WorkerName)
		result = append(result, &wCopy)
	}
	return result
}

// RecordBlockWithEffort records a block and calculates luck based on effort vs network difficulty
func (m *StatsManager) RecordBlockWithEffort(hash string, networkDiff float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.poolStats.BlocksFound++
	m.poolStats.LastBlockAt = time.Now()
	m.poolStats.LastBlockHash = hash

	// Calculate luck for this block
	// Luck = expected effort (network diff) / actual effort (round shares)
	// <1 means lucky (found faster), >1 means unlucky (took longer)
	if m.roundEffort > 0 && networkDiff > 0 {
		blockLuck := networkDiff / m.roundEffort
		m.luckHistory = append(m.luckHistory, blockLuck)
		// Keep only last 100 blocks
		if len(m.luckHistory) > 100 {
			m.luckHistory = m.luckHistory[len(m.luckHistory)-100:]
		}
	}

	// Store current round effort before resetting
	m.poolStats.RoundEffort = m.roundEffort
	// Reset round effort for next block
	m.roundEffort = 0
}

func (m *StatsManager) GetPoolStats() PoolStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var totalHashrate float64
	var onlineWorkers int
	cutoff := time.Now().Add(-5 * time.Minute)

	for _, w := range m.workers {
		if w.LastShareAt.After(cutoff) {
			totalHashrate += w.Hashrate60m
			onlineWorkers++
		}
	}

	// Calculate average luck
	var avgLuck float64 = 1.0 // Default to 100%
	if len(m.luckHistory) > 0 {
		var sum float64
		for _, luck := range m.luckHistory {
			sum += luck
		}
		avgLuck = sum / float64(len(m.luckHistory))
	}

	return PoolStats{
		Hashrate:      totalHashrate,
		Workers:       onlineWorkers,
		BlocksFound:   m.poolStats.BlocksFound,
		LastBlockAt:   m.poolStats.LastBlockAt,
		LastBlockHash: m.poolStats.LastBlockHash,
		RoundEffort:   m.roundEffort,
		AvgLuck:       avgLuck,
	}
}

// Block tracking per miner
type MinerBlock struct {
	Height     int64     `json:"height"`
	Hash       string    `json:"hash"`
	MinerID    string    `json:"miner_id"`
	WorkerName string    `json:"worker_name"`
	Reward     float64   `json:"reward"`
	Time       time.Time `json:"time"`
	Confirmed  bool      `json:"confirmed"`
	PayoutTxid string    `json:"payoutTxid,omitempty"`
}

var (
	minerBlocks      = make(map[string][]MinerBlock) // minerID -> blocks
	workerBlockCount = make(map[string]int64)        // minerID:workerName -> block count
	minerBlocksMu    sync.RWMutex
)

func RecordMinerBlockWithWorkerSolo(minerID, workerName string, height int64, hash string, reward float64, isSolo bool) {
	minerBlocksMu.Lock()
	defer minerBlocksMu.Unlock()

	block := MinerBlock{
		Height:     height,
		Hash:       hash,
		MinerID:    minerID,
		WorkerName: workerName,
		Reward:     reward,
		Time:       time.Now(),
		Confirmed:  false,
	}

	minerBlocks[minerID] = append(minerBlocks[minerID], block)

	// Track per-worker block count
	if workerName != "" {
		key := minerID + ":" + workerName
		workerBlockCount[key]++
	}

	// Keep only last 1000 blocks per miner
	if len(minerBlocks[minerID]) > 1000 {
		minerBlocks[minerID] = minerBlocks[minerID][len(minerBlocks[minerID])-1000:]
	}

	// Save to database with is_solo flag
	SaveBlockDBWithSolo(minerID, height, hash, reward, isSolo)
}

// GetWorkerBlockCount returns the number of blocks found by a specific worker
func GetWorkerBlockCount(minerID, workerName string) int64 {
	minerBlocksMu.RLock()
	defer minerBlocksMu.RUnlock()
	key := minerID + ":" + workerName
	return workerBlockCount[key]
}

func GetMinerBlocks(minerID string) []MinerBlock {
	minerBlocksMu.RLock()
	defer minerBlocksMu.RUnlock()

	blocks, exists := minerBlocks[minerID]
	if !exists {
		return []MinerBlock{}
	}

	// Return copy in reverse order (newest first)
	result := make([]MinerBlock, len(blocks))
	for i, b := range blocks {
		result[len(blocks)-1-i] = b
	}
	return result
}

// Payout tracking (COINBASE_MATURITY is in constants.go)

type PendingPayout struct {
	ID          string    `json:"id"` // Unique identifier (UUID)
	MinerID     string    `json:"miner_id"`
	BlockHeight int64     `json:"block_height"`
	Amount      float64   `json:"amount"`
	PaidAmount  float64   `json:"paid_amount"` // Track partial payments for split payouts
	Confirmed   bool      `json:"confirmed"`
	TxIDs       []string  `json:"txids"` // Multiple txids for split payouts
	TxID        string    `json:"txid"`  // Primary/last txid (backwards compat)
	CreatedAt   time.Time `json:"created_at"`
	PaidAt      time.Time `json:"paid_at,omitempty"`
}

// generateUUID creates a simple UUID for payout tracking
func generateUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

var (
	pendingPayouts   = make(map[string][]PendingPayout)
	pendingPayoutsMu sync.RWMutex
)

func AddPendingPayout(minerID string, blockHeight int64, amount float64) {
	pendingPayoutsMu.Lock()
	defer pendingPayoutsMu.Unlock()

	payout := PendingPayout{
		ID:          generateUUID(),
		MinerID:     minerID,
		BlockHeight: blockHeight,
		Amount:      amount,
		PaidAmount:  0,
		Confirmed:   false,
		TxIDs:       []string{},
		CreatedAt:   time.Now(),
	}

	pendingPayouts[minerID] = append(pendingPayouts[minerID], payout)
}

func GetMinerBalance(minerID string, currentHeight int64) (mature float64, immature float64) {
	pendingPayoutsMu.RLock()
	defer pendingPayoutsMu.RUnlock()

	payouts, exists := pendingPayouts[minerID]
	if !exists {
		return 0, 0
	}

	for _, p := range payouts {
		if p.TxID != "" {
			continue
		}
		confirmations := currentHeight - p.BlockHeight
		if confirmations >= COINBASE_MATURITY {
			mature += p.Amount
		} else {
			immature += p.Amount
		}
	}
	return
}

// CleanupPaidPayouts removes old paid payouts from memory to prevent unbounded growth
// Retains payouts for 24 hours after payment to allow UI to display recent transactions
func CleanupPaidPayouts() {
	pendingPayoutsMu.Lock()
	defer pendingPayoutsMu.Unlock()

	cutoff := time.Now().Add(-24 * time.Hour)
	totalRemoved := 0

	for minerID, payouts := range pendingPayouts {
		// Filter to keep only unpaid or recently paid
		filtered := make([]PendingPayout, 0, len(payouts))
		for _, p := range payouts {
			// Keep if unpaid OR paid within last 24 hours
			if p.TxID == "" || p.PaidAt.After(cutoff) {
				filtered = append(filtered, p)
			} else {
				totalRemoved++
			}
		}

		if len(filtered) == 0 {
			delete(pendingPayouts, minerID)
		} else if len(filtered) < len(payouts) {
			pendingPayouts[minerID] = filtered
		}
	}
}

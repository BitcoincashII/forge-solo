package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/bch2/forge-pool/internal/mergemining"
	"github.com/bch2/forge-pool/internal/mining"
	"github.com/bch2/forge-pool/internal/stats"
	"github.com/bch2/forge-pool/internal/stratum"
	"github.com/bch2/forge-pool/internal/stratumv2"
	"github.com/go-zeromq/zmq4"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

var (
	logger               *zap.Logger
	jobManager           *mining.JobManager
	currentJob           *mining.Job
	currentJobMu         sync.RWMutex                   // Protects currentJob access
	jobHistory           = make(map[string]*mining.Job) // Store jobs by ID for block submission
	jobHistoryOrder      []string                       // Track insertion order for FIFO cleanup
	jobHistoryMu         sync.RWMutex
	rpcURL               string
	walletRPCURL         string // RPC URL with wallet path for sendtoaddress
	rpcUser              string
	rpcPass              string
	networkDifficulty    float64      = 1.0
	latestCoinbaseBTC    float64      // most recent getblocktemplate coinbasevalue in BTC (guarded by networkDiffMu)
	networkDiffMu        sync.RWMutex // Protects networkDifficulty + latestCoinbaseBTC access
	poolAddress          string
	poolFee              float64           = 1.0 // PPLNS fee percentage
	soloFee              float64           = 0.5 // Solo fee percentage
	blockReward          float64           = 50.0
	minPayout            float64           = 5.0
	minPayoutMu          sync.RWMutex // guards minPayout: pool_config watcher writes vs payout processor reads
	pplnsWindow          int               = 100000 // PPLNS window size (shares)
	stratumServer        *stratum.Server            // Global reference for API handlers
	stratumBraiinsServer *stratum.Server            // Second stratum for Braiins (8-byte extranonce2)
	stratumV2Server      *stratumv2.Server          // Stratum V2 server (optional)
	v2JobIDCounter       uint32                     // V2 job ID counter

	// Shutdown channel for graceful termination
	shutdownCh = make(chan struct{})

	// ZMQ new block notification channel for instant block detection
	zmqBlockCh = make(chan string, 10)

	// Security: Payout mutex to prevent concurrent payout processing per miner
	payoutMu         sync.Mutex
	payoutInProgress = make(map[string]time.Time) // Track active payout requests per miner
	payoutMuMap      sync.RWMutex

	// Security: Required internal API token (must be set in environment)
	internalAPIToken string

	// Global HTTP client for RPC calls (reuses connections)
	httpClient = &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
		},
	}
)

// Thread-safe access to currentJob
func getCurrentJob() *mining.Job {
	currentJobMu.RLock()
	defer currentJobMu.RUnlock()
	return currentJob
}

func setCurrentJob(job *mining.Job) {
	currentJobMu.Lock()
	defer currentJobMu.Unlock()
	currentJob = job
}

// Thread-safe access to networkDifficulty
func getNetworkDifficulty() float64 {
	networkDiffMu.RLock()
	defer networkDiffMu.RUnlock()
	return networkDifficulty
}

func setNetworkDifficulty(diff float64) {
	networkDiffMu.Lock()
	defer networkDiffMu.Unlock()
	networkDifficulty = diff
}

func setLatestCoinbaseBTC(v float64) {
	networkDiffMu.Lock()
	defer networkDiffMu.Unlock()
	latestCoinbaseBTC = v
}

func getLatestCoinbaseBTC() float64 {
	networkDiffMu.RLock()
	defer networkDiffMu.RUnlock()
	return latestCoinbaseBTC
}

// Thread-safe access to minPayout. The runtime pool_config watcher mutates it while the
// payout processor reads it, so route both through this guard (mirrors the networkDiff pair).
func getMinPayout() float64 {
	minPayoutMu.RLock()
	defer minPayoutMu.RUnlock()
	return minPayout
}

func setMinPayout(v float64) {
	minPayoutMu.Lock()
	defer minPayoutMu.Unlock()
	minPayout = v
}

// ---- 1175 merge-mining payout ----

var (
	merge1175Enabled bool
	aux1175NodeURL   string
	aux1175WalletURL string
	aux1175User      string
	aux1175Pass      string
	min1175Payout    float64
)

// rpcCallAuth is like rpcCall but with explicit credentials, for the aux (1175)
// node whose RPC creds differ from the BCH2 node's.
func rpcCallAuth(url, user, pass, method string, params []interface{}) (interface{}, error) {
	reqBody, err := json.Marshal(map[string]interface{}{"jsonrpc": "1.0", "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(user, pass)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var result struct {
		Result interface{} `json:"result"`
		Error  interface{} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if result.Error != nil {
		// Prefix MUST match isDefinitelyNotBroadcast ("RPC error:") so a node-level
		// sendtoaddress rejection is correctly classified as definitely-not-broadcast.
		return nil, fmt.Errorf("RPC error: %v", result.Error)
	}
	return result.Result, nil
}

func sendPayoutAuth(walletURL, user, pass, address string, amount float64) (string, error) {
	result, err := rpcCallAuth(walletURL, user, pass, "sendtoaddress", []interface{}{address, amount})
	if err != nil {
		return "", err
	}
	txid, ok := result.(string)
	if !ok {
		return "", fmt.Errorf("unexpected sendtoaddress response: %T", result)
	}
	return txid, nil
}

// aux1175Maturity is the active-chain confirmation depth a 1175 aux block must reach
// before its credits are payable (1175 coinbase maturity).
const aux1175Maturity = 100

// aux1175BlockHandler durably records a found aux block and distributes its reward.
// The record is committed BEFORE distribution so a transient distribution failure is
// retried by the processor rather than losing the block. Invoked (in the stratum
// submit goroutine) only when submitauxblock is accepted.
func aux1175BlockHandler(height int64, hash string, coinbaseValueSat int64, finder string, isSolo bool) {
	gross := float64(coinbaseValueSat) / 1e8
	if err := stats.Record1175Block(height, hash, gross, finder, isSolo); err != nil {
		logger.Error("1175 record block FAILED (block may be lost — verify)", zap.Int64("height", height), zap.String("hash", hash), zap.Error(err))
		return
	}
	if err := stats.Distribute1175Block(height, pplnsWindow, poolFee, soloFee); err != nil {
		logger.Warn("1175 distribute failed (processor will retry)", zap.Int64("height", height), zap.Error(err))
		return
	}
	logger.Info("💠 1175 block distributed", zap.Int64("height", height), zap.Float64("gross", gross), zap.Bool("solo", isSolo))
}

// aux1175BlockConfirmations returns the aux block's confirmations on the 1175 node's
// ACTIVE chain, and whether it was found. A block off the active chain returns -1;
// an unknown hash returns found=false. This is the reorg-safety oracle: payout is
// gated on chain MEMBERSHIP + depth, never on height arithmetic.
func aux1175BlockConfirmations(hash string) (int64, bool) {
	res, err := rpcCallAuth(aux1175NodeURL, aux1175User, aux1175Pass, "getblock", []interface{}{hash})
	if err != nil {
		return -1, false
	}
	m, ok := res.(map[string]interface{})
	if !ok {
		return -1, false
	}
	cf, ok := m["confirmations"].(float64)
	if !ok {
		return -1, false
	}
	return int64(cf), true
}

func start1175PayoutProcessor() {
	ticker := time.NewTicker(120 * time.Second)
	defer ticker.Stop()
	logger.Info("💰 1175 payout processor started", zap.Float64("min_payout", min1175Payout))
	for {
		select {
		case <-shutdownCh:
			return
		case <-ticker.C:
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					logger.Error("1175 payout cycle panic recovered", zap.Any("panic", r))
				}
			}()
			run1175PayoutCycle()
		}()
	}
}

// run1175PayoutCycle: (1) retry undistributed blocks, (2) reconcile pending blocks
// against the aux chain (confirm mature ones, orphan reorged ones + void their unpaid
// credits), (3) pay confirmed credits, (4) surface stuck 'sending' batches.
func run1175PayoutCycle() {
	// 1. retry any distribution that failed transiently
	if heights, err := stats.UndistributedBlocks1175(); err == nil {
		for _, h := range heights {
			if err := stats.Distribute1175Block(h, pplnsWindow, poolFee, soloFee); err != nil {
				logger.Warn("1175 re-distribute failed", zap.Int64("height", h), zap.Error(err))
			}
		}
	}
	// 2. reconcile: confirm mature blocks, orphan reorged ones (voids their unpaid credits)
	if blocks, err := stats.UnconfirmedBlocks1175(); err == nil {
		for _, hb := range blocks {
			height := hb[0].(int64)
			hash := hb[1].(string)
			conf, found := aux1175BlockConfirmations(hash)
			// CRITICAL: found=false means the aux node could not be consulted (RPC error /
			// node down / restarting / behind), NOT that the block reorged. Skip so a node
			// blip never voids valid credits. Only conf<0 (node HAS the block on a side
			// branch) is a genuine orphan.
			if !found {
				continue // leave pending, retry next cycle
			}
			if conf < 0 {
				if err := stats.Orphan1175Block(height); err != nil {
					logger.Warn("1175 orphan mark failed", zap.Int64("height", height), zap.Error(err))
				}
			} else if conf >= aux1175Maturity {
				if err := stats.Confirm1175Block(height); err != nil {
					logger.Warn("1175 confirm mark failed", zap.Int64("height", height), zap.Error(err))
				}
			}
		}
	}
	// 3. pay miners with payable (pending, on a confirmed block) credits
	miners, err := stats.ConfirmedPendingMiners1175()
	if err != nil {
		return
	}
	for _, miner := range miners {
		// 1175 is aux-coinbase-DIRECT: getauxblock builds the aux coinbase to pay
		// PAYOUT_ADDRESS_1175 on-chain, so the reward is already delivered when the block
		// matures. Settle the confirmed credit as paid-by-coinbase — do NOT run a secondary
		// sendtoaddress (that would be a double-pay, and the per-miner address_1175 is a
		// leftover pool concept that does not apply to a coinbase-direct solo miner). Only
		// confirmed (mature + active-chain) credits are settled; orphaned ones are voided in
		// step 2 above, so a reorged block is never marked paid.
		n, err := stats.Settle1175ByCoinbase(miner)
		if err != nil {
			logger.Warn("1175 settle-by-coinbase failed", zap.String("miner", miner), zap.Error(err))
			continue
		}
		if n > 0 {
			logger.Info("✅ 1175 reward settled (paid on-chain by aux coinbase)",
				zap.String("miner", miner), zap.Int64("credits", n))
		}
	}
	// 4. surface stuck 'sending' batches (ambiguous send / crash between mark and finalize)
	if stuck, err := stats.StuckSending1175(600); err == nil && len(stuck) > 0 {
		logger.Error("1175 payouts STUCK in 'sending' — reconcile against the 1175 wallet manually", zap.Strings("batches", stuck))
	}
}

func startPayoutProcessor() {
	ticker := time.NewTicker(60 * time.Second) // Check every minute
	defer ticker.Stop()

	// Use global rpcURL configured from config file
	nodeURL := rpcURL

	// Track failed payouts for retry (address -> retry count)
	failedPayouts := make(map[string]int)
	const maxRetries = 3

	// Dust logging interval (every 10 cycles = ~10 minutes)
	dustLogCounter := 0

	// Run a full orphan reconciliation once, on the first cycle, to clear any
	// historical orphaned-block credits before they are ever paid.
	orphanFullScanDone := false

	for {
		select {
		case <-shutdownCh:
			log.Println("Payout processor shutting down")
			return
		case <-ticker.C:
		}
		// Continue with payout processing
		// Get current height
		heightResp, err := rpcCall(nodeURL, "getblockcount", []interface{}{})
		if err != nil {
			log.Printf("Failed to get block height: %v", err)
			continue
		}
		heightFloat, ok := heightResp.(float64)
		if !ok {
			log.Printf("Unexpected response type for getblockcount: %T", heightResp)
			continue
		}
		currentHeight := int64(heightFloat)

		// Orphan reconciliation: void payouts for pool blocks no longer on the
		// active chain BEFORE selecting anyone for payment. The first pass is a
		// full historical scan; subsequent passes only check the recent frontier.
		fullScan := !orphanFullScanDone
		reconcileOrphanHeights(currentHeight, fullScan)
		// Solo blocks are coinbase-direct: their payout row is already 'paid', so the payout-row
		// orphan reconciler above skips them. Reconcile them on their own — within the reorg-
		// plausible band, confirm blocks still on the active chain and orphan (void) any reorged
		// out — BEFORE ConfirmMatureSoloBlocks confirms the deep remainder. Without this a
		// reorged-out solo block would be blindly confirmed and overstate earnings.
		reconcileSoloBlocks(currentHeight, fullScan)
		orphanFullScanDone = true

		// Confirm solo blocks buried BELOW the reorg-plausible band unconditionally (too deep to
		// reorg — no active-chain check needed). In-band blocks were just confirmed or orphaned by
		// reconcileSoloBlocks after checking blocks.hash against getblockhash(height).
		if soloConfirmHeight := currentHeight - int64(stats.COINBASE_MATURITY) - orphanCheckBand; soloConfirmHeight >= 0 {
			if cErr := stats.ConfirmMatureSoloBlocks(soloConfirmHeight); cErr != nil {
				log.Printf("Confirm mature solo blocks: %v", cErr)
			}
		}

		mp := getMinPayout()

		// Periodic dust balance logging
		dustLogCounter++
		if dustLogCounter >= 10 {
			dustLogCounter = 0
			totalDust := stats.GetTotalDust(currentHeight, mp)
			if totalDust > 0 {
				dustCount := len(stats.GetDustBalances(currentHeight, mp))
				log.Printf("Dust balances: %.8f BCH2 across %d miners (below %.2f min payout)",
					totalDust, dustCount, mp)
			}
		}

		// Get ready payouts using global minPayout config
		// Use DB-based query for reliable payout detection (survives restarts)
		ready := stats.GetReadyPayoutsDB(currentHeight, mp)
		if ready == nil {
			// Fall back to in-memory if DB query fails
			ready = stats.GetReadyPayouts(currentHeight, mp)
		}
		if len(ready) == 0 {
			continue
		}

		matureHeight := currentHeight - int64(stats.COINBASE_MATURITY)
		for address := range ready {
			// Skip if exceeded max retries (will be retried after pool restart)
			if failedPayouts[address] >= maxRetries {
				continue
			}

			// Reserve-then-send: payMiner reserves the miner's mature balance in the
			// DB before broadcasting, sends in row-aligned chunks, and finalizes each
			// chunk to its real txid. It never re-broadcasts and never releases a
			// chunk that was already sent, so it cannot double-pay.
			txids, sent, err := payMiner(address, matureHeight, mp)
			if err != nil {
				failedPayouts[address]++
				log.Printf("Payout failed for %s (attempt %d/%d): %v",
					address, failedPayouts[address], maxRetries, err)
				continue
			}

			if len(txids) > 0 {
				delete(failedPayouts, address)
				// Keep the in-memory ledger roughly in sync (DB is authoritative).
				stats.MarkMaturePaidWithAmount(address, currentHeight, txids[len(txids)-1], sent)
				if len(txids) == 1 {
					log.Printf("Payout sent: %s -> %.8f BCH2 (txid: %s)", address, sent, txids[0])
				} else {
					log.Printf("Split payout complete for %s: %d transactions, total %.8f BCH2",
						address, len(txids), sent)
				}
			}
		}

		// Periodic cleanup of old paid payouts from memory (every cycle)
		stats.CleanupPaidPayouts()
	}
}

// isDefinitelyNotBroadcast reports whether a sendtoaddress failure proves that no
// transaction was created. rpcCall wraps a structured node rejection as an
// "RPC error:" — the node processed and refused the request, so nothing was
// broadcast, and the reserved rows are safe to release. Any other error (HTTP
// timeout, connection reset, decode failure) is an UNKNOWN outcome: the tx may
// already be on the wire, so the caller must NOT retry or release it.
func isDefinitelyNotBroadcast(err error) bool {
	if err == nil {
		return true
	}
	return strings.HasPrefix(err.Error(), "RPC error:")
}

// payMiner reserves a miner's mature unpaid payouts, then broadcasts them in
// row-aligned chunks (each a whole number of ledger rows, capped near 1000 BCH2)
// and finalizes each chunk to its real txid immediately after it is sent. Because
// every chunk's marked amount equals exactly what was broadcast, and reserved rows
// are excluded from selection the moment they are reserved, this path cannot
// double-pay and cannot silently under/over-mark:
//   - A definite pre-broadcast rejection releases the still-reserved rows for retry.
//   - An ambiguous send error (timeout/connection) leaves the rows RESERVED and
//     flags them for manual reconciliation — never re-broadcast automatically.
//   - A crash mid-run leaves rows reserved (not payable) rather than double-paid.
//
// maxPayoutPerTx caps a single sendtoaddress to avoid "transaction too large".
const maxPayoutPerTx = 1000.0

// chunkPayoutRows groups reserved payout rows into row-aligned chunks, each capped
// at maxPerTx BCH2. Because chunks never split a row, the amount broadcast for a
// chunk always equals the sum of that chunk's ledger rows, so sent == marked. A
// single row larger than the cap becomes its own chunk (never dropped or split).
func chunkPayoutRows(rows []stats.PayoutRow, maxPerTx float64) [][]stats.PayoutRow {
	var chunks [][]stats.PayoutRow
	var cur []stats.PayoutRow
	var curAmt float64
	for _, r := range rows {
		if len(cur) > 0 && curAmt+r.Amount > maxPerTx {
			chunks = append(chunks, cur)
			cur, curAmt = nil, 0
		}
		cur = append(cur, r)
		curAmt += r.Amount
	}
	if len(cur) > 0 {
		chunks = append(chunks, cur)
	}
	return chunks
}

func payMiner(address string, matureHeight int64, minPayout float64) (txids []string, totalSent float64, err error) {
	pendingID, rows, total, err := stats.ReserveMaturePayouts(address, matureHeight)
	if err != nil {
		return nil, 0, err
	}
	// Below the payout threshold (or nothing to pay): release the reservation and
	// leave the balance to accrue.
	if len(rows) == 0 || total < minPayout || total <= 0 {
		if pendingID != "" {
			stats.RevertPendingPayout(pendingID)
		}
		return nil, 0, nil
	}

	for _, chunk := range chunkPayoutRows(rows, maxPayoutPerTx) {
		var ids []int64
		var amt float64
		for _, r := range chunk {
			ids = append(ids, r.ID)
			amt += r.Amount
		}
		amt = math.Round(amt*1e8) / 1e8
		if amt <= 0 || math.IsNaN(amt) || math.IsInf(amt, 0) {
			// Non-sensible amount: release these rows, don't send.
			stats.RevertPayoutRows(ids)
			continue
		}

		txid, serr := sendPayout(walletRPCURL, address, amt)
		if serr != nil {
			if isDefinitelyNotBroadcast(serr) {
				// Nothing was sent: release every row still reserved under pendingID.
				stats.RevertPendingPayout(pendingID)
			} else {
				log.Printf("CRITICAL: payout to %s (%.8f BCH2) returned an ambiguous error; "+
					"rows left RESERVED under %s for manual reconciliation, NOT retried (avoids double-send): %v",
					address, amt, pendingID, serr)
			}
			return txids, totalSent, serr
		}
		if ferr := stats.FinalizePayoutRows(ids, txid); ferr != nil {
			// Coins WERE sent; do not release. Leave reserved for manual reconcile.
			log.Printf("CRITICAL: payout to %s sent (txid %s) but DB finalize failed; "+
				"rows left reserved under %s for manual reconciliation: %v", address, txid, pendingID, ferr)
			return txids, totalSent, ferr
		}
		txids = append(txids, txid)
		totalSent += amt
	}
	return txids, totalSent, nil
}

// getRPCCredentials returns RPC credentials from environment variables
func getRPCCredentials() (string, string) {
	user := os.Getenv("RPC_USER")
	if user == "" {
		user = os.Getenv("FORGE_RPC_USER")
	}
	pass := os.Getenv("RPC_PASSWORD")
	if pass == "" {
		pass = os.Getenv("FORGE_RPC_PASSWORD")
	}
	return user, pass
}

func rpcCall(url, method string, params []interface{}) (interface{}, error) {
	reqBody, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "1.0",
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal RPC request: %w", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create RPC request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	rpcUser, rpcPass := getRPCCredentials()
	if rpcUser == "" || rpcPass == "" {
		return nil, fmt.Errorf("RPC credentials not configured - set RPC_USER and RPC_PASSWORD environment variables")
	}
	req.SetBasicAuth(rpcUser, rpcPass)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Result interface{} `json:"result"`
		Error  interface{} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode RPC response: %w", err)
	}

	if result.Error != nil {
		return nil, fmt.Errorf("RPC error: %v", result.Error)
	}
	return result.Result, nil
}

// rpcCallRaw performs an RPC call and unmarshals the result into out.
func rpcCallRaw(url, method string, params []interface{}, out interface{}) error {
	reqBody, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "1.0", "method": method, "params": params,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	u, p := getRPCCredentials()
	if u == "" || p == "" {
		return fmt.Errorf("RPC credentials not configured")
	}
	req.SetBasicAuth(u, p)
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var r struct {
		Result json.RawMessage `json:"result"`
		Error  interface{}     `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return err
	}
	if r.Error != nil {
		return fmt.Errorf("rpc error: %v", r.Error)
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

// heightIsOrphaned reports whether the block this pool recorded at the given height
// is no longer the block on the active chain at that height (i.e. the pool's block
// was orphaned, so its coinbase was never received). known is false when the answer
// cannot be determined — no recorded block, or an RPC error — and callers MUST treat
// "unknown" as "do not pay and do not void" so a transient node hiccup never voids a
// legitimate payout.
func heightIsOrphaned(height int64) (orphaned bool, known bool) {
	recorded, ok := stats.GetRecordedBlockHash(height)
	if !ok {
		return false, false
	}
	var chainHash string
	if err := rpcCallRaw(rpcURL, "getblockhash", []interface{}{height}, &chainHash); err != nil || chainHash == "" {
		return false, false
	}
	return !strings.EqualFold(chainHash, recorded), true
}

// orphanCheckBand bounds the per-cycle orphan reconciliation to the reorg-plausible
// frontier just past maturity; blocks buried deeper than this cannot reorganize.
const orphanCheckBand = 500

// reconcileOrphanHeights voids unpaid payouts for pool blocks that are no longer on
// the active chain. When full is true it scans every unpaid mature height (used once
// at startup to clear historical orphans); otherwise it only scans the recent
// frontier band to bound RPC work on the 60s cycle.
func reconcileOrphanHeights(currentHeight int64, full bool) {
	matureHeight := currentHeight - int64(stats.COINBASE_MATURITY)
	minHeight := int64(0)
	if !full {
		minHeight = matureHeight - orphanCheckBand
		if minHeight < 0 {
			minHeight = 0
		}
	}
	heights, err := stats.GetUnpaidMatureHeights(matureHeight, minHeight)
	if err != nil {
		log.Printf("Orphan reconcile: failed to list unpaid heights: %v", err)
		return
	}
	for _, h := range heights {
		orphaned, ok := heightIsOrphaned(h)
		if !ok {
			continue // fail-safe: undecidable => neither void nor pay
		}
		if orphaned {
			n, amt, err := stats.VoidOrphanedPayouts(h)
			if err != nil {
				log.Printf("Orphan reconcile: failed to void height %d: %v", h, err)
				continue
			}
			if n > 0 {
				log.Printf("ORPHAN VOID: pool block at height %d is not on the active chain; voided %d payout rows totaling %.8f BCH2 (never received, will not be paid)", h, n, amt)
			}
		}
	}
}

// reconcileSoloBlocks reconciles still-pending solo blocks against the active chain. Solo
// blocks are coinbase-direct (their payout row is already 'paid'), so the payout-row orphan
// reconciler (reconcileOrphanHeights) skips them; without this a reorged-out solo block would
// be blindly confirmed and overstate earnings. Within the reorg-plausible band it confirms
// blocks still on the active chain and orphans (voids) those that are not. full=true scans
// every pending solo height (a one-time startup reconciliation).
func reconcileSoloBlocks(currentHeight int64, full bool) {
	matureHeight := currentHeight - int64(stats.COINBASE_MATURITY)
	if matureHeight < 0 {
		return
	}
	minHeight := int64(0)
	if !full {
		minHeight = matureHeight - orphanCheckBand
		if minHeight < 0 {
			minHeight = 0
		}
	}
	heights, err := stats.PendingSoloHeights(matureHeight, minHeight)
	if err != nil {
		log.Printf("Solo reconcile: failed to list pending solo heights: %v", err)
		return
	}
	for _, h := range heights {
		orphaned, known := heightIsOrphaned(h)
		if !known {
			continue // node blip / undecidable: leave pending, retry next cycle
		}
		if orphaned {
			n, oerr := stats.OrphanSoloBlock(h)
			if oerr != nil {
				log.Printf("Solo reconcile: failed to orphan height %d: %v", h, oerr)
				continue
			}
			if n > 0 {
				log.Printf("ORPHAN VOID (solo): block at height %d is not on the active chain; marked orphaned (coinbase-direct — nothing was sent; excluded from confirmed earnings)", h)
			}
		} else if cErr := stats.ConfirmSoloBlock(h); cErr != nil {
			log.Printf("Solo reconcile: failed to confirm height %d: %v", h, cErr)
		}
	}
}

// bitsToDifficulty converts compact "bits" from block template to difficulty
func bitsToDifficulty(bitsHex string) float64 {
	bits, err := strconv.ParseUint(bitsHex, 16, 32)
	if err != nil || bits == 0 {
		return 0
	}
	exp := bits >> 24
	mantissa := bits & 0xFFFFFF
	if mantissa == 0 {
		return 0
	}
	// diff1 target exponent = 0x1d (29)
	// difficulty = (0xFFFF / mantissa) * 256^(29 - exp)
	return (float64(0xFFFF) / float64(mantissa)) * math.Pow(256, float64(29-exp))
}
func sendPayout(rpcURL, address string, amount float64) (string, error) {
	result, err := rpcCall(rpcURL, "sendtoaddress", []interface{}{address, amount})
	if err != nil {
		return "", err
	}
	txid, ok := result.(string)
	if !ok {
		return "", fmt.Errorf("unexpected response type for sendtoaddress: %T", result)
	}
	return txid, nil
}

// sendWebhookAlert sends a webhook notification for important events
func sendWebhookAlert(event string, data map[string]interface{}) {
	webhookURL := os.Getenv("WEBHOOK_URL")
	if webhookURL == "" {
		return // No webhook configured
	}

	payload := map[string]interface{}{
		"event":     event,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"pool":      "Forge Pool",
		"data":      data,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		log.Printf("Failed to marshal webhook payload: %v", err)
		return
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(webhookURL, "application/json", bytes.NewReader(jsonData))
	if err != nil {
		log.Printf("Failed to send webhook: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		log.Printf("Webhook returned status %d", resp.StatusCode)
	}
}

// startZMQListener subscribes to ZMQ block notifications for instant block detection
// This reduces orphan rate by getting new block notifications in milliseconds vs 1-second polling
func startZMQListener(zmqEndpoint string, logger *zap.Logger) {
	ctx := context.Background()

	for {
		select {
		case <-shutdownCh:
			logger.Info("ZMQ listener shutting down")
			return
		default:
		}

		sub := zmq4.NewSub(ctx)
		if err := sub.Dial(zmqEndpoint); err != nil {
			logger.Warn("Failed to connect to ZMQ endpoint, retrying in 5s",
				zap.String("endpoint", zmqEndpoint),
				zap.Error(err))
			time.Sleep(5 * time.Second)
			continue
		}

		// Subscribe to hashblock topic
		if err := sub.SetOption(zmq4.OptionSubscribe, "hashblock"); err != nil {
			logger.Error("Failed to subscribe to hashblock topic", zap.Error(err))
			sub.Close()
			time.Sleep(5 * time.Second)
			continue
		}

		logger.Info("✅ ZMQ block notifications connected",
			zap.String("endpoint", zmqEndpoint))

		for {
			select {
			case <-shutdownCh:
				sub.Close()
				return
			default:
			}

			msg, err := sub.Recv()
			if err != nil {
				logger.Warn("ZMQ receive error, reconnecting", zap.Error(err))
				sub.Close()
				break
			}

			// ZMQ message format: [topic, blockhash, sequence]
			if len(msg.Frames) >= 2 {
				topic := string(msg.Frames[0])
				if topic == "hashblock" {
					blockHash := hex.EncodeToString(msg.Frames[1])
					logger.Info("⚡ ZMQ block notification received",
						zap.String("hash", blockHash))

					// Non-blocking send to trigger immediate job update
					select {
					case zmqBlockCh <- blockHash:
					default:
						// Channel full, job loop will pick it up on next tick
					}
				}
			}
		}
	}
}

// enableMergeMining1175 turns on 1175 (ESF) merge mining for the given esf1 payout address
// and returns an aux client the stratum servers use to submit solved aux blocks. It
// configures the job manager + the aux1175* globals and is safe to call at startup or at
// runtime (from watchPoolConfig). Server wiring + the 1175 payout processor are the caller's
// responsibility. The 1175 node is the authoritative address validator (via getauxblock).
// enableJobManagerAuxMergeMining points the job manager at the aux (1175) node so each new
// BCH2 job carries aux work. Call this ONLY after every stratum server's aux fields are wired
// (EnableMergeMining/SetAuxBlockHandler), so no aux job reaches a connection goroutine before
// a server is ready to submit its solved aux block.
func enableJobManagerAuxMergeMining(cfg *viper.Viper, auxPayout string) {
	auxURL := fmt.Sprintf("http://%s:%d",
		cfg.GetString("mergemining.aux_node.host"),
		cfg.GetInt("mergemining.aux_node.port"))
	jobManager.EnableMergeMining(auxURL,
		cfg.GetString("mergemining.aux_node.user"),
		cfg.GetString("mergemining.aux_node.pass"),
		auxPayout)
}

func enableMergeMining1175(cfg *viper.Viper, auxPayout string) *mergemining.Client {
	auxURL := fmt.Sprintf("http://%s:%d",
		cfg.GetString("mergemining.aux_node.host"),
		cfg.GetInt("mergemining.aux_node.port"))
	auxUser := cfg.GetString("mergemining.aux_node.user")
	auxPass := cfg.GetString("mergemining.aux_node.pass")
	if !strings.HasPrefix(auxPayout, "esf1") || len(auxPayout) < 42 {
		logger.Warn("⚠️  PAYOUT_ADDRESS_1175 does not look like a valid esf1… address — 1175 rewards may be rejected by the node, or if it decodes to a different valid address, mined to the WRONG place. Double-check it.", zap.String("configured", auxPayout))
	} else {
		logger.Info("💠 1175 (ESF) block rewards will be paid on-chain DIRECTLY to your configured address", zap.String("payout_address_1175", auxPayout))
	}
	ac := mergemining.NewClient(auxURL, auxUser, auxPass)
	auxWallet := cfg.GetString("mergemining.aux_node.wallet")
	if auxWallet == "" {
		auxWallet = "pool"
	}
	min1175Payout = cfg.GetFloat64("mergemining.min_payout")
	if min1175Payout <= 0 {
		min1175Payout = 1.0
	}
	merge1175Enabled = true
	aux1175NodeURL = auxURL
	aux1175WalletURL = fmt.Sprintf("%s/wallet/%s", auxURL, auxWallet)
	aux1175User = auxUser
	aux1175Pass = auxPass
	logger.Info("⛏️  Merge mining enabled", zap.String("aux_node", auxURL), zap.String("payout", auxPayout))
	return ac
}

// watchPoolConfig polls the dashboard-managed pool_config (DB) and applies changes to the
// running stratum WITHOUT a restart: a new/changed BCH2 payout address activates mining, a
// changed coinbase tag re-tags new jobs, and a first-time 1175 (esf1) address turns on merge
// mining (wiring the servers + starting its payout processor). SetPoolAddress fails soft, so
// a bad new value never clears an already-working address.
func watchPoolConfig(jm *mining.JobManager, cfg *viper.Viper) {
	ticker := time.NewTicker(8 * time.Second)
	defer ticker.Stop()
	var lastPool, lastTag, last1175 string
	// Seed with the values already applied at startup so we only act on real changes.
	if stats.IsDBConnected() {
		if p, p1175, t, _, err := stats.GetPoolConfig(); err == nil {
			lastPool, last1175, lastTag = p, p1175, t
		}
	}
	for {
		select {
		case <-shutdownCh:
			return
		case <-ticker.C:
		}
		if !stats.IsDBConnected() {
			continue
		}
		pool, p1175, tag, minP, err := stats.GetPoolConfig()
		if err != nil {
			continue
		}
		if minP > 0 {
			setMinPayout(minP)
		}
		if pool != lastPool && pool != "" {
			if serr := jm.SetPoolAddress(pool); serr != nil {
				logger.Warn("dashboard payout address rejected — keeping previous", zap.String("address", pool), zap.Error(serr))
			} else {
				logger.Info("✅ payout address updated from dashboard — mining active", zap.String("address", pool))
				lastPool = pool
			}
		}
		if tag != lastTag && tag != "" {
			jm.SetCoinbaseTag(tag)
			logger.Info("coinbase tag updated from dashboard", zap.String("tag", tag))
			lastTag = tag
		}
		if p1175 != last1175 && p1175 != "" {
			if !merge1175Enabled {
				ac := enableMergeMining1175(cfg, p1175)
				if stratumServer != nil {
					stratumServer.EnableMergeMining(ac)
					stratumServer.SetAuxBlockHandler(aux1175BlockHandler)
				}
				if stratumBraiinsServer != nil {
					stratumBraiinsServer.EnableMergeMining(ac)
					stratumBraiinsServer.SetAuxBlockHandler(aux1175BlockHandler)
				}
				// Enable aux work on the job manager LAST — only after the stratum servers' aux
				// fields are wired — so a produced aux job always has a server ready to submit it.
				enableJobManagerAuxMergeMining(cfg, p1175)
				if stats.IsDBConnected() {
					go start1175PayoutProcessor()
				}
				logger.Info("💠 1175 merge-mining enabled from dashboard", zap.String("payout_address_1175", p1175))
			} else {
				// Already mining 1175: just re-point the aux coinbase payout to the new address.
				jm.EnableMergeMining(
					fmt.Sprintf("http://%s:%d", cfg.GetString("mergemining.aux_node.host"), cfg.GetInt("mergemining.aux_node.port")),
					cfg.GetString("mergemining.aux_node.user"),
					cfg.GetString("mergemining.aux_node.pass"),
					p1175)
				logger.Info("💠 1175 payout address updated from dashboard", zap.String("payout_address_1175", p1175))
			}
			last1175 = p1175
		}
	}
}

func main() {
	configPath := flag.String("config", "config.yaml", "Path to config file")
	flag.Parse()

	var logErr error
	logger, logErr = zap.NewProduction()
	if logErr != nil {
		log.Fatalf("Failed to initialize logger: %v", logErr)
	}
	defer logger.Sync()

	logger.Info("🔥 Forge Pool - BCH2 Mining Pool")

	// Initialize database with credentials from environment
	dbConnStr := stats.GetDBConnStr()
	if dbErr := stats.InitDBWithRetry(dbConnStr, 30, 2*time.Second); dbErr != nil {
		logger.Warn("Database not available, using memory only", zap.Error(dbErr))
	} else {
		logger.Info("✅ Connected to PostgreSQL database")
		stats.LoadAllPendingPayouts()
		// Note: startPayoutProcessor is started later after config is loaded
	}
	defer stats.CloseDB()

	config, err := loadConfig(*configPath)
	if err != nil {
		logger.Fatal("Failed to load config", zap.Error(err))
	}

	serverConfig := &stratum.ServerConfig{
		Host:               config.GetString("stratum.host"),
		Port:               config.GetInt("stratum.port"),
		MaxConnections:     config.GetInt("stratum.max_connections"),
		BanDuration:        config.GetDuration("stratum.ban_duration"),
		MaxSharesPerSecond: config.GetInt("stratum.max_shares_per_second"),
		VardiffEnabled:     config.GetBool("stratum.vardiff.enabled"),
		MinDiff:            config.GetFloat64("stratum.vardiff.min_diff"),
		RentalMinDiff:      config.GetFloat64("stratum.vardiff.rental_min_diff"),
		RentalMaxDiff:      config.GetFloat64("stratum.vardiff.rental_max_diff"),
		MaxDiff:            config.GetFloat64("stratum.vardiff.max_diff"),
		TargetShareTime:    config.GetInt("stratum.vardiff.target_time"),
		RetargetTime:       config.GetInt("stratum.vardiff.retarget_time"),
		HighHashThreshold:  config.GetInt("stratum.high_hash_threshold"),
		HighHashDiff:       config.GetFloat64("stratum.high_hash_diff"),
		ExtraNonce1Size:    config.GetInt("stratum.extranonce1_size"),
		ExtraNonce2Size:    config.GetInt("stratum.extranonce2_size"),
		ServerName:         "main",
		SoloOnly:           config.GetString("pool.payout_scheme") == "solo",
	}

	// Build RPC URL from config
	nodeHost := config.GetString("node.host")
	nodePort := config.GetInt("node.port")
	nodeSSL := config.GetBool("node.use_ssl")
	if nodeHost == "" {
		nodeHost = "127.0.0.1"
	}
	if nodePort == 0 {
		nodePort = 8342
	}
	protocol := "http"
	if nodeSSL {
		protocol = "https"
	}
	rpcURL = fmt.Sprintf("%s://%s:%d", protocol, nodeHost, nodePort)
	walletRPCURL = fmt.Sprintf("%s://%s:%d/wallet/main%%2Fpool", protocol, nodeHost, nodePort)
	logger.Info("RPC URL configured", zap.String("url", rpcURL), zap.String("wallet_url", walletRPCURL))

	rpcUser, rpcPass = getRPCCredentials()

	// Load pool configuration
	poolAddress = config.GetString("pool.address")
	poolFee = config.GetFloat64("pool.fee")
	soloFee = config.GetFloat64("pool.solo_fee")
	blockReward = config.GetFloat64("pool.block_reward")
	setMinPayout(config.GetFloat64("pool.min_payout"))
	pplnsWindow = config.GetInt("pool.pplns_window")
	if pplnsWindow <= 0 {
		pplnsWindow = 100000 // Default PPLNS window
	}

	logger.Info("Pool configuration loaded",
		zap.String("address", poolAddress),
		zap.Float64("fee", poolFee),
		zap.Float64("solo_fee", soloFee),
		zap.Float64("block_reward", blockReward),
		zap.Float64("min_payout", minPayout),
		zap.Int("pplns_window", pplnsWindow))

	// Dashboard-managed config (DB pool_config) OVERRIDES the env-derived values when set,
	// so the whole app is configurable from the web UI with no SSH/restart. Falls back to
	// the env/config values when the DB row is empty or the DB is unavailable.
	effectivePoolAddr := poolAddress
	effectiveCoinbaseTag := config.GetString("pool.coinbase_tag")
	effective1175Payout := config.GetString("mergemining.payout_address")
	if stats.IsDBConnected() {
		if dbPool, db1175, dbTag, dbMin, cErr := stats.GetPoolConfig(); cErr == nil {
			if dbPool != "" {
				effectivePoolAddr = dbPool
			}
			if db1175 != "" {
				effective1175Payout = db1175
			}
			if dbTag != "" {
				effectiveCoinbaseTag = dbTag
			}
			if dbMin > 0 {
				setMinPayout(dbMin)
			}
		} else {
			logger.Warn("could not read dashboard pool_config; using env/config values", zap.Error(cErr))
		}
	}
	poolAddress = effectivePoolAddr

	// Start payout processor now that config is loaded
	if stats.IsDBConnected() {
		go startPayoutProcessor()
		logger.Info("💰 Payout processor started")
	}

	logger.Info("Vardiff configuration",
		zap.Bool("enabled", serverConfig.VardiffEnabled),
		zap.Float64("min_diff", serverConfig.MinDiff),
		zap.Float64("rental_min_diff", serverConfig.RentalMinDiff),
		zap.Float64("max_diff", serverConfig.MaxDiff),
		zap.Int("target_time", serverConfig.TargetShareTime),
		zap.Int("retarget_time", serverConfig.RetargetTime))

	jobManager = mining.NewJobManager(rpcURL, rpcUser, rpcPass, effectivePoolAddr, effectiveCoinbaseTag)

	// Merge mining (aux chain, e.g. 1175): the job manager fetches aux work and
	// embeds the commitment in the coinbase; the stratum servers submit solved
	// aux blocks. Entirely inert unless mergemining.enabled — BCH2 is unaffected.
	var auxClient *mergemining.Client
	// Merge-mining requires a 1175 (esf1…) payout address — the aux coinbase pays it
	// directly (wallet-free). With it blank, getauxblock("") is rejected by the node every
	// cycle and 1175 is never mined, so enable ONLY when it's set and warn loudly otherwise.
	// BCH2 mining is unaffected either way. When it is later set in the dashboard,
	// watchPoolConfig turns merge mining on at runtime via the same enableMergeMining1175 path.
	if config.GetBool("mergemining.enabled") && effective1175Payout == "" {
		logger.Warn("⚠️  Merge mining is enabled but PAYOUT_ADDRESS_1175 (your esf1… address) is not set — 1175 merge-mining is OFF until you set it in the dashboard. BCH2 mining continues normally.")
	}
	if config.GetBool("mergemining.enabled") && effective1175Payout != "" {
		auxClient = enableMergeMining1175(config, effective1175Payout)
	}

	shareProcessor := &BlockFindingShareProcessor{logger: logger}
	// Create API-backed miner settings store
	apiHost := os.Getenv("API_HOST")
	if apiHost == "" {
		apiHost = "127.0.0.1"
	}
	apiPort := os.Getenv("API_PORT")
	if apiPort == "" {
		apiPort = "8080"
	}
	minerSettings := stratum.NewAPIMinerSettings(fmt.Sprintf("http://%s:%s", apiHost, apiPort))
	if got := serverConfig.ExtraNonce1Size + serverConfig.ExtraNonce2Size; got != mining.CoinbaseExtranonceReserve {
		logger.Fatal("stratum extranonce1_size + extranonce2_size must equal the coinbase reserve, else assembled blocks are malformed and rejected",
			zap.Int("extranonce1_size", serverConfig.ExtraNonce1Size),
			zap.Int("extranonce2_size", serverConfig.ExtraNonce2Size),
			zap.Int("sum", got),
			zap.Int("required", mining.CoinbaseExtranonceReserve))
	}
	stratumServer = stratum.NewServer(serverConfig, logger, shareProcessor, minerSettings)
	if auxClient != nil {
		stratumServer.EnableMergeMining(auxClient)
		stratumServer.SetAuxBlockHandler(aux1175BlockHandler)
		// Wire the job manager only AFTER the server aux fields are set so no aux job is
		// produced before a server can submit its solved block.
		enableJobManagerAuxMergeMining(config, effective1175Payout)
	}

	if err := stratumServer.Start(); err != nil {
		logger.Fatal("Failed to start", zap.Error(err))
	}

	// Start Braiins-compatible stratum server (8-byte extranonce2)
	if config.GetBool("stratum_braiins.enabled") {
		braiinsConfig := &stratum.ServerConfig{
			Host:            config.GetString("stratum_braiins.host"),
			Port:            config.GetInt("stratum_braiins.port"),
			MaxConnections:  config.GetInt("stratum_braiins.max_connections"),
			VardiffEnabled:  config.GetBool("stratum_braiins.vardiff.enabled"),
			MinDiff:         config.GetFloat64("stratum_braiins.vardiff.min_diff"),
			MaxDiff:         config.GetFloat64("stratum_braiins.vardiff.max_diff"),
			TargetShareTime: config.GetInt("stratum_braiins.vardiff.target_time"),
			RetargetTime:    config.GetInt("stratum_braiins.vardiff.retarget_time"),
			ExtraNonce1Size: config.GetInt("stratum_braiins.extranonce1_size"),
			ExtraNonce2Size: config.GetInt("stratum_braiins.extranonce2_size"),
			ServerName:      "braiins",
			SoloOnly:        config.GetString("pool.payout_scheme") == "solo",
		}
		if got := braiinsConfig.ExtraNonce1Size + braiinsConfig.ExtraNonce2Size; got != mining.CoinbaseExtranonceReserve {
			logger.Fatal("stratum_braiins extranonce1_size + extranonce2_size must equal the coinbase reserve, else assembled blocks are malformed and rejected",
				zap.Int("extranonce1_size", braiinsConfig.ExtraNonce1Size),
				zap.Int("extranonce2_size", braiinsConfig.ExtraNonce2Size),
				zap.Int("sum", got),
				zap.Int("required", mining.CoinbaseExtranonceReserve))
		}
		stratumBraiinsServer = stratum.NewServer(braiinsConfig, logger, shareProcessor, minerSettings)
		if auxClient != nil {
			stratumBraiinsServer.EnableMergeMining(auxClient)
			stratumBraiinsServer.SetAuxBlockHandler(aux1175BlockHandler)
		}
		if err := stratumBraiinsServer.Start(); err != nil {
			logger.Error("Failed to start Braiins stratum", zap.Error(err))
		} else {
			logger.Info("✅ Braiins stratum running",
				zap.Int("port", braiinsConfig.Port),
				zap.Int("extranonce2_size", braiinsConfig.ExtraNonce2Size))
		}
	}

	// Start Stratum V2 server if enabled
	if config.GetBool("stratumv2.enabled") {
		v2Config := &stratumv2.ServerConfig{
			Host:              config.GetString("stratumv2.host"),
			Port:              config.GetInt("stratumv2.port"),
			MaxConnections:    config.GetInt("stratumv2.max_connections"),
			MinDiff:           config.GetFloat64("stratumv2.vardiff.min_diff"),
			MaxDiff:           config.GetFloat64("stratumv2.vardiff.max_diff"),
			TargetShareTime:   config.GetInt("stratumv2.vardiff.target_time"),
			RetargetTime:      config.GetInt("stratumv2.vardiff.retarget_time"),
			RequireEncryption: config.GetBool("stratumv2.require_encryption"),
			ExtranonceSize:    config.GetInt("stratumv2.extranonce_size"),
		}

		if got := v2Config.ExtranonceSize; got != mining.CoinbaseExtranonceReserve {
			logger.Fatal("stratumv2 extranonce_size must equal the coinbase reserve, else assembled blocks are malformed and rejected",
				zap.Int("extranonce_size", got),
				zap.Int("required", mining.CoinbaseExtranonceReserve))
		}

		// Create V2 share processor that bridges to V1 processing
		v2ShareProcessor := &V2ShareProcessor{logger: logger}
		v2MinerSettings := &V2MinerSettingsAdapter{v1Settings: minerSettings}

		var err error
		stratumV2Server, err = stratumv2.NewServer(v2Config, logger, v2ShareProcessor, v2MinerSettings)
		if err != nil {
			logger.Error("Failed to create V2 server", zap.Error(err))
		} else {
			if err := stratumV2Server.Start(); err != nil {
				logger.Error("Failed to start V2 server", zap.Error(err))
			} else {
				logger.Info("✅ Stratum V2 server running",
					zap.Int("port", v2Config.Port),
					zap.Bool("encryption", v2Config.RequireEncryption))
			}
		}
	}

	// Start worker timeout detection (marks workers offline after 5 min of no shares)
	workerTimeoutStop := make(chan struct{})
	go stats.GetManager().StartWorkerTimeoutChecker(workerTimeoutStop)

	go startStatsServer()

	// Watch the dashboard-managed pool_config (DB) and apply changes live — payout address,
	// coinbase tag, and first-time 1175 merge-mining enable — with no restart or SSH.
	go watchPoolConfig(jobManager, config)

	// 1175 merge-mining payout processor (pays miners their accrued 1175).
	if merge1175Enabled && stats.IsDBConnected() {
		go start1175PayoutProcessor()
	}

	logger.Info("✅ Stratum server running", zap.Int("port", serverConfig.Port))

	// Start ZMQ block notification listener for instant block detection
	zmqEndpoint := config.GetString("node.zmq_endpoint")
	if zmqEndpoint == "" {
		zmqEndpoint = "tcp://127.0.0.1:28332"
	}
	go startZMQListener(zmqEndpoint, logger)

	// Job broadcast loop
	// Miners expect periodic job updates to confirm pool is alive
	// Send new jobs on:
	//   1. New block detected via ZMQ (CleanJobs=true) - instant
	//   2. New block height via polling (CleanJobs=true) - fallback
	//   3. Periodic ntime update (CleanJobs=false) - every 15 seconds
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		var lastHeight int64
		var lastPrevHash string
		var lastJobTime time.Time

		for {
			var zmqTriggered bool
			select {
			case <-shutdownCh:
				logger.Info("Job broadcast loop shutting down")
				return
			case blockHash := <-zmqBlockCh:
				// ZMQ notification - immediate block template fetch
				logger.Info("⚡ ZMQ triggered job refresh", zap.String("block_hash", blockHash))
				zmqTriggered = true
			case <-ticker.C:
				// Regular polling (fallback)
			}

			// No payout address configured yet: accept stratum connections but PAUSE mining
			// (never build a job that would pay a null script). Set it in the dashboard.
			if !jobManager.IsConfigured() {
				continue
			}

			template, err := jobManager.GetBlockTemplate()
			if err != nil {
				logger.Error("Failed to get block template", zap.Error(err))
				continue
			}
			if template == nil {
				continue
			}

			// Update network difficulty from block template bits (actual next-block target)
			if templateDiff := bitsToDifficulty(template.Bits); templateDiff > 0 {
				oldDiff := getNetworkDifficulty()
				if templateDiff != oldDiff {
					logger.Info("Network difficulty updated from template",
						zap.Float64("old_diff", oldDiff),
						zap.Float64("new_diff", templateDiff),
						zap.String("bits", template.Bits))
				}
				setNetworkDifficulty(templateDiff)
			}
			setLatestCoinbaseBTC(float64(template.CoinbaseValue) / 1e8)

			curJob := getCurrentJob()
			isNewBlock := template.Height != lastHeight || template.PreviousBlockHash != lastPrevHash || curJob == nil
			needPeriodicUpdate := time.Since(lastJobTime) >= 15*time.Second // Faster updates for NiceHash

			if isNewBlock || needPeriodicUpdate {
				job := jobManager.CreateJob(template)
				if job == nil {
					continue
				}
				setCurrentJob(job)

				// Store job in history for block submission lookup
				jobHistoryMu.Lock()
				jobHistory[job.ID] = job
				jobHistoryOrder = append(jobHistoryOrder, job.ID)
				// Clean old jobs using FIFO. Keep at least as many as the share-validation
				// job history (500) so a winning share validated against an older job can
				// always be rebuilt from its EXACT job for block submission.
				for len(jobHistoryOrder) > 500 {
					oldestID := jobHistoryOrder[0]
					jobHistoryOrder = jobHistoryOrder[1:]
					delete(jobHistory, oldestID)
				}
				jobHistoryMu.Unlock()

				// CleanJobs=true only for new blocks, false for periodic updates
				cleanJobs := isNewBlock

				stratumJob := &stratum.Job{
					ID:               job.ID,
					Height:           job.Height,
					PrevBlockHash:    job.PrevBlockHash,
					OriginalPrevHash: job.OriginalPrevHash,
					CoinBase1:        job.CoinBase1,
					CoinBase2:        job.CoinBase2,
					MerkleBranches:   job.MerkleBranches,
					Version:          job.Version,
					NBits:            job.NBits,
					NTime:            job.NTime,
					CleanJobs:        cleanJobs,
					Target:           job.Target,
					CreatedAt:        time.Now(),
					Transactions:     job.Transactions,
					AuxWork:          job.AuxWork,
				}
				stratumServer.BroadcastJob(stratumJob)

				// Broadcast to Braiins server if enabled
				if stratumBraiinsServer != nil {
					stratumBraiinsServer.BroadcastJob(stratumJob)
				}

				// Broadcast to V2 server if enabled
				if stratumV2Server != nil {
					v2JobID := atomic.AddUint32(&v2JobIDCounter, 1)
					v2Job, err := stratumv2.ConvertV1ToV2Job(&stratumv2.V1JobData{
						ID:               job.ID,
						Height:           job.Height,
						PrevBlockHash:    job.PrevBlockHash,
						OriginalPrevHash: job.OriginalPrevHash,
						CoinBase1:        job.CoinBase1,
						CoinBase2:        job.CoinBase2,
						MerkleBranches:   job.MerkleBranches,
						Version:          job.Version,
						NBits:            job.NBits,
						NTime:            job.NTime,
						CleanJobs:        cleanJobs,
						CreatedAt:        time.Now(),
						Transactions:     job.Transactions,
					}, v2JobID)
					if err == nil {
						stratumV2Server.BroadcastJob(v2Job)
					}
				}

				if isNewBlock {
					source := "polling"
					if zmqTriggered {
						source = "ZMQ"
					}
					logger.Info("📢 New block job broadcast",
						zap.Int64("height", template.Height),
						zap.String("job_id", job.ID),
						zap.String("source", source))
				} else {
					logger.Debug("📢 Periodic job update",
						zap.Int64("height", template.Height),
						zap.String("job_id", job.ID))
				}

				lastHeight = template.Height
				lastPrevHash = template.PreviousBlockHash
				lastJobTime = time.Now()
			}
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	logger.Info("Shutting down...")
	close(shutdownCh)        // Signal all goroutines to stop
	close(workerTimeoutStop) // Stop worker timeout checker
	if stratumV2Server != nil {
		stratumV2Server.Stop()
	}
	if stratumBraiinsServer != nil {
		stratumBraiinsServer.Stop()
	}
	stratumServer.Stop()
}

func loadConfig(path string) (*viper.Viper, error) {
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")

	// Stratum defaults
	v.SetDefault("stratum.host", "0.0.0.0")
	v.SetDefault("stratum.port", 3333)
	v.SetDefault("stratum.max_connections", 10000)
	v.SetDefault("stratum.ban_duration", "10m")
	v.SetDefault("stratum.max_shares_per_second", 100)
	v.SetDefault("stratum.vardiff.enabled", true)
	v.SetDefault("stratum.vardiff.min_diff", 32768)
	v.SetDefault("stratum.vardiff.rental_min_diff", 500000)   // NiceHash/MRR require 500k+
	v.SetDefault("stratum.vardiff.rental_max_diff", 50000000) // Cap NiceHash/MRR at 50M for high-hashrate orders
	v.SetDefault("stratum.vardiff.max_diff", 1000000000)
	v.SetDefault("stratum.vardiff.target_time", 10)
	v.SetDefault("stratum.high_hash_threshold", 10)
	v.SetDefault("stratum.high_hash_diff", 1000000)

	// Node defaults - IMPORTANT: Set RPC_USER and RPC_PASSWORD env vars
	// DO NOT use default credentials in production
	v.SetDefault("node.user", "")
	v.SetDefault("node.password", "")

	// Pool defaults
	v.SetDefault("pool.fee", 1.0)
	v.SetDefault("pool.solo_fee", 0.5)
	v.SetDefault("pool.block_reward", 50.0)
	v.SetDefault("pool.min_payout", 5.0)
	v.SetDefault("pool.address", "")
	v.SetDefault("pool.coinbase_tag", "Forge") // Must be set in config or env

	if err := v.ReadInConfig(); err != nil {
		return nil, err
	}
	return v, nil
}

type BlockFindingShareProcessor struct {
	logger *zap.Logger
}

func (p *BlockFindingShareProcessor) ProcessShare(ctx context.Context, share *stratum.Share) error {
	mode := "PPLNS"
	if share.IsSolo {
		mode = "SOLO"
	}

	networkDiff := getNetworkDifficulty()

	// Track worker stats - log the difficulty being recorded for verification
	p.logger.Debug("Recording share for hashrate",
		zap.String("miner", share.MinerID),
		zap.Float64("target_diff", share.Difficulty),
		zap.Float64("actual_diff", share.ActualDiff))
	stats.GetManager().UpdateWorker(share.MinerID, share.WorkerName, true, share.Difficulty, share.ActualDiff)

	// Save share to database for PPLNS distribution
	// Use target difficulty as the credited work amount
	if err := stats.SaveShare(share.MinerID, share.WorkerName, share.Difficulty, share.IsSolo); err != nil {
		p.logger.Warn("Failed to save share to DB", zap.Error(err))
	}

	// Calculate how close this share is to network difficulty (use actual share diff)
	diffRatio := share.ActualDiff / networkDiff

	// Log exceptionally good shares (>1% of network diff)
	if diffRatio >= 0.01 {
		p.logger.Info("⚡ High difficulty share",
			zap.String("miner", share.MinerID),
			zap.Float64("actual_diff", share.ActualDiff),
			zap.Float64("network_diff", networkDiff),
			zap.Float64("ratio_percent", diffRatio*100),
			zap.String("job_id", share.JobID))
	}

	if share.ActualDiff >= networkDiff {
		p.logger.Info("🎉 BLOCK CANDIDATE!",
			zap.String("miner", share.MinerID),
			zap.Float64("actual_diff", share.ActualDiff),
			zap.Float64("network_diff", networkDiff),
			zap.String("job_id", share.JobID),
			zap.String("extranonce1", share.ExtraNonce1),
			zap.String("extranonce2", share.ExtraNonce2),
			zap.String("ntime", share.NTime),
			zap.String("nonce", share.Nonce))

		go p.submitBlock(share)
	}

	p.logger.Debug("Share processed",
		zap.String("miner", share.MinerID),
		zap.Float64("diff", share.Difficulty),
		zap.String("mode", mode))
	return nil
}

func (p *BlockFindingShareProcessor) submitBlock(share *stratum.Share) {
	// Look up the EXACT job that the share was submitted for
	// This is critical - using wrong job data would create an invalid block
	jobHistoryMu.RLock()
	job, exists := jobHistory[share.JobID]
	jobHistoryMu.RUnlock()

	if !exists {
		// The winning share's EXACT job is gone. Building from any other job (e.g.
		// getCurrentJob) can only produce an invalid block with the wrong merkle root,
		// so do NOT fall back — that would silently discard a real winner. Fail loudly
		// instead; the submission job history is sized to match the validation depth
		// (500), so this should be unreachable.
		p.logger.Error("CRITICAL: winning share's job is not in submission history — cannot rebuild the block; a found block may have been lost",
			zap.String("job_id", share.JobID))
		return
	}

	// Build coinbase using the correct job's coinbase parts
	coinbase, err := buildCoinbase(job.CoinBase1, share.ExtraNonce1, share.ExtraNonce2, job.CoinBase2)
	if err != nil {
		p.logger.Error("Failed to build coinbase", zap.Error(err))
		return
	}
	coinbaseHex := hex.EncodeToString(coinbase)

	// Build block using the correct job
	blockHex, err := buildBlock(job, coinbase, share.NTime, share.Nonce, share.VersionBits)
	if err != nil {
		p.logger.Error("Failed to build block", zap.Error(err))
		return
	}

	// Calculate block hash for debug
	headerBytes, err := hex.DecodeString(blockHex[:160]) // First 80 bytes = header
	if err != nil {
		p.logger.Error("Failed to decode block header", zap.Error(err))
		return
	}
	blockHash := doubleSHA256(headerBytes)
	reverseBytes(blockHash)

	p.logger.Info("Submitting block to node",
		zap.String("job_id", share.JobID),
		zap.Int64("height", job.Height),
		zap.Int("block_size", len(blockHex)/2),
		zap.String("nonce", share.Nonce),
		zap.String("ntime", share.NTime),
		zap.String("coinbase_full", coinbaseHex),
		zap.String("block_hash", hex.EncodeToString(blockHash)),
		zap.String("header_hex", blockHex[:160]))

	ourHash := hex.EncodeToString(blockHash)
	result, err := submitBlockToNode(blockHex)
	if err != nil || result != "" {
		// Not a clean accept. This may be a timeout AFTER the node already accepted
		// and relayed the block, or a "duplicate"/"inconclusive" result. Do NOT drop
		// the block: reconcile against the chain with brief retries, and credit only
		// if OUR block hash is the one on the active chain at this height (which also
		// prevents crediting a losing sibling block).
		reason := result
		if err != nil {
			reason = err.Error()
		}
		p.logger.Warn("submitblock was not a clean accept; reconciling against chain",
			zap.String("reason", reason), zap.String("our_hash", ourHash), zap.Int64("height", job.Height))
		result = reason // default to rejected unless reconciliation confirms our block
		for attempt := 1; attempt <= 3; attempt++ {
			var chainHash string
			if e := rpcCallRaw(rpcURL, "getblockhash", []interface{}{job.Height}, &chainHash); e == nil && strings.EqualFold(chainHash, ourHash) {
				result = "" // our block is on the active chain -> accepted
				break
			}
			// Re-submit (idempotent) in case the first attempt never reached the node.
			if r, e := submitBlockToNode(blockHex); e == nil && r == "" {
				result = ""
				break
			}
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		if result == "" {
			p.logger.Info("Block confirmed on chain after reconciliation",
				zap.String("our_hash", ourHash), zap.Int64("height", job.Height))
		} else {
			p.logger.Error("Block NOT confirmed on chain after retries; not crediting (manual verification advised)",
				zap.String("our_hash", ourHash), zap.Int64("height", job.Height), zap.String("reason", result))
		}
	}

	if result == "" {
		// Cap the reward at the block's live coinbase value so a stale block_reward
		// config (e.g. after a subsidy halving) can never pay out more than the coinbase
		// actually contains. No-op while the config matches the live coinbase.
		effectiveReward := blockReward
		if cbv := getLatestCoinbaseBTC(); cbv > 0 && cbv < effectiveReward {
			p.logger.Error("block_reward config exceeds live coinbase value; capping payout (update pool.block_reward after a halving)",
				zap.Float64("configured_reward", blockReward),
				zap.Float64("coinbase_value", cbv),
				zap.Int64("height", job.Height))
			effectiveReward = cbv
		}

		// Calculate payout after fee deduction
		var feePercent float64
		var mode string
		if share.IsSolo {
			feePercent = soloFee
			mode = "SOLO"
		} else {
			feePercent = poolFee
			mode = "PPLNS"
		}
		payoutAmount := effectiveReward * (1 - feePercent/100)
		hashStr := hex.EncodeToString(blockHash)

		p.logger.Info("🎉🎉🎉 BLOCK ACCEPTED BY NODE! 🎉🎉🎉",
			zap.Int64("height", job.Height),
			zap.String("miner", share.MinerID),
			zap.String("mode", mode),
			zap.Float64("reward", effectiveReward),
			zap.Float64("fee_percent", feePercent),
			zap.Float64("payout", payoutAmount))

		// Record block for miner stats with effort tracking for luck calculation
		stats.RecordMinerBlockWithWorkerSolo(share.MinerID, share.WorkerName, job.Height, hashStr, effectiveReward, share.IsSolo)
		stats.GetManager().RecordBlockWithEffort(hashStr, getNetworkDifficulty())

		// Send webhook alert for block found
		go sendWebhookAlert("block_found", map[string]interface{}{
			"height":      job.Height,
			"hash":        hashStr,
			"miner":       share.MinerID,
			"worker":      share.WorkerName,
			"mode":        mode,
			"reward":      effectiveReward,
			"payout":      payoutAmount,
			"fee_percent": feePercent,
		})

		if share.IsSolo {
			// SOLO MODE: the block reward is paid on-chain DIRECTLY by the coinbase to the
			// configured POOL_ADDRESS. Settle DB-only (txid='coinbase-direct') and never
			// create a sendable payout row: the wallet sendtoaddress path targets a
			// nonexistent wallet, would fail forever, and risks a double-pay. Mirrors the
			// 1175 coinbase-direct settle.
			if err := stats.SaveSoloBlockCoinbaseDirect(share.MinerID, job.Height, payoutAmount, hashStr); err != nil {
				p.logger.Error("Failed to record solo block", zap.Error(err))
			}
			p.logger.Info("💰 Solo block reward settled (paid on-chain by coinbase)",
				zap.String("miner", share.MinerID),
				zap.Float64("amount", payoutAmount))
		} else {
			// PPLNS MODE: Distribute reward among all PPLNS contributors
			pplnsShares, totalWork, err := stats.GetPPLNSShares(pplnsWindow)
			if err != nil || totalWork == 0 {
				// Fallback to block finder if PPLNS data unavailable
				p.logger.Warn("PPLNS shares unavailable, paying block finder only",
					zap.Error(err))
				stats.AddPendingPayout(share.MinerID, job.Height, payoutAmount)
				if err := stats.SavePayoutAtomic(share.MinerID, job.Height, payoutAmount, hashStr); err != nil {
					p.logger.Error("Failed to save payout", zap.Error(err))
				}
			} else {
				// Record the block BEFORE distributing so a reorg re-mine voids the
				// superseded distribution here, before we credit the new one (recording it
				// after the loop would wipe the just-credited rows and leave a dropped
				// contributor payable -- a double-pay).
				if err := stats.SaveBlock(job.Height, hashStr, share.MinerID, effectiveReward); err != nil {
					p.logger.Error("Failed to save block record", zap.Error(err))
				}

				// Distribute proportionally
				p.logger.Info("📊 Distributing PPLNS rewards",
					zap.Int("contributors", len(pplnsShares)),
					zap.Float64("total_work", totalWork),
					zap.Float64("reward_pool", payoutAmount))

				for minerAddr, work := range pplnsShares {
					// Calculate proportional share with safety bounds
					proportion := work / totalWork
					if proportion > 1.0 {
						proportion = 1.0 // Cap at 100% due to floating point errors
					}
					if proportion <= 0 {
						continue // Skip invalid proportions
					}
					minerPayout := payoutAmount * proportion

					// Skip dust amounts (< 0.00001 BCH2)
					if minerPayout < 0.00001 {
						continue
					}

					stats.AddPendingPayout(minerAddr, job.Height, minerPayout)
					if err := stats.SavePayout(minerAddr, job.Height, minerPayout); err != nil {
						p.logger.Error("Failed to save PPLNS payout",
							zap.String("miner", minerAddr),
							zap.Error(err))
					}

					p.logger.Info("💰 PPLNS payout credited",
						zap.String("miner", minerAddr),
						zap.Float64("work", work),
						zap.Float64("proportion", proportion*100),
						zap.Float64("amount", minerPayout))
				}

			}
		}

		// Reset round stats after block found
		if share.IsSolo {
			// Solo mode: only reset the block finder's stats
			stats.GetManager().ResetWorkerRoundStats(share.MinerID)
		} else {
			// PPLNS mode: reset all workers (shared round)
			stats.GetManager().ResetAllWorkerRoundStats()
		}

		// Cleanup old shares periodically (keep 2x window)
		go func() {
			if deleted, err := stats.CleanupOldShares(pplnsWindow); err == nil && deleted > 0 {
				p.logger.Info("Cleaned up old shares", zap.Int64("deleted", deleted))
			}
		}()
	} else {
		p.logger.Warn("Block rejected by node", zap.String("reason", result))
	}
}

func (p *BlockFindingShareProcessor) ProcessBlock(ctx context.Context, block *stratum.Block) error {
	p.logger.Info("🎉 BLOCK FOUND!", zap.String("hash", block.Hash), zap.Int64("height", block.Height))
	return nil
}

func buildCoinbase(cb1, extranonce1, extranonce2, cb2 string) ([]byte, error) {
	cb1Bytes, err := hex.DecodeString(cb1)
	if err != nil {
		return nil, fmt.Errorf("invalid cb1 hex: %w", err)
	}
	en1Bytes, err := hex.DecodeString(extranonce1)
	if err != nil {
		return nil, fmt.Errorf("invalid extranonce1 hex: %w", err)
	}
	en2Bytes, err := hex.DecodeString(extranonce2)
	if err != nil {
		return nil, fmt.Errorf("invalid extranonce2 hex: %w", err)
	}
	cb2Bytes, err := hex.DecodeString(cb2)
	if err != nil {
		return nil, fmt.Errorf("invalid cb2 hex: %w", err)
	}

	var coinbase bytes.Buffer
	coinbase.Write(cb1Bytes)
	coinbase.Write(en1Bytes)
	coinbase.Write(en2Bytes)
	coinbase.Write(cb2Bytes)

	return coinbase.Bytes(), nil
}

func buildBlock(job *mining.Job, coinbase []byte, ntime, nonce, versionBits string) (string, error) {
	var block bytes.Buffer

	// Version (4 bytes) - stratum sends as hex string like "20000000"
	// For block, we need little-endian, so reverse the bytes
	versionBytes, err := hex.DecodeString(job.Version)
	if err != nil {
		return "", fmt.Errorf("invalid version hex: %w", err)
	}
	if versionBits != "" {
		vbBytes, err := hex.DecodeString(versionBits)
		if err != nil {
			return "", fmt.Errorf("invalid versionBits hex: %w", err)
		}
		// BIP310 version-rolling: the miner submits the FULL rolled nVersion. Keep
		// the job's non-rollable bits and take only the masked (rollable) bits from
		// the miner. Plain XOR corrupted the header for full-version submitters
		// (NiceHash/Braiins/most ASICs), wrongly rejecting their version-rolled shares.
		versionRollingMask := []byte{0x1f, 0xff, 0xe0, 0x00}
		for i := 0; i < len(versionBytes) && i < len(vbBytes) && i < len(versionRollingMask); i++ {
			versionBytes[i] = (versionBytes[i] &^ versionRollingMask[i]) | (vbBytes[i] & versionRollingMask[i])
		}
	}
	reverseBytes(versionBytes)
	block.Write(versionBytes)

	// Previous block hash (32 bytes)
	// Stratum prevhash was reversed, reverse it back for block
	prevHashBytes, err := hex.DecodeString(job.OriginalPrevHash)
	if err != nil {
		return "", fmt.Errorf("invalid prevHash hex: %w", err)
	}
	reverseBytes(prevHashBytes)
	block.Write(prevHashBytes)

	// Merkle root calculation
	// Start with coinbase hash, then combine with merkle branches
	merkleRoot := doubleSHA256(coinbase)
	for i, branchHex := range job.MerkleBranches {
		branch, err := hex.DecodeString(branchHex)
		if err != nil {
			return "", fmt.Errorf("invalid merkle branch[%d] hex: %w", i, err)
		}
		combined := make([]byte, 64)
		copy(combined[:32], merkleRoot)
		copy(combined[32:], branch)
		merkleRoot = doubleSHA256(combined)
	}
	block.Write(merkleRoot)

	// Time (4 bytes) - ntime from miner is big-endian hex, need little-endian
	ntimeBytes, err := hex.DecodeString(ntime)
	if err != nil {
		return "", fmt.Errorf("invalid ntime hex: %w", err)
	}
	reverseBytes(ntimeBytes)
	block.Write(ntimeBytes)

	// Bits (4 bytes) - big-endian hex, need little-endian
	bitsBytes, err := hex.DecodeString(job.NBits)
	if err != nil {
		return "", fmt.Errorf("invalid nbits hex: %w", err)
	}
	reverseBytes(bitsBytes)
	block.Write(bitsBytes)

	// Nonce (4 bytes) - from miner, big-endian hex, need little-endian
	nonceBytes, err := hex.DecodeString(nonce)
	if err != nil {
		return "", fmt.Errorf("invalid nonce hex: %w", err)
	}
	reverseBytes(nonceBytes)
	block.Write(nonceBytes)

	// TX count (varint) - 1 coinbase + N transactions
	txCount := 1 + len(job.Transactions)
	writeVarInt(&block, uint64(txCount))

	// Coinbase transaction
	block.Write(coinbase)

	// Additional transactions from block template
	for i, txHex := range job.Transactions {
		txBytes, err := hex.DecodeString(txHex)
		if err != nil {
			return "", fmt.Errorf("invalid transaction[%d] hex: %w", i, err)
		}
		block.Write(txBytes)
	}

	return hex.EncodeToString(block.Bytes()), nil
}

// writeVarInt writes a variable-length integer to the buffer
func writeVarInt(buf *bytes.Buffer, n uint64) {
	if n < 0xfd {
		buf.WriteByte(byte(n))
	} else if n <= 0xffff {
		buf.WriteByte(0xfd)
		buf.WriteByte(byte(n))
		buf.WriteByte(byte(n >> 8))
	} else if n <= 0xffffffff {
		buf.WriteByte(0xfe)
		buf.WriteByte(byte(n))
		buf.WriteByte(byte(n >> 8))
		buf.WriteByte(byte(n >> 16))
		buf.WriteByte(byte(n >> 24))
	} else {
		buf.WriteByte(0xff)
		buf.WriteByte(byte(n))
		buf.WriteByte(byte(n >> 8))
		buf.WriteByte(byte(n >> 16))
		buf.WriteByte(byte(n >> 24))
		buf.WriteByte(byte(n >> 32))
		buf.WriteByte(byte(n >> 40))
		buf.WriteByte(byte(n >> 48))
		buf.WriteByte(byte(n >> 56))
	}
}

func reverseBytes(b []byte) {
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
}

func doubleSHA256(data []byte) []byte {
	first := sha256.Sum256(data)
	second := sha256.Sum256(first[:])
	return second[:]
}

func submitBlockToNode(blockHex string) (string, error) {
	reqBody, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "1.0",
		"id":      "submit",
		"method":  "submitblock",
		"params":  []interface{}{blockHex},
	})
	if err != nil {
		return "", fmt.Errorf("failed to marshal submitblock request: %w", err)
	}

	req, err := http.NewRequest("POST", rpcURL, bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(rpcUser, rpcPass)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var rpcResp struct {
		Result interface{} `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return "", err
	}

	if rpcResp.Error != nil {
		return rpcResp.Error.Message, nil
	}

	if rpcResp.Result == nil {
		return "", nil
	}

	return fmt.Sprintf("%v", rpcResp.Result), nil
}

// internalAuthMiddleware checks that requests come from localhost and have valid auth
// SECURITY: Token is REQUIRED for sensitive endpoints (trigger-payout, etc.)
func internalAuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// The internal stats port is NOT published to the host (compose only exposes it on
		// the private app network), so the api container must be able to reach it by service
		// name over that network. A hard localhost check would 403 the api container, so we
		// gate SOLELY on a constant-time match of the required internal token.
		token := os.Getenv("INTERNAL_API_TOKEN")
		if token == "" {
			log.Printf("🚫 SECURITY: INTERNAL_API_TOKEN not set - blocking all internal API access")
			http.Error(w, "Internal API token not configured", http.StatusServiceUnavailable)
			return
		}

		authHeader := r.Header.Get("X-Internal-Token")
		if subtle.ConstantTimeCompare([]byte(authHeader), []byte(token)) != 1 {
			log.Printf("⚠️ SECURITY: Invalid internal API token (path: %s)", r.URL.Path)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		next(w, r)
	}
}

// HTTP server for stats
func startStatsServer() {
	http.HandleFunc("/internal/miner-auth", internalAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		hash, ok := stratumServer.GetAuthPasswordHash(r.URL.Query().Get("miner"))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"set": ok, "hash": hash})
	}))
	http.HandleFunc("/internal/workers", internalAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		workers := stats.GetManager().GetAllWorkerStats()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"workers": workers,
		})
	}))
	http.HandleFunc("/internal/stats", internalAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		poolStats := stats.GetManager().GetPoolStats()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(poolStats)
	}))
	http.HandleFunc("/internal/rental-stats", internalAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		// Get rental service statistics from stratum server
		rentalStats := stratumServer.GetRentalStats()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"nicehash_miners": rentalStats.NiceHashMiners,
			"mrr_miners":      rentalStats.MRRMiners,
			"other_rentals":   rentalStats.OtherRentals,
			"total_rentals":   rentalStats.TotalRentals,
		})
	}))
	http.HandleFunc("/internal/miner-blocks", internalAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		minerID := r.URL.Query().Get("miner")
		blocks := stats.GetMinerBlocksDB(minerID)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"blocks": blocks,
			"total":  len(blocks),
		})
	}))
	http.HandleFunc("/internal/pool-blocks", internalAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		page := 1
		limit := 25
		if p := r.URL.Query().Get("page"); p != "" {
			if v, err := strconv.Atoi(p); err == nil && v > 0 {
				page = v
			}
		}
		if l := r.URL.Query().Get("limit"); l != "" {
			if v, err := strconv.Atoi(l); err == nil && v > 0 && v <= 100 {
				limit = v
			}
		}
		blocks, total := stats.GetAllPoolBlocksDB(page, limit)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"blocks": blocks,
			"total":  total,
			"page":   page,
			"limit":  limit,
		})
	}))
	http.HandleFunc("/internal/miner-payouts", internalAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		minerID := r.URL.Query().Get("miner")
		payouts, total, totalPaid := stats.GetMinerPayoutsDB(minerID)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"payouts":   payouts,
			"total":     total,
			"totalPaid": totalPaid,
		})
	}))
	http.HandleFunc("/internal/miner-solo-payouts", internalAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		minerID := r.URL.Query().Get("miner")
		payouts, total, totalPaid := stats.GetMinerSoloPayoutsDB(minerID)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"payouts":   payouts,
			"total":     total,
			"totalPaid": totalPaid,
		})
	}))
	http.HandleFunc("/internal/miner-contributions", internalAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		minerID := r.URL.Query().Get("miner")
		contributions := stats.GetMinerBlockContributionsDB(minerID)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"contributions": contributions,
			"total":         len(contributions),
		})
	}))
	http.HandleFunc("/internal/miner-solo-blocks", internalAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		minerID := r.URL.Query().Get("miner")
		blocks := stats.GetMinerSoloBlocksDB(minerID)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"blocks": blocks,
			"total":  len(blocks),
		})
	}))
	http.HandleFunc("/internal/trigger-payout", internalAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		minerID := r.URL.Query().Get("miner")

		// Validate miner address format first
		if minerID == "" || !strings.HasPrefix(minerID, "bitcoincash") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Invalid miner address"})
			return
		}

		// SECURITY: Check if payout already in progress for this miner (prevent double-payout)
		payoutMuMap.Lock()
		if lastPayout, exists := payoutInProgress[minerID]; exists {
			// Check if previous payout is still within cooldown (5 minutes)
			if time.Since(lastPayout) < 5*time.Minute {
				payoutMuMap.Unlock()
				log.Printf("⚠️ SECURITY: Blocked concurrent payout request for %s (last: %v ago)", minerID, time.Since(lastPayout))
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Payout already in progress, please wait"})
				return
			}
		}
		// Mark payout as in progress
		payoutInProgress[minerID] = time.Now()
		payoutMuMap.Unlock()

		// SECURITY: Ensure we clear the in-progress flag on exit
		defer func() {
			payoutMuMap.Lock()
			delete(payoutInProgress, minerID)
			payoutMuMap.Unlock()
		}()

		heightResp, err := rpcCall(rpcURL, "getblockcount", []interface{}{})
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Failed to get height"})
			return
		}
		heightFloat, ok := heightResp.(float64)
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Invalid height response"})
			return
		}
		currentHeight := int64(heightFloat)

		// Reserve-then-send via the shared idempotent path (row-aligned chunks,
		// finalize-per-chunk, never re-broadcast, never blanket-revert sent chunks).
		matureHeight := currentHeight - int64(stats.COINBASE_MATURITY)
		txids, totalSent, err := payMiner(minerID, matureHeight, 5.0)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Payout send failed: " + err.Error()})
			return
		}
		if len(txids) == 0 {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "no mature balance at or above the minimum payout"})
			return
		}
		lastTxid := txids[len(txids)-1]
		stats.MarkMaturePaidWithAmount(minerID, currentHeight, lastTxid, totalSent)
		log.Printf("💰 Manual payout: %s -> %.8f BCH2 in %d tx (last txid: %s)", minerID, totalSent, len(txids), lastTxid)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "txid": lastTxid, "amount": totalSent})
	}))
	http.HandleFunc("/internal/miner-balance", internalAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		minerID := r.URL.Query().Get("miner")
		heightStr := r.URL.Query().Get("height")
		height := int64(0)
		if h, err := strconv.ParseInt(heightStr, 10, 64); err == nil {
			height = h
		}
		// Try database first, fall back to memory
		mature, immature := stats.GetMinerBalanceDB(minerID, height)
		if mature == 0 && immature == 0 {
			mature, immature = stats.GetMinerBalance(minerID, height)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"matureBalance":   mature,
			"immatureBalance": immature,
		})
	}))

	http.HandleFunc("/internal/validate-address", internalAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		address := r.URL.Query().Get("address")
		if address == "" {
			json.NewEncoder(w).Encode(map[string]interface{}{"valid": false, "error": "No address provided"})
			return
		}
		result, err := rpcCall(rpcURL, "validateaddress", []interface{}{address})
		if err != nil {
			json.NewEncoder(w).Encode(map[string]interface{}{"valid": false, "error": err.Error()})
			return
		}
		if validResult, ok := result.(map[string]interface{}); ok {
			isValid, _ := validResult["isvalid"].(bool)
			json.NewEncoder(w).Encode(map[string]interface{}{"valid": isValid})
		} else {
			json.NewEncoder(w).Encode(map[string]interface{}{"valid": false, "error": "Invalid response"})
		}
	}))

	// Debug endpoint to verify block submission readiness
	http.HandleFunc("/internal/block-readiness", internalAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		jobHistoryMu.RLock()
		jobCount := len(jobHistory)
		var jobIDs []string
		for id := range jobHistory {
			jobIDs = append(jobIDs, id)
		}
		jobHistoryMu.RUnlock()

		var currentJobInfo map[string]interface{}
		curJob := getCurrentJob()
		if curJob != nil {
			currentJobInfo = map[string]interface{}{
				"id":     curJob.ID,
				"height": curJob.Height,
				"nbits":  curJob.NBits,
			}
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"ready":            curJob != nil && jobCount > 0,
			"network_diff":     getNetworkDifficulty(),
			"job_history_size": jobCount,
			"job_ids":          jobIDs,
			"current_job":      currentJobInfo,
			"message":          "Block submission will work when share.Difficulty >= network_diff",
		})
	}))

	// Internal endpoints: bind host defaults to localhost but is overridable so compose can
	// expose them to the api container over the private app network (INTERNAL_STATS_HOST).
	// The port is never published to the host; auth is the constant-time internal-token check.
	statsPort := os.Getenv("INTERNAL_STATS_PORT")
	if statsPort == "" {
		statsPort = "3337"
	}
	statsHost := os.Getenv("INTERNAL_STATS_HOST")
	if statsHost == "" {
		statsHost = "127.0.0.1"
	}
	statsAddr := statsHost + ":" + statsPort
	log.Printf("Internal stats server starting on %s", statsAddr)
	if err := http.ListenAndServe(statsAddr, nil); err != nil {
		log.Printf("ERROR: Internal stats server failed: %v", err)
	}
}

// V2ShareProcessor processes shares from the V2 server
type V2ShareProcessor struct {
	logger *zap.Logger
}

func (p *V2ShareProcessor) ProcessShare(ctx context.Context, share *stratumv2.Share) error {
	mode := "PPLNS"
	if share.IsSolo {
		mode = "SOLO"
	}

	networkDiff := getNetworkDifficulty()

	// Track worker stats
	stats.GetManager().UpdateWorker(share.MinerID, share.WorkerName, true, share.Difficulty, share.ActualDiff)

	// Save share to database
	if err := stats.SaveShare(share.MinerID, share.WorkerName, share.Difficulty, share.IsSolo); err != nil {
		p.logger.Warn("Failed to save V2 share to DB", zap.Error(err))
	}

	// Check for block
	if share.ActualDiff >= networkDiff {
		p.logger.Info("🎉 V2 BLOCK CANDIDATE!",
			zap.String("miner", share.MinerID),
			zap.Float64("actual_diff", share.ActualDiff),
			zap.Float64("network_diff", networkDiff),
			zap.Uint32("job_id", share.JobID))
		// V2 block submission would go here
		// For now, we log it - full block submission requires additional work
	}

	p.logger.Debug("V2 share processed",
		zap.String("miner", share.MinerID),
		zap.Float64("diff", share.Difficulty),
		zap.String("mode", mode))
	return nil
}

func (p *V2ShareProcessor) ProcessBlock(ctx context.Context, block *stratumv2.Block) error {
	p.logger.Info("🎉 V2 BLOCK FOUND!",
		zap.String("hash", block.Hash),
		zap.Int64("height", block.Height))
	return nil
}

// V2MinerSettingsAdapter adapts V1 miner settings to V2 interface
type V2MinerSettingsAdapter struct {
	v1Settings stratum.MinerSettingsStore
}

func (a *V2MinerSettingsAdapter) GetMinerSettings(minerID string) (*stratumv2.MinerSettings, error) {
	if a.v1Settings == nil {
		return nil, nil
	}
	v1Settings, err := a.v1Settings.GetMinerSettings(minerID)
	if err != nil || v1Settings == nil {
		return nil, err
	}
	return &stratumv2.MinerSettings{
		MinerID:    v1Settings.MinerID,
		SoloMining: v1Settings.SoloMining,
		ManualDiff: v1Settings.ManualDiff,
		MinPayout:  v1Settings.MinPayout,
	}, nil
}

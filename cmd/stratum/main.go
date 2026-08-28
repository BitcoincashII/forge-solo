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
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/mergemining"
	"github.com/BitcoincashII/forge-solo/internal/mining"
	"github.com/BitcoincashII/forge-solo/internal/stats"
	"github.com/BitcoincashII/forge-solo/internal/stratum"
	"github.com/go-zeromq/zmq4"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/yaml.v3"
)

var (
	logger              *zap.Logger
	jobManager          *mining.JobManager
	currentJob          *mining.Job
	currentJobMu        sync.RWMutex                   // Protects currentJob access
	jobHistory          = make(map[string]*mining.Job) // Store jobs by ID for block submission
	jobHistoryOrder     []string                       // Track insertion order for FIFO cleanup
	jobHistoryMu        sync.RWMutex
	rpcURL              string
	rpcUser             string
	rpcPass             string
	networkDifficulty   float64      = 1.0
	latestCoinbaseBTC   float64      // most recent getblocktemplate coinbasevalue in BTC (guarded by networkDiffMu)
	networkDiffMu       sync.RWMutex // Protects networkDifficulty + latestCoinbaseBTC access
	poolAddress         string
	blockReward         float64         = 50.0
	pplnsWindow         int             = 100000 // PPLNS window size (shares)
	stratumServer       *stratum.Server          // Global reference for API handlers
	stratumRentalServer *stratum.Server          // Second stratum for PROXIED rental hashpower (NiceHash/MiningRigRentals)

	// miningStatus records WHY the job loop is (or is not) producing work, so the
	// dashboard can say something truer than "ready to mine" when a miner is connected
	// and receiving nothing. Written only by the job-broadcast loop, read by the
	// /internal/mining-status handler.
	miningStatusMu  sync.RWMutex
	lastJobAt       time.Time // when a job was last built and broadcast
	lastJobHeight   int64     // height of that job
	lastTemplateErr string    // most recent getblocktemplate failure, "" once one succeeds
	lastShareAt     time.Time // when a share was last ACCEPTED from any miner

	// Shutdown channel for graceful termination
	shutdownCh = make(chan struct{})

	// ZMQ new block notification channel for instant block detection
	zmqBlockCh = make(chan string, 10)

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

// payoutProcessorOnce guards the payout processor against being started twice: main()
// starts it when the database is up at boot, and watchPoolConfig starts it if the database
// only becomes available later.
var payoutProcessorOnce sync.Once

func startPayoutProcessorOnce() {
	payoutProcessorOnce.Do(func() {
		go startPayoutProcessor()
		logger.Info("💰 Payout processor started")
	})
}

// applySoloPayoutAddress tells every stratum server which address the coinbase pays, so a
// solo miner authorizing with a plain worker label ("rig1") is credited under the address
// actually being mined to -- which is also the key the dashboard looks a miner up by.
func applySoloPayoutAddress(addr string) {
	if stratumServer != nil {
		stratumServer.SetSoloPayoutAddress(addr)
	}
	if stratumRentalServer != nil {
		stratumRentalServer.SetSoloPayoutAddress(addr)
	}
}

// aux1175Payout returns the 1175 payout address currently in effect, or "" if merge mining
// is not running.
func aux1175Payout() string {
	if !merge1175Enabled {
		return ""
	}
	return aux1175PayoutAddr
}

// jobDifficultyFor returns the difficulty of the job a share was mined on, or 0 when
// that job is no longer in history. This is the target the miner was actually working
// against, which is what a block candidate must be judged by.
func jobDifficultyFor(jobID string) float64 {
	jobHistoryMu.RLock()
	job, ok := jobHistory[jobID]
	jobHistoryMu.RUnlock()
	if !ok || job == nil {
		return 0
	}
	return bitsToDifficulty(job.NBits)
}

// noteJobBroadcast records a successful job build. Freshness is the only honest proof
// that mining is actually running: a configured job manager and a reachable node still
// produce nothing if the template fetch fails every cycle.
func noteJobBroadcast(height int64) {
	miningStatusMu.Lock()
	lastJobAt = time.Now()
	lastJobHeight = height
	lastTemplateErr = ""
	miningStatusMu.Unlock()
}

// noteShareAccepted records that a miner produced accepted work. This is the only proof
// that the whole chain -- authorize, job delivery, difficulty, validation -- is working;
// jobs being built proves only that the pool is talking to the node.
func noteShareAccepted() {
	miningStatusMu.Lock()
	lastShareAt = time.Now()
	miningStatusMu.Unlock()
}

// noteTemplateError records a getblocktemplate failure so the dashboard can name it.
func noteTemplateError(err error) {
	miningStatusMu.Lock()
	if err != nil {
		lastTemplateErr = err.Error()
	}
	miningStatusMu.Unlock()
}

// miningStatusSnapshot describes, in one place, whether this stratum is producing work
// and if not, why. Consumed by /internal/mining-status -> the api -> the dashboard banner.
type miningStatusSnapshot struct {
	Mining        bool   `json:"mining"`              // a job was broadcast recently
	Configured    bool   `json:"configured"`          // payout address resolved; jobs may be built
	DBConnected   bool   `json:"db_connected"`        // dashboard settings are readable
	Connections   int64  `json:"connections"`         // TCP connections; a refused miner reconnecting inflates this
	Authorized    int64  `json:"authorized"`          // miners that completed mining.authorize
	LastShareAge  int64  `json:"last_share_age_sec"`  // -1 if no share has ever been accepted
	MergeMining   string `json:"merge_mining"`        // off | ok | failing | never_worked
	AuxError      string `json:"aux_error"`           // last aux fetch failure, "" when healthy
	AuxLastOKAge  int64  `json:"aux_last_ok_age_sec"` // -1 if aux work has NEVER been fetched
	LastJobHeight int64  `json:"last_job_height"`     // 0 if none yet
	LastJobAgeSec int64  `json:"last_job_age_sec"`    // -1 if no job has ever been built
	TemplateError string `json:"template_error"`      // last getblocktemplate failure, if any
	Reason        string `json:"reason"`              // machine-readable pause cause, "" when mining
	Message       string `json:"message"`             // one line a home user can act on
}

// buildMiningStatus gathers the live inputs and hands them to the reason ladder.
// The ladder itself is a separate pure function so a test can drive the real decision
// instead of restating it -- a restated copy stays green after the original is deleted.
func buildMiningStatus() miningStatusSnapshot {
	miningStatusMu.RLock()
	jobAt, jobHeight, tmplErr := lastJobAt, lastJobHeight, lastTemplateErr
	miningStatusMu.RUnlock()

	var configured bool
	if jobManager != nil {
		configured = jobManager.IsConfigured()
	}
	// BOTH servers, summed. Counting only stratumServer reported connections=0 and
	// authorized=0 while a rented Antminer was connected to the RENTAL listener on 3335
	// and submitting accepted shares every second or two -- so the dashboard showed
	// "No miner connected" and Workers 0 during a paid rental that was working perfectly.
	//
	// This is the same mistake as /internal/rental-stats, which asked only stratumServer
	// and missed the 3335 listener. That one was found and fixed; nobody then checked
	// whether the pattern existed elsewhere. It did, here.
	var connections, authorized int64
	for _, srv := range []*stratum.Server{stratumServer, stratumRentalServer} {
		if srv == nil {
			continue
		}
		connections += srv.GetStats().ActiveConnections
		authorized += srv.CountAuthorized()
	}
	miningStatusMu.RLock()
	shareAt := lastShareAt
	miningStatusMu.RUnlock()

	var aux mining.AuxHealth
	if jobManager != nil {
		aux = jobManager.AuxHealth()
	}

	st := miningStatusFrom(configured, stats.IsDBConnected(), connections, authorized, jobHeight, jobAt, shareAt, tmplErr, time.Now())
	st.MergeMining, st.AuxError, st.AuxLastOKAge = auxStatusFrom(aux, time.Now())
	return st
}

// auxStaleAfter is how long without a successful getauxblock before merge mining counts as
// failing rather than merely quiet. The aux node is polled on every job build, so this is
// many missed fetches, not one.
// Tied to the freshness rule the job path actually applies. It used to be two minutes while
// fetchAuxWork stopped using cached work after twenty seconds, so for ~100s the dashboard
// showed merge mining green while every job was built with no aux commitment at all.
const auxStaleAfter = 2 * mining.AuxWorkMaxAge

// auxStatusFrom reduces aux health to something a dashboard can render and a user can act
// on. "never_worked" is deliberately distinct from "failing": on a fresh Umbrel install the
// stratum starts while the 1175 node is still doing IBD, so merge mining has never once
// produced work -- and the user needs to be told that rather than shown an address and left
// to assume it is earning.
func auxStatusFrom(a mining.AuxHealth, now time.Time) (state, errText string, lastOKAge int64) {
	if !a.Enabled {
		return "off", "", -1
	}
	lastOKAge = -1
	if !a.LastOKAt.IsZero() {
		lastOKAge = int64(now.Sub(a.LastOKAt).Seconds())
	}
	switch {
	case a.LastOKAt.IsZero():
		return "never_worked", a.LastErr, lastOKAge
	case a.LastErr != "" && lastOKAge > int64(auxStaleAfter.Seconds()):
		return "failing", a.LastErr, lastOKAge
	case a.LastErr != "":
		return "ok", a.LastErr, lastOKAge // a blip; work is still recent
	}
	return "ok", "", lastOKAge
}

// jobStaleAfter is how long without a freshly built job counts as "not mining". A job is
// rebuilt on every new block and at least every 15s regardless, so anything this old means
// the loop has stopped producing even if it once worked.
const jobStaleAfter = 90 * time.Second

// noShareAfter is how long an authorized miner may go without an accepted share before the
// dashboard stops calling it mining. Generous on purpose: at the 1024 floor a very small
// miner can legitimately take minutes, and a false alarm sends the user chasing a fault
// that does not exist.
const noShareAfter = 10 * time.Minute

// miningStatusFrom reports the CURRENT reason no work is flowing, checked in the order the
// job loop itself hits them, so the message always names the first real blocker.
func miningStatusFrom(configured, dbConnected bool, connections, authorized, jobHeight int64, jobAt, shareAt time.Time, tmplErr string, now time.Time) miningStatusSnapshot {
	st := miningStatusSnapshot{
		Configured:    configured,
		DBConnected:   dbConnected,
		Connections:   connections,
		Authorized:    authorized,
		LastJobHeight: jobHeight,
		LastJobAgeSec: -1,
		LastShareAge:  -1,
		TemplateError: tmplErr,
	}
	if !jobAt.IsZero() {
		st.LastJobAgeSec = int64(now.Sub(jobAt).Seconds())
	}
	if !shareAt.IsZero() {
		st.LastShareAge = int64(now.Sub(shareAt).Seconds())
	}

	jobStaleAfterSec := int64(jobStaleAfter.Seconds())
	noShareAfterSec := int64(noShareAfter.Seconds())
	switch {
	case !st.Configured:
		st.Reason = "no_payout_address"
		st.Message = "Mining is paused: no valid BCH2 payout address is in effect. Set (or re-save) it in Settings."
		if !st.DBConnected {
			st.Reason = "db_unavailable"
			st.Message = "Mining is paused: the stratum cannot read your settings (database unavailable), so it has no payout address."
		}
	case st.LastJobAgeSec < 0:
		st.Reason = "no_template_yet"
		st.Message = "Waiting for the first block template from the BCH2 node."
		if tmplErr != "" {
			st.Message = "Cannot build work from the BCH2 node yet: " + tmplErr
		}
	case st.LastJobAgeSec > jobStaleAfterSec:
		st.Reason = "stale_template"
		st.Message = fmt.Sprintf("No new work in %ds — the BCH2 node stopped serving block templates.", st.LastJobAgeSec)
		if tmplErr != "" {
			st.Message += " Last error: " + tmplErr
		}
	case connections == 0:
		// Nothing is attached. There was no rung for this, so an idle stratum fell
		// through to `default: Mining = true` and the dashboard reported mining with
		// nobody connected -- the one state a solo miner most needs to be told about,
		// because a rig that dropped at 3am looks identical to one that is working.
		st.Reason = "no_miners"
		st.Message = "No miner is connected. The node is synced and work is ready — point a miner at the stratum port."
	case connections > 0 && authorized == 0:
		// Jobs are being produced and miners keep arriving, but not one has got past
		// mining.authorize. The usual cause is the worker username, and a refused miner
		// reconnects -- so `connections` climbs while nothing works, which is exactly what
		// "it syncs but will not hash" looks like from the outside.
		st.Reason = "miners_refused"
		st.Message = "Miners are connecting but none are authorizing. Check the worker username in your miner — any label works, or use your BCH2 address."
	case authorized > 0 && (st.LastShareAge < 0 || st.LastShareAge > noShareAfterSec):
		// Connected, authorized and receiving work, yet producing nothing.
		st.Reason = "no_shares"
		st.Message = "A miner is connected and receiving work but has not submitted an accepted share recently. Check that the miner is actually hashing."
	default:
		st.Mining = true
	}
	return st
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

// blockCoinbaseBTC picks the coinbase value to record against a solved block.
//
// A job carries the exact satoshi value baked into the coinbase the miner hashed
// -- subsidy plus the fees of the transactions in THAT job. The process-global
// fallback is refreshed on every template poll, so it describes the most recent
// template rather than the block just solved: any template refresh landing
// between a job going out and its share coming back records the wrong fee total.
// Prefer the job's own value; fall back only for jobs that carry none.
func blockCoinbaseBTC(jobCoinbaseValue int64, latestGlobalBTC float64) float64 {
	if jobCoinbaseValue > 0 {
		return float64(jobCoinbaseValue) / 1e8
	}
	return latestGlobalBTC
}

func getLatestCoinbaseBTC() float64 {
	networkDiffMu.RLock()
	defer networkDiffMu.RUnlock()
	return latestCoinbaseBTC
}

// ---- 1175 merge-mining payout ----

var (
	merge1175Enabled bool
	// aux1175PayoutAddr is the esf1… address merge mining is currently paying. Recorded so
	// watchPoolConfig can seed itself with what actually took effect rather than with what
	// the database says, and therefore retry an address that failed to come up.
	aux1175PayoutAddr string
	aux1175NodeURL    string
	aux1175User       string
	aux1175Pass       string
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

// aux1175Maturity is the active-chain confirmation depth a 1175 aux block must reach
// before its credits are payable (1175 coinbase maturity).
const aux1175Maturity = 100

// aux1175BlockHandler durably records a found aux block and distributes its reward.
// The record is committed BEFORE distribution so a transient distribution failure is
// retried by the processor rather than losing the block. Invoked (in the stratum
// submit goroutine) only when submitauxblock is accepted.
func aux1175BlockHandler(height int64, hash string, coinbaseValueSat int64, finder string, isSolo bool) {
	gross := float64(coinbaseValueSat) / 1e8

	// The aux tip just moved. Cached aux work now names a parent that has been superseded,
	// and every job built until the next scheduled poll would commit to it -- on 1175's
	// ~600s mainnet spacing that is a few percent of merge-mined blocks committing to a
	// dead parent and forfeiting the aux reward. Ask for a poll immediately; it is
	// non-blocking and never touches the job-build path.
	if jobManager != nil {
		jobManager.RefreshAuxNow()
	}

	// Two candidates for one aux height is not a reorg -- it is the ordinary case of this
	// pool solving two siblings on the same parent, which happened in testing. The ledger's
	// supersede replaces whatever is recorded with whatever arrived LAST, so without this
	// the loser overwrites the winner and the block actually on the chain vanishes from the
	// ledger entirely. The chain decides, not arrival order.
	if existing, ok := stats.Get1175BlockHashAtHeight(height); ok && existing != hash {
		if _, onChain := aux1175BlockConfirmations(existing); onChain {
			logger.Info("💠 1175 sibling at an already-recorded height; the recorded block is the one on the aux chain — keeping it",
				zap.Int64("height", height),
				zap.String("recorded", existing),
				zap.String("this_sibling", hash))
			return
		}
	}

	if err := stats.Record1175Block(height, hash, gross, finder, isSolo); err != nil {
		logger.Error("1175 record block FAILED (block may be lost — verify)", zap.Int64("height", height), zap.String("hash", hash), zap.Error(err))
		return
	}
	if err := stats.Distribute1175Block(height, pplnsWindow); err != nil {
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
	logger.Info("💰 1175 payout processor started")
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
			if err := stats.Distribute1175Block(h, pplnsWindow); err != nil {
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
}

func startPayoutProcessor() {
	ticker := time.NewTicker(60 * time.Second) // Check every minute
	defer ticker.Stop()

	// Use global rpcURL configured from config file
	nodeURL := rpcURL

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

		// Periodic cleanup of old paid payouts from memory (every cycle)
		stats.CleanupPaidPayouts()
	}
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
// bitsToDifficulty delegates to the stratum package so the block-candidate test here and
// the intake limiter's "solves a block" exception there can never disagree.
func bitsToDifficulty(bitsHex string) float64 {
	return stratum.BitsToDifficulty(bitsHex)
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
		"pool":      "Forge Solo",
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
	var lastZMQWarn time.Time
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
			// Throttled: this retries forever, and the dial itself takes a few seconds, so
			// an endpoint that is simply not there produced thousands of identical warnings
			// a day. The interval is deliberately not quoted in the message -- it is the
			// sleep below plus however long the failing dial took.
			if time.Since(lastZMQWarn) >= time.Minute {
				lastZMQWarn = time.Now()
				logger.Warn("Cannot connect to ZMQ endpoint; retrying (further identical warnings suppressed for 1m). Block detection falls back to polling, so mining is unaffected. Set node.zmq_endpoint empty to disable ZMQ.",
					zap.String("endpoint", zmqEndpoint),
					zap.Error(err))
			}
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
	// No wallet URL is derived here: merge mining is aux-coinbase-direct. getauxblock is
	// given the payout address explicitly and the 1175 node builds the aux coinbase itself,
	// so the node needs no wallet and this path never touches one.
	merge1175Enabled = true
	aux1175PayoutAddr = auxPayout
	aux1175NodeURL = auxURL
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
	// lastDB1175 tracks what the DATABASE last said, as opposed to last1175 which tracks
	// what actually took effect. Only a transition from a non-empty DB value to an empty
	// one is a user clearing the field.
	var lastDB1175 string
	// Seed with the values that were actually APPLIED at startup, so we only act on real
	// changes -- and so anything that failed to apply is retried on the first tick.
	//
	// Deliberately NOT a fresh read of the database here. Two different failures come from
	// seeding what the DB currently says instead of what took effect:
	//
	//   * If startup resolution failed -- the node RPC not answering inside NewJobManager's
	//     ~20s window on a cold restart is the common one, since the stratum container does
	//     not wait for the node -- then seeding that address makes `pool != lastPool` false
	//     forever and the retry below never fires. That is a permanently paused miner: node
	//     synced, dashboard showing the address, no jobs, and no further log line.
	//   * If the user SAVED a new address during that same window, a fresh read returns the
	//     NEW address while the job manager is mining to the OLD one. Seeding the new value
	//     latches the change out forever: the coinbase keeps paying the old address while
	//     the dashboard reports the new one. Seeding the applied value instead makes the
	//     first tick notice the difference and apply it.
	//
	// poolAddress holds exactly the address NewJobManager was given (main sets it from
	// effectivePoolAddr), so it is the applied value whenever the job manager is configured.
	if jm.IsConfigured() {
		lastPool = poolAddress
	}
	// Same rule for the aux chain: seed only if merge mining actually came up, otherwise a
	// 1175 address that failed to take effect is never retried while Settings reports it on.
	if merge1175Enabled {
		last1175 = aux1175Payout()
	}
	if stats.IsDBConnected() {
		if _, p1175, _, err := stats.GetPoolConfig(); err == nil {
			lastDB1175 = p1175
		}
	}
	// lastTag is left empty on purpose: SetCoinbaseTag is idempotent, so the cost of not
	// seeding it is one redundant call on the first tick, against the risk of latching out
	// a tag the user changed during startup.
	for {
		select {
		case <-shutdownCh:
			return
		case <-ticker.C:
		}
		// Retry the env/config address (POOL_ADDRESS) whenever mining is unconfigured.
		// The stratum container does not depend_on the BCH2 node, and the node's own
		// healthcheck allows it 180s to come up, so NewJobManager's ~20s RPC window
		// routinely expires before validateaddress can answer on a cold start. This path
		// needs no database, so it also covers an install configured purely by env.
		if !jm.IsConfigured() && poolAddress != "" {
			if serr := jm.SetPoolAddress(poolAddress); serr == nil {
				applySoloPayoutAddress(poolAddress)
				logger.Info("✅ payout address resolved on retry — mining active", zap.String("address", poolAddress))
			}
		}
		if !stats.IsDBConnected() {
			// The DB may never have connected at all (InitDBWithRetry gives up after ~60s
			// and main() continues "memory only"). Without this the stratum stays deaf to
			// the dashboard for the life of the process: the payout address lives in the
			// DB, so no DB means no address, means no jobs, means a miner that connects
			// and never hashes while everything else looks healthy. Retry here rather than
			// exiting so an already-mining stratum is never taken down by a DB blip.
			//
			// Gated on IsDBInitialized, NOT on the failed IsDBConnected ping above: a pool
			// that exists heals itself, and InitDB replaces the handle without closing it,
			// so retrying on a mid-outage ping failure would leak a pool every 8 seconds.
			if stats.IsDBInitialized() {
				continue
			}
			if dbErr := stats.InitDB(stats.GetDBConnStr()); dbErr != nil {
				continue
			}
			logger.Info("✅ database connection established — dashboard config is live")
			stats.LoadAllPendingPayouts()
			// The payout processor is started from main() only when the DB was up at boot.
			// It owns solo block reconciliation (reconcileSoloBlocks, ConfirmMatureSoloBlocks,
			// reconcileOrphanHeights), so without this a block found after a late reconnect
			// would sit pending forever and an orphaned one would never be voided.
			startPayoutProcessorOnce()
		}
		pool, p1175, tag, err := stats.GetPoolConfig()
		if err != nil {
			continue
		}
		if pool != lastPool && pool != "" {
			if serr := jm.SetPoolAddress(pool); serr != nil {
				logger.Warn("dashboard payout address rejected — keeping previous", zap.String("address", pool), zap.Error(serr))
			} else {
				applySoloPayoutAddress(pool)
				logger.Info("✅ payout address updated from dashboard — mining active", zap.String("address", pool))
				lastPool = pool
			}
		}
		// Blank is a real value here: the dashboard clears a tag by sending an empty one,
		// and SetCoinbaseTag sanitises "" back to the built-in default.
		if tag != lastTag {
			jm.SetCoinbaseTag(tag)
			logger.Info("coinbase tag updated from dashboard", zap.String("tag", tag))
			lastTag = tag
		}
		// Clearing the 1175 address in the DASHBOARD turns merge mining off at runtime.
		// Without this the field could be emptied and saved while the previous address kept
		// right on being mined to until the next restart, with the UI showing nothing.
		//
		// Gated on the DATABASE having previously held a value, not on what is currently in
		// effect. Those are different, and conflating them was a bug caught by running the
		// thing: an install configured by config file or env has an aux address in effect
		// and an EMPTY database, so "in effect but not in the DB" looked exactly like "the
		// user just cleared it" and merge mining switched itself off eight seconds after
		// every startup.
		if p1175 == "" && lastDB1175 != "" && merge1175Enabled {
			jm.DisableMergeMining()
			merge1175Enabled = false
			aux1175PayoutAddr = ""
			if stratumServer != nil {
				stratumServer.DisableMergeMining()
			}
			if stratumRentalServer != nil {
				stratumRentalServer.DisableMergeMining()
			}
			logger.Info("💠 1175 merge-mining DISABLED from dashboard (address cleared) — BCH2 mining continues")
			last1175 = ""
		}
		lastDB1175 = p1175
		if p1175 != last1175 && p1175 != "" {
			if !merge1175Enabled {
				ac := enableMergeMining1175(cfg, p1175)
				if stratumServer != nil {
					stratumServer.EnableMergeMining(ac)
					stratumServer.SetAuxBlockHandler(aux1175BlockHandler)
				}
				if stratumRentalServer != nil {
					stratumRentalServer.EnableMergeMining(ac)
					stratumRentalServer.SetAuxBlockHandler(aux1175BlockHandler)
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
				aux1175PayoutAddr = p1175
				logger.Info("💠 1175 payout address updated from dashboard", zap.String("payout_address_1175", p1175))
			}
			last1175 = p1175
		}
	}
}

// buildLogger honours logging.level and logging.format ("console" or "json"). Both were
// shipped config keys read by nothing.
func buildLogger(configPath string) (*zap.Logger, error) {
	level, format := "info", "json"
	if raw, err := os.ReadFile(configPath); err == nil {
		var doc struct {
			Logging struct {
				Level  string `yaml:"level"`
				Format string `yaml:"format"`
			} `yaml:"logging"`
		}
		if yaml.Unmarshal(raw, &doc) == nil {
			if doc.Logging.Level != "" {
				level = doc.Logging.Level
			}
			if doc.Logging.Format != "" {
				format = doc.Logging.Format
			}
		}
	}
	cfg := zap.NewProductionConfig()
	if format == "console" {
		cfg.Encoding = "console"
		cfg.EncoderConfig = zap.NewDevelopmentEncoderConfig()
	}
	if lv, err := zapcore.ParseLevel(level); err == nil {
		cfg.Level = zap.NewAtomicLevelAt(lv)
	}
	return cfg.Build()
}

// recordInvalidShare charges a rejected share to the worker that sent it, so the
// dashboard's Reject % tile reports what the stratum actually rejected. Both
// stratum servers share the one stats manager, so both feed the same counters.
func recordInvalidShare(minerID, workerName, reason string) {
	stats.GetManager().RecordInvalidShare(minerID, workerName)
	_ = reason // reason rides along for future per-cause reporting; the log already carries it
}

// sumRentalStats totals the rented-hashpower counters across every stratum server that
// is running. Servers are skipped when nil: the rental listener is optional, and this runs
// on an HTTP handler where a nil dereference would take the process down.
func sumRentalStats(servers ...*stratum.Server) stratum.RentalStats {
	var total stratum.RentalStats
	for _, srv := range servers {
		if srv == nil {
			continue
		}
		s := srv.GetRentalStats()
		if s == nil {
			continue
		}
		total.NiceHashMiners += s.NiceHashMiners
		total.MRRMiners += s.MRRMiners
		total.OtherRentals += s.OtherRentals
		total.TotalRentals += s.TotalRentals
	}
	return total
}

func main() {
	configPath := flag.String("config", "config.yaml", "Path to config file")
	flag.Parse()

	// Honour logging.level / logging.format from the config, which were shipped keys that
	// nothing read: the logger was hardcoded to zap.NewProduction() regardless. Read before
	// loadConfig runs properly, so this uses a minimal early read of the same file.
	var logErr error
	logger, logErr = buildLogger(*configPath)
	if logErr != nil {
		log.Fatalf("Failed to initialize logger: %v", logErr)
	}
	defer logger.Sync()

	logger.Info("🔥 Forge Solo - BCH2 Solo Miner")

	// Initialize database with credentials from environment
	dbConnStr := stats.GetDBConnStr()
	if dbErr := stats.InitDBWithRetry(dbConnStr, 30, 2*time.Second); dbErr != nil {
		logger.Warn("Database not available, using memory only", zap.Error(dbErr))
	} else {
		logger.Info("✅ Connected to database")
		stats.LoadAllPendingPayouts()
		// Note: startPayoutProcessor is started later after config is loaded
	}
	defer stats.CloseDB()

	config, err := loadConfig(*configPath)
	if err != nil {
		logger.Fatal("Failed to load config", zap.Error(err))
	}

	// Forge Solo is solo-only, and that is not a preference -- the whole payout model
	// depends on it. Every reward is paid on-chain by the block's own coinbase to the
	// configured address; there is no operator wallet. In any other scheme miners
	// authorize as non-solo and payouts become sendable rows targeting a wallet this app
	// does not have.
	//
	// The guarantee rested on one line in the shipped template with no code-level default,
	// so a missing or misspelled key flipped the app to PPLNS in silence. loadConfig now
	// defaults it to "solo"; anything explicitly set to something else is refused here,
	// loudly, in the style of the extranonce invariant below.
	if scheme := config.GetString("pool.payout_scheme"); scheme != "solo" {
		logger.Fatal("Forge Solo is solo-only; pool.payout_scheme must be \"solo\"",
			zap.String("payout_scheme", scheme))
	}

	serverConfig := &stratum.ServerConfig{
		Host:               config.GetString("stratum.host"),
		Port:               config.GetInt("stratum.port"),
		MaxConnections:     config.GetInt("stratum.max_connections"),
		MaxSharesPerSecond: config.GetInt("stratum.max_shares_per_second"),
		VardiffEnabled:     config.GetBool("stratum.vardiff.enabled"),
		MinDiff:            config.GetFloat64("stratum.vardiff.min_diff"),
		// Accepted as a percentage in the config (25) and used as a fraction (0.25).
		VariancePercent: config.GetFloat64("stratum.vardiff.variance_percent") / 100.0,
		// Optional. Lowest difficulty a non-rental miner may be ASSIGNED; unset (0) means
		// "same as min_diff", which is what the shipped config wants. Values ABOVE min_diff
		// are clamped down in NewServer, because the assignment floor must never exceed the
		// judging floor.
		AbsoluteMinDiff:   config.GetFloat64("stratum.vardiff.absolute_min_diff"),
		RentalMinDiff:     config.GetFloat64("stratum.vardiff.rental_min_diff"),
		RentalMaxDiff:     config.GetFloat64("stratum.vardiff.rental_max_diff"),
		MaxDiff:           config.GetFloat64("stratum.vardiff.max_diff"),
		TargetShareTime:   config.GetInt("stratum.vardiff.target_time"),
		RetargetTime:      config.GetInt("stratum.vardiff.retarget_time"),
		HighHashThreshold: config.GetInt("stratum.high_hash_threshold"),
		HighHashDiff:      config.GetFloat64("stratum.high_hash_diff"),
		ExtraNonce1Size:   config.GetInt("stratum.extranonce1_size"),
		ExtraNonce2Size:   config.GetInt("stratum.extranonce2_size"),
		ServerName:        "main",
		SoloOnly:          config.GetString("pool.payout_scheme") == "solo",
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
	logger.Info("RPC URL configured", zap.String("url", rpcURL))

	rpcUser, rpcPass = getRPCCredentials()

	// Load pool configuration
	poolAddress = config.GetString("pool.address")
	blockReward = config.GetFloat64("pool.block_reward")
	pplnsWindow = config.GetInt("pool.pplns_window")
	if pplnsWindow <= 0 {
		pplnsWindow = 100000 // Default PPLNS window
	}

	logger.Info("Mining configuration loaded",
		zap.String("address", poolAddress),
		zap.Float64("block_reward", blockReward),
		zap.Int("pplns_window", pplnsWindow))

	// Dashboard-managed config (DB pool_config) OVERRIDES the env-derived values when set,
	// so the whole app is configurable from the web UI with no SSH/restart. Falls back to
	// the env/config values when the DB row is empty or the DB is unavailable.
	effectivePoolAddr := poolAddress
	effectiveCoinbaseTag := config.GetString("pool.coinbase_tag")
	effective1175Payout := config.GetString("mergemining.payout_address")
	if stats.IsDBConnected() {
		if dbPool, db1175, dbTag, cErr := stats.GetPoolConfig(); cErr == nil {
			if dbPool != "" {
				effectivePoolAddr = dbPool
			}
			if db1175 != "" {
				effective1175Payout = db1175
			}
			if dbTag != "" {
				effectiveCoinbaseTag = dbTag
			}
		} else {
			logger.Warn("could not read dashboard pool_config; using env/config values", zap.Error(cErr))
		}
	}
	poolAddress = effectivePoolAddr

	// Start payout processor now that config is loaded
	if stats.IsDBConnected() {
		startPayoutProcessorOnce()
	}

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
	stratumServer.SetInvalidShareHandler(recordInvalidShare)

	// Logged AFTER NewServer so it reports the RESOLVED values: NewServer fills in the
	// defaults and clamps absolute_min_diff down to min_diff. min_diff is the floor a
	// share is JUDGED against; absolute_min_diff is the lowest a miner may be ASSIGNED.
	// If these two ever print with absolute > min, the ordering invariant has been broken.
	logger.Info("Vardiff configuration",
		zap.Bool("enabled", serverConfig.VardiffEnabled),
		zap.Float64("min_diff", serverConfig.MinDiff),
		zap.Float64("absolute_min_diff", serverConfig.AbsoluteMinDiff),
		zap.Float64("rental_min_diff", serverConfig.RentalMinDiff),
		zap.Float64("max_diff", serverConfig.MaxDiff),
		zap.Int("target_time", serverConfig.TargetShareTime),
		zap.Int("retarget_time", serverConfig.RetargetTime))

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
	if config.GetBool("stratum_rental.enabled") {
		rentalConfig := &stratum.ServerConfig{
			Host:            config.GetString("stratum_rental.host"),
			Port:            config.GetInt("stratum_rental.port"),
			MaxConnections:  config.GetInt("stratum_rental.max_connections"),
			VardiffEnabled:  config.GetBool("stratum_rental.vardiff.enabled"),
			MinDiff:         config.GetFloat64("stratum_rental.vardiff.min_diff"),
			MaxDiff:         config.GetFloat64("stratum_rental.vardiff.max_diff"),
			TargetShareTime: config.GetInt("stratum_rental.vardiff.target_time"),
			RetargetTime:    config.GetInt("stratum_rental.vardiff.retarget_time"),
			ExtraNonce1Size: config.GetInt("stratum_rental.extranonce1_size"),
			ExtraNonce2Size: config.GetInt("stratum_rental.extranonce2_size"),
			ServerName:      "rental",
			SoloOnly:        config.GetString("pool.payout_scheme") == "solo",
		}
		if got := rentalConfig.ExtraNonce1Size + rentalConfig.ExtraNonce2Size; got != mining.CoinbaseExtranonceReserve {
			logger.Fatal("stratum_rental extranonce1_size + extranonce2_size must equal the coinbase reserve, else assembled blocks are malformed and rejected",
				zap.Int("extranonce1_size", rentalConfig.ExtraNonce1Size),
				zap.Int("extranonce2_size", rentalConfig.ExtraNonce2Size),
				zap.Int("sum", got),
				zap.Int("required", mining.CoinbaseExtranonceReserve))
		}
		stratumRentalServer = stratum.NewServer(rentalConfig, logger, shareProcessor, minerSettings)
		stratumRentalServer.SetInvalidShareHandler(recordInvalidShare)
		if auxClient != nil {
			stratumRentalServer.EnableMergeMining(auxClient)
			stratumRentalServer.SetAuxBlockHandler(aux1175BlockHandler)
		}
		if err := stratumRentalServer.Start(); err != nil {
			logger.Error("Failed to start the rental stratum", zap.Error(err))
		} else {
			logger.Info("✅ Rental stratum running (NiceHash/MiningRigRentals)",
				zap.Int("port", rentalConfig.Port),
				zap.Int("extranonce2_size", rentalConfig.ExtraNonce2Size))
		}
	}

	// Stratum V2 is not implemented in this build. The previous implementation was removed
	// rather than shipped disabled: a solved V2 block was logged and discarded (submission
	// was never written), the bridge never reversed the prev-hash so a V2 share could not be
	// a valid block anyway, and it had no duplicate check and no vardiff. Refuse loudly and
	// keep mining on V1 -- NOT logger.Fatal, because this runs after :3333 is already
	// listening, and aborting would turn a config typo into a crash loop in which no miner of
	// any kind can connect.
	if config.GetBool("stratumv2.enabled") {
		logger.Error("⛔ stratumv2.enabled is set, but Stratum V2 is not supported by this build. " +
			"Mining continues on the V1 stratum; remove the stratumv2 section from your config to silence this.")
	}

	// Start worker timeout detection (marks workers offline after 5 min of no shares)
	workerTimeoutStop := make(chan struct{})
	go stats.GetManager().StartWorkerTimeoutChecker(workerTimeoutStop)

	go startStatsServer()

	// Watch the dashboard-managed pool_config (DB) and apply changes live — payout address,
	// coinbase tag, and first-time 1175 merge-mining enable — with no restart or SSH.
	// AFTER every stratum server exists. This used to run right after the main server was
	// constructed, which is before the rental server is created -- so on a normal boot with
	// the address already in the database, the rental port never learned it and rejected
	// every plain worker label while the main port accepted them.
	if jobManager.IsConfigured() {
		applySoloPayoutAddress(effectivePoolAddr)
	}

	go watchPoolConfig(jobManager, config)

	// 1175 merge-mining payout processor (pays miners their accrued 1175).
	if merge1175Enabled && stats.IsDBConnected() {
		go start1175PayoutProcessor()
	}

	logger.Info("✅ Stratum server running", zap.Int("port", serverConfig.Port))

	// Start ZMQ block notification listener for instant block detection
	// An empty endpoint means DISABLED. It used to be silently replaced with a hardcoded
	// tcp://127.0.0.1:28332, so an operator whose node does not publish ZMQ there could not
	// turn the listener off by any means and collected a reconnect warning every few
	// seconds forever. Block detection falls back to 1s polling, which is what the job loop
	// already does.
	zmqEndpoint := config.GetString("node.zmq_endpoint")
	if zmqEndpoint == "" {
		logger.Info("ZMQ disabled (node.zmq_endpoint is empty) — new blocks are detected by polling")
	} else {
		go startZMQListener(zmqEndpoint, logger)
	}

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
		var lastPausedLog time.Time
		var lastAuxLog time.Time
		var lastTemplateLog time.Time
		var auxWasFailing bool

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
			//
			// Say so periodically. Silently doing nothing here is indistinguishable from
			// working correctly in the logs, and it is the state a miner sees as "connected
			// but never hashing" -- the one failure a home user cannot diagnose alone.
			if !jobManager.IsConfigured() {
				if time.Since(lastPausedLog) >= time.Minute {
					lastPausedLog = time.Now()
					logger.Warn("⏸️  MINING PAUSED — no valid BCH2 payout address in effect. " +
						"Set (or re-save) it in the dashboard Settings page; miners may connect but will receive no work.")
				}
				continue
			}

			template, err := jobManager.GetBlockTemplate()
			if err != nil {
				// Throttled like the pause warning six lines up, which it was not. This
				// loop runs every second, so a node that is syncing, restarting or
				// reindexing produced ~3,600 ERROR lines an hour and the log a user sends
				// for support became entirely this line, rotating the real cause out.
				noteTemplateError(err)
				if time.Since(lastTemplateLog) >= time.Minute {
					lastTemplateLog = time.Now()
					logger.Error("Failed to get block template (further identical errors suppressed for 1m)", zap.Error(err))
				}
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

			// Merge-mining health, said out loud. A persistent aux fault previously produced
			// exactly one un-levelled line for its whole duration, so an install that never
			// mined a single 1175 block looked identical in the log to one that did.
			if aux := jobManager.AuxHealth(); aux.Enabled {
				state, auxErr, _ := auxStatusFrom(aux, time.Now())
				failing := state == "failing" || state == "never_worked"
				if failing && time.Since(lastAuxLog) >= time.Minute {
					lastAuxLog = time.Now()
					auxWasFailing = true
					msg := "⚠️  1175 merge-mining is enabled but NOT producing work — BCH2 mining is unaffected and continues normally."
					if state == "never_worked" {
						msg = "⚠️  1175 merge-mining is enabled but has NEVER produced work since startup (the 1175 node may still be syncing) — BCH2 mining is unaffected and continues normally."
					}
					logger.Warn(msg, zap.String("aux_error", auxErr), zap.String("payout_address_1175", aux.Payout))
				}
				if !failing && auxWasFailing {
					auxWasFailing = false
					logger.Info("✅ 1175 merge-mining recovered — aux work is flowing again")
				}
			}

			curJob := getCurrentJob()
			isNewBlock := template.Height != lastHeight || template.PreviousBlockHash != lastPrevHash || curJob == nil
			needPeriodicUpdate := time.Since(lastJobTime) >= 15*time.Second // Faster updates for NiceHash

			if isNewBlock || needPeriodicUpdate {
				job := jobManager.CreateJob(template)
				if job == nil {
					continue
				}
				setCurrentJob(job)
				noteJobBroadcast(job.Height)

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
				if stratumRentalServer != nil {
					stratumRentalServer.BroadcastJob(stratumJob)
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
	if stratumRentalServer != nil {
		stratumRentalServer.Stop()
	}
	stratumServer.Stop()
	if stratumRentalServer != nil {
		stratumRentalServer.Stop()
	}
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

	// Mining defaults. No fee key of any kind: this app takes none on any path, and a
	// default here would be a live deduction the shipped template could not switch off.
	v.SetDefault("pool.block_reward", 50.0)
	// payout_scheme has no default in the template's absence, and its absence silently
	// flips the app to PPLNS -- authorizing miners as non-solo and queueing payouts against
	// a wallet this app does not have. Solo is the only mode it supports.
	v.SetDefault("pool.payout_scheme", "solo")
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
	noteShareAccepted()

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

	// Judge the candidate against the target of the job it was ACTUALLY mined on, not
	// against whatever the network difficulty has since become. BCH2 retargets with ASERT
	// on every block, so the global value routinely describes the NEXT block by the time a
	// winning share for the previous one arrives -- and a solo miner that discards a valid
	// block because the number moved has lost the entire reward. min() with the current
	// value means this can only ever submit MORE candidates than before, never fewer; a
	// superfluous submitblock costs one rejected RPC call.
	submitThreshold := networkDiff
	if jobDiff := jobDifficultyFor(share.JobID); jobDiff > 0 && jobDiff < submitThreshold {
		submitThreshold = jobDiff
	}

	if share.ActualDiff >= submitThreshold {
		p.logger.Info("🎉 BLOCK CANDIDATE!",
			zap.Float64("submit_threshold", submitThreshold),
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
		// The template's coinbasevalue is the truth: subsidy PLUS the block's transaction
		// fees. Preferring it in BOTH directions matters -- the old code only ever adjusted
		// DOWNWARD (a guard against a stale block_reward after a halving), so on mainnet,
		// where coinbasevalue exceeds the 50.0 default by the fee total, every solo block
		// was recorded, reported and webhooked as exactly 50.0 and under-stated its own
		// reward by its fees. Invisible on an empty regtest chain, where every block is
		// subsidy-only.
		cbv := blockCoinbaseBTC(job.CoinbaseValue, getLatestCoinbaseBTC())
		effectiveReward := blockReward
		if cbv > 0 && cbv != effectiveReward {
			// Only a configured reward ABOVE the coinbase is an operator error worth
			// shouting about (a stale block_reward after a halving). Below it is the
			// ordinary mainnet case -- the coinbase carries the block's fees on top of
			// the subsidy -- and logging that at Error made every healthy block look
			// like a fault.
			if blockReward > cbv {
				p.logger.Error("block_reward config exceeds this block's coinbase value; capping payout (update pool.block_reward after a halving)",
					zap.Float64("configured_reward", blockReward),
					zap.Float64("coinbase_value", cbv),
					zap.Int64("height", job.Height))
			} else {
				p.logger.Info("recording block reward from its own coinbase (subsidy plus fees)",
					zap.Float64("configured_reward", blockReward),
					zap.Float64("coinbase_value", cbv),
					zap.Int64("height", job.Height))
			}
			effectiveReward = cbv
		}

		// No fee. The block's own coinbase pays the configured payout address the FULL
		// reward, so the recorded payout IS the reward -- there is nothing to deduct and no
		// operator wallet to deduct it into. Deducting here would not take money from the
		// miner (the chain already paid them in full); it would under-report what they got.
		mode := "SOLO"
		if !share.IsSolo {
			mode = "PPLNS"
		}
		payoutAmount := effectiveReward
		hashStr := hex.EncodeToString(blockHash)

		p.logger.Info("🎉🎉🎉 BLOCK ACCEPTED BY NODE! 🎉🎉🎉",
			zap.Int64("height", job.Height),
			zap.String("miner", share.MinerID),
			zap.String("mode", mode),
			zap.Float64("reward", effectiveReward),
			zap.Float64("payout", payoutAmount))

		// Record block for miner stats with effort tracking for luck calculation
		stats.RecordMinerBlockWithWorkerSolo(share.MinerID, share.WorkerName, job.Height, hashStr, effectiveReward, share.IsSolo)
		stats.GetManager().RecordBlockWithEffort(hashStr, getNetworkDifficulty())

		// Send webhook alert for block found
		go sendWebhookAlert("block_found", map[string]interface{}{
			"height": job.Height,
			"hash":   hashStr,
			"miner":  share.MinerID,
			"worker": share.WorkerName,
			"mode":   mode,
			"reward": effectiveReward,
			"payout": payoutAmount,
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
	// For block, we need little-endian, so reverse the bytes.
	//
	// stratum.RollVersion is the SAME function the share validator used to decide this
	// share won. Calling it rather than repeating the merge is what guarantees the
	// submitted header is the header that was validated -- including for malformed
	// versionBits, which this path used to treat as a fatal error and so threw away the
	// block for a share the validator had happily accepted.
	versionBytes := stratum.RollVersion(job.Version, versionBits)
	if len(versionBytes) == 0 {
		return "", fmt.Errorf("invalid version hex: %q", job.Version)
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
	// Why work is or is not flowing. The dashboard's own view (node synced + an address
	// saved) cannot see a stratum that rejected that address or never got a template, and
	// that gap is exactly what a "it syncs but will not hash" report looks like.
	http.HandleFunc("/internal/mining-status", internalAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(buildMiningStatus())
	}))
	http.HandleFunc("/internal/rental-stats", internalAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		// Rented-hashpower counters (NiceHash / MiningRigRentals / other), summed across
		// BOTH stratum servers.
		//
		// This asked only the 3333 server, which is the one port an aggregated rental order
		// does NOT arrive on: NiceHash and MiningRigRentals are pointed at 3335, so their
		// connections live in stratumRentalServer's client map and were never counted. The
		// public "rentals" block in /api/v1/stats therefore reported {0,0,0,0} to external
		// aggregators while a rental order was actively mining.
		rentalStats := sumRentalStats(stratumServer, stratumRentalServer)
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

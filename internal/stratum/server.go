package stratum

import (
	"bufio"
	"bytes"
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/mergemining"
	"github.com/BitcoincashII/forge-solo/internal/netlisten"
	"go.uber.org/zap"
)

const (
	// MaxDifficultyMultiplier caps how much difficulty can change in one adjustment
	MaxDifficultyMultiplier = 1.5 // Max 50% change per adjustment (miningcore style)

	// VardiffVariancePercent - only adjust if outside this variance from target
	VardiffVariancePercent = 0.30 // 30% variance allowed

	// VardiffMinShares is the minimum shares needed before vardiff adjusts
	VardiffMinShares = 10

	// VardiffSampleShares is how many of a miner's latest shares vardiff measures its rate over at
	// the least (all of them while it has fewer). See measuredShareTime.
	VardiffSampleShares = 30

	// VardiffSampleTime is how far back vardiff measures a miner's rate where that holds more than
	// VardiffSampleShares shares (up to the maxShareSamples on record). See measuredShareTime.
	VardiffSampleTime = 240 * time.Second

	// RecentSubmissionsWindow is the number of submissions to track for rejection rate
	RecentSubmissionsWindow = 50

	// MaxRejectionRate is the rejection rate above which difficulty is reduced
	// Set higher (30%) to allow natural statistical variance in share difficulty
	MaxRejectionRate = 0.30 // 30%

	// DifficultyReductionCooldown is how long to wait before increasing above the ceiling
	DifficultyReductionCooldown = 2 * time.Minute

	// firstRampMaxStep is the most firstRamp multiplies a connection's floor by in its one step.
	firstRampMaxStep = 1000.0
)

type Server struct {
	config         *ServerConfig
	logger         *zap.Logger
	listener       net.Listener
	clients        sync.Map
	clientCount    atomic.Int64 // atomic types: 64-bit aligned on 32-bit platforms too; see serverCounters
	currentJob     atomic.Value
	acceptGate     atomic.Value // func() bool; see SetAcceptGate
	jobHistory     sync.Map
	extraNonce     uint32 // starts at a random value; see NewServer
	extraNonceMu   sync.Mutex
	clientSeq      atomic.Uint64
	inflight       atomic.Int64 // shares being processed; Stop waits for them (see inflightGrace)
	shareProcessor ShareProcessor
	minerSettings  MinerSettingsStore
	diffMemory     sync.Map // minerID(address) -> diffMem: last vardiff level, reused across reconnects
	ipConnsMu      sync.Mutex
	ipConns        map[string]int // remote host -> live connections, for the per-IP cap
	logs           logLimit       // lines clients cause, besides minerLogs'; see serverLogBudget
	minerLogs      logBucket      // a miner's own lines; see minerLogBurst
	leftOut        leftOutLines   // lines the budgets left out, summed up by logLeftOut
	shutdownCh     chan struct{}
	writeWait      time.Duration // how long one write to a miner may take; 0 is writeTimeout. Set before Start.
	stopOnce       sync.Once     // Stop may be called again; closing shutdownCh twice panics
	stats          *serverCounters
	// Duplicate share detection
	submittedShares sync.Map // block header hash -> the tip (job PrevBlockHash) it was found on
	shareCleanupMu  sync.Mutex

	departedMu sync.Mutex
	departed   map[string]departedProof // client ID -> the level it had proven; see departedProof

	probes healthChecks // marketplace health checks counted instead of logged; see noteHealthCheck

	// auxMu guards auxClient + onAuxBlock, which the pool_config watcher sets at runtime
	// (EnableMergeMining / SetAuxBlockHandler) while connection goroutines read them.
	// soloPayout is the BCH2 address the coinbase actually pays in a solo deployment.
	// It is the stats key for every miner, because in solo there is no per-miner payout
	// identity: the block reward goes to this one address whatever the worker calls itself.
	// Set at startup and whenever the dashboard changes the address.
	soloPayoutMu sync.RWMutex
	soloPayout   string

	auxMu sync.RWMutex
	// Merge mining: aux-chain (1175) submission client. nil unless enabled.
	auxClient *mergemining.Client
	// onAuxBlock, if set, is invoked when a 1175 aux block is accepted so the pool
	// can distribute the reward. Args: aux height, aux child hash, coinbase value
	// (satoshis), the finding miner, and whether that miner is solo.
	onAuxBlock func(height int64, hash string, coinbaseValueSat int64, finder string, isSolo bool)

	// onInvalidShare, if set, is invoked for every rejected share so the stats
	// manager can count it against the worker. Without this hook a reject was
	// recorded only in counters that nothing reads: the dashboard's "Reject %"
	// tile is fed by stats.WorkerStats.InvalidShares, whose sole increment site
	// sits behind an already-validated share and so can never fire. Stales and
	// duplicates -- the two things an overclocked miner actually produces --
	// were reported to the user as a flat 0.00%.
	onInvalidShare func(minerID, workerName, reason string)

	// onLogin, if set, runs after a miner logs in and has been sent its difficulty and the job.
	onLogin func()
}

// EnableMergeMining lets this server submit solved aux-chain (1175) blocks. Any
// accepted share whose parent (BCH2) header hash also meets the aux target for
// the job's committed aux work is submitted to the aux node via submitauxblock.
// DisableMergeMining stops the server submitting solved aux blocks. Pairs with
// JobManager.DisableMergeMining so clearing the 1175 address in the dashboard takes effect
// without a restart.
func (s *Server) DisableMergeMining() {
	s.auxMu.Lock()
	s.auxClient = nil
	s.auxMu.Unlock()
}

func (s *Server) EnableMergeMining(client *mergemining.Client) {
	s.auxMu.Lock()
	s.auxClient = client
	s.auxMu.Unlock()
}

// getAuxClient returns the aux submission client under the guard (nil if merge mining off).
func (s *Server) getAuxClient() *mergemining.Client {
	s.auxMu.RLock()
	defer s.auxMu.RUnlock()
	return s.auxClient
}

// SetAuxBlockHandler registers a callback invoked when a 1175 aux block is
// accepted, so the pool can distribute its reward to miners.
func (s *Server) SetAuxBlockHandler(fn func(height int64, hash string, coinbaseValueSat int64, finder string, isSolo bool)) {
	s.auxMu.Lock()
	s.onAuxBlock = fn
	s.auxMu.Unlock()
}

// getOnAuxBlock returns the aux-block callback under the guard (nil if none set).
func (s *Server) getOnAuxBlock() func(height int64, hash string, coinbaseValueSat int64, finder string, isSolo bool) {
	s.auxMu.RLock()
	defer s.auxMu.RUnlock()
	return s.onAuxBlock
}

// SetInvalidShareHandler registers a callback invoked for every rejected share.
func (s *Server) SetInvalidShareHandler(fn func(minerID, workerName, reason string)) {
	s.auxMu.Lock()
	s.onInvalidShare = fn
	s.auxMu.Unlock()
}

// SetLoginHandler registers a callback run each time a miner logs in and is sent work: its first
// login on a connection, or one that changed its difficulty. It runs on the miner's connection,
// so it must not block.
func (s *Server) SetLoginHandler(fn func()) {
	s.auxMu.Lock()
	s.onLogin = fn
	s.auxMu.Unlock()
}

// noteLogin runs the login callback, if one is set.
func (s *Server) noteLogin() {
	s.auxMu.RLock()
	fn := s.onLogin
	s.auxMu.RUnlock()
	if fn != nil {
		fn()
	}
}

// noteInvalidShare records a rejected share everywhere it has to be counted: the
// server totals, the client's own counter, and the stats manager behind the
// dashboard's Reject % tile.
//
// Every rejection path must go through here. The two atomic increments used to be
// written out by hand at each of the eight rejection sites, which is exactly how
// the third destination came to be missing from all of them.
func (s *Server) noteInvalidShare(client *Client, reason string) {
	// NewServer always sets stats, but this runs on the submit path: a nil here
	// would panic a connection goroutine and take the process with it.
	if s.stats != nil {
		s.stats.InvalidShares.Add(1)
	}
	if client == nil {
		return
	}
	client.InvalidShares.Add(1)

	s.auxMu.RLock()
	cb := s.onInvalidShare
	s.auxMu.RUnlock()
	if cb == nil {
		return
	}
	client.mu.RLock()
	minerID, worker := client.MinerID, client.WorkerName
	client.mu.RUnlock()
	if minerID == "" {
		return // not authorized yet; there is no worker to charge this to
	}
	cb(minerID, worker, reason)
}

// isLoopback reports whether a "host:port" peer address is a loopback client, for either
// IP family. Used to keep local health probes out of the logs; anything that cannot be
// parsed is treated as external, so a genuine handshake failure is never silently dropped.
func isLoopback(addr string) bool {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

type ServerConfig struct {
	Host                string
	Port                int
	MaxConnections      int
	MaxConnectionsPerIP int
	MaxSharesPerSecond  int
	VardiffEnabled      bool
	MinDiff             float64
	// VariancePercent is the dead-band around the target share time, as a fraction (0.30 =
	// 30%). Zero means "use VardiffVariancePercent". This was a shipped config key that
	// nothing read -- an operator whose miner settled at the wrong difficulty turned it and
	// nothing happened.
	VariancePercent   float64
	AbsoluteMinDiff   float64 // Lowest difficulty a non-rental client may be ASSIGNED
	RentalMinDiff     float64 // Minimum difficulty for NiceHash/MRR (they require 500k+)
	RentalMaxDiff     float64 // Maximum difficulty for NiceHash/MRR (cap to prevent issues)
	MaxDiff           float64
	TargetShareTime   int
	RetargetTime      int // Seconds between vardiff adjustments
	HighHashThreshold int
	HighHashDiff      float64
	ExtraNonce2Size   int    // Size of extranonce2 in bytes (default 4, Braiins needs 8)
	ExtraNonce1Size   int    // Size of extranonce1 in bytes (default 6)
	ServerName        string // Name for logging (e.g., "main", "braiins")
	IsRentalPort      bool   // this listener exists for marketplace hashpower; see GetRentalStats
	SoloOnly          bool   // Force every miner to SOLO regardless of settings (solo-only deployments)
	// CreditPayoutAddress (with SoloOnly): every miner is credited to the solo payout address,
	// the one address the install pays; a username that is another address is only a label.
	// Off, such a username is credited to itself (Forge Gateway serves several people).
	CreditPayoutAddress bool
}

// serverCounters are the live counters behind GetStats. They are atomic.Int64, not int64 fields
// under sync/atomic's functions: on 32-bit platforms (the Linux armv6l, armv7l and i686 builds) a
// 64-bit atomic operation panics unless its word is 8-byte aligned, and only the atomic types are
// guaranteed to be. Every 64-bit counter updated atomically is one of these types for that reason.
type serverCounters struct {
	ActiveConnections atomic.Int64
	ValidShares       atomic.Int64
	InvalidShares     atomic.Int64
	BlocksFound       atomic.Int64
	SoloMiners        atomic.Int64
	PPLNSMiners       atomic.Int64
}

type ServerStats struct {
	ActiveConnections int64
	ValidShares       int64
	InvalidShares     int64
	BlocksFound       int64
	SoloMiners        int64
	PPLNSMiners       int64
}

type ShareProcessor interface {
	ProcessShare(ctx context.Context, share *Share) error
	ProcessBlock(ctx context.Context, block *Block) error
}

type MinerSettingsStore interface {
	GetMinerSettings(minerID string) (*MinerSettings, error)
	SaveMinerSettings(settings *MinerSettings) error // Autosave for new miners
}

type MinerSettings struct {
	MinerID    string
	SoloMining bool
	ManualDiff float64
	Exists     bool // Whether settings exist in database
}

func NewServer(config *ServerConfig, logger *zap.Logger, sp ShareProcessor, ms MinerSettingsStore) *Server {
	if config.HighHashThreshold == 0 {
		config.HighHashThreshold = 10
	}
	if config.HighHashDiff == 0 {
		config.HighHashDiff = 1000000
	}
	normalizeDifficultyFloors(config)
	if config.RentalMinDiff == 0 {
		config.RentalMinDiff = 500000 // NiceHash/MRR require 500k+
	}

	s := &Server{
		config:         config,
		logger:         logger,
		shareProcessor: sp,
		minerSettings:  ms,
		shutdownCh:     make(chan struct{}),
		stats:          &serverCounters{},
		// Each server's extranonce1 counter starts at a random value. From 0, the first miner
		// on 3333 and the first on 3335 both got 00000001 for the same job and searched the
		// same headers; in TIDES mode so did the first miners of two installs, whose coinbases
		// are otherwise identical.
		extraNonce: randomUint32(),
	}

	// Start periodic share cleanup
	go s.shareCleanupLoop(shareCleanupEvery)
	// Self-correct any connection stuck at a too-high (e.g. resumed) difficulty.
	go s.idleDifficultyLoop()

	return s
}

// getMinDiffForClient returns the lowest difficulty this client may be ASSIGNED
// (i.e. told to work at), based on client type. Rental services (NiceHash/MRR) require a
// much higher minimum difficulty. Takes client.mu.RLock, so never call it from a path
// that already holds client.mu -- compute the floor inline there instead.
func (s *Server) getMinDiffForClient(client *Client) float64 {
	client.mu.RLock()
	rental := client.RentalService
	client.mu.RUnlock()

	return s.vardiffFloor(rental != RentalNone)
}

// vardiffFloor is the lowest difficulty vardiff may ASSIGN a client of this kind.
//
// Deliberately AbsoluteMinDiff, not MinDiff. They are equal in the shipped config, but
// keeping them distinct means the assignment floor can be lowered independently later
// (small solo hardware benefits from a low starting target: at a high floor merely
// COLLECTING the VardiffMinShares=10 samples needed for the first adjustment can take
// tens of minutes). Starting LOW is the self-correcting direction -- shares arrive
// quickly, so the pool measures the miner's real rate within seconds and ramps it up.
// Starting HIGH cannot self-correct, because the evidence needed to correct it is exactly
// what is missing. Assigning below MinDiff is safe only because shareFloorFor() judges a
// share against min(assigned, MinDiff) -- without that, this would be the 55-miner outage
// all over again.
//
// Lock-free by design: most callers already hold client.mu.
func (s *Server) vardiffFloor(isRental bool) float64 {
	if isRental {
		return s.config.RentalMinDiff
	}
	return s.config.AbsoluteMinDiff
}

// networkDifficulty is the network difficulty the current job states (its nBits), or 0 before
// the first job.
func (s *Server) networkDifficulty() float64 {
	if j, ok := s.currentJob.Load().(*Job); ok && j != nil {
		return BitsToDifficulty(j.NBits)
	}
	return 0
}

// belowNetwork is diff lowered to the network difficulty netDiff where it is above it. A miner sends
// no share below the difficulty it was given, so one given more than the network's keeps back every
// block between the two. Every difficulty a miner is given passes through here. Where the network
// difficulty is below the floor (a test chain) or not known yet (0), diff is left as it is.
func belowNetwork(diff, netDiff, floor float64) float64 {
	if netDiff <= 0 || netDiff < floor || diff <= netDiff {
		return diff
	}
	return netDiff
}

// normalizeDifficultyFloors applies defaults and enforces the ordering the whole difficulty
// design rests on: the floor a miner may be ASSIGNED must be positive, and must never exceed
// the floor its shares are JUDGED against.
//
// AbsoluteMinDiff is the ASSIGNMENT floor; MinDiff is the JUDGING floor. This app already
// ships min_diff: 1024 (low enough for Bitaxe/Qaxe-class hardware to submit steadily), so
// there is no gap to close and the default deliberately mirrors MinDiff: an operator who
// raised min_diff to cut share spam must not have the assignment floor silently dropped out
// from under them on hardware we cannot observe. The knob exists so a lower assignment floor
// CAN be set, and setting one is safe only because shareFloorFor() judges a share against
// min(assigned, MinDiff).
//
// Both guards test <= 0 rather than == 0. A negative value from a config typo would otherwise
// survive: vardiffFloor would hand back a negative assignment floor, while shareFloorFor
// treats a non-positive assigned difficulty as "unset" and falls back to MinDiff -- telling
// the miner a negative target and judging it above that. That is the outage this whole file
// exists to prevent, reachable by one bad character in a YAML file.
//
// Deliberately a named function rather than inline setup, so a test can exercise the real
// code instead of restating it; a test that restates the rule passes even when the rule is
// deleted.
func normalizeDifficultyFloors(config *ServerConfig) {
	if config.MinDiff <= 0 {
		config.MinDiff = 32768
	}
	if config.AbsoluteMinDiff <= 0 {
		config.AbsoluteMinDiff = config.MinDiff
	}
	if config.AbsoluteMinDiff > config.MinDiff {
		config.AbsoluteMinDiff = config.MinDiff
	}
}

// effectiveJudgingDifficulty returns the target a submitted share should actually be
// measured against.
//
// Work begun before a difficulty RAISE was computed against the old target, and
// mining.set_difficulty only governs work the miner starts AFTER it arrives. Judging that
// in-flight work at the new, harder target rejects correct shares -- on the reference pool
// that was 252 rejects in two minutes the moment the judging floor began tracking each
// client. So for a short window after a raise, the previous (lower) target still counts.
//
// A LOWER change needs no window: the old target was harder, so work against it clears the
// new one anyway. And this can only ever return something <= assigned, which is what keeps
// the judged<=assigned invariant intact.
//
// Safe against inflation because an accepted share is credited min(assigned, proved), so a
// share that only proves the old target is credited only what it proved.
//
// `now` is a parameter, and this is a named function rather than an inline expression, so a
// test can drive the real decision deterministically.
func (s *Server) effectiveJudgingDifficulty(assigned, previous float64, changedAt time.Time, now time.Time) float64 {
	if previous > 0 && previous < assigned && now.Sub(changedAt) < difficultyGracePeriod {
		return previous
	}
	return assigned
}

// judgeShare returns the difficulty a share must meet and the most it may be credited, given the
// difficulty the client had been sent when the share's job went out to it (jobDiff, 0 if not
// known).
//
// A miner applies mining.set_difficulty to the jobs that come AFTER it. The stratum spec says so,
// and the firmware does it: ESP-Miner (main/tasks/stratum_v1_client.c) and NerdQAxe
// (main/tasks/create_jobs_task.cpp) stamp each job with the difficulty in force when its notify
// arrived and filter that job's shares by the stamp. So a share on a job that went out under a
// lower difficulty was found against that lower target, however late it arrives -- and it arrives
// late whenever the pool's messages are held up. On Forge Pool a miner on a lossy link
// (37-44% of the bytes sent to it retransmitted, the retransmit timer backed off to 39 s) worked
// one job for up to 2 min 40 s while vardiff kept raising a difficulty it had not yet received.
// The grace in effectiveJudgingDifficulty covers one raise, so its shares were refused by the
// dozen -- the same happens to a home miner on weak Wi-Fi.
//
// Such a share is judged at its job's difficulty AND credited no more than that. A share found
// against target T is worth T; crediting it min(assigned, proved) instead would count a miner
// that keeps to old jobs at T*(1+ln(assigned/T)) per share. Both values can only fall below what
// they were without jobDiff, so nothing accepted before is refused now, and no share is credited
// more.
func judgeShare(assigned, effective, jobDiff float64) (judge, creditCap float64) {
	judge, creditCap = effective, assigned
	if jobDiff > 0 && jobDiff < judge {
		judge = jobDiff
	}
	if jobDiff > 0 && jobDiff < creditCap {
		creditCap = jobDiff
	}
	return judge, creditCap
}

// maxJobDiffs bounds each client's record of the difficulty its jobs went out under. Jobs go out
// every few seconds to a minute, so this reaches back far longer than a miner goes on working a
// replaced job. A share on a job older than the record is judged by the client's current
// difficulty, as it was before the record existed.
const maxJobDiffs = 128

// noteJobDifficulty records diff as the difficulty job id went out to this client under. A job
// sent twice keeps the lower of the two. Caller holds c.mu.
func (c *Client) noteJobDifficulty(id string, diff float64) {
	if old, seen := c.jobDiff[id]; seen {
		if diff < old {
			c.jobDiff[id] = diff
		}
		return
	}
	if c.jobDiff == nil {
		c.jobDiff = make(map[string]float64, maxJobDiffs)
	}
	c.jobDiff[id] = diff
	c.jobDiffOrder = append(c.jobDiffOrder, id)
	if len(c.jobDiffOrder) > maxJobDiffs {
		delete(c.jobDiff, c.jobDiffOrder[0])
		c.jobDiffOrder = c.jobDiffOrder[1:]
	}
}

// shareFloorFor returns the difficulty a submitted share must actually meet.
//
// INVARIANT: the pool must never judge a share against a HARDER target than the one it
// told the miner to work at. Breaking that invariant fails silently and totally -- the
// miner produces correct work at its assigned target, the pool refuses it, and the
// automatic vardiff remedy cannot fire because it is gated on manualDiff == 0 and on
// client.Difficulty > floor, both of which are false in exactly this situation. On the
// main pool that was 55 miners, every one of them carrying a stored manual_diff below
// MinDiff and mining into a black hole from the moment it next reconnected.
//
// This is checked against the ASSIGNED value rather than at each assignment site on
// purpose: it is robust to every path that can hand out a difficulty, including
// adjustVardiff's post-rejection ceiling (DifficultyReducedFrom * 0.8), which can legally
// land below the vardiff floor and would otherwise open a silent rejection band.
//
// ABOVE the assigned difficulty the pool stays deliberately generous: a share clearing
// MinDiff is accepted even if the client's vardiff target has since ramped past it, so
// ramping never discards work already in flight.
func (s *Server) shareFloorFor(assigned float64) float64 {
	floor := s.config.MinDiff
	if assigned > 0 && assigned < floor {
		floor = assigned
	}
	return floor
}

// difficultyGracePeriod is how long after a difficulty change the PREVIOUS (lower) target
// is still accepted. It covers work the miner had already begun when the new target
// arrived: mining.set_difficulty only governs work STARTED after it is received, so
// judging in-flight shares at the new, harder target rejects correct work (measured live
// at 252 rejects in 2 minutes on the main pool before this was added).
// Generous on purpose: accepting one of these costs nothing, because credit is
// min(assigned, proved), while rejecting it discards correct work and shows up on the
// miner's own dashboard as a fault the pool caused.
const difficultyGracePeriod = 60 * time.Second

// idleResetAfter must stay BELOW the connection read deadline (see handleConnection's
// SetReadDeadline), which is re-armed from the last INBOUND line only. When the two were
// both 5 minutes, a silent client was disconnected at almost exactly the moment this rescue
// was due, leaving a window about one tick wide: the rescue could essentially never fire for
// the case its own comment describes. Keeping a margin also matters while mining is paused,
// where every authorized miner would otherwise be dropped every 5 minutes and the user goes
// hunting for a network fault instead of a missing payout address.
//
// idleResetAfter is how long a connection may hold an above-floor difficulty without
// producing a single accepted share before it is reset to its floor. Longer than the
// expected first-share time even for a large miner at a high difficulty, so it only
// ever fires on a connection that genuinely cannot mine at its assigned level (e.g. a
// resumed difficulty that is too high for this connection's hashrate).
const idleResetAfter = 3 * time.Minute

// idleDifficultyLoop resets any connection that was handed an above-floor difficulty but
// has produced zero accepted shares within idleResetAfter, so a too-high resumed level
// self-corrects (vardiff then ramps back up from real shares) instead of stalling the
// miner. It never touches a connection at its floor or one that has submitted a share,
// so a legitimately-mining miner of any size is unaffected.
func (s *Server) idleDifficultyLoop() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.shutdownCh:
			return
		case <-ticker.C:
		}
		s.resetIdleDifficulties(time.Now())
	}
}

// resetIdleDifficulties is one round of idleDifficultyLoop, at time now.
func (s *Server) resetIdleDifficulties(now time.Time) {
	s.clients.Range(func(_, v interface{}) bool {
		c, ok := v.(*Client)
		if !ok {
			return true
		}
		c.mu.Lock()
		// vardiffFloor is lock-free, so it is safe to call with c.mu held.
		floor := s.vardiffFloor(c.RentalService != RentalNone)
		prevDiff := c.Difficulty
		connFor := now.Sub(c.ConnectedAt)
		stuck := c.Authorized && c.ValidShares.Load() == 0 &&
			prevDiff > floor && connFor > idleResetAfter
		if stuck {
			c.Difficulty = floor
		}
		minerID := c.MinerID
		workerName := c.WorkerName
		c.mu.Unlock()
		if stuck {
			s.rememberDifficulty(minerID, workerName, hostOf(c.IP), floor) // don't re-hand the too-high level next time
			s.sendCurrentDifficulty(c)
			s.limitedLog(minersBudget, false, "Idle difficulty reset", 0,
				zap.String("miner", minerID),
				zap.Float64("from_diff", prevDiff),
				zap.Float64("to_diff", floor),
				zap.Duration("connected_for", connFor))
		}
		return true
	})
}

// shareCleanupEvery is how often shareCleanupLoop runs. A test shortens it.
var shareCleanupEvery = 30 * time.Second

// shareCleanupLoop periodically removes old share entries, logs the health checks counted once
// their line is due, and sums up the log lines left out since its last round.
func (s *Server) shareCleanupLoop(every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-s.shutdownCh:
			return
		case <-ticker.C:
			s.cleanupOldShares()
			s.cleanupDiffMemory()
			s.pruneDeparted(time.Now())
			s.flushHealthChecks(time.Now(), false)
			s.logLeftOut()
		}
	}
}

// cleanupDiffMemory drops remembered vardiff levels past their TTL.
//
// The read path already ignores an entry older than diffMemoryTTL, but nothing deleted it,
// so the map only ever grew -- and its key is miner+worker name, which a marketplace order
// rotates. Small per entry, unbounded in aggregate, and invisible because the stale entries
// were never returned anyway.
func (s *Server) cleanupDiffMemory() {
	s.diffMemory.Range(func(key, value interface{}) bool {
		if m, ok := value.(diffMem); ok && time.Since(m.at) > diffMemoryTTL {
			s.diffMemory.Delete(key)
		}
		return true
	})
}

// cleanupOldShares removes shares older than 5 minutes
func (s *Server) cleanupOldShares() { s.pruneSubmittedShares() }

// clearSharesForJob prunes the duplicate record when a new job is broadcast.
func (s *Server) clearSharesForJob() { s.pruneSubmittedShares() }

// pruneSubmittedShares forgets the shares found on any tip but the current one. Those are refused
// as stale before the duplicate check, so their records are no longer needed; the current tip's
// are kept for as long as its jobs are accepted. Pruning by age instead let a share be accepted a
// second time once its record aged out while its job was still valid.
func (s *Server) pruneSubmittedShares() {
	s.shareCleanupMu.Lock()
	defer s.shareCleanupMu.Unlock()
	cur, ok := s.currentJob.Load().(*Job)
	if !ok || cur == nil {
		return
	}
	s.submittedShares.Range(func(key, value interface{}) bool {
		if tip, _ := value.(string); tip != cur.PrevBlockHash {
			s.submittedShares.Delete(key)
		}
		return true
	})
}

// isDuplicateShare reports whether a share with this block header hash was already accepted, and
// records it if not. tip is the PrevBlockHash of the job it was found on.
func (s *Server) isDuplicateShare(headerHash []byte, tip string) bool {
	_, exists := s.submittedShares.LoadOrStore(string(headerHash), tip)
	return exists
}

// validateShare verifies that the submitted share meets the difficulty target
// Returns: (isValid bool, actualDifficulty float64, blockHash []byte, error)

// calculateMerkleRoot computes the merkle root from coinbase and branches
func calculateMerkleRoot(coinbase []byte, merkleBranches []string) []byte {
	result := doubleSHA256(coinbase)
	for _, branchHex := range merkleBranches {
		branch, _ := hex.DecodeString(branchHex)
		combined := make([]byte, 64)
		copy(combined[:32], result)
		copy(combined[32:], branch)
		result = doubleSHA256(combined)
	}
	return result
}

func (s *Server) validateShare(job *Job, extranonce1, extranonce2, ntime, nonce, versionBits string, targetDiff float64) (bool, float64, []byte, error) {
	// Validate hex input lengths based on configured sizes
	en1Size := s.config.ExtraNonce1Size
	if en1Size == 0 {
		en1Size = 6
	}
	en2Size := s.config.ExtraNonce2Size
	if en2Size == 0 {
		en2Size = 4
	}
	if len(extranonce1) != en1Size*2 || len(extranonce2) != en2Size*2 {
		return false, 0, nil, fmt.Errorf("invalid extranonce length (got en1=%d, en2=%d, want en1=%d, en2=%d)",
			len(extranonce1)/2, len(extranonce2)/2, en1Size, en2Size)
	}
	if len(ntime) != 8 || len(nonce) != 8 {
		return false, 0, nil, fmt.Errorf("invalid ntime/nonce length")
	}

	// Validate hex format
	if _, err := hex.DecodeString(extranonce1); err != nil {
		return false, 0, nil, fmt.Errorf("invalid extranonce1 hex")
	}
	if _, err := hex.DecodeString(extranonce2); err != nil {
		return false, 0, nil, fmt.Errorf("invalid extranonce2 hex")
	}
	if _, err := hex.DecodeString(ntime); err != nil {
		return false, 0, nil, fmt.Errorf("invalid ntime hex")
	}
	if _, err := hex.DecodeString(nonce); err != nil {
		return false, 0, nil, fmt.Errorf("invalid nonce hex")
	}

	// Build coinbase transaction
	coinbase := buildCoinbaseFromParts(job.CoinBase1, extranonce1, extranonce2, job.CoinBase2)

	// Calculate merkle root (for single tx, it's just double SHA256 of coinbase)
	merkleRoot := calculateMerkleRoot(coinbase, job.MerkleBranches)

	// Build block header (80 bytes)
	header := buildBlockHeader(job, merkleRoot, ntime, nonce, versionBits)

	// Calculate block hash (double SHA256 of header)
	blockHash := doubleSHA256(header)

	// Reverse for display (Bitcoin uses little-endian internally)
	displayHash := make([]byte, 32)
	copy(displayHash, blockHash)
	reverseBytes(displayHash)

	// Calculate difficulty from hash
	actualDiff := hashToDifficulty(blockHash)

	// Check if meets target difficulty
	isValid := actualDiff >= targetDiff

	return isValid, actualDiff, displayHash, nil
}

// auxHashMeetsTarget reports whether the parent (BCH2) block hash (big-endian
// display bytes, as returned by validateShare) is <= the aux-chain target
// (big-endian hex from getauxblock) — i.e. this share is a valid aux block.
func auxHashMeetsTarget(displayHashBE []byte, targetHexBE string) bool {
	t, ok := new(big.Int).SetString(targetHexBE, 16)
	if !ok {
		return false
	}
	return new(big.Int).SetBytes(displayHashBE).Cmp(t) <= 0
}

// submitAux rebuilds the parent coinbase + header for a winning share, assembles
// the CAuxPow proof, and submits it to the aux node. Called only when a share
// already meets the aux target, so the (small) rebuild cost is paid rarely.
func (s *Server) submitAux(job *Job, en1, en2, ntime, nonce, versionBits, finder string, isSolo bool) {
	coinbase := buildCoinbaseFromParts(job.CoinBase1, en1, en2, job.CoinBase2)
	merkleRoot := calculateMerkleRoot(coinbase, job.MerkleBranches)
	header := buildBlockHeader(job, merkleRoot, ntime, nonce, versionBits)

	// job.MerkleBranches is the coinbase's branch in internal (little-endian) order,
	// exactly what CAuxPow.vMerkleBranch expects (validated by the merkle-branch and
	// integration tests).
	auxHex, err := mergemining.AssembleAuxPowHex(coinbase, header, job.MerkleBranches)
	if err != nil {
		s.logger.Warn("aux: assemble failed", zap.Error(err))
		return
	}
	ac := s.getAuxClient()
	if ac == nil {
		return
	}
	s.sendAuxBlock(ac, job, auxHex, finder, isSolo)
}

// buildCoinbaseFromParts constructs the coinbase transaction
func buildCoinbaseFromParts(cb1, en1, en2, cb2 string) []byte {
	cb1Bytes, _ := hex.DecodeString(cb1)
	en1Bytes, _ := hex.DecodeString(en1)
	en2Bytes, _ := hex.DecodeString(en2)
	cb2Bytes, _ := hex.DecodeString(cb2)

	var coinbase bytes.Buffer
	coinbase.Write(cb1Bytes)
	coinbase.Write(en1Bytes)
	coinbase.Write(en2Bytes)
	coinbase.Write(cb2Bytes)

	return coinbase.Bytes()
}

// buildBlockHeader constructs the 80-byte block header
// VersionRollingMask is the set of nVersion bits a miner is allowed to roll (BIP310).
var VersionRollingMask = []byte{0x1f, 0xff, 0xe0, 0x00}

// RollVersion merges a miner's rolled nVersion into the job's version and returns the
// 4-byte big-endian result.
//
// BIP310 version-rolling: the miner submits the FULL rolled nVersion. Keep the job's
// non-rollable bits and take only the masked (rollable) bits from the miner. Plain XOR
// corrupted the header for full-version submitters (NiceHash/Braiins/most ASICs), wrongly
// rejecting their version-rolled shares.
//
// SINGLE SOURCE OF TRUTH, and it must stay that way. The header is assembled twice -- here
// for every share, to decide whether it won, and again in cmd/stratum's buildBlock, to
// submit the block that did. Those were separate copies, and they did not merely risk
// drifting: they already disagreed on malformed input. This one tolerated undecodable
// versionBits by ignoring it and using the job version, while buildBlock returned an error
// and abandoned the submission. A miner that puts non-hex in the optional 6th submit
// parameter while not actually version-rolling therefore had every share validated
// normally and the one block it found silently discarded. Tolerating it here is the
// correct half: the share was validated against the job version, so the block must be
// built the same way.
//
// A malformed or empty versionBits means "no rolling", never an error.
func RollVersion(jobVersionHex, versionBits string) []byte {
	versionBytes, err := hex.DecodeString(jobVersionHex)
	if err != nil {
		return nil
	}
	if versionBits == "" {
		return versionBytes
	}
	vbBytes, err := hex.DecodeString(versionBits)
	if err != nil {
		return versionBytes
	}
	for i := 0; i < len(versionBytes) && i < len(vbBytes) && i < len(VersionRollingMask); i++ {
		versionBytes[i] = (versionBytes[i] &^ VersionRollingMask[i]) | (vbBytes[i] & VersionRollingMask[i])
	}
	return versionBytes
}

func buildBlockHeader(job *Job, merkleRoot []byte, ntime, nonce, versionBits string) []byte {
	var header bytes.Buffer

	// Version (4 bytes, little-endian)
	versionBytes := RollVersion(job.Version, versionBits)
	reverseBytes(versionBytes)
	header.Write(versionBytes)

	// Previous block hash (32 bytes)
	// job.PrevBlockHash is in stratum format, need to reverse back
	prevHashBytes, _ := hex.DecodeString(job.PrevBlockHash)
	// Undo the 4-byte swap that stratum does
	for i := 0; i < 32; i += 4 {
		prevHashBytes[i], prevHashBytes[i+1], prevHashBytes[i+2], prevHashBytes[i+3] =
			prevHashBytes[i+3], prevHashBytes[i+2], prevHashBytes[i+1], prevHashBytes[i]
	}
	header.Write(prevHashBytes)

	// Merkle root (32 bytes)
	header.Write(merkleRoot)

	// Time (4 bytes, little-endian)
	ntimeBytes, _ := hex.DecodeString(ntime)
	reverseBytes(ntimeBytes)
	header.Write(ntimeBytes)

	// Bits (4 bytes, little-endian)
	bitsBytes, _ := hex.DecodeString(job.NBits)
	reverseBytes(bitsBytes)
	header.Write(bitsBytes)

	// Nonce (4 bytes, little-endian)
	nonceBytes, _ := hex.DecodeString(nonce)
	reverseBytes(nonceBytes)
	header.Write(nonceBytes)

	return header.Bytes()
}

// doubleSHA256 performs double SHA256 hash
func doubleSHA256(data []byte) []byte {
	first := sha256.Sum256(data)
	second := sha256.Sum256(first[:])
	return second[:]
}

// reverseBytes reverses a byte slice in place
func reverseBytes(b []byte) {
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
}

// hashToDifficulty converts a block hash to its difficulty value
func hashToDifficulty(hash []byte) float64 {
	// Difficulty 1 target (Bitcoin)
	// 0x00000000FFFF0000000000000000000000000000000000000000000000000000
	diff1Target := new(big.Int)
	diff1Target.SetString("00000000FFFF0000000000000000000000000000000000000000000000000000", 16)

	// Convert hash to big.Int (hash is in internal byte order, need to reverse for big.Int)
	hashReversed := make([]byte, 32)
	copy(hashReversed, hash)
	reverseBytes(hashReversed)

	hashInt := new(big.Int).SetBytes(hashReversed)

	if hashInt.Sign() == 0 {
		return 0
	}

	// Difficulty = diff1Target / hashInt
	// Use floating point for precision
	diff1Float := new(big.Float).SetInt(diff1Target)
	hashFloat := new(big.Float).SetInt(hashInt)

	result := new(big.Float).Quo(diff1Float, hashFloat)
	difficulty, _ := result.Float64()

	return difficulty
}

func (s *Server) Start() error {
	addr := fmt.Sprintf("%s:%d", s.config.Host, s.config.Port)
	listener, err := netlisten.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}
	s.listener = listener
	s.logger.Info("Stratum server started", zap.String("addr", addr))
	go s.acceptLoop()
	return nil
}

// ListenAddr is the address the server is listening on (useful with port 0).
func (s *Server) ListenAddr() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

func (s *Server) Stop() {
	s.stopOnce.Do(s.stop)
}

func (s *Server) stop() {
	s.logger.Info("Initiating graceful shutdown...")

	// Signal all goroutines to stop
	close(s.shutdownCh)

	// Stop accepting new connections
	if s.listener != nil {
		s.listener.Close()
	}

	// Give connected miners a moment to finish what they are sending, then close their
	// connections. A miner never hangs up by itself: its handler only sees shutdownCh when the
	// next message arrives, so waiting for it (this was 30 s) outlasted Docker's 10 s stop
	// timeout whenever a rental was quiet for a few seconds, and the process was killed with
	// the TIDES shares still queued.
	deadline := time.Now().Add(shutdownGrace)
	for s.clientCount.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if n := s.clientCount.Load(); n > 0 {
		s.logger.Info("Closing the miners' connections", zap.Int64("clients", n))
		s.clients.Range(func(key, value interface{}) bool {
			if c, ok := value.(*Client); ok && c.Conn != nil {
				c.closeFor("the stratum is stopping")
			}
			return true
		})
		// Each handler sees its connection closed at once and exits.
		end := time.Now().Add(time.Second)
		for s.clientCount.Load() > 0 && time.Now().Before(end) {
			time.Sleep(20 * time.Millisecond)
		}
	}

	// Shares accepted in the last moments are still being processed: a block share in TIDES mode
	// waits up to 2 s for the pool before it is submitted to the local node. Exiting under them
	// lost the block.
	end := time.Now().Add(inflightGrace)
	for s.inflight.Load() > 0 && time.Now().Before(end) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := s.inflight.Load(); n > 0 {
		s.logger.Warn("Shares still being processed at shutdown", zap.Int64("shares", n))
	}
	s.flushHealthChecks(time.Now(), true)
	s.logLeftOut()

	s.logger.Info("Graceful shutdown complete")
}

// inflightGrace bounds how long Stop waits for accepted shares still being processed.
const inflightGrace = 5 * time.Second

// shutdownGrace is how long Stop lets connected miners finish a message before it closes their
// connections.
const shutdownGrace = 2 * time.Second

func (s *Server) acceptLoop() {
	for {
		select {
		case <-s.shutdownCh:
			return
		default:
		}
		conn, err := s.listener.Accept()
		if err != nil {
			continue
		}
		if !s.acceptOpen() {
			conn.Close()
			continue
		}
		// The slot is taken here, before the handler starts, and handleClient gives it back. Taken
		// in the handler, every connection accepted before the handlers ran was let in: on a 1- or
		// 2-CPU host a burst went far past the cap and past the quarter kept for this network.
		if s.clientCount.Add(1) > s.connectionLimitFor(conn.RemoteAddr()) {
			s.clientCount.Add(-1)
			conn.Close()
			continue
		}
		go s.handleClient(conn)
	}
}

// hostOf strips the port so every connection from one machine shares a counter.
func hostOf(remoteAddr string) string {
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return h
	}
	return remoteAddr
}

// reserveIPSlot bounds how many live connections one remote host may hold.
//
// MaxConnections alone is a global cap, so a single peer could take all of it and lock the
// machine's own miners out. This listener is published on 0.0.0.0 and has no authentication,
// deliberately: a stranger's hashrate is paid to the operator's own coinbase, so it is
// welcome. What is not welcome is one source consuming the whole pool.
//
// The cap is generous rather than tight. A stratum proxy, or Braiins fronting a farm, opens
// many sessions from a single address, and refusing those would break real setups to deter an
// attacker who can simply use a second address. It guarantees only that no single source
// takes everything. 0 disables it.
//
// Loopback is exempt: a healthcheck or a miner on the same machine must never be refused by a
// limit meant for the outside world.
func (s *Server) reserveIPSlot(host string) bool {
	limit := s.config.MaxConnectionsPerIP
	if limit <= 0 || isLoopback(host) {
		return true
	}
	s.ipConnsMu.Lock()
	defer s.ipConnsMu.Unlock()
	if s.ipConns == nil {
		s.ipConns = make(map[string]int)
	}
	if s.ipConns[host] >= limit {
		return false
	}
	s.ipConns[host]++
	return true
}

func (s *Server) releaseIPSlot(host string) {
	if s.config.MaxConnectionsPerIP <= 0 || isLoopback(host) {
		return
	}
	s.ipConnsMu.Lock()
	defer s.ipConnsMu.Unlock()
	if n := s.ipConns[host]; n <= 1 {
		delete(s.ipConns, host) // never retain an entry for a host with nothing open
	} else {
		s.ipConns[host] = n - 1
	}
}

// handleClient serves one connection. acceptLoop has counted it in clientCount; this gives the slot
// back when it ends.
func (s *Server) handleClient(conn net.Conn) {
	defer s.clientCount.Add(-1)
	host := hostOf(conn.RemoteAddr().String())
	if !s.reserveIPSlot(host) {
		s.limitedLog(portBudget, true, "refused connection: per-IP limit reached", 0,
			zap.String("ip", host),
			zap.Int("limit", s.config.MaxConnectionsPerIP))
		conn.Close()
		return
	}
	defer s.releaseIPSlot(host)

	// Configure TCP connection for mining
	if tc, ok := conn.(*net.TCPConn); ok {
		// Disable Nagle algorithm for low-latency share responses
		tc.SetNoDelay(true)
		// Enable TCP keepalive to detect dead connections and prevent NAT timeout
		// This is critical for rental services (NiceHash, MRR) that may have
		// intermediate proxies/firewalls that drop idle connections
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(30 * time.Second) // Check every 30 seconds
	}

	s.stats.ActiveConnections.Add(1)
	defer func() {
		s.stats.ActiveConnections.Add(-1)
		conn.Close()
	}()

	client := &Client{
		// A sequence number, not the time alone: Windows' clock advances only every 0.5-15.6 ms,
		// so two miners reconnecting at once could get the same ID and one drop out of s.clients.
		ID:           fmt.Sprintf("%d-%d", time.Now().UnixNano(), s.clientSeq.Add(1)),
		Conn:         conn,
		IP:           conn.RemoteAddr().String(),
		Difficulty:   s.config.AbsoluteMinDiff, // assignment floor, not the judging floor
		ConnectedAt:  time.Now(),
		ShareSamples: make([]shareSample, 0, maxShareSamples),
	}

	client.out = make(chan []byte, clientQueue)
	client.progress = make(chan struct{}, 1)
	go s.writeLoop(client)
	defer client.closeOut()

	s.clients.Store(client.ID, client)
	defer s.clients.Delete(client.ID)

	// At debug: MiningRigRentals' health checks connect about 550 times an hour during a rental, and
	// the line said nothing the next one does not. A miner names itself and its address when it
	// subscribes; a connection that never does is logged when it ends.
	s.logger.Debug("Client connected", zap.String("ip", client.IP))

	// A stratum client speaks first, and every stratum message is a JSON object. Anything else is
	// closed at once instead of being held until the read deadline: a TLS handshake (MiningRigRentals'
	// pool check tries TLS before plain stratum and then runs out of time), an HTTP request, a scanner.
	first, ok := s.readFirstByte(client, conn)
	if !ok {
		return
	}
	scanner := bufio.NewScanner(io.MultiReader(bytes.NewReader(first), conn))
	scanner.Buffer(make([]byte, 64*1024), 64*1024)

	for {
		// Until it authorizes, a connection has authTimeout from its start in all; after, each
		// line may take up to authorizedIdleTimeout.
		client.mu.RLock()
		authorized := client.Authorized
		client.mu.RUnlock()
		if authorized {
			conn.SetReadDeadline(time.Now().Add(authorizedIdleTimeout))
		} else {
			conn.SetReadDeadline(client.ConnectedAt.Add(authTimeout))
		}

		if !scanner.Scan() {
			break
		}

		// Messages that arrive during Stop's grace are handled, not dropped: a share sent in the
		// last moments, a block among them, was read and thrown away. Stop closes the connection
		// when the grace ends, which ends this loop.
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		client.waitForRoom()
		if !s.handleMessage(client, line) {
			// A line that is not JSON: a few are tolerated from a subscribed miner (logged, as
			// ever), but a connection that has not subscribed has shown it is not a stratum client.
			client.mu.Lock()
			subscribed := client.Subscribed
			client.badLines++
			bad := client.badLines
			client.mu.Unlock()
			if !subscribed || bad > maxBadLines {
				client.closeFor("it sent lines that are not stratum")
				break
			}
		}
	}

	// A miner that proved its level counts in MaxDifficulty for a while longer (departedProof).
	s.keepDeparted(client, time.Now())

	// Log disconnection with details
	client.mu.RLock()
	minerID := client.MinerID
	workerName := client.WorkerName
	rental := client.RentalService
	authorized := client.Authorized
	subscribed := client.Subscribed
	difficulty := client.Difficulty
	lastShare := client.ProvenAt // when its last accepted share came
	userAgent := client.UserAgent
	client.mu.RUnlock()

	duration := time.Since(client.ConnectedAt)
	scanErr := scanner.Err()
	reason := client.whyClosed(scanErr, authorized)

	// The line about the connection ending also reports the lines its own budget left out since
	// its last one, so they are counted even when it says nothing more. A miner that logged in has
	// its line in the miners' budget.
	left := client.logs.drain()
	endLog := func(b logBudget, warn bool, msg string, fields ...zap.Field) {
		s.limitedLog(b, warn, msg, left, fields...)
		left = 0
	}

	// Log EXTERNAL connections that never subscribed. A local probe that opens a socket
	// and closes it is the container healthcheck, not a miner with a problem.
	//
	// This used to test strings.HasPrefix(ip, "127.0.0.1"), which only covers IPv4.
	// `nc -z localhost 3333` resolves to ::1 first on a dual-stack container, so every
	// healthcheck slipped past and logged a warning: ~84 an hour, ~2000 a day, all
	// self-inflicted. That volume is not just untidy -- it buries the case the warning
	// exists for, a real miner failing to complete the handshake.
	if !isLoopback(client.IP) && !subscribed {
		endLog(portBudget, true, "External client disconnected without subscribing",
			zap.String("ip", client.IP),
			zap.String("reason", reason),
			zap.Duration("connected_duration", duration),
			zap.Error(scanErr))
	}
	// Subscribed but never logged in: a miner with a username the stratum refuses, or a probe.
	if !isLoopback(client.IP) && subscribed && !authorized {
		endLog(portBudget, false, "External client disconnected before logging in",
			zap.String("ip", client.IP),
			zap.String("reason", reason),
			zap.Duration("connected_duration", duration))
	}

	// A marketplace's health check that logged in, sent no share and closed the connection itself,
	// as each of MiningRigRentals' does about 550 times an hour, is counted, not logged one by one.
	// Closed for any other reason it is logged in full, as a miner is.
	healthCheckDone := isHealthCheck(userAgent) && client.ValidShares.Load() == 0 &&
		reason == minerClosedIt && left == 0
	if authorized && minerID != "" && healthCheckDone {
		s.noteHealthCheck(userAgent, client.IP, time.Now())
	} else if authorized && minerID != "" {
		// How long it had been silent says whether a miner that left had stopped sending shares
		// before it went, as a rental's rig does when its own side stalls.
		fields := []zap.Field{
			zap.String("ip", client.IP),
			zap.String("user_agent", userAgent),
			zap.String("miner", minerID),
			zap.String("worker", workerName),
			zap.String("rental_service", rental.String()),
			zap.String("reason", reason),
			zap.Float64("difficulty", difficulty),
			zap.Duration("connected_duration", duration),
			zap.Int64("valid_shares", client.ValidShares.Load()),
		}
		if !lastShare.IsZero() {
			fields = append(fields, zap.Duration("last_share_age", time.Since(lastShare)))
		}
		endLog(minersBudget, false, "Client disconnected", append(fields, zap.Error(scanErr))...)
	}
}

// firstMessageTimeout is how long a new connection may stay silent: a stratum client sends its
// first message (mining.subscribe or mining.configure) as soon as it connects.
var firstMessageTimeout = 60 * time.Second

// readFirstByte reads a new connection up to its first non-blank byte and reports whether that
// byte opens a JSON object, as every stratum message does. It returns what it read, for the line
// reader to start from.
func (s *Server) readFirstByte(client *Client, conn net.Conn) ([]byte, bool) {
	conn.SetReadDeadline(time.Now().Add(firstMessageTimeout))
	got := make([]byte, 0, 16)
	one := make([]byte, 1)
	for {
		if _, err := conn.Read(one); err != nil {
			// The container healthcheck opens a socket and closes it; that is not a miner.
			if !isLoopback(client.IP) {
				s.limitedLog(portBudget, true, "External client disconnected without subscribing", 0,
					zap.String("ip", client.IP),
					zap.Duration("connected_duration", time.Since(client.ConnectedAt)),
					zap.Error(err))
			}
			return nil, false
		}
		c := one[0]
		got = append(got, c)
		if c == '{' {
			return got, true
		}
		if (c == ' ' || c == '\t' || c == '\r' || c == '\n') && len(got) < 64 {
			continue
		}
		looks := "not stratum"
		if c == 0x16 {
			looks = "a TLS handshake"
		}
		s.limitedLog(portBudget, true, "Closed a connection that did not start with a stratum message", 0,
			zap.String("ip", client.IP),
			zap.String("first_byte", fmt.Sprintf("0x%02x", c)),
			zap.String("looks_like", looks))
		return nil, false
	}
}

// handleMessage handles one line from a client and reports whether it was JSON at all.
func (s *Server) handleMessage(client *Client, data []byte) bool {
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		s.clientLog(client, true, "Failed to parse stratum message",
			zap.String("ip", client.IP),
			zap.Error(err),
			zap.String("data", clip(string(data), 128)))
		return false
	}

	// Log all messages from NiceHash clients for debugging
	client.mu.RLock()
	rental := client.RentalService
	minerID := client.MinerID
	client.mu.RUnlock()
	if rental == RentalNiceHash && req.Method != "" {
		s.clientLog(client, false, "NiceHash message received",
			zap.String("miner", minerID),
			zap.String("method", req.Method))
	}

	switch req.Method {
	case MethodSubscribe:
		resp := s.handleSubscribe(client, &req)
		s.sendResponse(client, resp)
		// Push the starting difficulty immediately, before authorize.
		//
		// Difficulty used to be withheld until authorize succeeded. Real ASICs never
		// noticed -- an Antminer fires mining.authorize straight after subscribe without
		// waiting for anything -- but MiningRigRentals' endpoint validator subscribes and
		// then WAITS for mining.set_difficulty before going further. It never arrived, the
		// validator timed out, and MRR reported the pool as unusable, which is what
		// "blocked pool" looked like from the outside. Verified against MRR's own probe
		// (user_agent "MiningRigRentals/Test/1.0"): it subscribed, received nothing, and
		// gave up without ever sending authorize.
		//
		// Most pools announce difficulty here, so this is the conventional shape as well as
		// the interoperable one. It leaks nothing: the value is not a secret, and no WORK is
		// sent until the client has authorized. sendDifficulty suppresses an identical
		// repeat within 500ms, so the authorize path's own send is a no-op when the value
		// has not changed, and still fires when it has.
		client.mu.RLock()
		startDiff := client.Difficulty
		client.mu.RUnlock()
		if startDiff > 0 {
			s.sendDifficulty(client, startDiff)
		}
	case MethodAuthorize:
		resp, auth := s.authorize(client, &req)
		// Send auth response FIRST
		s.sendResponse(client, resp)
		// Then the difficulty and the job, on the connection's first login or when this one changed
		// the difficulty. A repeat is answered alone: re-sending the whole job each time let one
		// short line make the stratum upload a job, as often as a client cared to send it.
		if resp.Result == true && auth.sendWork {
			s.sendCurrentDifficulty(client)
			if job := s.currentJob.Load(); job != nil {
				// Send initial job with clean=true so miner starts fresh
				initialJob := *job.(*Job)
				initialJob.CleanJobs = true
				s.sendJob(client, &initialJob)
				s.logger.Debug("Sent initial job after auth",
					zap.String("miner", auth.minerID),
					zap.String("job_id", initialJob.ID))
			} else {
				// Usual for a moment after a start: miners reconnect before the first job is made, and
				// BroadcastJob sends it to them when it is.
				s.minerLog(client, false, "No job yet for a miner that just logged in: it gets the first one when it is made",
					zap.String("miner", auth.minerID))
			}
			s.noteLogin()
		}
	case MethodConfigure:
		// At debug: it comes before mining.subscribe, so it has no user agent to give, and the
		// subscribe line says the rest.
		s.logger.Debug("mining.configure received", zap.String("ip", client.IP))
		resp := s.handleConfigure(client, &req)
		s.sendResponse(client, resp)
	case MethodSubmit:
		resp := s.handleSubmit(client, &req)
		s.sendResponse(client, resp)
	case "mining.suggest_difficulty":
		// Handle miner's difficulty suggestion
		var params []float64
		if err := json.Unmarshal(req.Params, &params); err == nil && len(params) > 0 {
			suggestedDiff := params[0]
			// Clamp to our min/max range (use appropriate min for client type)
			minDiff := s.getMinDiffForClient(client)
			if suggestedDiff < minDiff {
				suggestedDiff = minDiff
			}
			if suggestedDiff > s.config.MaxDiff {
				suggestedDiff = s.config.MaxDiff
			}
			suggestedDiff = belowNetwork(suggestedDiff, s.networkDifficulty(), minDiff)
			client.mu.Lock()
			// Record the outgoing target when this RAISES the client, so the grace window in
			// handleSubmit has something to fall back to. Without this the machinery is present
			// but never populated on this path, so the protection silently does not apply --
			// and mining.suggest_difficulty is precisely what Bitaxe/AxeOS-class firmware uses,
			// i.e. this app's entire audience. Unlike subscribe/authorize (which run before the
			// client has submitted anything) this fires mid-session, so there IS work in flight
			// against the old target. A lowering suggestion needs no grace: the old target was
			// harder, so that work still clears the new one.
			if suggestedDiff > client.Difficulty {
				client.PreviousDifficulty = client.Difficulty
				client.DifficultyChangedAt = time.Now()
			}
			client.Difficulty = suggestedDiff
			client.mu.Unlock()
			s.clientLog(client, false, "Miner suggested difficulty accepted",
				zap.String("ip", client.IP),
				zap.Float64("difficulty", suggestedDiff))
			s.sendDifficulty(client, suggestedDiff)
		}
		s.sendResponse(client, &Response{ID: req.ID, Result: true})
	case "mining.extranonce.subscribe":
		// Support extranonce subscription for rental services (NiceHash, MRR)
		client.mu.Lock()
		client.SupportsExtranonce = true
		rental := client.RentalService
		client.mu.Unlock()

		if rental != RentalNone {
			s.clientLog(client, false, "Rental service subscribed to extranonce updates",
				zap.String("ip", client.IP),
				zap.String("rental_service", rental.String()))
		}
		s.sendResponse(client, &Response{ID: req.ID, Result: true})
	default:
		s.clientLog(client, false, "Ignoring unsupported stratum method",
			zap.String("method", clip(req.Method, 64)),
			zap.String("ip", client.IP))
		s.sendResponse(client, &Response{ID: req.ID, Result: true})
	}
	return true
}

func (s *Server) handleSubscribe(client *Client, req *Request) *Response {
	client.mu.Lock()
	defer client.mu.Unlock()

	// Parse subscription params to detect user agent
	// Format: ["user-agent/version", "session-id"] or ["user-agent/version"]
	var params []interface{}
	if err := json.Unmarshal(req.Params, &params); err == nil && len(params) > 0 {
		if ua, ok := params[0].(string); ok {
			client.UserAgent = clip(ua, maxUserAgent)
			// Not in a solo home app: there is no rental payout identity here, the floor it
			// would impose (RentalMinDiff, 500000) has no operator knob in the shipped
			// config, and nothing can climb back down from it -- vardiffFloor returns it, so
			// the idle rescue's `prevDiff > floor` is false, suggest_difficulty clamps UP to
			// it, and adjustVardiff is only ever reached from handleSubmit, which a miner
			// stuck at 500000 never reaches. A stratum proxy in front of a few Bitaxes is a
			// normal home topology and "proxy" is one of the trigger substrings; that miner
			// would need over an hour per share. firstRamp already gets genuinely large
			// connections off the floor in a single adjustment, so the floor buys nothing here.
			// Identity is always recorded; only the difficulty policy is withheld in solo.
			client.DetectedMarketplace = detectRentalService(ua)
			if !s.config.SoloOnly {
				client.RentalService = client.DetectedMarketplace
			}
			// Bump difficulty to rental minimum if rental service detected
			if client.RentalService != RentalNone && client.Difficulty < s.config.RentalMinDiff {
				client.Difficulty = s.config.RentalMinDiff
			}
		}
	}

	s.extraNonceMu.Lock()
	s.extraNonce++
	// Use configured extranonce1 size (default 6 bytes = 12 hex chars)
	en1Size := s.config.ExtraNonce1Size
	if en1Size == 0 {
		en1Size = 6
	}
	en1Format := fmt.Sprintf("%%0%dx", en1Size*2)
	// Mask the counter to the configured field width so the hex string is ALWAYS
	// exactly en1Size*2 chars. %0Nx pads but never truncates, so without this, once
	// the uint32 counter passes the field max (e.g. 0xFFFF for a 2-byte field) every
	// new connection gets an over-length extranonce1 and validateShare then rejects
	// 100% of its shares. Wrap-around collisions are harmless for a home solo miner
	// (a handful of connections) since extranonce2 still differs per share.
	en1Value := s.extraNonce
	if en1Size < 4 {
		en1Value &= (uint32(1) << (uint(en1Size) * 8)) - 1
	}
	client.ExtraNonce1 = fmt.Sprintf(en1Format, en1Value)
	s.extraNonceMu.Unlock()

	// Use configured extranonce2 size (default 4, Braiins needs 8)
	client.ExtraNonce2Size = s.config.ExtraNonce2Size
	if client.ExtraNonce2Size == 0 {
		client.ExtraNonce2Size = 4
	}
	client.SubscriptionID = fmt.Sprintf("forge_%s", client.ID)
	client.Subscribed = true

	result := []interface{}{
		[][]string{
			{"mining.set_difficulty", client.SubscriptionID},
			{"mining.notify", client.SubscriptionID},
		},
		client.ExtraNonce1,
		client.ExtraNonce2Size,
	}

	// Log with rental service detection. A marketplace's health check, which logs in and closes
	// again hundreds of times an hour, is logged at debug; noteHealthCheck counts it.
	if isHealthCheck(client.UserAgent) {
		s.logger.Debug("Marketplace health check subscribed",
			zap.String("ip", client.IP),
			zap.String("user_agent", client.UserAgent))
	} else if client.RentalService != RentalNone {
		s.clientLog(client, false, "Rental service client subscribed",
			zap.String("ip", client.IP),
			zap.String("extranonce", client.ExtraNonce1),
			zap.String("rental_service", client.RentalService.String()),
			zap.String("user_agent", client.UserAgent))
	} else {
		s.clientLog(client, false, "Client subscribed",
			zap.String("ip", client.IP),
			zap.String("extranonce", client.ExtraNonce1),
			zap.String("user_agent", client.UserAgent))
	}

	return &Response{ID: req.ID, Result: result}
}

// detectRentalService identifies rental services from user agent string
func detectRentalService(userAgent string) RentalService {
	ua := strings.ToLower(userAgent)

	// NiceHash detection patterns
	// Examples: "NiceHash/1.0.0", "nhmp/1.0.0", "excavator/1.6.3"
	nicehashPatterns := []string{
		"nicehash",
		"nhmp",
		"excavator",
		"nh/",
	}
	for _, pattern := range nicehashPatterns {
		if strings.Contains(ua, pattern) {
			return RentalNiceHash
		}
	}

	// Mining Rig Rentals detection patterns
	// Examples: "MiningRigRentals/1.0", "mrr/", "miningrigrentals", and MRR's rig proxy, which
	// connects rented rigs as "xminer-1.2.6" (also "-rc3", "-rc5") with worker "mrr".
	mrrPatterns := []string{
		"miningrigrentals",
		"mrr/",
		"mrr-",
		"rigrentals",
		"xminer",
	}
	for _, pattern := range mrrPatterns {
		if strings.Contains(ua, pattern) {
			return RentalMRR
		}
	}

	// Generic rental/proxy indicators
	rentalPatterns := []string{
		"rental",
		"proxy",
		"stratum-proxy",
	}
	for _, pattern := range rentalPatterns {
		if strings.Contains(ua, pattern) {
			return RentalOther
		}
	}

	return RentalNone
}

// provenFor and provenAhead bound what a miner's shares let it count for in MaxDifficulty: a valid
// share in the last provenFor, and up to provenAhead times that share's credited difficulty, or
// the level firstRamp set, if higher. Vardiff raises a working miner by at most provenAhead
// between shares, but for firstRamp's one step from the floor, which the floor shares that measured
// it prove; a connection that only claims a difficulty (d= in its password,
// mining.suggest_difficulty) proves nothing.
const (
	provenFor   = 5 * time.Minute
	provenAhead = 4.0
)

// MaxDifficulty is the highest share difficulty a miner is on: the one it was last sent, or the
// one vardiff has set for it if that is higher, as far as its shares have proven it (see
// provenFor). A TIDES job commits to at least this (see tidesgw.Config.MaxDifficulty), so the pool
// credits every share in full. Counting claims as well let one connection with
// d=1000000000000 make every job commit 2^41, so no other miner's share ever qualified. 0 with
// no miner.
//
// A miner with no share in the last provenFor counts at the difficulty it was given, up to its
// port's floor or, where higher, the level remembered for it, which only its shares set. Counting
// proven miners alone, a job registered while a rental had logged in but sent no share yet, or
// while only a marketplace's health checks were logged in, committed to nothing, and the pool
// credited each of the rental's shares on it at its own 1024: 0.2% of the work. A claim still
// counts at the floor only. A miner that has disconnected counts until provenFor after its last
// share (see departedProof).
func (s *Server) MaxDifficulty() float64 { return s.maxDifficultyAt(time.Now()) }

// maxDifficultyAt is MaxDifficulty at time now.
func (s *Server) maxDifficultyAt(now time.Time) float64 {
	var max float64
	s.clients.Range(func(_, v interface{}) bool {
		c, ok := v.(*Client)
		if !ok {
			return true
		}
		c.mu.RLock()
		if c.Authorized {
			max = math.Max(max, s.countedDifficulty(c, now))
		}
		c.mu.RUnlock()
		return true
	})
	s.departedMu.Lock()
	s.pruneDepartedLocked(now)
	for _, p := range s.departed {
		max = math.Max(max, p.diff)
	}
	s.departedMu.Unlock()
	return max
}

// countedDifficulty is what an authorized client counts for in MaxDifficulty at now. c.mu is held.
func (s *Server) countedDifficulty(c *Client, now time.Time) float64 {
	d := math.Max(c.Difficulty, c.LastDifficultySent)
	if c.ProvenDifficulty > 0 && now.Sub(c.ProvenAt) < provenFor {
		// Until the first share at the level firstRamp set, the last share is at the floor. Counted
		// at provenAhead times that alone, each job registered before that share committed to a
		// fraction of the level, and the pool credited each of the miner's shares on it at that.
		return math.Min(d, math.Max(provenAhead*c.ProvenDifficulty, c.firstRampLevel))
	}
	level := s.vardiffFloor(c.RentalService != RentalNone)
	if r, ok := s.recallDifficulty(c.MinerID, c.WorkerName, hostOf(c.IP)); ok && r > level {
		level = r
	}
	return math.Min(d, level)
}

// departedProof is the level a disconnected miner had proven, and when its last share came. It
// counts in MaxDifficulty until provenFor after that share, as it would connected: the job
// registered while a rental reconnected, its old connection gone and the new one without a share
// yet, committed to nothing.
type departedProof struct {
	diff float64
	at   time.Time
}

// maxDeparted bounds the record of departed miners. Each entry costs a valid share.
const maxDeparted = 1024

// keepDeparted records c's proven level as it disconnects, if its last share is recent.
func (s *Server) keepDeparted(c *Client, now time.Time) {
	c.mu.RLock()
	proven := c.Authorized && c.ProvenDifficulty > 0 && now.Sub(c.ProvenAt) < provenFor
	p := departedProof{at: c.ProvenAt}
	if proven {
		p.diff = s.countedDifficulty(c, now)
	}
	c.mu.RUnlock()
	if !proven {
		return
	}
	s.departedMu.Lock()
	defer s.departedMu.Unlock()
	s.pruneDepartedLocked(now)
	if len(s.departed) >= maxDeparted {
		return
	}
	if s.departed == nil {
		s.departed = make(map[string]departedProof)
	}
	s.departed[c.ID] = p
}

// pruneDeparted forgets departed miners whose last share is provenFor old.
func (s *Server) pruneDeparted(now time.Time) {
	s.departedMu.Lock()
	defer s.departedMu.Unlock()
	s.pruneDepartedLocked(now)
}

func (s *Server) pruneDepartedLocked(now time.Time) {
	for id, p := range s.departed {
		if now.Sub(p.at) >= provenFor {
			delete(s.departed, id)
		}
	}
}

// WorkerRef names a worker: the miner it is credited to and its label, and when its connection
// began.
type WorkerRef struct {
	MinerID, WorkerName string
	ConnectedAt         time.Time
}

// isProbe reports whether c is a marketplace's health check (isHealthCheck) that has sent no share:
// not a miner, though it logs in as one. One that sends shares is a miner like any other. c.mu is
// held.
func (c *Client) isProbe() bool {
	return isHealthCheck(c.UserAgent) && c.ValidShares.Load() == 0
}

// CountMiners is how many connections are miners, and how many of those have logged in, counted
// in one pass. A marketplace's health checks are left out: MiningRigRentals' log in under the
// order's own name all through a rental, two at a time, and the dashboard showed 2 or 3 workers
// for one rented rig, "3 miner(s) authorized and submitting shares", and, before the rig arrived,
// a miner connected that should be checked for hashing.
func (s *Server) CountMiners() (connected, authorized int64) {
	s.clients.Range(func(_, v interface{}) bool {
		c, ok := v.(*Client)
		if !ok {
			return true
		}
		c.mu.RLock()
		if !c.isProbe() {
			connected++
			if c.Authorized {
				authorized++
			}
		}
		c.mu.RUnlock()
		return true
	})
	return connected, authorized
}

// AuthorizedWorkers names the workers of the clients connected and authorized now (Braiins'
// probes and marketplaces' health checks left out).
func (s *Server) AuthorizedWorkers() []WorkerRef {
	var out []WorkerRef
	s.clients.Range(func(_, v interface{}) bool {
		c, ok := v.(*Client)
		if !ok {
			return true
		}
		c.mu.RLock()
		if c.Authorized && c.MinerID != "" && c.MinerID != "probe" && !c.isProbe() {
			out = append(out, WorkerRef{MinerID: c.MinerID, WorkerName: c.WorkerName, ConnectedAt: c.ConnectedAt})
		}
		c.mu.RUnlock()
		return true
	})
	return out
}

// CountAuthorized returns the number of clients that have completed mining.authorize.
//
// Deliberately distinct from ActiveConnections, which counts TCP accepts: a rig that is
// being refused reconnects in a loop and INFLATES that number, so the one signal that
// would reveal a pool refusing every miner reads as a healthy, busy pool.
func (s *Server) CountAuthorized() int64 {
	var n int64
	s.clients.Range(func(_, v interface{}) bool {
		c, ok := v.(*Client)
		if !ok {
			return true
		}
		c.mu.RLock()
		if c.Authorized {
			n++
		}
		c.mu.RUnlock()
		return true
	})
	return n
}

// SetSoloPayoutAddress tells the server which address the coinbase pays, so a solo miner
// may authorize with any worker name and still be credited under the address that is
// actually being mined to. Safe to call at any time; the dashboard can change the address
// while miners are connected.
func (s *Server) SetSoloPayoutAddress(addr string) {
	s.soloPayoutMu.Lock()
	s.soloPayout = addr
	s.soloPayoutMu.Unlock()
	// Where the install pays one address, connected miners are credited to the new one from
	// now on. Their address was fixed at authorize, so after a change on the dashboard every
	// connected miner stayed on the old address (0 H/s for the new one, invalid shares and
	// blocks recorded under an address the jobs no longer pay), and one that logged in with
	// its own address before any payout address was set stayed on that. handleSubmit also
	// credits each share to the address in effect, for a miner authorizing during this loop.
	if !s.config.SoloOnly || !s.config.CreditPayoutAddress {
		return
	}
	payout := normalizeMinerAddress(addr)
	if payout == "" {
		return
	}
	s.clients.Range(func(_, v interface{}) bool {
		c := v.(*Client)
		c.mu.Lock()
		if c.Authorized && c.MinerID != "" && c.MinerID != "probe" {
			c.MinerID = payout
		}
		c.mu.Unlock()
		return true
	})
}

// SoloPayoutAddress returns the address set by SetSoloPayoutAddress, or "" if none.
func (s *Server) SoloPayoutAddress() string {
	s.soloPayoutMu.RLock()
	defer s.soloPayoutMu.RUnlock()
	return s.soloPayout
}

// maxWorkerLabel bounds a worker label accepted from an unauthenticated connection before
// it reaches a database column.
const maxWorkerLabel = 64

// workerLabel is a miner's chosen worker name as it is kept and shown: letters, digits and . _ - @ +
// stay, every other byte (spaces, colons, slashes, control and non-ASCII characters) becomes "_",
// and it is at most maxWorkerLabel long. Anyone who can reach the stratum chooses a name -- the
// rental port is open to the internet -- and the dashboard showed it as written, so a name such as
// "WARNING: payout address changed, see …" read there as a message to the owner.
func workerLabel(name string) string {
	if len(name) > maxWorkerLabel {
		name = name[:maxWorkerLabel]
	}
	b := []byte(name)
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '_', c == '-', c == '@', c == '+':
		default:
			b[i] = '_'
		}
	}
	if len(b) == 0 {
		return "default"
	}
	return string(b)
}

// shortAddress is a CashAddr as a worker label: the start and end of its payload, e.g.
// "qruu6e2t…crwj94", distinct enough to tell two rigs apart.
func shortAddress(addr string) string {
	p := addr[strings.LastIndex(addr, ":")+1:]
	if len(p) <= 16 {
		return p
	}
	return p[:8] + "…" + p[len(p)-6:]
}

// authorized is what an accepted mining.authorize set, read while the client's lock was held.
type authorized struct {
	minerID string
	// sendWork: the connection's first login, or one that changed its difficulty. Either is
	// followed by the difficulty and the current job.
	sendWork bool
}

func (s *Server) handleAuthorize(client *Client, req *Request) *Response {
	resp, _ := s.authorize(client, req)
	return resp
}

func (s *Server) authorize(client *Client, req *Request) (*Response, authorized) {
	var params []string
	// Debug log
	if err := json.Unmarshal(req.Params, &params); err != nil || len(params) < 1 {
		return &Response{ID: req.ID, Result: false, Error: ErrUnauthorized}, authorized{}
	}

	username := params[0]
	minerID, workerName := parseUsername(username)
	workerName = workerLabel(workerName)

	// Solo: any worker name is a valid username, because the username is not a payout
	// identity here. The coinbase pays the configured address (see SaveSoloBlockCoinbaseDirect);
	// minerID is only the stats key. Refusing "rig1" is what the pool-derived code did, and
	// it is exactly what this app's own Settings page, README and manifest tell the user to
	// type -- the rig connects, is refused, receives no job, and sits at 0 H/s on a fully
	// synced node with no error the user can see.
	//
	// The address is used as the minerID rather than a constant, because the dashboard looks
	// a miner up by matching the configured payout address (getMiner in cmd/api), so a
	// constant would render every tile empty.
	if minerID == "" && s.config.SoloOnly {
		if payout := s.SoloPayoutAddress(); payout != "" {
			minerID = payout
			workerName = workerLabel(username)
			s.minerLog(client, false, "Solo miner authorized by worker label",
				zap.String("worker", workerName),
				zap.String("credited_to", payout),
				zap.String("ip", client.IP))
		}
	}

	// A username that is some other address is a label too where the install pays one
	// address (CreditPayoutAddress). Keying its stats to that address hid the miner from the
	// dashboard, which shows the payout address -- a rental that logged in with its own address
	// sat at 0 H/s there for its whole length -- and recorded its blocks under an address the
	// coinbase never paid.
	if minerID != "" && s.config.SoloOnly && s.config.CreditPayoutAddress {
		payout := normalizeMinerAddress(s.SoloPayoutAddress())
		if minerID != payout && (workerName == "" || workerName == "default") {
			// Labelled by the address even while no payout address is set yet: shares are
			// credited to the payout address in effect when they arrive (handleSubmit), so
			// this miner is credited to it as soon as one is set.
			workerName = shortAddress(minerID)
		}
		if payout != "" && minerID != payout {
			s.minerLog(client, false, "Solo miner authorized with another address as its label",
				zap.String("username_address", minerID),
				zap.String("worker", workerName),
				zap.String("credited_to", payout),
				zap.String("ip", client.IP))
			minerID = payout
		}
	}

	// Allow Braiins probe connections (used to verify pool connectivity).
	// These use usernames like "braiinstest" which aren't valid addresses.
	//
	// Runs AFTER the solo fallback on purpose. A rented-hashpower worker label also starts
	// "braiins", and crediting a whole rental to the fake miner "probe" would leave the
	// dashboard blank for its entire duration. In solo the fallback has already claimed
	// such a username under the real payout address, so only an install with no payout
	// address set -- which cannot serve a job anyway -- still reaches this.
	if strings.HasPrefix(strings.ToLower(username), "braiins") && minerID == "" {
		s.minerLog(client, false, "Braiins probe connection accepted",
			zap.String("username", clip(username, maxWorkerLabel)),
			zap.String("ip", client.IP))
		// Set a dummy address for probe - won't receive payouts
		minerID = "probe"
		workerName = workerLabel(username)
	}

	// Reject invalid addresses - they cannot receive payouts
	if minerID == "" {
		s.clientLog(client, true, "Rejected connection with invalid address",
			zap.String("username", clip(username, maxWorkerLabel)),
			zap.String("ip", client.IP))
		return &Response{ID: req.ID, Result: false, Error: ErrUnauthorized}, authorized{}
	}
	// Every label is bounded, on every path above. Uncapped, a username of the payout address
	// plus a 60 KB label reached the TIDES share queue, made a batch the pool refuses as too
	// large, and stalled every other miner's shares behind it.
	if len(workerName) > maxWorkerLabel {
		workerName = workerName[:maxWorkerLabel]
	}

	// Detect rental service from worker name patterns. Skipped in solo for the same reason
	// as the user-agent path above -- and here the docs make it worse: they promise the
	// worker name is "just a label", while a label containing "rental" or starting "nh_"
	// would silently impose a 500000 floor.
	// Identity is always recorded; only the difficulty policy is withheld in solo.
	detectedFromWorker := detectRentalFromWorker(workerName)
	var rentalFromWorker RentalService
	if !s.config.SoloOnly {
		rentalFromWorker = detectedFromWorker
	}

	var soloMode bool
	var manualDiff float64

	// Solo-only deployment (home miner): everyone mines solo, ignore any PPLNS setting.
	if s.config.SoloOnly {
		soloMode = true
	}

	if !s.config.SoloOnly && s.minerSettings != nil {
		if settings, err := s.minerSettings.GetMinerSettings(minerID); err == nil && settings != nil {
			soloMode = settings.SoloMining
			manualDiff = settings.ManualDiff

			// Autosave: Create default settings for new miners
			// Default to SOLO mode for solo-only pools
			if !settings.Exists && minerID != "probe" {
				go func(mid string) {
					newSettings := &MinerSettings{
						MinerID:    mid,
						SoloMining: true,
						ManualDiff: 0,
					}
					if err := s.minerSettings.SaveMinerSettings(newSettings); err != nil {
						s.logger.Debug("Autosave settings for new miner",
							zap.String("miner", mid),
							zap.Error(err))
					}
				}(minerID)
			}
		}
	}

	// The stratum password is NOT retained. It used to be hashed and kept as a
	// proof-of-control secret for settings changes, but that route is gone: the mining
	// password must never override a settings PIN, or whoever rents the rig inherits the
	// payout address with it. Only the difficulty hint is read out of it now.
	var hintedDiff float64
	if len(params) >= 2 {
		hintedDiff = parsePasswordDiffHint(params[1])
	}
	netDiff := s.networkDifficulty()

	client.mu.Lock()
	// A connection may authorize a few names (a proxy can carry several rigs), and the same
	// name any number of times. Each new name is a worker entry on the dashboard, so a client
	// cycling through new names could otherwise create them without end.
	nameKey := minerID + ":" + workerName
	if _, known := client.workerNames[nameKey]; !known {
		if len(client.workerNames) >= maxWorkerNamesPerConnection {
			client.mu.Unlock()
			return &Response{ID: req.ID, Result: false, Error: ErrTooManyWorkers}, authorized{}
		}
		if client.workerNames == nil {
			client.workerNames = make(map[string]struct{})
		}
		client.workerNames[nameKey] = struct{}{}
	}
	wasAuthorized, diffBefore := client.Authorized, client.Difficulty
	client.Authorized = true
	client.MinerID = minerID
	client.WorkerName = workerName
	client.SoloMining = soloMode
	client.ManualDiff = manualDiff
	client.LastSettingsRefresh = time.Now()

	// Update rental detection if found from worker name (user agent takes priority)
	if client.DetectedMarketplace == RentalNone && detectedFromWorker != RentalNone {
		client.DetectedMarketplace = detectedFromWorker
	}
	if client.RentalService == RentalNone && rentalFromWorker != RentalNone {
		client.RentalService = rentalFromWorker
	}

	rental := client.RentalService

	// startDiff is the difficulty this connection opens at, from the strongest evidence
	// available before it has submitted anything.
	//
	// A stored manual difficulty wins. Otherwise a difficulty pinned in the stratum
	// password ("d=2000000") is honoured: that is the convention rented hashpower uses to
	// declare how big it is up front, it arrives before the first share, and it is clamped
	// to exactly the same assignment floor and maximum as every other path, so trusting it
	// costs the operator nothing. A marketplace that sets it skips the vardiff ramp
	// entirely.
	//
	// Deliberately NOT written back to client.ManualDiff: that field switches vardiff OFF
	// for the connection (see the call site of adjustVardiff), and a password hint is a
	// starting point, not a contract. Keeping vardiff live means a hint that turns out to
	// be wrong self-corrects, while a stored manual difficulty -- which the operator set
	// deliberately -- still pins the client as before.
	startDiff := manualDiff
	if startDiff <= 0 && hintedDiff > 0 {
		startDiff = hintedDiff
	}

	if startDiff > 0 {
		// For rental services, ensure manual diff meets their minimum requirement
		if rental != RentalNone && startDiff < s.config.RentalMinDiff {
			client.Difficulty = s.config.RentalMinDiff
		} else {
			client.Difficulty = startDiff
		}
		// Clamp to the configured maximum so a client-supplied difficulty cannot be
		// pinned to an absurd value.
		if s.config.MaxDiff > 0 && client.Difficulty > s.config.MaxDiff {
			client.Difficulty = s.config.MaxDiff
		}
		// ...and up to the assignment floor, so a stored d=1 cannot turn one miner into a
		// share flood. Anything at or above the floor is honoured exactly as requested.
		// Computed inline from the in-scope `rental`: client.mu is already held here, and
		// getMinDiffForClient takes RLock -- calling it would deadlock.
		assignFloor := s.config.AbsoluteMinDiff
		if rental != RentalNone {
			assignFloor = s.config.RentalMinDiff
		}
		if client.Difficulty < assignFloor {
			client.Difficulty = assignFloor
		}
	} else if client.Difficulty <= s.config.MinDiff {
		// Vardiff mode with no client-suggested difficulty above the floor. Resume the
		// miner's recently-ramped level (or the rental floor) instead of restarting at
		// the tiny default — this stops a reconnecting/churning miner (e.g. a big rental
		// proxy that cycles connections) from flooding the pool with low-difficulty,
		// often-stale shares while vardiff slowly ramps back up. The value is sent to the
		// miner via sendDifficulty right after authorize, so there is no diff mismatch.
		// vardiffFloor is lock-free, so it is safe to call with client.mu held.
		floor := s.vardiffFloor(rental != RentalNone)
		ceil := s.config.MaxDiff
		if rental != RentalNone && s.config.RentalMaxDiff > 0 {
			ceil = s.config.RentalMaxDiff
		}
		// Bound a RESUMED difficulty to the rental cap even for non-rentals: a level
		// ramped under one connection must not resume at an absurd value on another
		// (defence-in-depth alongside idleDifficultyLoop, which self-corrects a too-high
		// resume within idleResetAfter regardless).
		if s.config.RentalMaxDiff > 0 && ceil > s.config.RentalMaxDiff {
			ceil = s.config.RentalMaxDiff
		}
		start := floor
		// Resume a remembered (ramped-up) difficulty EXCEPT for hashrate marketplaces
		// that fan many individual, differently-sized miners onto one payout address
		// (Braiins Hashpower). Those must each vardiff from the floor for THEIR OWN
		// hashrate — resuming one miner's ramped level onto all of them starves the
		// small ones and floods the pool with stale shares. A single proxy or a normal
		// miner (identified by NOT being such a marketplace) keeps the resume so it
		// does not re-ramp from the floor on every reconnect.
		if !isManyMinerMarketplace(client.UserAgent) {
			if remembered, ok := s.recallDifficulty(minerID, workerName, hostOf(client.IP)); ok && remembered > start {
				start = remembered
			}
		}
		if ceil > 0 && start > ceil {
			start = ceil
		}
		client.Difficulty = start
	}
	client.Difficulty = belowNetwork(client.Difficulty, netDiff, s.vardiffFloor(rental != RentalNone))
	difficulty := client.Difficulty
	userAgent := client.UserAgent
	client.mu.Unlock()

	if soloMode {
		s.stats.SoloMiners.Add(1)
	} else {
		s.stats.PPLNSMiners.Add(1)
	}

	modeStr := "PPLNS"
	if soloMode {
		modeStr = "SOLO"
	}

	// The address and user agent tell a rented rig from the marketplace's health checks, which log
	// in under the order's own name.
	switch {
	case isHealthCheck(userAgent):
		s.logger.Debug("Marketplace health check authorized",
			zap.String("ip", client.IP),
			zap.String("user_agent", userAgent),
			zap.String("miner", minerID),
			zap.String("worker", workerName),
			zap.Float64("difficulty", difficulty))
	case rental != RentalNone:
		s.minerLog(client, false, "Rental miner authorized",
			zap.String("ip", client.IP),
			zap.String("user_agent", userAgent),
			zap.Int64("valid_shares", client.ValidShares.Load()),
			zap.String("miner", minerID),
			zap.String("worker", workerName),
			zap.String("accounting", modeStr), // how this miner's shares are counted, not the payout mode
			zap.String("rental_service", rental.String()),
			zap.Float64("difficulty", difficulty))
	default:
		s.minerLog(client, false, "Miner authorized",
			zap.String("ip", client.IP),
			zap.String("user_agent", userAgent),
			zap.Int64("valid_shares", client.ValidShares.Load()),
			zap.String("miner", minerID),
			zap.String("worker", workerName),
			zap.String("accounting", modeStr), // how this miner's shares are counted, not the payout mode
			zap.Float64("difficulty", difficulty))
	}

	return &Response{ID: req.ID, Result: true}, authorized{
		minerID:  minerID,
		sendWork: !wasAuthorized || difficulty != diffBefore,
	}
}

// parsePasswordDiffHint extracts a fixed difficulty from the stratum password field.
//
// "d=2000000" (optionally among other comma- or semicolon-separated terms, which is how
// rental proxies and marketplace order forms commonly pass several settings at once) is the
// de-facto convention for a client declaring the difficulty it wants. Returns 0 when there
// is no usable hint, which every caller treats as "no opinion".
//
// Deliberately tolerant of the surrounding syntax and strict about the value: a malformed
// or non-positive number is no hint at all rather than an assignment of zero, which would
// otherwise flood the pool.
func parsePasswordDiffHint(password string) float64 {
	for _, term := range strings.FieldsFunc(strings.TrimSpace(password), func(r rune) bool {
		return r == ',' || r == ';' || r == ' '
	}) {
		if !strings.HasPrefix(strings.ToLower(term), "d=") {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(term[2:]), 64)
		if err != nil || v <= 0 {
			return 0
		}
		return v
	}
	return 0
}

// detectRentalFromWorker detects rental services from worker name patterns
func detectRentalFromWorker(worker string) RentalService {
	w := strings.ToLower(worker)

	// NiceHash often uses worker names like "nh_xxxx" or contains "nicehash"
	if strings.HasPrefix(w, "nh_") || strings.HasPrefix(w, "nh-") ||
		strings.Contains(w, "nicehash") {
		return RentalNiceHash
	}

	// MRR often uses worker names like "mrr_xxxx" or "rig_xxxx"
	if w == "mrr" || strings.HasPrefix(w, "mrr_") || strings.HasPrefix(w, "mrr-") ||
		strings.HasPrefix(w, "mrr.") || strings.Contains(w, "miningrigrentals") {
		return RentalMRR
	}

	// Generic rental patterns
	if strings.Contains(w, "rental") || strings.Contains(w, "rent_") {
		return RentalOther
	}

	return RentalNone
}

func (s *Server) handleSubmit(client *Client, req *Request) *Response {
	// Intake rate limit: bound submits per client per second so the live stratum
	// cannot be flooded (invalid/duplicate submits still cost parse + dedup work).
	//
	// Counted here but ENFORCED after validation. Discarding a submit unvalidated throws
	// away whatever it happened to be, and one of those submits can be a block: a large
	// miner that has not yet ramped off the difficulty floor exceeds this limit for as long
	// as the ramp takes, and a solution found in that window would be answered with error
	// 26 and never looked at. A block is worth more than every share this limit protects
	// against, so the limit drops shares, never solutions: a BCH2 block is accepted, and a
	// 1175 block is sent to the 1175 node (its share still refused).
	var overRate bool
	if maxRate := s.config.MaxSharesPerSecond; maxRate > 0 {
		client.mu.Lock()
		now := time.Now()
		if now.Sub(client.submitWindowStart) >= time.Second {
			client.submitWindowStart = now
			client.submitCount = 0
		}
		client.submitCount++
		overRate = client.submitCount > maxRate
		client.mu.Unlock()
	}

	client.mu.RLock()
	if !client.Authorized {
		client.mu.RUnlock()
		s.clientLog(client, true, "Submit from unauthorized client", zap.String("ip", client.IP))
		return &Response{ID: req.ID, Result: false, Error: ErrUnauthorized}
	}
	minerID := client.MinerID
	workerName := client.WorkerName
	difficulty := client.Difficulty
	// Grace-period state, captured under the lock the handler already holds. Both fields
	// are written on every difficulty change; validation simply never read them before.
	prevDifficulty := client.PreviousDifficulty
	difficultyChangedAt := client.DifficultyChangedAt
	soloMining := client.SoloMining
	manualDiff := client.ManualDiff
	extranonce1 := client.ExtraNonce1
	extranonce2Size := client.ExtraNonce2Size
	lastSettingsRefresh := client.LastSettingsRefresh
	userAgent := client.UserAgent
	client.mu.RUnlock()

	// Refresh settings every 15 seconds to allow on-the-fly mode changes.
	// Solo-only deployments never read a PPLNS setting: mode is locked to SOLO.
	if !s.config.SoloOnly && s.minerSettings != nil && time.Since(lastSettingsRefresh) > 15*time.Second {
		if settings, err := s.minerSettings.GetMinerSettings(minerID); err == nil && settings != nil {
			client.mu.Lock()
			if client.SoloMining != settings.SoloMining {
				s.minerLog(client, false, "Miner mode changed on-the-fly",
					zap.String("miner", minerID),
					zap.Bool("old_solo", client.SoloMining),
					zap.Bool("new_solo", settings.SoloMining))
			}
			client.SoloMining = settings.SoloMining
			client.ManualDiff = settings.ManualDiff
			client.LastSettingsRefresh = time.Now()
			soloMining = settings.SoloMining
			manualDiff = settings.ManualDiff
			client.mu.Unlock()
		}
	}

	// Parse params as []interface{} to handle miners that send mixed types
	var rawParams []interface{}
	if err := json.Unmarshal(req.Params, &rawParams); err != nil {
		s.minerLog(client, true, "Failed to parse submit params",
			zap.String("miner", minerID),
			zap.Error(err),
			zap.String("params", clip(string(req.Params), 128)))
		s.noteInvalidShare(client, "malformed_params")
		return &Response{ID: req.ID, Result: false, Error: ErrLowDifficulty}
	}

	if len(rawParams) < 5 {
		s.minerLog(client, true, "Insufficient submit params",
			zap.String("miner", minerID),
			zap.Int("count", len(rawParams)))
		s.noteInvalidShare(client, "malformed_params")
		return &Response{ID: req.ID, Result: false, Error: ErrLowDifficulty}
	}

	// Convert all params to strings (handles both string and numeric values)
	// SECURITY: Validate numeric ranges to prevent integer overflow attacks
	params := make([]string, len(rawParams))
	for i, p := range rawParams {
		switch v := p.(type) {
		case string:
			params[i] = v
		case float64:
			// SECURITY: Validate range before casting to prevent overflow
			if v < 0 || v > float64(^uint32(0)) {
				s.minerLog(client, true, "Invalid numeric parameter - out of range",
					zap.Int("param_index", i),
					zap.Float64("value", v))
				s.noteInvalidShare(client, "param_out_of_range")
				return &Response{ID: req.ID, Result: false, Error: ErrLowDifficulty}
			}
			params[i] = fmt.Sprintf("%08x", uint32(v))
		case json.Number:
			if n, err := v.Int64(); err == nil {
				// SECURITY: Validate range before casting
				if n < 0 || n > int64(^uint32(0)) {
					s.minerLog(client, true, "Invalid numeric parameter - out of range",
						zap.Int("param_index", i),
						zap.Int64("value", n))
					s.noteInvalidShare(client, "param_out_of_range")
					return &Response{ID: req.ID, Result: false, Error: ErrLowDifficulty}
				}
				params[i] = fmt.Sprintf("%08x", uint32(n))
			} else {
				params[i] = string(v)
			}
		default:
			params[i] = fmt.Sprintf("%v", p)
		}
	}

	jobID := params[1]
	extranonce2 := normalizeHex(params[2], extranonce2Size*2) // Use client's configured size
	ntime := normalizeHex(params[3], 8)
	nonce := normalizeHex(params[4], 8)
	versionBits := ""
	if len(params) > 5 {
		versionBits = normalizeVersionBits(params[5])
	}
	// Every field has a fixed, short form. Anything else is refused before it is looked up or
	// remembered, and logged clipped: a submit could carry tens of kilobytes in one field, and the
	// duplicate record used to keep each one for minutes.
	if !wellFormedSubmit(jobID, extranonce2, ntime, nonce, versionBits, extranonce2Size) {
		s.minerLog(client, true, "Malformed share refused",
			zap.String("miner", minerID),
			zap.String("ip", client.IP),
			zap.String("user_agent", userAgent),
			zap.String("job", clip(jobID, 16)),
			zap.String("extranonce2", clip(extranonce2, 32)),
			zap.String("ntime", clip(ntime, 16)),
			zap.String("nonce", clip(nonce, 16)))
		s.noteInvalidShare(client, "malformed_params")
		return &Response{ID: req.ID, Result: false, Error: ErrMalformedShare}
	}

	// Debug, not Info: two lines for every share filled a busy miner's log by megabytes a day.
	s.logger.Debug("Share submitted",
		zap.String("miner", minerID),
		zap.String("worker", workerName),
		zap.String("job", jobID),
		zap.String("extranonce2", extranonce2),
		zap.String("ntime", ntime),
		zap.String("nonce", nonce))

	// Get the job from history
	jobInterface, exists := s.jobHistory.Load(jobID)
	if !exists {
		s.minerLog(client, true, "Job not found",
			zap.String("miner", minerID),
			zap.String("job", jobID),
			zap.String("ip", client.IP),
			zap.String("user_agent", userAgent),
			zap.String("extranonce1", extranonce1))
		s.noteInvalidShare(client, "job_not_found")
		return &Response{ID: req.ID, Result: false, Error: ErrJobNotFound}
	}
	job := jobInterface.(*Job)

	// A share on a job from before the last block can no longer become a block: it is stale, as
	// ckpool marks every share on a workbase older than the last block change (stratifier.c:
	// "if (id < sdata->blockchange_id) stale = true;"). It can still solve its job's 1175 block,
	// which does not depend on the BCH2 tip: that block is sent (ckpool, too, tests a stale share
	// for a block).
	if cur, ok := s.currentJob.Load().(*Job); ok && cur != nil && cur.PrevBlockHash != job.PrevBlockHash {
		if job.AuxWork != nil && s.getAuxClient() != nil {
			if _, _, staleHash, verr := s.validateShare(job, extranonce1, extranonce2, ntime, nonce, versionBits, 0); verr == nil {
				s.submitRefusedShareAux(job, staleHash, extranonce1, extranonce2, ntime, nonce, versionBits, minerID, soloMining)
			}
		}
		// Expected now and then: a share found just before a new block reaches the stratum just
		// after it. Information, not a warning; its own label tells it from a job not found.
		s.minerLog(client, false, "Share on a job from before the last block refused as stale (expected now and then, just after a new block)",
			zap.String("miner", minerID),
			zap.String("job", jobID),
			zap.String("ip", client.IP),
			zap.String("user_agent", userAgent))
		s.noteInvalidShare(client, "stale_tip")
		return &Response{ID: req.ID, Result: false, Error: ErrJobNotFound}
	}
	// ckpool: "Ntime cannot be less, but allow forward ntime rolling up to max" -- not before the
	// job's time, nor more than 7000 seconds after it.
	if !ntimeInRange(ntime, job.NTime) {
		s.minerLog(client, true, "Share refused: its ntime is outside its job's time range",
			zap.String("miner", minerID),
			zap.String("job", jobID),
			zap.String("ntime", ntime),
			zap.String("job_ntime", job.NTime),
			zap.String("ip", client.IP),
			zap.String("user_agent", userAgent))
		s.noteInvalidShare(client, "invalid_ntime")
		return &Response{ID: req.ID, Result: false, Error: ErrInvalidNTime}
	}

	client.mu.RLock()
	jobDiff := client.jobDiff[jobID]
	client.mu.RUnlock()

	// Validate the share - verify proof of work
	// Accept any share that meets the share floor - don't waste miner's work
	// Target difficulty is for rate limiting/vardiff, not rejection
	effectiveDiff := s.effectiveJudgingDifficulty(difficulty, prevDifficulty, difficultyChangedAt, time.Now())
	judgeDiff, creditCap := judgeShare(difficulty, effectiveDiff, jobDiff)
	shareFloor := s.shareFloorFor(judgeDiff)
	isValid, actualDiff, blockHash, err := s.validateShare(job, extranonce1, extranonce2, ntime, nonce, versionBits, shareFloor)

	// Now enforce the intake limit, with the one exception it must always make.
	if overRate {
		netDiff := BitsToDifficulty(job.NBits)
		if err != nil || !isValid || netDiff <= 0 || actualDiff < netDiff {
			if err == nil {
				s.submitRefusedShareAux(job, blockHash, extranonce1, extranonce2, ntime, nonce, versionBits, minerID, soloMining)
			}
			// Counted, so the dashboard's reject figure matches what the miner was told. Not
			// logged: a miner over the limit sends a hundred a second.
			s.noteInvalidShare(client, "rate_limited")
			return &Response{ID: req.ID, Result: false, Error: ErrRateLimited}
		}
		s.logger.Warn("submit exceeded the intake rate limit but SOLVES A BLOCK — accepting it",
			zap.String("miner", minerID),
			zap.Float64("actual_diff", actualDiff),
			zap.Float64("network_diff", netDiff))
	}
	if err != nil {
		s.minerLog(client, true, "Share validation error",
			zap.String("miner", minerID),
			zap.Error(err))
		s.noteInvalidShare(client, "invalid")
		return &Response{ID: req.ID, Result: false, Error: ErrLowDifficulty}
	}

	if !isValid {
		// Report the floor ACTUALLY applied plus what the miner was assigned. Logging
		// s.config.MinDiff here would hide exactly the mismatch this fix exists to
		// prevent: an operator debugging a 100%-reject miner needs to see both numbers.
		s.minerLog(client, true, "Share below minimum difficulty",
			zap.String("miner", minerID),
			zap.Float64("required", shareFloor),
			zap.Float64("assigned", difficulty),
			zap.Float64("job_diff", jobDiff),
			zap.Float64("actual", actualDiff),
			zap.String("ip", client.IP),
			zap.String("user_agent", userAgent),
			zap.String("extranonce1", extranonce1))
		s.noteInvalidShare(client, "low_difficulty")

		// Track rejection for vardiff adjustment
		client.mu.Lock()
		client.RecentSubmissions = append(client.RecentSubmissions, false)
		if len(client.RecentSubmissions) > RecentSubmissionsWindow {
			client.RecentSubmissions = client.RecentSubmissions[1:]
		}
		// Check if rejection rate is too high and reduce difficulty
		if len(client.RecentSubmissions) >= 20 && manualDiff == 0 && s.config.VardiffEnabled {
			rejections := 0
			for _, accepted := range client.RecentSubmissions {
				if !accepted {
					rejections++
				}
			}
			rejectionRate := float64(rejections) / float64(len(client.RecentSubmissions))
			// Use appropriate assignment floor based on client type (rental vs regular).
			// vardiffFloor is lock-free, so it is safe to call with client.mu held.
			minDiff := s.vardiffFloor(client.RentalService != RentalNone)
			if rejectionRate > MaxRejectionRate && client.Difficulty > minDiff {
				// Use more aggressive reduction (2x) to settle faster
				oldDiff := client.Difficulty
				newDiff := client.Difficulty / 2.0
				if newDiff < minDiff {
					newDiff = minDiff
				}
				client.PreviousDifficulty = oldDiff
				client.DifficultyChangedAt = time.Now()
				client.DifficultyReducedAt = time.Now()
				client.DifficultyReducedFrom = oldDiff // Remember the ceiling that caused rejections
				client.Difficulty = newDiff
				client.RecentSubmissions = client.RecentSubmissions[:0] // Reset after adjustment
				client.mu.Unlock()

				// In the miners' budget, not the connection's: the shares refused before it have spent that.
				s.limitedLog(minersBudget, false, "High rejection rate, reducing difficulty", 0,
					zap.String("miner", minerID),
					zap.Float64("rejection_rate", rejectionRate),
					zap.Float64("old_diff", oldDiff),
					zap.Float64("new_diff", newDiff))

				s.sendDifficulty(client, newDiff)
				return &Response{ID: req.ID, Result: false, Error: ErrLowDifficulty}
			}
		}
		client.mu.Unlock()

		return &Response{ID: req.ID, Result: false, Error: ErrLowDifficulty}
	}

	// Only work that passed is remembered, so the record grows with real hashing, not with what
	// a client sends. A duplicate is the same block header, whatever job id or spelling it comes
	// under: two jobs on one block can carry identical work, since the coinbase holds nothing per
	// job, and several spellings of a version build the same header.
	if s.isDuplicateShare(blockHash, job.PrevBlockHash) {
		// The whole share, so a miner or a marketplace's proxy sending one result twice can be told
		// from two results that build the same header.
		s.minerLog(client, true, "Duplicate share rejected",
			zap.String("miner", minerID),
			zap.String("job", jobID),
			zap.String("ip", client.IP),
			zap.String("user_agent", userAgent),
			zap.String("extranonce1", extranonce1),
			zap.String("extranonce2", extranonce2),
			zap.String("ntime", ntime),
			zap.String("nonce", nonce),
			zap.String("version", versionBits))
		s.noteInvalidShare(client, "duplicate")
		return &Response{ID: req.ID, Result: false, Error: ErrDuplicateShare}
	}

	now := time.Now()

	// The share is timed at the difficulty it was found against: its job's (see judgeShare), or
	// the current one where the job is not on record. Timed at the current one, a share on a job
	// sent before a raise read as a miner faster than it is, and vardiff raised again and again
	// while a miner that had not yet received the last raise went on working old jobs.
	foundAt := difficulty
	if jobDiff > 0 {
		foundAt = jobDiff
	}

	client.mu.Lock()
	shareCount := client.addShareSample(now, foundAt)
	// Track accepted submission for rejection rate calculation
	client.RecentSubmissions = append(client.RecentSubmissions, true)
	if len(client.RecentSubmissions) > RecentSubmissionsWindow {
		client.RecentSubmissions = client.RecentSubmissions[1:]
	}

	// Save the difficulty at which this share was actually submitted
	// (before any adjustments that apply to future shares).
	//
	// Credit the LESSER of the assigned difficulty and the difficulty the share
	// actually proved. Shares are accepted whenever they meet min_diff (not the
	// per-client assigned difficulty), so crediting the vardiff-inflated assigned
	// difficulty for a share that only meets min_diff would let a miner inflate its
	// PPLNS work weight and drain the shared reward pool. Taking min() still credits
	// an honest miner its full assigned difficulty (its shares meet or exceed it),
	// while a miner submitting cheap min_diff shares is credited only what it proved.
	// A share on a job sent under a lower difficulty is capped at that one (judgeShare).
	shareDifficulty := creditCap
	if actualDiff < shareDifficulty {
		shareDifficulty = actualDiff
	}
	client.ProvenDifficulty, client.ProvenAt = shareDifficulty, now

	client.mu.Unlock()

	// Vardiff adjustment - respects retarget_time interval
	if manualDiff == 0 && s.config.VardiffEnabled && shareCount >= VardiffMinShares {
		s.adjustVardiffAt(client, now)
	}

	// Where the install pays one address, a share is credited to the payout address in effect
	// when it arrives, not the one at authorize. Before, after a change on the dashboard every
	// connected miner stayed on the old address (the dashboard read 0 H/s for the new one, and a
	// block was recorded under an address the new jobs no longer pay), and a miner that logged
	// in with an address before any payout address was set stayed on that address for good.
	if s.config.SoloOnly && s.config.CreditPayoutAddress {
		if payout := normalizeMinerAddress(s.SoloPayoutAddress()); payout != "" {
			minerID = payout
		}
	}

	share := &Share{
		JobID:       jobID,
		MinerID:     minerID,
		WorkerName:  workerName,
		Difficulty:  shareDifficulty, // Use target difficulty for hashrate calculation
		ActualDiff:  actualDiff,      // Use actual share difficulty for block candidate detection
		IP:          client.IP,
		ExtraNonce2: extranonce2,
		ExtraNonce1: extranonce1,
		NTime:       ntime,
		Nonce:       nonce,
		VersionBits: versionBits,
		IsValid:     true,
		IsSolo:      soloMining,
		SubmittedAt: now,
		BlockHash:   hex.EncodeToString(blockHash),
	}

	s.stats.ValidShares.Add(1)
	client.ValidShares.Add(1)

	if s.shareProcessor != nil {
		s.inflight.Add(1)
		go func() {
			defer s.inflight.Add(-1)
			s.shareProcessor.ProcessShare(context.Background(), share)
		}()
	}

	// Merge mining: if this share's parent hash also meets the aux (1175) target,
	// submit the AuxPoW. The target check is cheap and inline; the rebuild+submit
	// runs async only on a winner, so BCH2 share handling is never delayed.
	if s.auxSolution(job, blockHash) {
		go s.submitAux(job, extranonce1, extranonce2, ntime, nonce, versionBits, minerID, soloMining)
	}

	s.logger.Debug("Share accepted",
		zap.String("miner", minerID),
		zap.String("worker", workerName),
		zap.Float64("diff", actualDiff),
		zap.Float64("target_diff", difficulty),
		zap.Bool("solo", soloMining),
		zap.String("hash", hex.EncodeToString(blockHash)[:16]+"..."))

	return &Response{ID: req.ID, Result: true}
}

// diffMemoryTTL bounds how long a miner's last vardiff level is reused across
// reconnects: long enough to survive rapid reconnect churn (e.g. rental proxies that
// cycle connections every fraction of a second), short enough that a miner whose
// hashrate genuinely dropped re-ramps from a sane level.
const diffMemoryTTL = 30 * time.Minute

type diffMem struct {
	diff float64
	at   time.Time
}

// rememberDifficulty records a miner's current vardiff level. Called when vardiff changes it and
// when the miner's shares confirm it, both share-driven, so the remembered value reflects hashrate
// the miner actually proved: a share-less connection cannot inflate it.
// diffMemoryKey identifies ONE piece of hardware.
//
// Keying on minerID alone is wrong wherever several miners share a payout identity, and in
// this solo app they always do: the coinbase pays one address, so every worker authorizes
// under it. Without the worker name, plugging an S19 and a Bitaxe into the same Umbrel
// resumes the S19's ramped difficulty onto the Bitaxe, which then cannot submit at all --
// adjustVardiff only runs from handleSubmit, so nothing corrects it until
// idleDifficultyLoop notices minutes later.
//
// The worker name alone does not tell devices apart either: several can log in under one label
// (the bare payout address, or the same "rig" on each). So the key also holds the host the
// device connects from. A miner or rental proxy that reconnects comes back from the same address
// and keeps its level.
func diffMemoryKey(minerID, workerName, host string) string {
	return minerID + "\x00" + workerName + "\x00" + host
}

func (s *Server) rememberDifficulty(minerID, workerName, host string, diff float64) {
	if minerID == "" || diff <= 0 {
		return
	}
	s.diffMemory.Store(diffMemoryKey(minerID, workerName, host), diffMem{diff: diff, at: time.Now()})
}

// isManyMinerMarketplace reports whether the client's user-agent identifies a
// hashrate marketplace that points many individual miners at the pool under one
// shared payout address (e.g. Braiins Hashpower reports "Braiins/Hashpower").
// Such connections must each vardiff for their own hashrate rather than inherit the
// address's aggregate remembered level. Detecting on the user-agent (rather than an
// instantaneous connection count) is robust to reconnect churn and overlapping
// reconnects. Match only "braiins": it is the definitive Braiins signal and, unlike
// "hashpower", cannot false-match a single-proxy miner like NiceHash's excavator
// (which should keep the resume). Add other many-miner marketplaces here as they
// appear. A false positive is harmless anyway — a normal single miner that skips the
// resume simply re-ramps via vardiff, which is its ordinary behaviour.
func isManyMinerMarketplace(userAgent string) bool {
	return strings.Contains(strings.ToLower(userAgent), "braiins")
}

// recallDifficulty returns a miner's remembered vardiff level if it is still fresh.
func (s *Server) recallDifficulty(minerID, workerName, host string) (float64, bool) {
	v, ok := s.diffMemory.Load(diffMemoryKey(minerID, workerName, host))
	if !ok {
		return 0, false
	}
	m, ok := v.(diffMem)
	if !ok || time.Since(m.at) > diffMemoryTTL {
		return 0, false
	}
	return m.diff, true
}

// maxShareSamples bounds each client's record of its accepted shares.
const maxShareSamples = 100

// shareSample is an accepted share: when it arrived, and the difficulty it was found against.
type shareSample struct {
	at   time.Time
	diff float64 // 0: not known, taken as the difficulty in force when the sample is read
}

// addShareSample records an accepted share for vardiff and returns how many are on record.
// Caller holds c.mu.
func (c *Client) addShareSample(at time.Time, diff float64) int {
	c.ShareSamples = append(c.ShareSamples, shareSample{at: at, diff: diff})
	if len(c.ShareSamples) > maxShareSamples {
		c.ShareSamples = c.ShareSamples[1:]
		c.samplesDropped = true
	}
	return len(c.ShareSamples)
}

// measuredShareTime is how long the miner takes to find a share at difficulty current, in
// seconds, measured over its shares of the last window, or its latest VardiffSampleShares where
// those are more (all of them while it has fewer): the time they span, over the work found after
// the first, counted in shares at current. It is 0 where that cannot be measured.
//
// Each share counts as the work it was found against. Counted as one share at the current
// difficulty, the shares from before a change made the rate they were found at look like the
// rate at the new difficulty: vardiff raised again after a raise and cut again after a cut, and a
// steady miner swung between a fraction and several times its level.
//
// adjustVardiffAt gives VardiffSampleTime as the window, or 0 while firstRamp may still lift the
// connection off its floor (see there). At the main port's target_time a miner sends 30 shares in
// under three minutes, and judged again at every share, luck alone took its difficulty over the
// edge of the variance window several times an hour; VardiffSampleTime holds more of its shares.
// A miner that sends fewer in that time, as a rental at the rental port's target_time does, is
// measured over its latest VardiffSampleShares as before. The window stops at a share found below
// half the current difficulty: that one belongs to the climb to this difficulty, not to the rate
// now. The cost: a miner whose hashrate rises takes longer to reach its new level, its shares
// meanwhile faster, never refused.
func measuredShareTime(samples []shareSample, current float64, window time.Duration) float64 {
	if current <= 0 {
		return 0
	}
	n := VardiffSampleShares
	if len(samples) > 0 {
		last, k := samples[len(samples)-1].at, 0
		for i := len(samples) - 1; i >= 0 && last.Sub(samples[i].at) <= window; i-- {
			if d := samples[i].diff; d > 0 && d < current/2 {
				break
			}
			k++
		}
		n = max(n, k)
	}
	if len(samples) > n {
		samples = samples[len(samples)-n:]
	}
	if len(samples) < 2 {
		return 0
	}
	span := samples[len(samples)-1].at.Sub(samples[0].at).Seconds()
	shares := 0.0
	for _, sm := range samples[1:] {
		diff := sm.diff
		if diff <= 0 {
			diff = current
		}
		shares += diff / current
	}
	if span <= 0 || shares <= 0 {
		return 0
	}
	return span / shares
}

// shareTimeSince is the time per share over the whole time since `since`: from then to now, over
// the work of every share in samples, counted in shares at current. 0 where since is not known.
func shareTimeSince(samples []shareSample, current float64, since, now time.Time) float64 {
	if since.IsZero() || current <= 0 || !now.After(since) {
		return 0
	}
	work := 0.0
	for _, sm := range samples {
		diff := sm.diff
		if diff <= 0 {
			diff = current
		}
		work += diff / current
	}
	if work <= 0 {
		return 0
	}
	return now.Sub(since).Seconds() / work
}

// adjustVardiff retargets client's difficulty from its latest shares.
func (s *Server) adjustVardiff(client *Client) {
	s.adjustVardiffAt(client, time.Now())
}

// adjustVardiffAt is adjustVardiff at time now.
func (s *Server) adjustVardiffAt(client *Client, now time.Time) {
	client.mu.Lock()

	// Only adjust every RetargetTime seconds (default 60)
	retargetTime := s.config.RetargetTime
	if retargetTime == 0 {
		retargetTime = 60
	}
	if now.Sub(client.DifficultyChangedAt) < time.Duration(retargetTime)*time.Second {
		client.mu.Unlock()
		return
	}

	if len(client.ShareSamples) < VardiffMinShares {
		client.mu.Unlock()
		return
	}
	// Use appropriate assignment floor based on client type (rental vs regular).
	// vardiffFloor is lock-free, so it is safe to call with client.mu held.
	minDiff := s.vardiffFloor(client.RentalService != RentalNone)
	// A connection that firstRamp may still lift off its floor is measured over its latest shares
	// alone. Once the record has dropped its first share, firstRamp's step is no longer checked
	// against the time since the first job, and rests on the latest shares having come after any
	// that were read together; the last VardiffSampleTime can still hold those.
	window := VardiffSampleTime
	if !client.FirstRampDone && client.Difficulty <= minDiff {
		window = 0
	}
	avgTime := measuredShareTime(client.ShareSamples, client.Difficulty, window)
	if avgTime <= 0 {
		client.mu.Unlock()
		return
	}

	targetTime := float64(s.config.TargetShareTime)
	ratio := targetTime / avgTime
	// The measured ratio before any clamping. firstRamp below needs it: the clamps are
	// exactly what it must bypass.
	measuredRatio := ratio

	// Check rejection rate before adjusting
	rejections := 0
	for _, accepted := range client.RecentSubmissions {
		if !accepted {
			rejections++
		}
	}
	rejectionRate := 0.0
	if len(client.RecentSubmissions) > 0 {
		rejectionRate = float64(rejections) / float64(len(client.RecentSubmissions))
	}

	// If rejection rate is high, don't increase difficulty even if timing suggests we should
	if rejectionRate > MaxRejectionRate && ratio > 1.0 {
		client.mu.Unlock()
		return
	}

	// Only adjust if outside variance window (miningcore style)
	// This prevents constant small adjustments
	variance := s.config.VariancePercent
	if variance <= 0 {
		variance = VardiffVariancePercent
	}
	varianceLow := 1.0 - variance
	varianceHigh := 1.0 + variance
	if ratio >= varianceLow && ratio <= varianceHigh {
		// The shares confirm the level, so it is remembered again: a reconnect, or a marketplace's
		// health check under the same name, resumes it. It used to be remembered only when it
		// changed, so a steady rental's level aged out after diffMemoryTTL (at target_time 25, a
		// quarter of the time) and those opened at the floor.
		minerID, workerName, level := client.MinerID, client.WorkerName, client.Difficulty
		client.mu.Unlock()
		s.rememberDifficulty(minerID, workerName, hostOf(client.IP), level)
		return
	}

	// Clamp ratio to prevent extreme difficulty changes (max 50% per adjustment)
	if ratio > MaxDifficultyMultiplier {
		ratio = MaxDifficultyMultiplier
	} else if ratio < 1.0/MaxDifficultyMultiplier {
		ratio = 1.0 / MaxDifficultyMultiplier
	}

	// firstRamp: let a connection that starts at the floor and immediately proves it is
	// far bigger take the whole measured ratio in ONE step, instead of crawling up at
	// +50% per retarget.
	//
	// The clamped ramp is not merely slow, it is self-harming for large hashrate. 1 PH/s
	// of rented Braiins hashpower assigned the shipped min_diff of 1024 produces ~227
	// shares/s against the 100/s intake cap in handleSubmit: more than half of its
	// submissions are refused, and it takes ~20 clamped steps at 30s each -- about ten
	// minutes -- to climb to the ~2.3M difficulty its rate actually warrants. Marketplace
	// hashpower reconnects often, and isManyMinerMarketplace deliberately denies it the
	// remembered-difficulty resume so each fanned miner sizes itself, which means it
	// re-enters that ten-minute window on EVERY reconnect. A marketplace judges a pool by
	// its reject rate; this reads as a broken pool.
	//
	// Deliberately bounded so it cannot become a yo-yo: once per connection, only from the
	// floor, and only upward. Everything after it is the ordinary clamped ramp. A small miner
	// is untouched: at the floor its measured ratio sits inside the variance window and the
	// function has already returned above.
	//
	// Overshoot is not harmless. The next adjustment needs accepted shares, and a miner set far
	// above its level sends almost none, so it stays there for hours. The shares are timed as the
	// stratum reads them, and ones that arrive together (the segment carrying the first ones lost
	// and resent, the stratum held up for a moment) read as a rate hundreds of thousands of times
	// the real one. So while the record holds every share since the connection's first job, the
	// step is checked against the rate over that whole time:
	//   - more than twice as slow: the shares arrived together, or the miner started hashing
	//     after its first job (a reboot, an order filled later, an idle reset). Nothing changes
	//     until more shares are in, at most until the record drops its first share; from then
	//     the latest shares, read after any such bunch, measure the step alone.
	//   - otherwise the step is the lower of the two.
	// The step is capped, for a record that is all one bunch, and taken only where it goes further
	// than the ordinary step.
	firstRamp := !client.FirstRampDone && client.Difficulty <= minDiff && measuredRatio > 1.0
	rampRatio := math.Min(measuredRatio, firstRampMaxStep)
	if firstRamp && !client.samplesDropped {
		since := client.firstJobAt
		if since.IsZero() {
			since = client.ConnectedAt
		}
		sinceJob := shareTimeSince(client.ShareSamples, client.Difficulty, since, now)
		if sinceJob > 2*avgTime {
			client.mu.Unlock()
			return
		}
		if sinceJob > avgTime {
			rampRatio = math.Min(rampRatio, targetTime/sinceJob)
		}
	}

	// Calculate new difficulty
	newDiff := client.Difficulty * ratio
	// For rental services, apply gentler MaxDelta (max 25% change)
	maxDelta := client.Difficulty * 0.5 // 50% max change for regular miners
	if client.RentalService != RentalNone {
		maxDelta = client.Difficulty * 0.25 // 25% max change for NiceHash/MRR
	}
	diffDelta := newDiff - client.Difficulty
	if diffDelta > maxDelta {
		newDiff = client.Difficulty + maxDelta
	} else if diffDelta < -maxDelta {
		newDiff = client.Difficulty - maxDelta
	}
	ramped := false
	if firstRamp && client.Difficulty*rampRatio > newDiff {
		newDiff = client.Difficulty * rampRatio
		client.FirstRampDone, ramped = true, true
	}
	if newDiff < minDiff {
		newDiff = minDiff
	}
	// Use rental-specific max diff for NiceHash/MRR to prevent over-ramping
	maxDiff := s.config.MaxDiff
	if client.RentalService != RentalNone && s.config.RentalMaxDiff > 0 {
		maxDiff = s.config.RentalMaxDiff
	}
	if newDiff > maxDiff {
		newDiff = maxDiff
	}
	newDiff = belowNetwork(newDiff, s.networkDifficulty(), minDiff)

	// If we recently reduced difficulty due to high rejection rate,
	// don't increase above 80% of the ceiling that caused the rejection.
	//
	// NOTE: this ceiling is applied AFTER the floor clamp above and can legitimately land
	// BELOW the floor (DifficultyReducedFrom is only ever set to a value above the floor,
	// so the ceiling bottoms out at 0.8x the floor). That is deliberate -- backing off is
	// the correct response to rejections -- and it is only safe because shareFloorFor()
	// judges the share at min(assigned, MinDiff). Before that existed, this line handed
	// the miner a sub-floor target while still judging at MinDiff, i.e. a silent rejection
	// band that could not self-heal: the remedy above is gated on
	// client.Difficulty > floor, which this very line had just made false.
	// Do NOT "fix" this by clamping the ceiling back up to the floor -- that re-imposes
	// the difficulty that was causing rejections in the first place.
	if client.DifficultyReducedFrom > 0 && now.Sub(client.DifficultyReducedAt) < DifficultyReductionCooldown {
		ceiling := client.DifficultyReducedFrom * 0.8
		if newDiff > ceiling {
			newDiff = ceiling
			s.logger.Debug("Vardiff capped at ceiling",
				zap.String("miner", client.MinerID),
				zap.Float64("ceiling", ceiling),
				zap.Float64("original", client.Difficulty*ratio))
		}
	}

	if newDiff != client.Difficulty {
		oldDiff := client.Difficulty
		client.PreviousDifficulty = oldDiff
		client.DifficultyChangedAt = now
		client.Difficulty = newDiff
		if ramped {
			client.firstRampLevel = newDiff
		}
		minerID := client.MinerID
		workerName := client.WorkerName
		client.mu.Unlock()

		// Remember this share-proven level so the miner resumes near it on reconnect.
		s.rememberDifficulty(minerID, workerName, hostOf(client.IP), newDiff)

		// Send difficulty synchronously to ensure miner receives it
		s.sendDifficulty(client, newDiff)

		s.limitedLog(minersBudget, false, "Vardiff adjusted", 0,
			zap.String("miner", minerID),
			zap.Float64("avg_time", avgTime),
			zap.Float64("old_diff", oldDiff),
			zap.Float64("new_diff", newDiff))
		return
	}
	client.mu.Unlock()
}

// clientQueue is how many messages may wait for one miner. A miner that stops reading is dropped
// when its queue is full, rather than holding up everyone else's messages.
const clientQueue = 64

// readerRoom is how many queue slots a connection's reader keeps free before it handles its next
// line: room for the answers to that line (a login sends three) and for a job broadcast meanwhile.
const readerRoom = 8

// waitForRoom holds the connection's reader while its queue is nearly full, until the writer has
// sent enough of it. A miner whose own requests came faster than the answers could be written (a
// pipelined burst, or a backlog read after the stratum was held up) was dropped as if it had
// stopped reading: on a 1-CPU host the writer gets no CPU while the reader works through lines it
// already has. A peer that has stopped reading is still dropped: the write to it fails after
// writeTimeout, the connection is closed, and the writer goes on taking messages off the queue.
func (c *Client) waitForRoom() {
	if c.out == nil {
		return
	}
	for c.queued() > clientQueue-readerRoom {
		<-c.progress
	}
}

// queued is how many messages wait for the client's writer; 0 once its queue is closed.
func (c *Client) queued() int {
	c.outMu.Lock()
	defer c.outMu.Unlock()
	if c.outClosed {
		return 0
	}
	return len(c.out)
}

// enqueue queues data for client's writer, and reports whether it was queued. A full queue means
// the miner has stopped reading: it is disconnected.
func (c *Client) enqueue(data []byte) bool {
	c.outMu.Lock()
	defer c.outMu.Unlock()
	if c.outClosed {
		return false
	}
	select {
	case c.out <- data:
		return true
	default:
		c.outClosed = true
		if c.closeReason == "" {
			c.closeReason = fmt.Sprintf("it stopped reading: %d messages were waiting for it", clientQueue)
		}
		close(c.out)
		c.Conn.Close()
		return false
	}
}

// closeFor closes client's connection, recording why. The first reason given is the one logged
// when the connection ends ("Client disconnected"): without it a connection the stratum closed and
// one the miner closed read the same, and a marketplace's complaint could not be told from either.
func (c *Client) closeFor(reason string) {
	c.outMu.Lock()
	if c.closeReason == "" {
		c.closeReason = reason
	}
	c.outMu.Unlock()
	c.Conn.Close()
}

// minerClosedIt is why a connection ended that the far end closed cleanly.
const minerClosedIt = "the miner closed the connection"

// whyClosed is why a connection ended: the reason the stratum gave when it closed it, or else
// what the read that ended it says (readErr is the line reader's error, nil at end of input).
func (c *Client) whyClosed(readErr error, authorized bool) string {
	c.outMu.Lock()
	reason := c.closeReason
	c.outMu.Unlock()
	if reason != "" {
		return reason
	}
	var ne net.Error
	switch {
	case readErr == nil:
		return minerClosedIt
	case errors.Is(readErr, bufio.ErrTooLong):
		return "it sent a message over 64 KB"
	case errors.As(readErr, &ne) && ne.Timeout():
		if authorized {
			return fmt.Sprintf("it was silent for %.0f minutes", authorizedIdleTimeout.Minutes())
		}
		return fmt.Sprintf("it did not log in within %.0f seconds", authTimeout.Seconds())
	default:
		return "the connection broke: " + readErr.Error()
	}
}

// closeOut ends client's queue; its writer finishes what is queued and stops.
func (c *Client) closeOut() {
	c.outMu.Lock()
	defer c.outMu.Unlock()
	if !c.outClosed {
		c.outClosed = true
		close(c.out)
	}
}

// writeLoop sends client's queued messages in order. It is the only writer of a connection that
// has a queue: a job broadcast used to write to each miner in turn, waiting up to 10 seconds on
// each, so one miner gone without closing (a power cut, Wi-Fi dropped) held every other miner's
// new-block job back by that long. ckpool likewise sends "non-blocking to only send to those
// clients ready to receive data".
func (s *Server) writeLoop(client *Client) {
	for data := range client.out {
		client.Conn.SetWriteDeadline(s.writeDeadline())
		if _, err := client.Conn.Write(data); err != nil {
			client.closeFor("a write to it failed: " + err.Error()) // the rest fail at once, and the reader ends the connection
		}
		select {
		case client.progress <- struct{}{}:
		default:
		}
	}
}

// writeTimeout is how long one write to a miner may take.
const writeTimeout = 10 * time.Second

// writeDeadline is when a write to a miner started now must have finished.
func (s *Server) writeDeadline() time.Time {
	wait := s.writeWait
	if wait <= 0 {
		wait = writeTimeout
	}
	return time.Now().Add(wait)
}

// send sends data to client: through its queue where it has one (every real connection), or
// written at once where it has none.
func (s *Server) send(client *Client, data []byte) error {
	if client.out != nil {
		if !client.enqueue(data) {
			return net.ErrClosed
		}
		return nil
	}
	client.Conn.SetWriteDeadline(s.writeDeadline())
	_, err := client.Conn.Write(data)
	if err != nil {
		client.closeFor("a write to it failed: " + err.Error()) // Force disconnect on write error
	}
	return err
}

func (s *Server) sendResponse(client *Client, resp *Response) {
	data, _ := json.Marshal(resp)
	data = append(data, '\n')
	_ = s.send(client, data)
}

func (s *Server) sendNotification(client *Client, notif *Notification) {
	data, _ := json.Marshal(notif)
	data = append(data, '\n')
	if err := s.send(client, data); err != nil {
		// Log write error for NiceHash clients
		client.mu.RLock()
		rental := client.RentalService
		minerID := client.MinerID
		client.mu.RUnlock()
		if rental == RentalNiceHash {
			s.clientLog(client, true, "Write error to NiceHash client",
				zap.String("miner", minerID),
				zap.String("method", notif.Method),
				zap.Error(err))
		}
		client.closeFor("a write to it failed: " + err.Error()) // Force disconnect on write error
	}
}

func (s *Server) sendDifficulty(client *Client, diff float64) {
	// Prevent sending duplicate difficulty notifications within 500ms.
	// This avoids race conditions between authorize and job broadcast.
	//
	// Only an IDENTICAL value is suppressed. Every caller has already written
	// client.Difficulty by the time it gets here, so dropping a notification that carries a
	// DIFFERENT value leaves the pool and the miner permanently disagreeing about the
	// target -- including swallowing the post-rejection back-off, whose whole purpose is to
	// tell a struggling miner to work easier.
	s.sendDifficultyAs(client, diff, false)
}

// sendCurrentDifficulty sends the client's difficulty as it stands when the message is queued. A
// value read before that can be one the idle rescue or a broadcast has since replaced and sent, and
// sending it afterwards leaves the miner on a difficulty the stratum no longer has for it.
func (s *Server) sendCurrentDifficulty(client *Client) {
	s.sendDifficultyAs(client, 0, true)
}

func (s *Server) sendDifficultyAs(client *Client, diff float64, current bool) {
	client.sendMu.Lock()
	defer client.sendMu.Unlock()
	client.mu.Lock()
	if current {
		diff = client.Difficulty
	}
	if (current && diff <= 0) ||
		(client.LastDifficultySent == diff && time.Since(client.LastDifficultySentAt) < 500*time.Millisecond) {
		client.mu.Unlock()
		return
	}
	client.LastDifficultySentAt = time.Now()
	client.LastDifficultySent = diff
	client.mu.Unlock()

	notif := &Notification{
		Method: MethodSetDifficulty,
		Params: []interface{}{diff},
	}
	s.sendNotification(client, notif)
}

func (s *Server) sendJob(client *Client, job *Job) {
	// The difficulty this job goes out under is recorded and the job sent as one step, serialised
	// with sendDifficulty, so the record names exactly the last set_difficulty the miner received
	// before this notify -- the one it will judge this job's shares by (judgeShare). Recorded any
	// other way, a raise sent from another goroutine between the two could land on the wire
	// after the notify while the record claimed it came before, and the job's honest shares would
	// be judged too hard.
	client.sendMu.Lock()
	defer client.sendMu.Unlock()
	client.mu.Lock()
	client.noteJobDifficulty(job.ID, client.LastDifficultySent)
	if client.firstJobAt.IsZero() {
		client.firstJobAt = time.Now()
	}
	client.mu.Unlock()

	notif := &Notification{
		Method: MethodNotify,
		Params: []interface{}{
			job.ID,
			job.PrevBlockHash,
			job.CoinBase1,
			job.CoinBase2,
			job.MerkleBranches,
			job.Version,
			job.NBits,
			job.NTime,
			job.CleanJobs,
		},
	}

	// Log job delivery for NiceHash clients for debugging
	client.mu.RLock()
	rental := client.RentalService
	minerID := client.MinerID
	client.mu.RUnlock()

	if rental == RentalNiceHash {
		s.clientLog(client, false, "Sending job to NiceHash client",
			zap.String("miner", minerID),
			zap.String("job_id", job.ID),
			zap.String("prevhash", job.PrevBlockHash[:16]+"..."),
			zap.String("version", job.Version),
			zap.String("nbits", job.NBits),
			zap.String("ntime", job.NTime),
			zap.Bool("clean", job.CleanJobs))
	}

	s.sendNotification(client, notif)
}

func (s *Server) BroadcastJob(job *Job) {
	s.currentJob.Store(job)
	s.jobHistory.Store(job.ID, job)

	// CRITICAL FIX: Clean up old jobs to prevent unbounded memory growth
	// Keep only the last 100 jobs in history
	s.cleanupJobHistory(500)

	// Clear old shares when broadcasting clean jobs (new block height)
	if job.CleanJobs {
		s.clearSharesForJob()
	}

	netDiff := BitsToDifficulty(job.NBits)
	s.clients.Range(func(key, value interface{}) bool {
		client := value.(*Client)
		client.mu.Lock()
		authorized := client.Authorized
		// The network difficulty moves with every block: a miner above the new one is brought
		// down to it.
		lowered := false
		if authorized {
			d := belowNetwork(client.Difficulty, netDiff, s.vardiffFloor(client.RentalService != RentalNone))
			lowered = d != client.Difficulty
			client.Difficulty = d
		}
		client.mu.Unlock()
		if authorized {
			// For clean jobs (new block), resend difficulty to ensure miners have it
			// Some miners (like Whatsminer) may miss difficulty notifications
			if job.CleanJobs || lowered {
				s.sendCurrentDifficulty(client)
			}
			s.sendJob(client, job)
		}
		return true
	})
}

// cleanupJobHistory removes old jobs from history to prevent unbounded memory growth
// CRITICAL FIX: Prevents memory exhaustion from accumulating job history
func (s *Server) cleanupJobHistory(maxJobs int) {
	type jobEntry struct {
		id    string
		idNum uint64
		valid bool // id parsed as an integer
	}
	var jobs []jobEntry

	s.jobHistory.Range(func(key, value interface{}) bool {
		id := key.(string)
		n, err := strconv.ParseUint(id, 16, 64) // job IDs are hex; parse as base-16 for correct oldest-first eviction
		jobs = append(jobs, jobEntry{id: id, idNum: n, valid: err == nil})
		return true
	})

	// If under limit, no cleanup needed
	if len(jobs) <= maxJobs {
		return
	}

	// Evict OLDEST-first, DETERMINISTICALLY. Job IDs are monotonically increasing
	// integers, so a smaller ID is an older job. sync.Map.Range order is randomized,
	// so deleting in iteration order (the previous behaviour) could drop recent,
	// still-active jobs and reject their timely shares as "Job not found" — worst for
	// higher-latency miners (e.g. Braiins routed through Cloudflare). Sort ascending
	// and delete only the oldest excess; unparseable IDs sort first so they drain out.
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].valid != jobs[j].valid {
			return !jobs[i].valid
		}
		return jobs[i].idNum < jobs[j].idNum
	})

	var currentID string
	if cj := s.currentJob.Load(); cj != nil {
		currentID = cj.(*Job).ID
	}

	toDelete := len(jobs) - maxJobs
	for i := 0; i < len(jobs) && toDelete > 0; i++ {
		if jobs[i].id == currentID {
			continue // never evict the job miners are currently working on
		}
		s.jobHistory.Delete(jobs[i].id)
		toDelete--
	}
}

func (s *Server) GetStats() *ServerStats {
	return &ServerStats{
		ActiveConnections: s.stats.ActiveConnections.Load(),
		ValidShares:       s.stats.ValidShares.Load(),
		InvalidShares:     s.stats.InvalidShares.Load(),
		BlocksFound:       s.stats.BlocksFound.Load(),
		SoloMiners:        s.stats.SoloMiners.Load(),
		PPLNSMiners:       s.stats.PPLNSMiners.Load(),
	}
}

// RentalStats contains statistics about rental service connections
type RentalStats struct {
	NiceHashMiners int64
	MRRMiners      int64
	OtherRentals   int64
	TotalRentals   int64
}

// GetRentalStats returns statistics about connected rental miners
func (s *Server) GetRentalStats() *RentalStats {
	stats := &RentalStats{}

	s.clients.Range(func(key, value interface{}) bool {
		client := value.(*Client)
		client.mu.RLock()
		// DetectedMarketplace, not RentalService: the latter is a difficulty-policy flag
		// that is deliberately never set in solo, which is why this endpoint reported
		// {0,0,0,0} to external aggregators while a rental order was actively mining.
		rental := client.DetectedMarketplace
		// A marketplace's health check is not an order: MiningRigRentals' were counted as two more
		// rentals ("other", for the "proxy" in their user agent), and alone they had the dashboard
		// say rented hashrate was reaching the pool.
		authorized := client.Authorized && !client.isProbe()
		client.mu.RUnlock()

		if !authorized {
			return true
		}

		switch rental {
		case RentalNiceHash:
			stats.NiceHashMiners++
			stats.TotalRentals++
		case RentalMRR:
			stats.MRRMiners++
			stats.TotalRentals++
		case RentalOther:
			stats.OtherRentals++
			stats.TotalRentals++
		default:
			// Nothing recognisable in the user agent, but this listener exists ONLY for
			// marketplace hashpower -- that is the whole reason the difficulty floor is
			// taken from the port instead of guessed from a user agent. A rented rig
			// relayed to this port announces itself as the ASIC it is ("Antminer S21 XP"),
			// not as the marketplace that rented it, so keying identity off the user agent
			// alone reports zero rentals while a paid order is actively mining. Observed
			// doing exactly that. The port is the authoritative signal; the user agent only
			// refines WHICH marketplace.
			if s.config.IsRentalPort {
				stats.OtherRentals++
				stats.TotalRentals++
			}
		}
		return true
	})

	return stats
}

func parseUsername(username string) (minerID, workerName string) {
	parts := strings.SplitN(username, ".", 2)
	minerID = parts[0]
	if len(parts) > 1 {
		workerName = parts[1]
	} else {
		workerName = "default"
	}

	// Normalize address: ensure bitcoincashii: prefix (lowercase)
	minerID = normalizeMinerAddress(minerID)

	// CashAddr is exactly 42 chars after prefix. If extra chars remain
	// (e.g. NiceHash appends worker suffix without dot separator),
	// split them off as worker name.
	if strings.HasPrefix(minerID, "bitcoincashii:") {
		hash := minerID[len("bitcoincashii:"):]
		if len(hash) > 42 {
			extra := hash[42:]
			minerID = "bitcoincashii:" + hash[:42]
			if workerName == "default" {
				workerName = extra
			}
		}
	}

	return
}

// normalizeMinerAddress ensures the address has the correct bitcoincashii: prefix
// Returns empty string for invalid/rejected address formats
// validCashAddrChecksum verifies a CashAddr's BCH-style polymod checksum.
//
// Length and first-character checks are not validation: a single mistyped character in an
// address used as the stratum username produced a perfectly happy authorize, and shares
// recorded under a minerID that the dashboard API then refused with "Invalid BCH2 address
// format" -- so every tile read zero forever with nothing to explain it. Funds were never
// at risk (the coinbase only ever uses the configured payout pubkeyHash), but the app's own
// banner invites this by telling users they may authorize with their address.
//
// In a solo deployment a rejection here is not fatal: handleAuthorize falls back to the
// configured payout address and treats the whole username as a worker label, so the miner
// still mines and is credited correctly.
func validCashAddrChecksum(address string) bool {
	const charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"
	i := strings.LastIndex(address, ":")
	if i < 0 {
		return false
	}
	prefix, payload := address[:i], address[i+1:]
	if len(payload) < 8 {
		return false
	}
	v := make([]byte, 0, len(prefix)+1+len(payload))
	for j := 0; j < len(prefix); j++ {
		v = append(v, prefix[j]&0x1f)
	}
	v = append(v, 0)
	for _, c := range payload {
		k := strings.IndexRune(charset, c)
		if k < 0 {
			return false
		}
		v = append(v, byte(k))
	}
	return cashaddrPolymodStratum(v) == 0
}

// cashaddrPolymodStratum is the BCH CashAddr polymod (a 40-bit BCH code over GF(32)).
func cashaddrPolymodStratum(v []byte) uint64 {
	c := uint64(1)
	for _, d := range v {
		c0 := c >> 35
		c = ((c & 0x07ffffffff) << 5) ^ uint64(d)
		if c0&0x01 != 0 {
			c ^= 0x98f2bc8e61
		}
		if c0&0x02 != 0 {
			c ^= 0x79b76d99e2
		}
		if c0&0x04 != 0 {
			c ^= 0xf33e5fb3c4
		}
		if c0&0x08 != 0 {
			c ^= 0xae2eabe2a8
		}
		if c0&0x10 != 0 {
			c ^= 0x1e4f43e470
		}
	}
	return c ^ 1
}

// NormalizeMinerAddress is addr in the form miners are credited and looked up under, or "" if it
// is not a BCH2 address.
func NormalizeMinerAddress(addr string) string { return normalizeMinerAddress(addr) }

func normalizeMinerAddress(addr string) string {
	// Convert to lowercase for comparison
	lowerAddr := strings.ToLower(addr)

	// REJECT bitcoincash2: prefix - invalid format
	if strings.HasPrefix(lowerAddr, "bitcoincash2:") {
		return "" // Signal rejection
	}

	// If already has correct prefix, validate it has an actual hash after the prefix
	if strings.HasPrefix(lowerAddr, "bitcoincashii:") {
		hash := lowerAddr[len("bitcoincashii:"):]
		if len(hash) < 42 || (hash[0] != 'q' && hash[0] != 'p') {
			return "" // Reject: prefix without valid hash
		}
		if !validCashAddrChecksum(lowerAddr) {
			return "" // one-character typo: see validCashAddrChecksum
		}
		return lowerAddr
	}

	// Handle truncated prefix: bitcoinii: -> bitcoincashii: (WhatsMiner firmware bug)
	if strings.HasPrefix(lowerAddr, "bitcoinii:") {
		hash := lowerAddr[len("bitcoinii:"):]
		if len(hash) >= 42 && (hash[0] == 'q' || hash[0] == 'p') {
			return "bitcoincashii:" + hash
		}
		return "" // Reject: invalid hash after prefix
	}

	// Reject bare prefix variants with no hash (firmware truncation)
	if lowerAddr == "bitcoincashii" || lowerAddr == "bitcoincash" || lowerAddr == "bitcoinii" {
		return "" // Signal rejection
	}

	// If it's just the hash part (starts with 'q' for mainnet), add prefix
	if len(addr) >= 42 && (strings.HasPrefix(lowerAddr, "q") || strings.HasPrefix(lowerAddr, "p")) {
		return "bitcoincashii:" + lowerAddr
	}

	// Reject anything else that isn't a valid address
	return ""
}

// normalizeHex pads a hex string to the required length with leading zeros
// connectionLimitFor is how many connections may be open when one from addr arrives. A quarter of
// the slots are kept for this network's own miners (private, loopback and link-local addresses):
// connections from the internet, where a forwarded port lets anyone in, can take only the rest,
// so two addresses holding open as many as they may (the per-IP cap is half the total) can no
// longer keep the owner's own rigs out.
func (s *Server) connectionLimitFor(addr net.Addr) int64 {
	limit := int64(s.config.MaxConnections)
	if isLocalNetwork(addr) {
		return limit
	}
	return limit - limit/4
}

// isLocalNetwork reports whether addr is on this machine or its local network.
func isLocalNetwork(addr net.Addr) bool {
	var ip net.IP
	switch a := addr.(type) {
	case *net.TCPAddr:
		ip = a.IP
	default:
		ip = net.ParseIP(hostOf(addr.String()))
	}
	if ip == nil {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && ip4[1]&0xc0 == 64 {
		return true // 100.64.0.0/10, the address a carrier-grade NAT gives this network
	}
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
}

// authTimeout is how long a connection may take to authorize. A miner subscribes and authorizes in
// its first seconds; a connection that has not by then is holding a slot without mining in it.
var authTimeout = 60 * time.Second

// authorizedIdleTimeout is how long an authorized miner may stay silent. Silence is normal: a rig
// sends nothing between shares, and at its difficulty floor a small one can go many minutes
// between them, while a marketplace keeps spare connections that send nothing at all. These were
// dropped every five minutes and reconnected, over and over. A dead peer is found by TCP
// keepalive and by the job notifications that fail to send. It must stay above idleResetAfter.
// A variable so a test can shorten it.
var authorizedIdleTimeout = 30 * time.Minute

// clientLogBudget is how many log lines one connection's own messages may write a minute. Past it
// the lines are counted, not written: the connection's next line says how many were left out, and
// so does the line logLeftOut writes. A client could otherwise write the log full -- the stratum's
// is kept at 30 MB on Umbrel -- and rotate away everything else in it, a found block's lines
// included. Counting what was left out matters as much as the limit: a throttle that hides the
// evidence would hide a miner losing all its work.
const clientLogBudget = 20

// serverLogBudget is how many log lines a minute clients may cause on one port, besides a miner's
// own lines (minerLogBurst): the lines about connections opening, closing and being refused, and
// about what they send. A budget per connection alone did not bound them: every new connection came
// with a fresh one, and opening and closing connections in a loop rotated the whole log away within
// minutes.
const serverLogBudget = 120

// minerLogBurst and minerLogRate are the budget on one port for a miner's own lines: its login, the
// shares refused to it, the changes to its difficulty and its disconnect. A rental's start brings
// its rigs at once, all through the marketplace's one address and each logging in a few times in
// its first minute. While one budget held every line, their logins used it up and the refused
// shares and disconnects after them were left out. These lines have a budget of their own, which
// connections that never log in cannot use up, and it holds a burst: minerLogBurst lines at once,
// refilled at minerLogRate a minute. Logging in costs nothing, so a flood of logins is held to
// minerLogRate once the burst is spent.
const (
	minerLogBurst = 480
	minerLogRate  = 120
)

// logBudget is a budget a line a client caused counts in.
type logBudget int

const (
	ownBudget    logBudget = iota // the connection's own: clientLogBudget
	portBudget                    // the port's: serverLogBudget
	minersBudget                  // the port's for a miner's own lines: minerLogBurst
)

// why says why the lines a budget left out were left out.
func (b logBudget) why() string {
	switch b {
	case ownBudget:
		return fmt.Sprintf("one connection's own messages caused more than %d lines a minute", clientLogBudget)
	case portBudget:
		return fmt.Sprintf("lines about connections, besides miners' logins, refused shares, difficulty changes and disconnects, came to more than %d a minute on this port",
			serverLogBudget)
	default:
		return fmt.Sprintf("miners' logins, refused shares, difficulty changes and disconnects came to more than %d lines at once, or %d a minute after that, on this port",
			minerLogBurst, minerLogRate)
	}
}

// maxUserAgent is how much of a client's user agent is kept and logged.
const maxUserAgent = 128

// maxBadLines is how many lines that are not JSON a subscribed client may send before it is
// disconnected. ckpool disconnects at the first ("Invalid JSON, disconnecting").
const maxBadLines = 5

// logLimit is a budget of lines a minute: a connection's own, or a port's.
type logLimit struct {
	mu          sync.Mutex
	windowStart time.Time
	n           int
	suppressed  int64
}

// take reports whether a line may be written now within budget lines a minute, and how many were
// left out since the last one written.
func (l *logLimit) take(now time.Time, budget int) (bool, int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.windowStart) >= time.Minute {
		l.windowStart, l.n = now, 0
	}
	if l.n >= budget {
		l.suppressed++
		return false, 0
	}
	l.n++
	skipped := l.suppressed
	l.suppressed = 0
	return true, skipped
}

// giveBack counts n lines as left out again: the line that was to say so was left out itself.
func (l *logLimit) giveBack(n int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.suppressed += n
}

// drain returns the count of lines left out and not yet reported, and clears it.
func (l *logLimit) drain() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := l.suppressed
	l.suppressed = 0
	return n
}

// logBucket is a budget that holds a burst: up to burst lines at once, refilled at perMinute a
// minute. Its zero value is full.
type logBucket struct {
	mu     sync.Mutex
	at     time.Time // when tokens was brought up to date
	tokens float64   // the lines that may be written now
}

// take reports whether a line may be written now.
func (b *logBucket) take(now time.Time, burst, perMinute int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case b.at.IsZero():
		b.tokens, b.at = float64(burst), now
	case now.After(b.at):
		// Only a later time refills it: a caller that read the clock before another, then took the
		// lock after it, adds nothing.
		b.tokens = math.Min(float64(burst), b.tokens+now.Sub(b.at).Minutes()*float64(perMinute))
		b.at = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// leftOutLines counts the lines each budget left out, by message, until logLeftOut sums them up.
type leftOutLines struct {
	mu sync.Mutex
	by [minersBudget + 1]map[string]int64
}

// add counts a line with message msg that budget b left out.
func (l *leftOutLines) add(b logBudget, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.by[b] == nil {
		l.by[b] = make(map[string]int64)
	}
	l.by[b][msg]++
}

// take returns the counts and starts again from none.
func (l *leftOutLines) take() [minersBudget + 1]map[string]int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	by := l.by
	l.by = [minersBudget + 1]map[string]int64{}
	return by
}

// logLeftOut writes, for each budget that left lines out since it last ran, a line saying how many,
// why, and how many of each message. The cleanup round runs it every shareCleanupEvery, and Stop
// before it returns, so lines left out show within that in a line of their own, not as a count on
// whatever line comes next, about whatever connection, maybe minutes later.
func (s *Server) logLeftOut() {
	for b, lines := range s.leftOut.take() {
		var n int64
		for _, c := range lines {
			n += c
		}
		if n == 0 {
			continue
		}
		s.logger.Warn("Log lines left out to keep the log from filling",
			zap.String("reason", logBudget(b).why()),
			zap.Int64("count", n),
			zap.Any("lines", lines))
	}
}

// clientLog writes a line that a client's own message caused, within that connection's budget and
// the port's (serverLogBudget).
func (s *Server) clientLog(client *Client, warn bool, msg string, fields ...zap.Field) {
	s.ownLog(client, portBudget, warn, msg, fields...)
}

// minerLog is clientLog for a miner's login and the shares refused to it: they count in the
// miners' budget on the port (minerLogBurst), which connections that never log in cannot use up.
func (s *Server) minerLog(client *Client, warn bool, msg string, fields ...zap.Field) {
	s.ownLog(client, minersBudget, warn, msg, fields...)
}

// ownLog writes a line a client's own message caused, within that connection's budget and then the
// port's budget b. How many of the connection's lines its own budget left out goes with the line,
// or, when the port's budget leaves this one out, with its next.
func (s *Server) ownLog(client *Client, b logBudget, warn bool, msg string, fields ...zap.Field) {
	ok, own := client.logs.take(time.Now(), clientLogBudget)
	if !ok {
		s.leftOut.add(ownBudget, msg)
		return
	}
	if !s.limitedLog(b, warn, msg, own, fields...) {
		client.logs.giveBack(own)
	}
}

// limitedLog writes a line a client caused within the port's budget b. Every line a client can cause
// goes through here, but the lines about a block it solves and the counts written on a timer (see
// TestEveryLineAClientCausesIsBudgeted). own is how many of the connection's lines its own budget
// left out since its last one, said with this line. It reports whether the line was written; one
// left out is counted for logLeftOut.
func (s *Server) limitedLog(b logBudget, warn bool, msg string, own int64, fields ...zap.Field) bool {
	now := time.Now()
	var ok bool
	if b == minersBudget {
		ok = s.minerLogs.take(now, minerLogBurst, minerLogRate)
	} else {
		ok, _ = s.logs.take(now, serverLogBudget)
	}
	if !ok {
		s.leftOut.add(b, msg)
		return false
	}
	if own > 0 {
		fields = append(fields, zap.Int64("suppressed_since_last", own))
	}
	if warn {
		s.logger.Warn(msg, fields...)
	} else {
		s.logger.Info(msg, fields...)
	}
	return true
}

// isHealthCheck reports whether a user agent is a marketplace's check that the pool works, not a
// miner: MiningRigRentals' "infinite-hash-proxy/probe", which logs in under the order's own name
// about 550 times an hour during a rental, holds the connection about 10 s and sends no share, and
// its pool test "MiningRigRentals/Test/1.0".
func isHealthCheck(userAgent string) bool {
	ua := strings.ToLower(userAgent)
	return strings.HasSuffix(ua, "/probe") || strings.HasPrefix(ua, "miningrigrentals/test")
}

// healthChecks counts the marketplace health checks that logged in and closed again.
type healthChecks struct {
	mu    sync.Mutex
	n     int64     // counted since the last line
	since time.Time // the last line
	said  bool      // the first one has been logged
	ua    string    // the latest one's user agent and address
	ip    string
}

// healthCheckSummaryEvery is how often the health checks counted are logged while they come: the
// count still shows, to within minutes, that the marketplace could reach the pool, as the lines of
// each one did.
const healthCheckSummaryEvery = 10 * time.Minute

// noteHealthCheck counts a health check that logged in and closed the connection itself. The first
// is logged at once, the rest together every healthCheckSummaryEvery (flushHealthChecks).
func (s *Server) noteHealthCheck(ua, ip string, now time.Time) {
	s.probes.mu.Lock()
	first := !s.probes.said
	if first {
		s.probes.said, s.probes.since = true, now
	} else {
		s.probes.n++
	}
	s.probes.ua, s.probes.ip = ua, ip
	s.probes.mu.Unlock()
	if first {
		s.logger.Info("Marketplace health check logged in and closed again: the next ones are counted and logged together every 10 minutes",
			zap.String("user_agent", ua),
			zap.String("ip", ip))
	}
}

// flushHealthChecks logs how many health checks came since the last line, once
// healthCheckSummaryEvery has passed since it, or at once when final (the stratum is stopping).
func (s *Server) flushHealthChecks(now time.Time, final bool) {
	s.probes.mu.Lock()
	n, since, ua, ip := s.probes.n, s.probes.since, s.probes.ua, s.probes.ip
	due := n > 0 && (final || now.Sub(since) >= healthCheckSummaryEvery)
	if due {
		s.probes.n, s.probes.since = 0, now
	}
	s.probes.mu.Unlock()
	if due {
		s.logger.Info("Marketplace health checks logged in and closed again",
			zap.Int64("count", n),
			zap.Duration("over", now.Sub(since)),
			zap.String("user_agent", ua),
			zap.String("ip", ip))
	}
}

// clip shortens a client-supplied string for keeping or logging.
func clip(v string, n int) string {
	if len(v) <= n {
		return v
	}
	return v[:n] + "…"
}

// maxWorkerNamesPerConnection is how many different worker names one connection may authorize.
const maxWorkerNamesPerConnection = 8

// normalizeVersionBits is a submit's optional version bits as RollVersion takes them: at most 8
// hex characters, a leading 0x dropped. Anything else -- a JSON null, text, a longer field -- means
// "no rolling", as RollVersion has always taken it, rather than a refused share: a miner that sends
// the field without rolling versions had every share refused otherwise, its blocks included. Mapped
// to "" here, a long field is never kept or logged either.
func normalizeVersionBits(v string) string {
	v = strings.TrimPrefix(strings.TrimPrefix(v, "0x"), "0X")
	if len(v) > 8 || !isHex(v) {
		return ""
	}
	return v
}

// wellFormedSubmit reports whether a submit's fields have the form a share's do: a job id this
// server issues (hex, at most 16 characters), an extranonce2 of the client's size, ntime and nonce
// of 8 hex characters each, and version bits of at most 8.
func wellFormedSubmit(jobID, extranonce2, ntime, nonce, versionBits string, extranonce2Size int) bool {
	return len(jobID) >= 1 && len(jobID) <= 16 && isHex(jobID) &&
		len(extranonce2) == extranonce2Size*2 && isHex(extranonce2) &&
		len(ntime) == 8 && isHex(ntime) && len(nonce) == 8 && isHex(nonce) &&
		len(versionBits) <= 8 && isHex(versionBits)
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// ntimeInRange reports whether a share's ntime is no earlier than its job's and at most 7000
// seconds later, ckpool's bounds (stratifier.c: "if (ntime32 < wb->ntime32 || ntime32 >
// wb->ntime32 + 7000)"). Both are 8 hex characters.
func ntimeInRange(ntime, jobNTime string) bool {
	got, err1 := strconv.ParseUint(ntime, 16, 32)
	base, err2 := strconv.ParseUint(jobNTime, 16, 32)
	return err1 == nil && err2 == nil && got >= base && got <= base+7000
}

func normalizeHex(s string, length int) string {
	// Remove any "0x" prefix
	s = strings.TrimPrefix(s, "0x")
	s = strings.TrimPrefix(s, "0X")

	// Hex is case-insensitive; lowercase so case-permuted duplicates of the
	// same proof-of-work collapse to one shareKey (prevents dup-share credit inflation).
	s = strings.ToLower(s)

	// If already correct length, return as-is
	if len(s) == length {
		return s
	}

	// If shorter, pad with leading zeros
	if len(s) < length {
		return strings.Repeat("0", length-len(s)) + s
	}

	// If longer, return as-is (validation will catch it)
	return s
}

func (s *Server) handleConfigure(client *Client, req *Request) *Response {
	// mining.configure for version rolling (AsicBoost) and other extensions
	// Request format: [["extension1", "extension2", ...], {"param1": value1, ...}]

	var params []json.RawMessage
	var supportsMultiVersion bool
	if err := json.Unmarshal(req.Params, &params); err == nil && len(params) >= 1 {
		var extensions []string
		if err := json.Unmarshal(params[0], &extensions); err == nil {
			// Check requested extensions
			for _, ext := range extensions {
				if ext == "version-rolling" {
					client.mu.Lock()
					client.SupportsVersionRolling = true
					client.VersionRollingMask = "1fffe000"
					client.mu.Unlock()
				}
				if ext == "multi_version" {
					supportsMultiVersion = true
				}
			}
		}

		// Parse extension parameters if provided
		if len(params) >= 2 {
			var extParams map[string]interface{}
			if err := json.Unmarshal(params[1], &extParams); err == nil {
				// Check for version-rolling.mask request
				if mask, ok := extParams["version-rolling.mask"].(string); ok {
					// Intersect with our supported mask
					client.mu.Lock()
					client.VersionRollingMask = intersectMasks("1fffe000", mask)
					client.mu.Unlock()
				}
			}
		}
	}

	client.mu.RLock()
	rental := client.RentalService
	mask := client.VersionRollingMask
	if mask == "" {
		mask = "1fffe000"
	}
	client.mu.RUnlock()

	if rental != RentalNone {
		s.clientLog(client, false, "Rental service configured",
			zap.String("ip", client.IP),
			zap.String("rental_service", rental.String()),
			zap.String("version_rolling_mask", mask))
	}

	// Return supported extensions
	// Use min-bit-count of 0 - we don't require any minimum bits
	result := map[string]interface{}{
		"version-rolling":               true,
		"version-rolling.mask":          mask,
		"version-rolling.min-bit-count": 0,
	}
	// Add multi_version support if requested (NiceHash compatibility)
	if supportsMultiVersion {
		result["multi_version"] = true
	}
	return &Response{ID: req.ID, Result: result}
}

// intersectMasks returns the intersection of two hex masks
// BitsToDifficulty converts a compact nBits target to a difficulty.
//
// Exported and shared with cmd/stratum so the intake limiter here and the block-candidate
// test there cannot disagree about what "solves a block" means.
func BitsToDifficulty(bitsHex string) float64 {
	bits, err := strconv.ParseUint(bitsHex, 16, 32)
	if err != nil || bits == 0 {
		return 0
	}
	// int, NOT the uint the parse produces: the exponent is compared against 29, and for
	// any target EASIER than difficulty 1 (exp > 29) unsigned arithmetic wraps 29-exp to a
	// huge positive number and the result is +Inf. Caught by running the real thing against
	// a regtest node, whose 207fffff target made the pool believe the network difficulty
	// was infinite -- which silently disables block submission, because a share can never
	// compare >= +Inf.
	exp := int(bits >> 24)
	mantissa := bits & 0xFFFFFF
	if mantissa == 0 {
		return 0
	}
	// diff1 target exponent = 0x1d (29)
	return (float64(0xFFFF) / float64(mantissa)) * math.Pow(256, float64(29-exp))
}

// intersectMasks returns the version-rolling bits both sides support.
//
// fmt.Sscanf("%x") stops at the first character it cannot consume and reports success, so a
// "0x1fffe000" mask -- which firmware does send -- parsed as 0 and the pool answered
// version-rolling with the mask "00000000". Depending on the firmware that is either a
// silent loss of AsicBoost or 100% rejects, and neither is distinguishable from a pool
// fault at the miner. Parse strictly, and treat an unparseable client mask as "no opinion"
// (use ours) rather than as zero.
func intersectMasks(mask1, mask2 string) string {
	m1, ok1 := parseVersionMask(mask1)
	m2, ok2 := parseVersionMask(mask2)
	switch {
	case !ok1 && !ok2:
		return "00000000"
	case !ok1:
		return fmt.Sprintf("%08x", m2)
	case !ok2:
		return fmt.Sprintf("%08x", m1)
	}
	return fmt.Sprintf("%08x", m1&m2)
}

// parseVersionMask accepts a 32-bit hex mask, with or without an "0x" prefix.
func parseVersionMask(mask string) (uint32, bool) {
	m := strings.TrimSpace(mask)
	m = strings.TrimPrefix(strings.TrimPrefix(m, "0X"), "0x")
	if m == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(m, 16, 32)
	if err != nil {
		return 0, false
	}
	return uint32(v), true
}

// NewServerForTest builds a bare Server with just enough state for the rented-hashpower
// counters to be exercised. Test-only helper; not used by the running app.
func NewServerForTest() *Server {
	return &Server{stats: &serverCounters{}}
}

// AddAuthorizedRentalClientForTest registers one authorized client attributed to the given
// marketplace, so GetRentalStats has something to count.
//
// It sets DetectedMarketplace and deliberately leaves RentalService at RentalNone, which is
// exactly the shape a solo install produces: identity recorded, difficulty policy withheld.
// Setting RentalService here instead would make the counters look right for the one case
// that never occurs in this app. Test-only helper.
func (s *Server) AddAuthorizedRentalClientForTest(svc RentalService) {
	c := &Client{Authorized: true, DetectedMarketplace: svc}
	s.clients.Store(fmt.Sprintf("test-%d-%p", svc, c), c)
}

// randomUint32 is a random 32-bit value; zero if the system's random source fails, which only
// costs the collision avoidance it exists for.
func randomUint32() uint32 {
	var b [4]byte
	if _, err := crand.Read(b[:]); err != nil {
		return 0
	}
	return binary.BigEndian.Uint32(b[:])
}

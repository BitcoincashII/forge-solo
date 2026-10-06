// Package tidesgw is Forge Solo's TIDES mode: this install as a DATUM gateway to Forge Pool.
//
// In TIDES mode every job still comes from the local node's own block template, but its coinbase
// pays Forge Pool's TIDES split of the DATUM share log instead of this install's own address, and
// the pool has checked and registered it before any miner sees it. The shares the miners find at
// or above the pool's share difficulty are sent on and credited to the payout address in that
// share log, so whichever gateway finds a block, it pays every DATUM miner with work in the
// window -- this one included. The pool keeps no wallet and takes no fee, and TIDES mode is BCH2
// only: no 1175 merge-mining.
//
// When the pool cannot be reached, or will not take a job, the stratum mines solo meanwhile; this
// package records which it is doing so the dashboard can say.
package tidesgw

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/cashaddr"
	"github.com/BitcoincashII/forge-solo/internal/datum/gateway"
	"github.com/BitcoincashII/forge-solo/internal/datum/wire"
	"github.com/BitcoincashII/forge-solo/internal/mining"
	"github.com/BitcoincashII/forge-solo/internal/stratum"
	"go.uber.org/zap"
)

// DefaultPoolURL is Forge Pool, whose DATUM intake takes Forge Solo gateways.
const DefaultPoolURL = "https://pool.bch2.org"

// State is what the gateway's miners are doing.
type State string

const (
	StateStarting State = "starting" // TIDES was chosen and no job has been registered yet
	StateActive   State = "active"   // miners are on a job the pool registered
	StateFallback State = "fallback" // the pool is unreachable or refused: mining solo meanwhile
)

// Registration is one job the pool took.
type Registration struct {
	PoolJobID    string
	ShareDiff    float64
	Height       int64
	PrevHash     string // RPC byte order
	Coinb1       string
	Coinb2       string
	Txs          []mining.TxData // the transactions registered, block order
	CoinbaseSats int64
	FinderSats   int64 // what the coinbase pays this install's own payout address
	Outputs      int
	Snapshot     int64
	At           time.Time
	Bits         string  // the block's target (compact), which caps the share difficulty committed to
	Committed    float64 // the share difficulty the coinbase commits to, 0 when it commits to none
}

// Config tunes a Gateway. Zero values take the defaults.
type Config struct {
	PoolURL string
	Key     ed25519.PrivateKey
	Logger  *zap.Logger
	// RequestTimeout bounds one HTTP request. Registration runs on the stratum's job loop, so
	// a pool that swallows packets must not hold new work back for the client's 20 s default.
	RequestTimeout time.Duration
	// RegisterFor bounds a whole registration, the pool's "retry shortly" answers included --
	// they come while its node catches up with a block this node has already seen.
	RegisterFor time.Duration
	// RetryEvery is how often an install that fell back to solo tries the pool again.
	RetryEvery time.Duration
	// FlushEvery is how often queued shares go to the pool; a block goes at once.
	FlushEvery time.Duration
	// PoolOnly says miners are turned away rather than mining solo while the pool cannot be
	// reached (the gateway program's pool_only); it only changes what Fallback says.
	PoolOnly bool
	// CreditTo, when set, is the address every share is credited to at the pool, read as each
	// share is queued (Forge Solo: the one payout address, which the dashboard can change while
	// miners stay connected). Nil, or "", credits each share to the address its miner logged
	// in with (the gateway program, which may serve several people).
	CreditTo func() string
	// MaxDifficulty, when set, is the highest difficulty a miner of this gateway works at. Every
	// job then commits its coinbase to a share difficulty above it (wire.CommitShareDiff), and the
	// pool credits each share at least that: without it the pool credits its own share difficulty,
	// which a busy miner's shares far exceed (a 500k rental was credited 1024 a share).
	MaxDifficulty func() float64
	Now           func() time.Time
}

func (c *Config) defaults() {
	if c.PoolURL == "" {
		c.PoolURL = DefaultPoolURL
	}
	if c.Logger == nil {
		c.Logger = zap.NewNop()
	}
	if c.RequestTimeout <= 0 {
		c.RequestTimeout = 5 * time.Second
	}
	if c.RegisterFor <= 0 {
		c.RegisterFor = 6 * time.Second
	}
	if c.RetryEvery <= 0 {
		c.RetryEvery = time.Minute
	}
	if c.FlushEvery <= 0 {
		c.FlushEvery = 2 * time.Second
	}
	if c.Now == nil {
		c.Now = time.Now
	}
}

// tracked is a registered job the stratum handed out, under its local job ID.
type tracked struct {
	reg     *Registration
	version string // the job's block version (hex), to send a rolled version in full
}

type queued struct {
	share    wire.Share
	prevHash string
	block    bool
	at       time.Time
}

// Gateway is the TIDES gateway. The stratum's job loop calls Register/Track/Fallback; its share
// processor calls Forward; Run sends the shares.
type Gateway struct {
	cfg    Config
	client *gateway.Client
	logger *zap.Logger

	mu        sync.Mutex
	snap      *wire.Snapshot
	snapAt    time.Time // when snap was fetched, by this clock
	jobs      map[string]*tracked
	order     []string
	state     State
	reason    string
	since     time.Time
	lastTry   time.Time
	lastOK    time.Time
	shareDiff float64
	tip       string // prevhash the newest registered job builds on
	queue     []queued
	counts    Counts
	lastWarn  time.Time
	wake      chan struct{}

	// acceptedBy is the address the pool last credited one of this install's shares to, and
	// firstAccepted when it first did (notInWindow).
	acceptedBy    string
	firstAccepted time.Time
	warnedWindow  bool
}

// Counts are the gateway's running totals since start.
type Counts struct {
	Registered int64  `json:"jobs_registered"`
	Forwarded  int64  `json:"shares_forwarded"`
	Accepted   int64  `json:"shares_accepted"`
	Rejected   int64  `json:"shares_rejected"`
	Dropped    int64  `json:"shares_dropped"` // stale before they could be sent
	Blocks     int64  `json:"blocks_found"`
	LastReject string `json:"last_reject,omitempty"`
}

// maxTracked bounds the local-job map: the pool keeps 64 registrations per gateway, and a share
// for anything older is refused there anyway.
const maxTracked = 128

// maxQueue bounds shares waiting for the pool. At the pool's ~12 shares a minute this is hours;
// the bound only matters if the pool answers nothing for a long time.
const maxQueue = 20000

// snapFresh is how old a snapshot may be before a registration fetches a newer one. The pool
// publishes one every 30 s and at each block; it takes a job paying any of the last 10 minutes'.
const snapFresh = 25 * time.Second

// snapUsable is how old a cached snapshot may be when the pool cannot be asked for a newer one:
// comfortably inside the pool's 10 minutes, since this clock and the pool's differ.
const snapUsable = 7 * time.Minute

// New builds a gateway for the pool at cfg.PoolURL.
func New(cfg Config) *Gateway {
	cfg.defaults()
	c := gateway.New(cfg.PoolURL, cfg.Key)
	c.HTTP = &http.Client{Timeout: cfg.RequestTimeout, CheckRedirect: gateway.NoRedirects}
	c.Now = cfg.Now
	return &Gateway{cfg: cfg, client: c, logger: cfg.Logger, jobs: map[string]*tracked{},
		state: StateStarting, since: cfg.Now(), wake: make(chan struct{}, 1)}
}

// KeyFromSeed turns a stored hex seed into the gateway's key.
func KeyFromSeed(seedHex string) (ed25519.PrivateKey, error) {
	seed, err := hex.DecodeString(strings.TrimSpace(seedHex))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("the stored gateway key is not a 32-byte hex seed")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// ID is the gateway's public key, as the pool knows it.
func (g *Gateway) ID() string { return g.client.ID() }

// CanonicalAddress is addr in the one spelling the pool keys its share log by: prefixed, lower
// case. The pool canonicalises too, but a coinbase output compared by script and a share credited
// by string should never depend on how the dashboard happened to store the address.
func CanonicalAddress(addr string) (string, error) {
	a, err := cashaddr.Decode(addr, cashaddr.MainnetPrefix)
	if err != nil {
		return "", err
	}
	return cashaddr.Encode(cashaddr.MainnetPrefix, a.Type, a.Hash), nil
}

// Due reports whether the job loop should try the pool now. Active and starting gateways try
// with every job the loop makes. A gateway that fell back to solo tries at most once per
// RetryEvery, and never on a new block: a new block needs work at once, and a pool that did not
// answer a minute ago should not hold it back.
func (g *Gateway) Due(newBlock bool) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state != StateFallback {
		return true
	}
	return !newBlock && g.cfg.Now().Sub(g.lastTry) >= g.cfg.RetryEvery
}

func (g *Gateway) snapshot(ctx context.Context, height int64) (*wire.Snapshot, error) {
	now := g.cfg.Now()
	g.mu.Lock()
	cached, at := g.snap, g.snapAt
	g.mu.Unlock()
	if cached != nil && cached.Height == height && now.Sub(at) < snapFresh {
		return cached, nil
	}
	s, err := g.client.SnapshotCtx(ctx)
	if err != nil {
		if cached != nil && now.Sub(at) < snapUsable {
			return cached, nil
		}
		return nil, fmt.Errorf("the pool's TIDES snapshot: %w", err)
	}
	g.mu.Lock()
	g.snap, g.snapAt = s, now
	g.mu.Unlock()
	return s, nil
}

// retryPace is the wait between registration attempts the pool asked to retry: its rate limit
// admits 2 a second.
const retryPace = 500 * time.Millisecond

// rateLimited reports the pool's "429 Too Many Requests" (the client's errors carry the status).
func rateLimited(err error) bool {
	return err != nil && strings.Contains(err.Error(), " 429 ")
}

// briefReason is err as a reason for the dashboard: one line, at most maxReason characters, and
// without the body of an HTML error page. The client puts up to 8 MB of a failed response into its
// error, and a proxy's "502 Bad Gateway" page appeared whole in the banner, resent on every poll.
func briefReason(err error) string {
	s := err.Error()
	if i := strings.Index(s, "<"); i > 0 {
		s = strings.TrimRight(strings.TrimSpace(s[:i]), ":")
	}
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxReason {
		s = string(r[:maxReason]) + "…"
	}
	return s
}

const maxReason = 200

// refusedTx reports a registration the pool refused over this node's transactions: one its node
// will not take (a conflicting spend, most likely), or an order or parent it disagrees with.
func refusedTx(err error) bool {
	s := err.Error()
	return strings.Contains(s, "refuses transaction") || strings.Contains(s, "out of the node's order") ||
		strings.Contains(s, "which the template leaves out")
}

// Register turns the node's template into a job whose coinbase pays the pool's TIDES split (or
// everything to finder while the pool's DATUM share log is empty) and registers it. finder is
// this install's payout address; tag goes in the coinbase.
//
// If the pool refuses the template's transactions it registers the block without any: that
// costs the block's fees, where giving up would cost TIDES until the conflict clears.
func (g *Gateway) Register(t *mining.BlockTemplate, finder string, tag []byte) (*Registration, error) {
	return g.RegisterCtx(context.Background(), t, finder, tag)
}

// RegisterCtx is Register, given up when parent ends: the stratum gives up a registration whose
// job miners will never get, once a new block has come.
func (g *Gateway) RegisterCtx(parent context.Context, t *mining.BlockTemplate, finder string, tag []byte) (*Registration, error) {
	g.mu.Lock()
	g.lastTry = g.cfg.Now()
	g.mu.Unlock()

	finder, err := CanonicalAddress(finder)
	if err != nil {
		return nil, fmt.Errorf("payout address: %w", err)
	}
	// One deadline for all of it, the fallbacks below included, and every request to the pool
	// ends with it. Registration runs on the stratum's job loop, where a new block's work waits for
	// it: each fallback used to get a deadline of its own and each request its own timeout, so a
	// slow pool held new-block work for 10 seconds and a hostile one for 40, while the miners
	// hashed the old tip.
	deadline := g.cfg.Now().Add(g.cfg.RegisterFor)
	ctx, cancel := context.WithTimeout(parent, g.cfg.RegisterFor)
	defer cancel()
	reg, err := g.register(ctx, t, finder, tag, t.Transactions, deadline)
	if err != nil && strings.Contains(err.Error(), "unknown or expired snapshot") {
		// The pool restarted (its snapshot versions start over) or this one aged out: fetch
		// its current snapshot rather than fall back to solo over a stale cache.
		g.mu.Lock()
		g.snap = nil
		g.mu.Unlock()
		reg, err = g.register(ctx, t, finder, tag, t.Transactions, deadline)
	}
	if err != nil && len(t.Transactions) > 0 && (refusedTx(err) || errors.Is(err, errBlockTooBig)) {
		g.logger.Warn("TIDES: registering the block without this node's transactions",
			zap.Int64("height", t.Height), zap.Error(err))
		reg, err = g.register(ctx, t, finder, tag, nil, deadline)
	}
	if err != nil {
		return nil, err
	}
	g.mu.Lock()
	g.lastOK = g.cfg.Now()
	g.shareDiff = reg.ShareDiff
	g.counts.Registered++
	if missing := g.notInWindow(); missing && !g.warnedWindow {
		g.logger.Warn("TIDES: Forge Pool's window lists none of this install's work, though the pool credits its shares: "+
			"blocks found in TIDES mode pay the others in the window only. See the pool's TIDES page, or choose solo.",
			zap.String("credited_to", g.acceptedBy), zap.Int64("shares_accepted", g.counts.Accepted))
		g.warnedWindow = true
	} else if !missing {
		g.warnedWindow = false
	}
	g.mu.Unlock()
	return reg, nil
}

// windowGrace is how long after the pool first credits this install's shares its TIDES window may
// still leave them out: the pool takes its snapshot from time to time, not at every share, and its
// clock and this one may differ.
const windowGrace = 10 * time.Minute

// notInWindow reports the pool crediting this install's shares while its window -- as of a
// snapshot taken windowGrace after the first of them -- holds none of this install's work. Nothing
// else would show it: every block mined in TIDES mode then pays the others in the window only, and
// the registration takes the pool's split as it comes. g.mu is held.
func (g *Gateway) notInWindow() bool {
	if g.snap == nil || g.acceptedBy == "" || len(g.snap.Work) == 0 || g.snap.At.Sub(g.firstAccepted) < windowGrace {
		return false
	}
	for addr, work := range g.snap.Work {
		if canon, err := CanonicalAddress(addr); err == nil && canon == g.acceptedBy && work > 0 {
			return false
		}
	}
	return true
}

// maxCoinbaseOutputs is the most outputs a TIDES coinbase built here may have. Nothing bounded the
// pool's split: a snapshot of a million entries built a 34 MB coinbase (an invalid block) and held
// a gigabyte of memory, and every job the stratum keeps carries its coinbase. OCEAN's gateway takes
// at most 512 outputs from its pool; a split larger than this is refused, and the miners mine solo.
const maxCoinbaseOutputs = 2000

// maxBlockBytes is the BCH2 block size limit as far as a block built here may rely on it: the
// node's ABLA limit never goes below 32,000,000 bytes (abla.cpp: epsilon0 + beta0, each half the
// 32 MB default, are floors), and its block assembler fills a template to that size less 4,000
// bytes kept for a coinbase (miner.cpp). A TIDES coinbase paying many miners is larger than that, so
// on a full template it would have made the block too big. A variable so a test can reach it.
var maxBlockBytes = 32_000_000

// errBlockTooBig is a registration whose coinbase and transactions would not fit in a block.
var errBlockTooBig = errors.New("the coinbase and the transactions would exceed the block size limit")

func (g *Gateway) register(ctx context.Context, t *mining.BlockTemplate, finder string, tag []byte, txs []mining.TxData, deadline time.Time) (*Registration, error) {
	value := t.CoinbaseValue
	if len(txs) < len(t.Transactions) {
		// Only the subsidy and these transactions' fees are earned without the rest.
		value = t.CoinbaseValue
		for _, tx := range t.Transactions {
			value -= tx.Fee
		}
		for _, tx := range txs {
			value += tx.Fee
		}
	}
	snap, err := g.snapshot(ctx, t.Height)
	if err != nil {
		return nil, err
	}
	outs, err := wire.Payouts(snap, value, finder)
	if err != nil {
		return nil, err
	}
	if len(outs) > maxCoinbaseOutputs {
		return nil, fmt.Errorf("the pool's TIDES split has %d outputs, more than the %d a coinbase here may carry", len(outs), maxCoinbaseOutputs)
	}
	gt := &gateway.Template{Height: t.Height, PrevHash: t.PreviousBlockHash, Version: uint32(t.Version),
		Bits: t.Bits, CurTime: uint32(t.CurTime), CoinbaseValue: value}
	for _, tx := range txs {
		gt.Txs = append(gt.Txs, gateway.TemplateTx{TxID: tx.TxID, Data: tx.Data})
	}
	var exp *int
	var committed float64
	if e, ok := g.commitExp(t.Bits); ok {
		exp, tag, committed = &e, wire.CommitShareDiff(tag, e), math.Ldexp(1, e)
	}
	req, err := gateway.BuildJob(snap, gt, finder, tag)
	if err != nil {
		return nil, err
	}
	req.ShareDiffExp = exp
	size := 80 + 9 + (len(req.Coinb1)+len(req.Coinb2))/2 + mining.CoinbaseExtranonceReserve
	for _, tx := range txs {
		size += len(tx.Data) / 2
	}
	if size > maxBlockBytes {
		return nil, errBlockTooBig
	}

	// The pool answers "retry" while its node has not yet seen the block this one is building
	// on, or has just taken one of this node's transactions; that usually clears in well under
	// a second. Keep asking until the deadline, never past it.
	var resp *wire.JobResponse
	for {
		// Two tries per call: when the pool asks for a transaction's data, the client adds it
		// and sends it on the second try at once. A "retry" answer waits retryPace before the
		// second try and again before the next call. The pool admits 2 registrations a second
		// (burst 10), and asking faster (no wait, then 250 ms) turned a few seconds of "retry"
		// into "429 slow down", which ended the registration and, on a new block, put the miners
		// on solo for a minute. A 429 itself is waited out while the deadline allows.
		resp, err = g.client.RegisterCtx(ctx, req, gt, 2, retryPace)
		if err == nil {
			break
		}
		wait, retryable := retryPace, resp != nil && resp.Retry
		if resp == nil && rateLimited(err) {
			wait, retryable = time.Second, true
		}
		if !retryable || !g.cfg.Now().Add(wait).Before(deadline) {
			if resp != nil && resp.Error != "" {
				return nil, errors.New(resp.Error)
			}
			return nil, err
		}
		select {
		case <-time.After(wait): // ends before the deadline
		case <-ctx.Done(): // given up
			return nil, ctx.Err()
		}
	}

	var mine int64
	for _, o := range outs {
		if canon, cerr := CanonicalAddress(o.Address); cerr == nil && canon == finder {
			mine += o.Sats
		}
	}
	return &Registration{PoolJobID: resp.JobID, ShareDiff: resp.ShareDifficulty, Height: t.Height,
		PrevHash: t.PreviousBlockHash, Coinb1: req.Coinb1, Coinb2: req.Coinb2, Txs: txs, CoinbaseSats: value,
		FinderSats: mine, Outputs: len(outs), Snapshot: snap.Version, At: g.cfg.Now(), Bits: t.Bits,
		Committed: committed}, nil
}

// commitExp is the share difficulty exponent a job on a block of target bits commits to now: the
// power of two at or above twice the highest difficulty a miner works at (Config.MaxDifficulty),
// never above the network difficulty's. The pool refuses a share below the job's difficulty before
// it looks for a block, so a block whose difficulty fell between the network's and 2^e would have
// been refused there and never recorded. ok is false when there is nothing to commit to.
func (g *Gateway) commitExp(bits string) (int, bool) {
	if g.cfg.MaxDifficulty == nil {
		return 0, false
	}
	e, ok := gateway.ShareDiffExp(g.cfg.MaxDifficulty())
	if !ok {
		return 0, false
	}
	if nd := stratum.BitsToDifficulty(bits); nd > 0 {
		if top := int(math.Floor(math.Log2(nd))); e > top {
			e = top
		}
	}
	return e, true
}

// Undercommitted reports whether local job localID credits its shares at less than its miners now
// work at: the share difficulty a job registered now would commit to is above both the one this
// job commits to and the pool's for it. A miner that logs in after a job was registered, above the
// miners it was registered for, is credited the pool's own difficulty for each share on it until
// the next job: a rental connecting at 500000 was credited 1024 a share, 0.2% of its work. Both
// sides are capped at the network difficulty, as Register caps them, so a job already committed as
// high as the network allows is never undercommitted, and a miner given more than that does not
// ask for a new job each time it logs in.
func (g *Gateway) Undercommitted(localID string) bool {
	g.mu.Lock()
	t := g.jobs[localID]
	g.mu.Unlock()
	if t == nil {
		return false
	}
	e, ok := g.commitExp(t.reg.Bits)
	if !ok {
		return false
	}
	want := math.Ldexp(1, e)
	return t.reg.Committed < want && t.reg.ShareDiff < want
}

// Track records that the stratum handed out reg as local job localID: miners are on TIDES work.
// version is the job's block version in hex.
func (g *Gateway) Track(localID string, reg *Registration, version string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.jobs[localID] = &tracked{reg: reg, version: version}
	g.order = append(g.order, localID)
	g.shareDiff = reg.ShareDiff // what the pool credits a share on the work miners now have
	for len(g.order) > maxTracked {
		delete(g.jobs, g.order[0])
		g.order = g.order[1:]
	}
	if g.state != StateActive {
		g.logger.Info("🌊 TIDES: mining for the Forge Pool TIDES window",
			zap.Int64("height", reg.Height), zap.String("pool_job", reg.PoolJobID),
			zap.Float64("share_difficulty", reg.ShareDiff), zap.Int("coinbase_outputs", reg.Outputs))
	}
	g.state, g.reason, g.since = StateActive, "", g.cfg.Now()
	g.tip = reg.PrevHash
}

// Fallback records that the stratum handed out a solo job because of err.
func (g *Gateway) Fallback(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	why := "the pool did not answer"
	if err != nil {
		why = briefReason(err)
	}
	if g.state != StateFallback {
		if g.cfg.PoolOnly {
			g.logger.Warn("⚠️  TIDES: Forge Pool unavailable — miners are turned away until it is back (pool_only)",
				zap.String("reason", why))
		} else {
			g.logger.Warn("⚠️  TIDES: Forge Pool unavailable — mining SOLO until it is back (blocks found meanwhile pay your own address in full)",
				zap.String("reason", why))
		}
		g.since = g.cfg.Now()
	}
	g.state, g.reason = StateFallback, why
}

// Note records a refresh that failed while miners stay on a job the pool still holds.
func (g *Gateway) Note(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reason = "last refresh failed: " + briefReason(err)
	if g.cfg.Now().Sub(g.lastWarn) >= time.Minute {
		g.lastWarn = g.cfg.Now()
		g.logger.Warn("TIDES: could not refresh the job with the pool; miners stay on the current one", zap.Error(err))
	}
}

// Reset starts the gateway over, as when TIDES has just been chosen: the next job loop tries the
// pool at once.
func (g *Gateway) Reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.state, g.reason, g.since = StateStarting, "", g.cfg.Now()
	g.lastTry = time.Time{}
}

// KeepFor is how long miners may stay on a job the pool registered while refreshing it keeps
// failing. The pool holds the job until the tip moves, so one missed refresh is not worth leaving
// TIDES over -- but a pool that is really down credits none of that work, and an install that
// chose TIDES mines solo meanwhile, so this is two missed refreshes.
const KeepFor = 45 * time.Second

// Keep reports whether miners may stay on local job localID after a failed refresh: it is a
// TIDES job, registered less than KeepFor ago.
func (g *Gateway) Keep(localID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	t := g.jobs[localID]
	return t != nil && g.cfg.Now().Sub(t.reg.At) < KeepFor
}

// Registered returns the registration behind local job localID, or nil.
func (g *Gateway) Registered(localID string) *Registration {
	g.mu.Lock()
	defer g.mu.Unlock()
	if t := g.jobs[localID]; t != nil {
		return t.reg
	}
	return nil
}

// Forward queues a share found on local job localID for the pool if that job is a TIDES job and
// the share reaches the pool's difficulty for it. It reports whether the share was queued. A
// block is not queued: call SendBlock for it.
func (g *Gateway) Forward(localID string, sh *stratum.Share) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	t := g.jobs[localID]
	if t == nil || sh.ActualDiff < t.reg.ShareDiff {
		return false
	}
	ws, ok := g.wireShare(t, sh)
	if !ok {
		return false
	}
	if len(g.queue) >= maxQueue {
		g.queue = g.queue[1:]
		g.counts.Dropped++
	}
	g.queue = append(g.queue, queued{share: ws, prevHash: t.reg.PrevHash, at: g.cfg.Now()})
	g.counts.Forwarded++
	return true
}

// wireShare is sh as the pool takes it. g.mu is held.
func (g *Gateway) wireShare(t *tracked, sh *stratum.Share) (wire.Share, bool) {
	en := strings.ToLower(sh.ExtraNonce1 + sh.ExtraNonce2)
	if len(en) != 2*wire.ExtranonceSize {
		return wire.Share{}, false
	}
	miner := sh.MinerID
	if g.cfg.CreditTo != nil {
		if a := g.cfg.CreditTo(); a != "" {
			miner = a
		}
	}
	ws := wire.Share{JobID: t.reg.PoolJobID, Miner: miner, Worker: sh.WorkerName, Extranonce: en,
		NTime: strings.ToLower(sh.NTime), Nonce: strings.ToLower(sh.Nonce)}
	// The miner may send only some of the version bits, or fewer than eight hex digits; the pool
	// wants the whole rolled version, which gives the same header there.
	if sh.VersionBits != "" {
		if v := stratum.RollVersion(t.version, sh.VersionBits); len(v) == 4 {
			ws.Version = hex.EncodeToString(v)
		}
	}
	if canon, err := CanonicalAddress(ws.Miner); err == nil {
		ws.Miner = canon
	}
	return ws, true
}

// SendBlock sends a share that solves a block to the pool at once, ahead of anything queued,
// and waits for the answer. Call it BEFORE submitting the block to the local node: the pool
// judges a share against its own node's tip, so once this node's block reached the pool's node
// by the peer-to-peer network, the share would be stale there and the pool would never record
// the block in its TIDES ledger. The pool submits the block to its own node as well.
//
// Shares still queued on the same tip go in the same request, after the block: the pool judges a
// whole batch against the template it held when the request arrived, so they are credited too,
// where on their own they would reach the pool after the block had moved its tip.
func (g *Gateway) SendBlock(localID string, sh *stratum.Share) error {
	g.mu.Lock()
	t := g.jobs[localID]
	if t == nil {
		g.mu.Unlock()
		return errors.New("not a TIDES job")
	}
	ws, ok := g.wireShare(t, sh)
	if !ok {
		g.mu.Unlock()
		return errors.New("the share's extranonce is not the DATUM coinbase's")
	}
	g.counts.Forwarded++
	blk := queued{share: ws, prevHash: t.reg.PrevHash, block: true, at: g.cfg.Now()}
	sent := []queued{blk}
	keep := g.queue[:0]
	for _, q := range g.queue {
		if len(sent) < wire.MaxBatch && q.prevHash == t.reg.PrevHash {
			sent = append(sent, q)
		} else {
			keep = append(keep, q)
		}
	}
	g.queue = keep
	g.mu.Unlock()

	shares := make([]wire.Share, len(sent))
	for i, q := range sent {
		shares[i] = q.share
	}
	resp, err := g.client.SubmitShares(shares)

	g.mu.Lock()
	defer g.mu.Unlock()
	if err != nil || len(resp.Results) != len(sent) {
		// Put them all back, the block first: the pool may just have been slow, and a late
		// block share still earns the window credit if the pool has not moved on.
		g.queue = append(sent, g.queue...)
		if err == nil {
			err = fmt.Errorf("the pool answered %d results for %d shares", len(resp.Results), len(sent))
		}
		return err
	}
	g.queue = append(g.tally(sent, resp), g.queue...)
	if r := resp.Results[0]; !r.Accepted {
		return fmt.Errorf("the pool refused the block share: %s", r.Error)
	}
	return nil
}

// tally counts the pool's verdicts on sent and returns the shares it asked to be resent. g.mu is
// held.
func (g *Gateway) tally(sent []queued, resp *wire.ShareBatchResponse) []queued {
	var resend []queued
	for i, r := range resp.Results {
		switch {
		case r.Accepted:
			g.counts.Accepted++
			if miner := sent[i].share.Miner; miner != g.acceptedBy {
				g.acceptedBy, g.firstAccepted = miner, g.cfg.Now()
			}
			if r.Block {
				g.counts.Blocks++
			}
		case strings.Contains(r.Error, "resend"):
			resend = append(resend, sent[i])
		default:
			g.counts.Rejected++
			g.counts.LastReject = briefReason(errors.New(r.Error)) // the pool's text, one short line
		}
	}
	// resp.ShareDifficulty is the pool's own difficulty for this gateway, which the next job
	// starts from; the status shows what the current job is credited at (Register, Track).
	return resend
}

// Run sends queued shares every FlushEvery until stop closes.
func (g *Gateway) Run(stop <-chan struct{}) {
	tick := time.NewTicker(g.cfg.FlushEvery)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
		case <-g.wake:
		}
		g.Flush()
	}
}

// Flush sends what is queued, up to one batch, and reports how many shares went.
func (g *Gateway) Flush() int {
	g.mu.Lock()
	now := g.cfg.Now()
	var batch []queued
	keep := g.queue[:0]
	for _, q := range g.queue {
		// A share on a block the pool has moved past is refused there; do not send it.
		// A block share always goes: the pool may still be on its tip.
		if !q.block && (q.prevHash != g.tip || now.Sub(q.at) > 10*time.Minute) {
			g.counts.Dropped++
			continue
		}
		if len(batch) < wire.MaxBatch {
			batch = append(batch, q)
		} else {
			keep = append(keep, q)
		}
	}
	g.queue = keep
	g.mu.Unlock()
	if len(batch) == 0 {
		return 0
	}

	shares := make([]wire.Share, len(batch))
	for i, q := range batch {
		shares[i] = q.share
	}
	resp, err := g.client.SubmitShares(shares)

	g.mu.Lock()
	defer g.mu.Unlock()
	if err != nil || len(resp.Results) != len(batch) {
		// Put them back in front, in order; they are retried on the next flush.
		g.queue = append(append([]queued{}, batch...), g.queue...)
		if len(g.queue) > maxQueue {
			g.counts.Dropped += int64(len(g.queue) - maxQueue)
			g.queue = g.queue[len(g.queue)-maxQueue:]
		}
		if now.Sub(g.lastWarn) >= time.Minute {
			g.lastWarn = now
			if err == nil {
				err = fmt.Errorf("the pool answered %d results for %d shares", len(resp.Results), len(batch))
			}
			g.logger.Warn("TIDES: shares not delivered to the pool yet; will retry", zap.Int("shares", len(batch)), zap.Error(err))
		}
		return 0
	}
	g.queue = append(g.tally(batch, resp), g.queue...)
	return len(batch)
}

// Status is the gateway as the dashboard shows it.
type Status struct {
	State           State   `json:"state"`
	Reason          string  `json:"reason,omitempty"`
	SinceSec        int64   `json:"since_sec"`
	Pool            string  `json:"pool"`
	Gateway         string  `json:"gateway"`
	ShareDifficulty float64 `json:"share_difficulty"`
	LastRegistered  int64   `json:"last_registered_age_sec"` // -1 before the first
	Snapshot        int64   `json:"snapshot"`
	WindowMiners    int     `json:"window_miners"`
	Queued          int     `json:"shares_queued"`
	// NotInWindow: the pool credits this install's shares, but its window holds none of its work.
	NotInWindow bool `json:"not_in_window,omitempty"`
	Counts
}

// Status reports the gateway's state.
func (g *Gateway) Status() Status {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.cfg.Now()
	st := Status{State: g.state, Reason: g.reason, SinceSec: int64(now.Sub(g.since).Seconds()), Pool: g.cfg.PoolURL,
		Gateway: g.client.ID(), ShareDifficulty: g.shareDiff, LastRegistered: -1, Queued: len(g.queue), Counts: g.counts}
	if !g.lastOK.IsZero() {
		st.LastRegistered = int64(now.Sub(g.lastOK).Seconds())
	}
	if g.snap != nil {
		st.Snapshot = g.snap.Version
		st.WindowMiners = len(g.snap.Work)
		st.NotInWindow = g.notInWindow()
	}
	return st
}

// Wake asks Run to flush now.
func (g *Gateway) Wake() {
	select {
	case g.wake <- struct{}{}:
	default:
	}
}

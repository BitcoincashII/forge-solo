// Package tidesgw is Forge Solo's TIDES mode: this install as a DATUM gateway to Forge Pool.
//
// In TIDES mode every job still comes from the local node's own block template, but its coinbase
// pays Forge Pool's TIDES split of the DATUM share log instead of this install's own address, and
// the pool has checked and registered it before any miner sees it. The shares the miners find at
// or above the pool's share difficulty are sent on and credited to the payout address in that
// share log, so whichever gateway finds a block, it pays every DATUM miner with work in the
// window -- this one included. The pool keeps no wallet and takes no fee (owner decisions
// 2026-09-29), and TIDES mode is BCH2 only: no 1175 merge-mining (owner decision 2026-09-30).
//
// When the pool cannot be reached, or will not take a job, the stratum mines solo meanwhile
// (owner decision 2026-09-30); this package records which it is doing so the dashboard can say.
package tidesgw

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
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
	Now        func() time.Time
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
	c.HTTP = &http.Client{Timeout: cfg.RequestTimeout}
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

func (g *Gateway) snapshot(height int64) (*wire.Snapshot, error) {
	now := g.cfg.Now()
	g.mu.Lock()
	cached, at := g.snap, g.snapAt
	g.mu.Unlock()
	if cached != nil && cached.Height == height && now.Sub(at) < snapFresh {
		return cached, nil
	}
	s, err := g.client.Snapshot()
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
	g.mu.Lock()
	g.lastTry = g.cfg.Now()
	g.mu.Unlock()

	finder, err := CanonicalAddress(finder)
	if err != nil {
		return nil, fmt.Errorf("payout address: %w", err)
	}
	deadline := g.cfg.Now().Add(g.cfg.RegisterFor)
	reg, err := g.register(t, finder, tag, t.Transactions, deadline)
	if err != nil && strings.Contains(err.Error(), "unknown or expired snapshot") {
		// The pool restarted (its snapshot versions start over) or this one aged out: fetch
		// its current snapshot rather than fall back to solo over a stale cache.
		g.mu.Lock()
		g.snap = nil
		g.mu.Unlock()
		reg, err = g.register(t, finder, tag, t.Transactions, deadline.Add(g.cfg.RegisterFor))
	}
	if err != nil && len(t.Transactions) > 0 && refusedTx(err) {
		g.logger.Warn("TIDES: the pool refused this node's transactions; registering the block without them",
			zap.Int64("height", t.Height), zap.Error(err))
		reg, err = g.register(t, finder, tag, nil, deadline.Add(g.cfg.RegisterFor))
	}
	if err != nil {
		return nil, err
	}
	g.mu.Lock()
	g.lastOK = g.cfg.Now()
	g.shareDiff = reg.ShareDiff
	g.counts.Registered++
	g.mu.Unlock()
	return reg, nil
}

func (g *Gateway) register(t *mining.BlockTemplate, finder string, tag []byte, txs []mining.TxData, deadline time.Time) (*Registration, error) {
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
	snap, err := g.snapshot(t.Height)
	if err != nil {
		return nil, err
	}
	gt := &gateway.Template{Height: t.Height, PrevHash: t.PreviousBlockHash, Version: uint32(t.Version),
		Bits: t.Bits, CurTime: uint32(t.CurTime), CoinbaseValue: value}
	for _, tx := range txs {
		gt.Txs = append(gt.Txs, gateway.TemplateTx{TxID: tx.TxID, Data: tx.Data})
	}
	req, err := gateway.BuildJob(snap, gt, finder, tag)
	if err != nil {
		return nil, err
	}

	// The pool answers "retry" while its node has not yet seen the block this one is building
	// on, or has just taken one of this node's transactions; that usually clears in well under
	// a second. Keep asking until the deadline, never past it.
	var resp *wire.JobResponse
	for {
		// Two tries per call: when the pool asks for a transaction's data, the client adds it
		// and needs the second try to send it.
		resp, err = g.client.Register(req, gt, 2, 0)
		if err == nil {
			break
		}
		if resp == nil || !resp.Retry || !g.cfg.Now().Add(250*time.Millisecond).Before(deadline) {
			if resp != nil && resp.Error != "" {
				return nil, errors.New(resp.Error)
			}
			return nil, err
		}
		time.Sleep(250 * time.Millisecond)
	}

	outs, err := wire.Payouts(snap, value, finder)
	if err != nil {
		return nil, err
	}
	var mine int64
	for _, o := range outs {
		if canon, cerr := CanonicalAddress(o.Address); cerr == nil && canon == finder {
			mine += o.Sats
		}
	}
	return &Registration{PoolJobID: resp.JobID, ShareDiff: resp.ShareDifficulty, Height: t.Height,
		PrevHash: t.PreviousBlockHash, Coinb1: req.Coinb1, Coinb2: req.Coinb2, Txs: txs, CoinbaseSats: value,
		FinderSats: mine, Outputs: len(outs), Snapshot: snap.Version, At: g.cfg.Now()}, nil
}

// Track records that the stratum handed out reg as local job localID: miners are on TIDES work.
// version is the job's block version in hex.
func (g *Gateway) Track(localID string, reg *Registration, version string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.jobs[localID] = &tracked{reg: reg, version: version}
	g.order = append(g.order, localID)
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
		why = err.Error()
	}
	if g.state != StateFallback {
		g.logger.Warn("⚠️  TIDES: Forge Pool unavailable — mining SOLO until it is back (blocks found meanwhile pay your own address in full)",
			zap.String("reason", why))
		g.since = g.cfg.Now()
	}
	g.state, g.reason = StateFallback, why
}

// Note records a refresh that failed while miners stay on a job the pool still holds.
func (g *Gateway) Note(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reason = "last refresh failed: " + err.Error()
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
// chose TIDES mines solo meanwhile (owner decision 2026-09-30), so this is two missed refreshes.
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
	ws := wire.Share{JobID: t.reg.PoolJobID, Miner: sh.MinerID, Worker: sh.WorkerName, Extranonce: en,
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
			if r.Block {
				g.counts.Blocks++
			}
		case strings.Contains(r.Error, "resend"):
			resend = append(resend, sent[i])
		default:
			g.counts.Rejected++
			g.counts.LastReject = r.Error
		}
	}
	if resp.ShareDifficulty > 0 {
		g.shareDiff = resp.ShareDifficulty
	}
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

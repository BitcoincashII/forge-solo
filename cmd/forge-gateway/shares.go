package main

import (
	"context"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/blockbuild"
	"github.com/BitcoincashII/forge-solo/internal/mining"
	"github.com/BitcoincashII/forge-solo/internal/stratum"
	"github.com/BitcoincashII/forge-solo/internal/tidesgw"
	"go.uber.org/zap"
)

// processor takes every share the stratum accepts: it counts it, forwards it to the pool when
// it was found on work the pool registered, and turns a block into a block.
type processor struct {
	log   *zap.Logger
	gw    *tidesgw.Gateway
	hist  *jobHistory
	node  *node
	stats *minerStats

	mu     sync.Mutex
	blocks []foundBlock // newest last
}

// foundBlock is a block one of this gateway's miners found.
type foundBlock struct {
	Height int64     `json:"height"`
	Hash   string    `json:"hash"`
	At     time.Time `json:"at"`
	Tides  bool      `json:"tides"`  // paid the TIDES split; false = a solo block to the payout address
	Result string    `json:"result"` // "accepted", or why the node would not take it
	Miner  string    `json:"miner"`
	Worker string    `json:"worker"`
}

const keepBlocks = 50

func (p *processor) ProcessShare(_ context.Context, sh *stratum.Share) error {
	p.stats.add(sh, time.Now())
	job := p.hist.get(sh.JobID)
	if job != nil && solvesBlock(sh, job) {
		p.takeBlock(sh, job)
		return nil
	}
	if p.gw.Registered(sh.JobID) != nil {
		p.gw.Forward(sh.JobID, sh)
	}
	return nil
}

// solvesBlock reports whether sh meets its own job's network target. Both ways of asking are
// used and either is enough: the exact hash-against-target comparison, and the difficulty the
// stratum computed. An extra submitblock costs one refused RPC call; a missed block costs the
// block.
func solvesBlock(sh *stratum.Share, job *mining.Job) bool {
	if target := compactTarget(job.NBits); target != nil {
		if h, ok := new(big.Int).SetString(sh.BlockHash, 16); ok && h.Sign() > 0 && h.Cmp(target) <= 0 {
			return true
		}
	}
	netDiff := stratum.BitsToDifficulty(job.NBits)
	return netDiff > 0 && sh.ActualDiff >= netDiff
}

// compactTarget decodes nBits (big-endian hex, as the job carries it) to the target.
func compactTarget(nbits string) *big.Int {
	n, err := strconv.ParseUint(nbits, 16, 32)
	if err != nil || n&0x00800000 != 0 {
		return nil
	}
	exp := uint(n >> 24)
	mant := new(big.Int).SetUint64(n & 0x007fffff)
	if exp <= 3 {
		return mant.Rsh(mant, 8*(3-exp))
	}
	return mant.Lsh(mant, 8*(exp-3))
}

// takeBlock submits a block. When the pool registered its job, the share goes to the pool FIRST,
// waiting up to two seconds, and only then the block to this node: the pool judges a share
// against its own node's tip, so if this node's block reached the pool's node over the
// peer-to-peer network first, the pool would refuse the share as stale and never record the
// block in its TIDES ledger. The block pays the TIDES split on-chain either way, and the pool
// submits it to its own node too.
func (p *processor) takeBlock(sh *stratum.Share, job *mining.Job) {
	p.log.Info("🎉 BLOCK FOUND", zap.Int64("height", job.Height), zap.String("hash", sh.BlockHash),
		zap.String("worker", sh.WorkerName), zap.Bool("tides", job.Tides))
	if job.Tides && p.gw.Registered(sh.JobID) != nil {
		done := make(chan error, 1)
		go func() { done <- p.gw.SendBlock(sh.JobID, sh) }()
		select {
		case err := <-done:
			if err != nil {
				p.log.Warn("the pool did not take the block share; the block still pays the TIDES split on-chain", zap.Error(err))
			} else {
				p.log.Info("🌊 the pool has the block share and is submitting the block too")
			}
		case <-time.After(2 * time.Second):
			p.log.Warn("the pool is slow to answer the block share; submitting to this node now")
		}
	}
	result := p.submit(sh, job)
	p.mu.Lock()
	p.blocks = append(p.blocks, foundBlock{Height: job.Height, Hash: sh.BlockHash, At: time.Now(), Tides: job.Tides,
		Result: result, Miner: sh.MinerID, Worker: sh.WorkerName})
	if len(p.blocks) > keepBlocks {
		p.blocks = p.blocks[len(p.blocks)-keepBlocks:]
	}
	p.mu.Unlock()
}

// submit builds the block from the share's exact job and gives it to the node. A result other
// than a clean accept may still be an accepted block -- a timeout after the node took and relayed
// it, or a duplicate from the pool's copy arriving first -- so the chain decides: the block counts
// as accepted when its hash is the one at its height.
func (p *processor) submit(sh *stratum.Share, job *mining.Job) string {
	coinbase, err := blockbuild.Coinbase(job.CoinBase1, sh.ExtraNonce1, sh.ExtraNonce2, job.CoinBase2)
	if err != nil {
		p.log.Error("CRITICAL: cannot build the coinbase of a found block", zap.Error(err))
		return err.Error()
	}
	blockHex, err := blockbuild.Block(job, coinbase, sh.NTime, sh.Nonce, sh.VersionBits)
	if err != nil {
		p.log.Error("CRITICAL: cannot build a found block", zap.Error(err))
		return err.Error()
	}
	ourHash, _ := blockbuild.Hash(blockHex)
	reason, err := p.node.submitBlock(blockHex)
	if err == nil && reason == "" {
		p.log.Info("✅ block accepted by the node", zap.Int64("height", job.Height), zap.String("hash", ourHash))
		return "accepted"
	}
	if err != nil {
		reason = err.Error()
	}
	p.log.Warn("submitblock was not a clean accept; checking the chain", zap.String("reason", reason), zap.String("hash", ourHash))
	for attempt := 1; attempt <= 3; attempt++ {
		if h, e := p.node.blockHash(job.Height); e == nil && strings.EqualFold(h, ourHash) {
			p.log.Info("✅ block is on the chain", zap.Int64("height", job.Height), zap.String("hash", ourHash))
			return "accepted"
		}
		if r, e := p.node.submitBlock(blockHex); e == nil && r == "" {
			p.log.Info("✅ block accepted by the node on a retry", zap.Int64("height", job.Height), zap.String("hash", ourHash))
			return "accepted"
		}
		time.Sleep(time.Duration(attempt) * time.Second)
	}
	p.log.Error("block NOT accepted by the node", zap.Int64("height", job.Height), zap.String("hash", ourHash), zap.String("reason", reason))
	return reason
}

func (p *processor) foundBlocks() []foundBlock {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]foundBlock(nil), p.blocks...)
}

// minerStats keeps each worker's recent shares, for the status page's hashrates.
type minerStats struct {
	mu      sync.Mutex
	workers map[workerKey]*workerStat
}

type workerKey struct{ miner, worker string }

type workerStat struct {
	shares  int64
	last    time.Time
	first   time.Time
	samples []sample // within hashrateWindow
}

type sample struct {
	at   time.Time
	diff float64
}

// hashrateWindow is how far back a worker's hashrate looks; forgetAfter is how long an idle
// worker stays on the page.
const (
	hashrateWindow = 10 * time.Minute
	forgetAfter    = time.Hour
)

func newMinerStats() *minerStats { return &minerStats{workers: map[workerKey]*workerStat{}} }

func (m *minerStats) add(sh *stratum.Share, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := workerKey{sh.MinerID, sh.WorkerName}
	w := m.workers[k]
	if w == nil {
		w = &workerStat{first: now}
		m.workers[k] = w
	}
	w.shares++
	w.last = now
	w.samples = append(w.samples, sample{at: now, diff: sh.Difficulty})
	cut := 0
	for cut < len(w.samples) && now.Sub(w.samples[cut].at) > hashrateWindow {
		cut++
	}
	w.samples = w.samples[cut:]
}

// workerView is one worker on the status page.
type workerView struct {
	Miner     string    `json:"miner"`
	Worker    string    `json:"worker"`
	Hashrate  float64   `json:"hashrate"` // hashes per second over the last 10 minutes
	Shares    int64     `json:"shares"`
	LastShare time.Time `json:"last_share"`
}

// view is every worker seen within forgetAfter, busiest first. A hashrate is the credited
// difficulty of its shares in the window times 2^32 over the window -- or over the time since
// its first share, if that is shorter, but never under a minute.
func (m *minerStats) view(now time.Time) []workerView {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []workerView
	for k, w := range m.workers {
		if now.Sub(w.last) > forgetAfter {
			delete(m.workers, k)
			continue
		}
		var sum float64
		for _, s := range w.samples {
			if now.Sub(s.at) <= hashrateWindow {
				sum += s.diff
			}
		}
		span := math.Min(hashrateWindow.Seconds(), math.Max(60, now.Sub(w.first).Seconds()))
		out = append(out, workerView{Miner: k.miner, Worker: k.worker, Hashrate: sum * 4294967296 / span,
			Shares: w.shares, LastShare: w.last})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Hashrate != out[j].Hashrate {
			return out[i].Hashrate > out[j].Hashrate
		}
		return out[i].Worker < out[j].Worker
	})
	return out
}

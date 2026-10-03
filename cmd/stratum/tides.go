package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"sync/atomic"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/mining"
	"github.com/BitcoincashII/forge-solo/internal/stats"
	"github.com/BitcoincashII/forge-solo/internal/stratum"
	"github.com/BitcoincashII/forge-solo/internal/tidesgw"
	"go.uber.org/zap"
)

// TIDES mode: this install as a DATUM gateway to Forge Pool. See internal/tidesgw for what that
// means; this file is where the stratum hands work to it.
var (
	tidesGWPtr    atomic.Pointer[tidesgw.Gateway]
	payoutModeVal atomic.Value // stats.PayoutModeSolo or stats.PayoutModeTides
)

func tidesGateway() *tidesgw.Gateway { return tidesGWPtr.Load() }

// currentPayoutMode is the mode in effect: solo until the dashboard chose TIDES and the gateway
// could be started.
func currentPayoutMode() string {
	if m, ok := payoutModeVal.Load().(string); ok {
		return m
	}
	return stats.PayoutModeSolo
}

// ensureTidesGateway starts the gateway on first need. Its key lives in the database beside the
// dashboard settings, so an install keeps one identity at the pool across restarts and updates,
// and the stratum needs no writable volume of its own.
func ensureTidesGateway() error {
	if tidesGateway() != nil {
		return nil
	}
	if err := tidesgw.CheckPoolURL(tidesgw.PoolURL()); err != nil {
		return err
	}
	fresh := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(fresh); err != nil {
		return fmt.Errorf("gateway key: %w", err)
	}
	stored, err := stats.GatewaySeed(hex.EncodeToString(fresh))
	if err != nil {
		return fmt.Errorf("gateway key: %w", err)
	}
	key, err := tidesgw.KeyFromSeed(stored)
	if err != nil {
		return err
	}
	g := tidesgw.New(tidesgw.Config{PoolURL: tidesgw.PoolURL(), Key: key, Logger: logger, CreditTo: tidesPayoutAddress,
		MaxDifficulty: tidesMaxDifficulty})
	if !tidesGWPtr.CompareAndSwap(nil, g) {
		return nil
	}
	go g.Run(shutdownCh)
	logger.Info("🌊 TIDES gateway ready", zap.String("pool", tidesgw.PoolURL()), zap.String("gateway", g.ID()))
	return nil
}

// tidesMaxDifficulty is the highest difficulty a miner works at on either port: every TIDES job
// commits to a share difficulty above it, so the pool credits a rental's shares in full.
func tidesMaxDifficulty() float64 {
	var m float64
	if stratumServer != nil {
		m = stratumServer.MaxDifficulty()
	}
	if stratumRentalServer != nil {
		m = math.Max(m, stratumRentalServer.MaxDifficulty())
	}
	return m
}

// tidesPayoutAddress is the address TIDES credits: the one the coinbase would pay in solo. The
// gateway credits every share this install sends to it (Config.CreditTo), whatever address a
// miner logged in with.
func tidesPayoutAddress() string {
	if stratumServer != nil {
		if a := stratumServer.SoloPayoutAddress(); a != "" {
			return a
		}
	}
	return poolAddress
}

// tidesNextJob picks this turn's job in TIDES mode: one the pool registered, or -- when the pool
// will not answer or will not take it -- a solo job meanwhile. keep=true means miners stay on the
// TIDES job they have, which the pool still holds while the tip stands still.
func tidesNextJob(g *tidesgw.Gateway, template *mining.BlockTemplate, isNewBlock bool, cur *mining.Job) (job *mining.Job, keep bool) {
	if !g.Due(isNewBlock) {
		return jobManager.CreateJob(template), false
	}
	reg, err := g.Register(template, tidesPayoutAddress(), jobManager.CoinbaseTag())
	if err == nil {
		job = jobManager.CreateJobWithCoinbase(template, reg.Coinb1, reg.Coinb2, reg.Txs, reg.CoinbaseSats)
		job.TidesFinderSats = reg.FinderSats
		g.Track(job.ID, reg, job.Version)
		return job, false
	}
	// A missed refresh or two is not a reason to leave TIDES: the job miners are on is still
	// registered at the pool until the tip moves. A pool that stays silent is: see KeepFor.
	if !isNewBlock && cur != nil && cur.Tides && cur.OriginalPrevHash == template.PreviousBlockHash && g.Keep(cur.ID) {
		g.Note(err)
		return nil, true
	}
	g.Fallback(err)
	return jobManager.CreateJob(template), false
}

// tidesTakeShare hands an accepted share to the gateway. A share that solves a block goes to the
// pool FIRST, waiting up to two seconds, and only then to the local node: the pool judges a
// share against its own node's tip, so if this node's block reached the pool's node over the
// peer-to-peer network first, the pool would refuse the share as stale and never record the
// block in its TIDES ledger. The pool submits the block to its own, well-connected node too.
func tidesTakeShare(share *stratum.Share, isBlock bool) {
	g := tidesGateway()
	if g == nil || g.Registered(share.JobID) == nil {
		return
	}
	if !isBlock {
		g.Forward(share.JobID, share)
		return
	}
	done := make(chan error, 1)
	go func() { done <- g.SendBlock(share.JobID, share) }()
	select {
	case err := <-done:
		if err != nil {
			logger.Warn("TIDES: the pool did not take the block share; the block still pays the TIDES split on-chain",
				zap.String("job_id", share.JobID), zap.Error(err))
		} else {
			logger.Info("🌊 TIDES: the pool has the block share and is submitting the block too", zap.String("job_id", share.JobID))
		}
	case <-time.After(2 * time.Second):
		logger.Warn("TIDES: the pool is slow to answer the block share; submitting to the local node now",
			zap.String("job_id", share.JobID))
	}
}

// applyTidesMode puts mode into effect: TIDES starts the gateway and switches 1175 merge-mining
// off (TIDES is BCH2 only); solo leaves merge-mining to the watcher, which turns it back on when
// a 1175 address is set. It returns the mode now in effect, which is
// solo if TIDES could not start.
func applyTidesMode(mode string, jm *mining.JobManager) string {
	if mode != stats.PayoutModeTides {
		if currentPayoutMode() == stats.PayoutModeTides {
			logger.Info("⛏️  payout mode: SOLO — blocks pay your own address in full")
		}
		payoutModeVal.Store(stats.PayoutModeSolo)
		return stats.PayoutModeSolo
	}
	if err := ensureTidesGateway(); err != nil {
		logger.Error("TIDES was chosen but cannot start — mining SOLO", zap.Error(err))
		payoutModeVal.Store(stats.PayoutModeSolo)
		return stats.PayoutModeSolo
	}
	if merge1175Enabled {
		jm.DisableMergeMining()
		merge1175Enabled = false
		aux1175PayoutAddr = ""
		for _, srv := range []*stratum.Server{stratumServer, stratumRentalServer} {
			if srv != nil {
				srv.DisableMergeMining()
			}
		}
		logger.Info("💠 1175 merge-mining OFF — TIDES mode mines BCH2 only")
	}
	if currentPayoutMode() != stats.PayoutModeTides {
		tidesGateway().Reset()
		logger.Info("🌊 payout mode: TIDES — mining for the Forge Pool TIDES window (no fee; paid in every DATUM block's coinbase)",
			zap.String("payout_address", tidesPayoutAddress()))
	}
	payoutModeVal.Store(stats.PayoutModeTides)
	return stats.PayoutModeTides
}

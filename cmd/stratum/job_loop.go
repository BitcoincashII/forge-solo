package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/mining"
	"github.com/BitcoincashII/forge-solo/internal/stats"
	"github.com/BitcoincashII/forge-solo/internal/stratum"
	"github.com/BitcoincashII/forge-solo/internal/tidesgw"
	"go.uber.org/zap"
)

// newBlockWait is how long a new block's work waits for Forge Pool to register it before the
// miners get solo work for the block meanwhile; the pool's job follows when it comes. A
// registration takes about 60 ms, and one or two "retry" answers, while the pool's node takes the
// block, about 0.6 to 1.1 s: those still get the block's TIDES job first.
const newBlockWait = 2 * time.Second

// While the BCH2 node catches up with the chain it connects blocks as fast as it can, a thousand a
// second when they are small, and each one made a new block's job with clean_jobs: a hundred jobs a
// second, every share in flight refused as stale, for blocks long buried. The node leaves initial
// sync at once after any restart (its tip may be up to ten years old), so nothing else held that
// back. While it is catchUpGap or more blocks behind the headers it knows, a new block's work goes
// out at most every catchUpEvery; the block that brings it level goes out at once.
const (
	catchUpEvery = 5 * time.Second
	// A header always arrives just before its block, so one block behind is every block.
	catchUpGap = 2
	// catchUpFor bounds one catch-up: a header the node never connects (a block withheld, or one it
	// found invalid) must not slow the work of every block after it. catchUpQuiet without a sign of
	// the node being behind ends a catch-up; a block that then comes within catchUpEvery of the one
	// before starts another.
	catchUpFor   = 10 * time.Minute
	catchUpQuiet = time.Minute
)

// Each block the node connects while it catches up is a ZMQ notice, and each notice was two info
// lines, "ZMQ block notification received" from the listener and "ZMQ triggered job refresh" from
// the loop: a 14,000-block sync on Windows wrote 28,000 of them in under three minutes, and the
// warnings said once a minute were lost among them. While mining, each block brought a third,
// "Network difficulty updated from template": BCH2's difficulty changes at nearly every block. A
// notice that comes within catchUpEvery of the one before has the node asked, at most once every
// noticeAskEvery, how far behind its headers it is. While it is catchUpGap or more blocks behind,
// these lines are left out and its progress is said once every noticeProgressEvery instead; once it
// is level, or catchUpEvery passes without a notice, each notice is logged again. At the tip, where
// blocks come minutes apart, the node is not asked and every notice is logged as before.
const (
	noticeAskEvery      = time.Second
	noticeProgressEvery = time.Minute
)

// quietNotices is set while the ZMQ notices, and the network difficulty line, are left out of the
// log. The loop sets it; the ZMQ listener reads it too.
var quietNotices atomic.Bool

// sayNotices logs each ZMQ notice again.
func sayNotices() {
	if quietNotices.Swap(false) {
		logger.Info("✅ The ZMQ lines of each new block are logged again")
	}
}

// jobLoop builds work from the BCH2 node's block templates and hands it to both stratum ports.
// Miners expect periodic job updates to confirm the pool is alive. It sends new jobs on:
//  1. a new block detected via ZMQ (CleanJobs=true), at once;
//  2. a new block height seen by polling (CleanJobs=true), the fallback;
//  3. the periodic ntime update (CleanJobs=false), every 15 seconds.
type jobLoop struct {
	blocks <-chan string    // ZMQ new-block notices
	tick   <-chan time.Time // the 1 s poll; run makes one when nil
	stop   <-chan struct{}
	now    func() time.Time
	// newBlockWait is newBlockWait; a test may shorten it.
	newBlockWait time.Duration

	// pending is the TIDES registration running beside the loop, nil when none is.
	pending *registration

	// lastNewBlockAt is when a new block's work last went out. behindSince is when the node was
	// first seen behind its headers in this catch-up, zero when it is not catching up; lastBehind is
	// when it was last seen so.
	lastNewBlockAt time.Time
	behindSince    time.Time
	lastBehind     time.Time

	// lastNotice is when the last ZMQ notice came, and lastNoticeAsk when one last had the node
	// asked about its chain. progressSaid is when the node's progress was last said while the
	// notices are left out.
	lastNotice    time.Time
	lastNoticeAsk time.Time
	progressSaid  time.Time

	lastHeight      int64
	lastPrevHash    string
	lastJobTime     time.Time
	lastPausedLog   time.Time
	lastAuxLog      time.Time
	lastTemplateLog time.Time
	auxWasFailing   bool
}

// newJobLoop is the job loop main runs: ZMQ notices and the 1 s poll, until shutdown.
func newJobLoop() *jobLoop {
	return &jobLoop{blocks: zmqBlockCh, stop: shutdownCh, now: time.Now, newBlockWait: newBlockWait}
}

func (l *jobLoop) run() {
	if l.tick == nil {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		l.tick = ticker.C
	}
	for {
		select {
		case <-l.stop:
			l.drop()
			logger.Info("Job broadcast loop shutting down")
			return
		case blockHash := <-l.blocks:
			// ZMQ notification - immediate block template fetch
			l.notice(blockHash)
			l.turn(true)
		case <-l.tick:
			// Regular polling (fallback)
			l.noticesAfterQuiet()
			l.turn(false)
		case answer := <-l.pendingAnswer():
			l.finish(answer)
		}
	}
}

// notice logs a ZMQ notice, unless the node is catching up with the chain. A node that does not
// answer changes nothing.
func (l *jobLoop) notice(blockHash string) {
	now := l.now()
	if now.Sub(l.lastNotice) < catchUpEvery && now.Sub(l.lastNoticeAsk) >= noticeAskEvery {
		l.lastNoticeAsk = now
		if blocks, headers, ok := bch2ChainState(); ok && headers-blocks < catchUpGap {
			sayNotices()
		} else if ok {
			l.quiet(now, blocks, headers)
		}
	}
	l.lastNotice = now
	if !quietNotices.Load() {
		logger.Info("⚡ ZMQ triggered job refresh", zap.String("block_hash", blockHash))
	}
}

// quiet leaves the ZMQ notices out of the log while the node is behind its headers, and says its
// progress once every noticeProgressEvery.
func (l *jobLoop) quiet(now time.Time, blocks, headers int64) {
	if !quietNotices.Swap(true) {
		logger.Info("⏳ The BCH2 node is catching up with the chain: the ZMQ lines of each new block are left out of the log until it has, and its progress said once a minute",
			zap.Int64("block", blocks), zap.Int64("headers", headers))
	} else if now.Sub(l.progressSaid) >= noticeProgressEvery {
		logger.Info("⏳ The BCH2 node is still catching up with the chain", zap.Int64("block", blocks), zap.Int64("headers", headers))
	} else {
		return
	}
	l.progressSaid = now
}

// noticesAfterQuiet logs each ZMQ notice again once catchUpEvery has passed without one: the
// catch-up is over, or the node waits for blocks.
func (l *jobLoop) noticesAfterQuiet() {
	if l.now().Sub(l.lastNotice) >= catchUpEvery {
		sayNotices()
	}
}

// turn is one pass of the loop: fetch the node's template and send miners a job if one is due.
func (l *jobLoop) turn(zmqTriggered bool) {
	// No payout address configured yet: accept stratum connections but PAUSE mining
	// (never build a job that would pay a null script). Set it in the dashboard.
	//
	// Say so periodically. Silently doing nothing here is indistinguishable from
	// working correctly in the logs, and it is the state a miner sees as "connected
	// but never hashing" -- the one failure a home user cannot diagnose alone.
	if !jobManager.IsConfigured() {
		if time.Since(l.lastPausedLog) >= time.Minute {
			l.lastPausedLog = time.Now()
			logger.Warn("⏸️  MINING PAUSED — no valid BCH2 payout address in effect. " +
				"Set (or re-save) it in the dashboard Settings page; miners may connect but will receive no work.")
		}
		return
	}

	template, err := jobManager.GetBlockTemplate()
	if err != nil {
		// Throttled like the pause warning six lines up, which it was not. This
		// loop runs every second, so a node that is syncing, restarting or
		// reindexing produced ~3,600 ERROR lines an hour and the log a user sends
		// for support became entirely this line, rotating the real cause out.
		noteTemplateError(err)
		if time.Since(l.lastTemplateLog) >= time.Minute {
			l.lastTemplateLog = time.Now()
			if nodeNotReady(err) {
				logger.Info("Waiting for the BCH2 node before mining (said again once a minute)", zap.String("node", err.Error()))
			} else {
				logger.Error("Failed to get block template (further identical errors suppressed for 1m)", zap.Error(err))
			}
		}
		return
	}
	if template == nil {
		return
	}

	// Update network difficulty from block template bits (actual next-block target). Its line is
	// left out with the ZMQ notices while the node catches up (quietNotices).
	if templateDiff := bitsToDifficulty(template.Bits); templateDiff > 0 {
		oldDiff := getNetworkDifficulty()
		if templateDiff != oldDiff && !quietNotices.Load() {
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
		if failing && time.Since(l.lastAuxLog) >= time.Minute {
			l.lastAuxLog = time.Now()
			l.auxWasFailing = true
			msg := "⚠️  1175 merge-mining is enabled but NOT producing work — BCH2 mining is unaffected and continues normally."
			if state == "never_worked" {
				msg = "⚠️  1175 merge-mining is enabled but has NEVER produced work since startup (the 1175 node may still be syncing) — BCH2 mining is unaffected and continues normally."
			}
			logger.Warn(msg, zap.String("aux_error", auxErr), zap.String("payout_address_1175", aux.Payout))
		}
		if !failing && l.auxWasFailing {
			l.auxWasFailing = false
			logger.Info("✅ 1175 merge-mining recovered — aux work is flowing again")
		}
	}

	curJob := getCurrentJob()
	isNewBlock := template.Height != l.lastHeight || template.PreviousBlockHash != l.lastPrevHash || curJob == nil

	// TIDES: jobs come from the gateway, which falls back to solo when the pool will not
	// take them. A change of payout mode moves miners at once -- leaving TIDES, or a
	// fallen-back install whose retry of the pool is due.
	gw := tidesGateway()
	tides := gw != nil && currentPayoutMode() == stats.PayoutModeTides
	if isNewBlock || !tides {
		l.drop() // a registration for the block before, or for TIDES, which miners have left
	}
	if isNewBlock && l.catchingUp() {
		return // miners keep the work they have, on a block as old as this one
	}
	if l.pending != nil {
		return // the pool's answer makes the next job on this block (finish)
	}
	needPeriodicUpdate := periodicJobDue(l.lastJobTime, l.now())
	modeSwitch := curJob != nil && curJob.Tides != tides && (!tides || gw.Due(false))

	auxWork, auxPayTo := jobManager.AuxWorkNow()
	if !jobDue(curJob, isNewBlock, needPeriodicUpdate, modeSwitch, tides, jobManager.PayoutAddress(), auxWork, auxPayTo) {
		return
	}
	var job *mining.Job
	if tides {
		var keep bool
		if job, keep = l.tidesJob(gw, template, isNewBlock, curJob); keep {
			l.lastJobTime = l.now()
			return
		}
	} else {
		job = jobManager.CreateJob(template)
	}
	if job == nil {
		return
	}
	l.send(job, curJob, template, isNewBlock, zmqTriggered)
}

// send hands job to both stratum ports: miners drop the work they have when it is for another
// block or pays differently (mustDropWork).
func (l *jobLoop) send(job, curJob *mining.Job, template *mining.BlockTemplate, isNewBlock, zmqTriggered bool) {
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

	cleanJobs := mustDropWork(curJob, job, isNewBlock)

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
		l.lastNewBlockAt = l.now()
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

	l.lastHeight = template.Height
	l.lastPrevHash = template.PreviousBlockHash
	l.lastJobTime = l.now()
}

// catchingUp reports whether a new block's work waits this turn because the BCH2 node is catching up
// with the chain. The node is asked only when the block comes within catchUpEvery of the last
// block's work, so at the tip, where blocks come minutes apart, it is never asked. The work waits
// only while the node says it has catchUpGap or more blocks still to connect, and never past
// catchUpEvery after the last block's work: a node that does not answer, a header it never
// connects, or one that stays behind can delay a block's work by catchUpEvery at most, and within
// one catch-up only for catchUpFor. The first block's work, when miners have none, goes out at once.
func (l *jobLoop) catchingUp() bool {
	now := l.now()
	if now.Sub(l.lastNewBlockAt) >= catchUpEvery {
		return false
	}
	blocks, headers, ok := bch2ChainState()
	if !ok {
		return false
	}
	if headers-blocks < catchUpGap {
		if !l.behindSince.IsZero() {
			logger.Info("✅ The BCH2 node has caught up with the chain", zap.Int64("block", blocks))
		}
		l.behindSince = time.Time{}
		return false
	}
	if l.behindSince.IsZero() || now.Sub(l.lastBehind) >= catchUpQuiet {
		l.behindSince = now
		logger.Info(fmt.Sprintf("⏳ The BCH2 node is catching up with the chain: a new block's work goes out at most every %s until it has", catchUpEvery),
			zap.Int64("block", blocks), zap.Int64("headers", headers))
	}
	l.lastBehind = now
	return now.Sub(l.behindSince) < catchUpFor
}

// chainStateClient asks the BCH2 node about its chain from the job loop, which must not wait on it
// long: a node that does not answer within a second counts as caught up.
var chainStateClient = &http.Client{Timeout: time.Second}

// bch2ChainState is the BCH2 node's chain height and the height of the best header it knows. ok is
// false when it did not answer, or not with both.
func bch2ChainState() (blocks, headers int64, ok bool) {
	req, err := http.NewRequest("POST", rpcURL, strings.NewReader(`{"jsonrpc":"1.0","id":"forge","method":"getblockchaininfo","params":[]}`))
	if err != nil {
		return 0, 0, false
	}
	req.SetBasicAuth(rpcUser, rpcPass)
	req.Header.Set("Content-Type", "application/json")
	resp, err := chainStateClient.Do(req)
	if err != nil {
		return 0, 0, false
	}
	defer resp.Body.Close()
	var r struct {
		Result *struct {
			Blocks  *int64 `json:"blocks"`
			Headers *int64 `json:"headers"`
		} `json:"result"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r) != nil || r.Result == nil || r.Result.Blocks == nil || r.Result.Headers == nil {
		return 0, 0, false
	}
	return *r.Result.Blocks, *r.Result.Headers, true
}

// registration is a TIDES registration running beside the loop.
type registration struct {
	gw       *tidesgw.Gateway
	template *mining.BlockTemplate
	newBlock bool
	cancel   context.CancelFunc
	answer   chan poolAnswer // gets the pool's answer, once
}

type poolAnswer struct {
	reg *tidesgw.Registration
	err error
}

// tidesJob is this turn's job in TIDES mode. The pool registers a job before miners see it, which
// takes one or two round trips, and a pool that is slow or does not answer must not hold the loop:
// the registration runs beside it (register). A new block's work waits for it up to newBlockWait,
// then goes out solo meanwhile. A job on the same block waits for nothing: miners keep the one they
// have, and the pool's answer makes the next (finish).
func (l *jobLoop) tidesJob(gw *tidesgw.Gateway, template *mining.BlockTemplate, isNewBlock bool, cur *mining.Job) (*mining.Job, bool) {
	if !gw.DueAt(isNewBlock, template.Height) {
		return jobManager.CreateJob(template), false
	}
	p := l.register(gw, template, isNewBlock)
	if !isNewBlock {
		return nil, false
	}
	wait := time.NewTimer(l.newBlockWait)
	defer wait.Stop()
	select {
	case a := <-p.answer:
		l.pending = nil
		p.cancel()
		return tidesAnswer(gw, template, true, cur, a.reg, a.err)
	case <-wait.C:
		logger.Info("TIDES: Forge Pool has not registered the new block's work yet; miners get solo work for it meanwhile, and the pool's job once it comes",
			zap.Int64("height", template.Height), zap.Duration("waited", l.newBlockWait))
		return jobManager.CreateJob(template), false
	case <-l.stop:
		return nil, false
	}
}

// register asks the pool to register template, beside the loop. One registration runs at a time:
// any before it is given up.
func (l *jobLoop) register(gw *tidesgw.Gateway, template *mining.BlockTemplate, newBlock bool) *registration {
	l.drop()
	ctx, cancel := context.WithCancel(context.Background())
	p := &registration{gw: gw, template: template, newBlock: newBlock, cancel: cancel, answer: make(chan poolAnswer, 1)}
	finder, tag := tidesPayoutAddress(), jobManager.CoinbaseTag()
	go func() {
		reg, err := gw.RegisterCtx(ctx, template, finder, tag)
		p.answer <- poolAnswer{reg, err}
	}()
	l.pending = p
	return p
}

// drop gives up the registration running beside the loop, if any. Its answer is never used.
func (l *jobLoop) drop() {
	if l.pending != nil {
		l.pending.cancel()
		l.pending = nil
	}
}

// pendingAnswer is where the answer to the registration running beside the loop comes; nil, which
// never delivers, when none is running.
func (l *jobLoop) pendingAnswer() <-chan poolAnswer {
	if l.pending == nil {
		return nil
	}
	return l.pending.answer
}

// finish makes a job from the pool's answer to the registration that ran beside the loop. An answer
// for a block other than the one the miners are on, or that comes when TIDES is no longer the
// mode, is thrown away and never tracked: the gateway would drop every share on the miners' block
// as stale until the next job.
func (l *jobLoop) finish(a poolAnswer) {
	p := l.pending
	l.pending = nil
	if p == nil {
		return
	}
	p.cancel()
	gw := tidesGateway()
	if gw != p.gw || currentPayoutMode() != stats.PayoutModeTides ||
		p.template.Height != l.lastHeight || p.template.PreviousBlockHash != l.lastPrevHash {
		return
	}
	if p.newBlock && a.err != nil {
		// The miners have had solo work for this block since newBlockWait.
		gw.Fallback(a.err)
		return
	}
	cur := getCurrentJob()
	job, keep := tidesAnswer(gw, p.template, p.newBlock, cur, a.reg, a.err)
	if keep {
		l.lastJobTime = l.now()
		return
	}
	if job == nil {
		return
	}
	if p.newBlock {
		logger.Info("🌊 TIDES: Forge Pool registered the new block's work; miners move to it from the solo work they had",
			zap.Int64("height", p.template.Height), zap.String("job_id", job.ID))
	}
	l.send(job, cur, p.template, false, false)
}

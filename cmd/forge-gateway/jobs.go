package main

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/mining"
	"github.com/BitcoincashII/forge-solo/internal/stratum"
	"github.com/BitcoincashII/forge-solo/internal/tidesgw"
	"go.uber.org/zap"
)

// jobHistoryDepth matches the stratum's own job history: a share it validated against any job
// it still holds can always be rebuilt into a block from that exact job.
const jobHistoryDepth = 500

// jobHistory is every job handed to the stratum, by ID.
type jobHistory struct {
	mu    sync.RWMutex
	jobs  map[string]*mining.Job
	order []string
}

func newJobHistory() *jobHistory { return &jobHistory{jobs: map[string]*mining.Job{}} }

func (h *jobHistory) put(j *mining.Job) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, seen := h.jobs[j.ID]; !seen {
		h.order = append(h.order, j.ID)
	}
	h.jobs[j.ID] = j
	for len(h.order) > jobHistoryDepth {
		delete(h.jobs, h.order[0])
		h.order = h.order[1:]
	}
}

func (h *jobHistory) get(id string) *mining.Job {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.jobs[id]
}

// refreshEvery is how often miners get fresh work while the tip stands still (new transactions,
// a newer TIDES window), as Forge Solo does.
const refreshEvery = 15 * time.Second

// jobLoop builds work from the node's block templates and hands it to the stratum: work the
// pool registered for its TIDES window, or -- while the pool cannot be reached -- solo work, or
// with pool_only none at all.
type jobLoop struct {
	log      *zap.Logger
	jm       *mining.JobManager
	gw       *tidesgw.Gateway
	srv      *stratum.Server
	hist     *jobHistory
	payout   string
	poolOnly bool
	notify   chan struct{} // a new block: fetch a template now rather than at the next tick

	door       atomic.Bool // pool_only: miners are let in only while the pool takes this gateway's work
	current    atomic.Pointer[mining.Job]
	template   atomic.Pointer[mining.BlockTemplate]
	templateAt atomic.Int64 // unix nanoseconds
	lastErr    atomic.Value // string: the node's last template error, "" once templates flow
}

func newJobLoop(log *zap.Logger, jm *mining.JobManager, gw *tidesgw.Gateway, srv *stratum.Server, hist *jobHistory,
	payout string, poolOnly bool) *jobLoop {
	l := &jobLoop{log: log, jm: jm, gw: gw, srv: srv, hist: hist, payout: payout, poolOnly: poolOnly,
		notify: make(chan struct{}, 1)}
	l.lastErr.Store("")
	// With pool_only the door opens at the first job the pool registers, and closes when it
	// cannot be reached; otherwise it is always open.
	srv.SetAcceptGate(func() bool { return !l.poolOnly || l.door.Load() })
	return l
}

// wake asks for a template now (the node's blocknotify, through the status server).
func (l *jobLoop) wake() {
	select {
	case l.notify <- struct{}{}:
	default:
	}
}

func (l *jobLoop) run(stop <-chan struct{}) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	var lastHeight int64
	var lastPrev string
	var lastJobAt, lastErrLog time.Time
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		case <-l.notify:
		}
		tmpl, err := l.jm.GetBlockTemplate()
		if err != nil || tmpl == nil {
			if err != nil {
				l.lastErr.Store(err.Error())
				// Once a minute: this loop runs every second, and a node that is syncing or
				// restarting would otherwise fill the log with this one line.
				if time.Since(lastErrLog) >= time.Minute {
					lastErrLog = time.Now()
					l.log.Error("no block template from the node (repeats suppressed for a minute)", zap.Error(err))
				}
			}
			continue
		}
		l.lastErr.Store("")
		l.template.Store(tmpl)
		l.templateAt.Store(time.Now().UnixNano())

		cur := l.current.Load()
		isNew := cur == nil || tmpl.Height != lastHeight || tmpl.PreviousBlockHash != lastPrev
		periodic := time.Since(lastJobAt) >= refreshEvery
		// Mining solo meanwhile (or, pool_only, turned away): try the pool again when it is due.
		retry := (cur == nil || !cur.Tides || (l.poolOnly && !l.door.Load())) && l.gw.Due(false)
		if !isNew && !periodic && !retry {
			continue
		}
		job, keep := l.next(tmpl, isNew, cur)
		// Recorded whatever came of it, or a pool_only gateway with no work to give would see
		// this block as new on every tick -- and a fallen-back gateway never retries the pool
		// on a new block (Gateway.Due).
		lastHeight, lastPrev, lastJobAt = tmpl.Height, tmpl.PreviousBlockHash, time.Now()
		if keep || job == nil {
			continue
		}
		l.hist.put(job)
		clean := isNew || (cur != nil && cur.Tides != job.Tides)
		l.current.Store(job)
		l.srv.BroadcastJob(toStratumJob(job, clean))
		if isNew {
			l.log.Info("new work", zap.Int64("height", job.Height), zap.Bool("tides", job.Tides), zap.String("job", job.ID))
		}
	}
}

// next picks this turn's job: one the pool registered; or, when the pool will not answer or will
// not take it, solo work meanwhile (nil with pool_only). keep=true means miners stay on the TIDES
// job they have, which the pool still holds while the tip stands still. Forge Solo's TIDES mode
// makes the same choice (cmd/stratum/tides.go).
func (l *jobLoop) next(t *mining.BlockTemplate, isNew bool, cur *mining.Job) (job *mining.Job, keep bool) {
	if !l.gw.Due(isNew) {
		return l.solo(t), false
	}
	reg, err := l.gw.Register(t, l.payout, l.jm.CoinbaseTag())
	if err == nil {
		job = l.jm.CreateJobWithCoinbase(t, reg.Coinb1, reg.Coinb2, reg.Txs, reg.CoinbaseSats)
		job.TidesFinderSats = reg.FinderSats
		l.gw.Track(job.ID, reg, job.Version)
		l.openDoor()
		return job, false
	}
	// A missed refresh or two is not a reason to leave TIDES: the job miners are on is still
	// registered at the pool until the tip moves. A pool that stays silent is: see KeepFor.
	if !isNew && cur != nil && cur.Tides && cur.OriginalPrevHash == t.PreviousBlockHash && l.gw.Keep(cur.ID) {
		l.gw.Note(err)
		return nil, true
	}
	l.gw.Fallback(err)
	return l.solo(t), false
}

// solo is work while the pool cannot be reached: the whole block to the payout address -- or,
// with pool_only, no work, and every miner turned away so it fails over to its backup pool.
func (l *jobLoop) solo(t *mining.BlockTemplate) *mining.Job {
	if l.poolOnly {
		l.closeDoor()
		return nil
	}
	return l.jm.CreateJob(t)
}

func (l *jobLoop) openDoor() {
	if l.poolOnly && !l.door.Swap(true) {
		l.log.Info("🌊 Forge Pool is taking this gateway's work — letting miners in")
	}
}

func (l *jobLoop) closeDoor() {
	wasOpen := l.door.Swap(false)
	if n := l.srv.DisconnectAll("pool_only: Forge Pool cannot be reached"); wasOpen || n > 0 {
		l.log.Warn("Forge Pool cannot be reached and pool_only is set: miners are turned away until it is back, so they fail over to their backup pool",
			zap.Int("disconnected", n))
	}
}

func toStratumJob(j *mining.Job, clean bool) *stratum.Job {
	return &stratum.Job{
		ID:               j.ID,
		Height:           j.Height,
		PrevBlockHash:    j.PrevBlockHash,
		OriginalPrevHash: j.OriginalPrevHash,
		CoinBase1:        j.CoinBase1,
		CoinBase2:        j.CoinBase2,
		MerkleBranches:   j.MerkleBranches,
		Version:          j.Version,
		NBits:            j.NBits,
		NTime:            j.NTime,
		CleanJobs:        clean,
		Target:           j.Target,
		CreatedAt:        time.Now(),
		Transactions:     j.Transactions,
	}
}

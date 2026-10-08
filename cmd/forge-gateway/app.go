package main

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/mining"
	"github.com/BitcoincashII/forge-solo/internal/stratum"
	"github.com/BitcoincashII/forge-solo/internal/tidesgw"
	"go.uber.org/zap"
)

// app is the running gateway. What lives as long as the process is built once, from the config it
// started with: the stratum server and its miners, the pool gateway with its identity and queued
// shares, the status page. The node, its job loop and its health check are an engine, which
// re-read settings replace without a restart.
type app struct {
	log     *zap.Logger
	cfgPath string
	started time.Time
	start   *Config // the pool, the stratum and the status page are this config's until a restart
	gw      *tidesgw.Gateway
	srv     *stratum.Server
	hist    *jobHistory
	proc    *processor
	jobIDs  atomic.Uint64 // every engine's jobs are numbered from this one count
	eng     atomic.Pointer[engine]

	saveMu sync.Mutex // one change of the config file, or one re-read of it, at a time
	pendMu sync.Mutex
	pend   *reloadReq                // the newest reload asked for, until the applier takes it
	want   atomic.Pointer[reloadReq] // the newest reload asked for, kept after it is applied
	kick   chan struct{}

	stop        <-chan struct{} // the process stop
	applierDone chan struct{}   // closed when the applier has returned

	statusSrv  *http.Server // nil with status.listen "off"
	statusAddr string
}

// reloadReq is settings to apply: a config and what setupProblem says of it.
type reloadReq struct {
	cfg     *Config
	problem string
}

// engine is what re-read settings replace. Nothing in it changes after it is stored but loop, set
// once when the payout address has been resolved.
type engine struct {
	cfg      *Config
	problem  string // cfg.setupProblem(); "" when set up
	loginErr error  // the cookie file could not be read
	node     *node  // nil when problem != "" or loginErr != nil
	loop     atomic.Pointer[jobLoop]
	health   *nodeHealth // nil when problem != ""
	stop     chan struct{}
	stopOnce sync.Once
	done     sync.WaitGroup // the loop and the health check
}

func newEngine(cfg *Config, problem string) *engine {
	return &engine{cfg: cfg, problem: problem, stop: make(chan struct{})}
}

// halt tells the engine's loop and health check to stop.
func (e *engine) halt() { e.stopOnce.Do(func() { close(e.stop) }) }

func (e *engine) stopped() bool {
	select {
	case <-e.stop:
		return true
	default:
		return false
	}
}

// run runs f as one of the engine's goroutines, until the engine is halted.
func (e *engine) run(f func(stop <-chan struct{})) {
	e.done.Add(1)
	go func() {
		defer e.done.Done()
		f(e.stop)
	}()
}

var (
	// applyWait is how long an apply waits for the engine before it to stop.
	applyWait = 30 * time.Second
	// statusStopWait is how long the status page may take, at the stop, to answer a save under way.
	statusStopWait = 5 * time.Second
	// engineStopWait is how long the stop waits for the engine to end.
	engineStopWait = 5 * time.Second
)

// newApp builds the gateway's long-lived parts from cfg, as 1.0.0 built them at start, and starts
// the applier. Nothing listens yet.
func newApp(log *zap.Logger, cfgPath string, key ed25519.PrivateKey, cfg *Config, stop <-chan struct{}) *app {
	a := &app{log: log, cfgPath: cfgPath, started: time.Now(), start: cfg, hist: newJobHistory(),
		kick: make(chan struct{}, 1), stop: stop, applierDone: make(chan struct{})}
	a.gw = tidesgw.New(tidesgw.Config{PoolURL: cfg.Pool.URL, Key: key, Logger: log.Named("pool"), PoolOnly: cfg.Mining.PoolOnly,
		// Each job commits to a share difficulty above the busiest miner's, so the pool credits
		// every share in full (srv is set before any job loop, which alone calls this, starts).
		MaxDifficulty: func() float64 { return a.srv.MaxDifficulty() }})
	a.proc = &processor{log: log, gw: a.gw, hist: a.hist, stats: newMinerStats()}
	host, port, _ := hostPort(cfg.Stratum.Listen)
	a.srv = stratum.NewServer(&stratum.ServerConfig{
		Host:                host,
		Port:                port,
		MaxConnections:      cfg.Stratum.MaxConnections,
		MaxConnectionsPerIP: cfg.Stratum.MaxConnectionsPerIP,
		MaxSharesPerSecond:  100,
		VardiffEnabled:      true,
		MinDiff:             cfg.Stratum.MinDifficulty,
		AbsoluteMinDiff:     cfg.Stratum.MinDifficulty,
		MaxDiff:             cfg.Stratum.MaxDifficulty,
		VariancePercent:     0.25,
		TargetShareTime:     cfg.Stratum.TargetShareSeconds,
		RetargetTime:        cfg.Stratum.RetargetSeconds,
		// The DATUM coinbase reserves 12 extranonce bytes: 4 per connection, 8 for the miner.
		ExtraNonce1Size: 4,
		ExtraNonce2Size: 8,
		ServerName:      "gateway",
		// Solo-style logins: a miner's username is its BCH2 address (credited to it at the pool)
		// or any worker name (credited to the payout address).
		SoloOnly: true,
	}, log.Named("stratum"), a.proc)
	// No miner is let in before there is work for it: the first engine's job loop opens the door.
	a.srv.SetAcceptGate(func() bool { return false })
	go a.applier()
	return a
}

// reload asks for cfg to be applied. The newest request wins; the applier applies it.
func (a *app) reload(cfg *Config, problem string) {
	r := &reloadReq{cfg: cfg, problem: problem}
	a.pendMu.Lock()
	a.pend = r
	a.pendMu.Unlock()
	a.want.Store(r)
	select {
	case a.kick <- struct{}{}:
	default:
	}
}

// reloadFromFile applies the config file again, as it is now. Everything but a save made from the
// status page goes through here, never through reload with a config of its own: a config held
// from before could otherwise replace a save made a moment before.
func (a *app) reloadFromFile(why string) {
	a.saveMu.Lock()
	defer a.saveMu.Unlock()
	cfg, err := readConfig(a.cfgPath)
	if err != nil {
		a.log.Error(fmt.Sprintf("settings could not be read again (%s): %v", why, err))
		return
	}
	a.reload(cfg, cfg.setupProblem())
}

func (a *app) stopped() bool {
	select {
	case <-a.stop:
		return true
	default:
		return false
	}
}

// applier applies each reload in turn: applies never overlap. It returns at the process stop.
func (a *app) applier() {
	defer close(a.applierDone)
	for {
		select {
		case <-a.stop:
			return
		case <-a.kick:
		}
		if a.stopped() {
			return
		}
		a.pendMu.Lock()
		r := a.pend
		a.pend = nil
		a.pendMu.Unlock()
		if r != nil {
			a.apply(r.cfg, r.problem)
		}
	}
}

// waitEngine waits up to d for e's loop and health check to end. It reports false when the process
// stop came first.
func (a *app) waitEngine(e *engine, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		e.done.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-a.stop:
		return false
	case <-time.After(d):
		a.log.Warn(fmt.Sprintf("the old job loop did not stop in %s; going on", d))
		return true
	}
}

// turnAway closes the stratum to miners: there is no work to give them.
func (a *app) turnAway(why string) {
	a.srv.SetAcceptGate(func() bool { return false })
	a.srv.DisconnectAll(why)
}

// apply replaces the engine with one for cfg. Once the process stop has come, it stores nothing
// and starts nothing.
func (a *app) apply(cfg *Config, problem string) {
	if old := a.eng.Load(); old != nil {
		old.halt()
		if !a.waitEngine(old, applyWait) {
			return
		}
	}
	if a.stopped() {
		return
	}
	e := newEngine(cfg, problem)
	if problem != "" {
		a.turnAway("Forge Gateway is not set up: set the node and payout address in Settings")
		a.eng.Store(e)
		a.log.Warn("not set up: " + problem)
		return
	}
	user, pass, err := cfg.rpcLogin()
	if err != nil {
		// The node deletes its cookie when it stops and writes a new one when it starts: the
		// health check reads the file until it can, then applies the settings again.
		a.turnAway("Forge Gateway cannot read the node's cookie file")
		e.loginErr = err
		e.health = newNodeHealth(a, e, "", "")
		a.eng.Store(e)
		e.run(e.health.run)
		a.log.Warn("cannot read the node's cookie file: " + err.Error())
		return
	}
	n := newNode(cfg.Node.RPCURL, user, pass)
	e.node = n
	e.health = newNodeHealth(a, e, user, pass)
	// Stored, with its health check running, before the payout address is resolved: that asks the
	// node, which may take its 10 s to answer, and the status page must say why meanwhile.
	a.eng.Store(e)
	e.run(e.health.run)

	payout := cfg.Mining.PayoutAddress
	// No payout address here: with one, the job manager asks the node up to 10 times, 2 s apart.
	jm := mining.NewJobManager(cfg.Node.RPCURL, user, pass, "", cfg.Mining.CoinbaseTag)
	if err := jm.SetPoolAddress(payout); err != nil {
		a.log.Error("the payout address cannot be used", zap.Error(err))
		e.halt()
		if !a.waitEngine(e, applyWait) || a.stopped() {
			return
		}
		a.turnAway("Forge Gateway's payout address cannot be used")
		a.eng.Store(newEngine(cfg, "Settings has a mistake: "+err.Error()+". Correct it in Settings."))
		return
	}
	if a.stopped() {
		return
	}
	a.srv.SetSoloPayoutAddress(payout)
	a.gw.SetPoolOnly(cfg.Mining.PoolOnly)
	a.gw.Reset() // the new loop registers with the pool at once
	a.proc.setNode(n)
	loop := newJobLoop(a.log, jm, a.gw, a.srv, a.hist, payout, cfg.Mining.PoolOnly)
	loop.ids = &a.jobIDs
	e.loop.Store(loop)
	e.run(loop.run)
	login := "user " + cfg.Node.RPCUser
	if cfg.Node.RPCUser == "" {
		login = "cookie file " + cfg.Node.RPCCookieFile
	}
	a.log.Info(fmt.Sprintf("using the settings: node %s, login %s, payout %s, coinbase tag %s, pool_only %t",
		cfg.Node.RPCURL, login, payout, cfg.Mining.CoinbaseTag, cfg.Mining.PoolOnly))
}

// serveStatus starts the status page at addr.
func (a *app) serveStatus(addr string) error {
	ln, err := listenStatus(addr)
	if err != nil {
		return err
	}
	a.statusAddr = ln.Addr().String()
	a.statusSrv = &http.Server{Handler: a.statusHandler(), ReadHeaderTimeout: 10 * time.Second}
	go a.statusSrv.Serve(ln)
	return nil
}

// shutdown is the stop, in this order: the status page (a save under way is still written and
// answered; a request after it finds the port closed), the applier, the engine, then the miners and
// the shares still queued for the pool.
func (a *app) shutdown() {
	a.log.Info("stopping: closing the miners' connections, then sending the pool the shares still queued")
	a.closeStatus()
	<-a.applierDone
	a.endEngine()
	// The miners first: Stop handles what they send in its grace and waits for shares, a block
	// among them, still being processed. Flushing before it left the shares accepted in the last
	// seconds queued after the flush, and lost.
	a.srv.Stop()
	a.gw.Flush()
}

// closeStatus ends the status page, letting a request under way finish for up to statusStopWait.
func (a *app) closeStatus() {
	if a.statusSrv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), statusStopWait)
	defer cancel()
	_ = a.statusSrv.Shutdown(ctx)
	_ = a.statusSrv.Close()
}

// endEngine stops the engine in use and waits for it, at most engineStopWait. Stopping the miners
// and flushing the shares do not need it gone: a loop told to stop hands out no more work.
func (a *app) endEngine() {
	e := a.eng.Load()
	if e == nil {
		return
	}
	e.halt()
	done := make(chan struct{})
	go func() {
		e.done.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(engineStopWait):
	}
}

// currentLoop is the job loop in use, or nil.
func (a *app) currentLoop() *jobLoop {
	if e := a.eng.Load(); e != nil {
		return e.loop.Load()
	}
	return nil
}

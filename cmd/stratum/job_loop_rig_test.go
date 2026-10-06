package main

import (
	"bufio"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/datum/wire"
	"github.com/BitcoincashII/forge-solo/internal/mining"
	"github.com/BitcoincashII/forge-solo/internal/stats"
	"github.com/BitcoincashII/forge-solo/internal/stratum"
	"github.com/BitcoincashII/forge-solo/internal/tidesgw"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// The job loop's test rig: a fake BCH2 node, a fake Forge Pool, both stratum ports with a miner
// logged in on each, and the loop itself, run as main runs it, on a clock the test moves.

// loopClock is the loop's and the gateway's clock: the real time plus however far the test has
// moved it on. The fake pool checks request signatures by it too.
type loopClock struct{ off atomic.Int64 }

func (c *loopClock) Now() time.Time      { return time.Now().Add(time.Duration(c.off.Load())) }
func (c *loopClock) Add(d time.Duration) { c.off.Add(int64(d)) }

// blockHash is the hash of the block at height h on the chain the tests mine.
func chainHash(h int64) string { return fmt.Sprintf("%064x", 0x1000000+h) }

// loopNode is the BCH2 node's RPC as far as the job loop can tell.
type loopNode struct {
	srv *httptest.Server

	mu        sync.Mutex
	tip       int64  // the node's chain height (getblockchaininfo blocks)
	tipHash   string // its hash; chainHash(tip) unless a test set another
	headers   int64  // the best header it knows of; never below tip
	ibd       bool
	chainDown bool          // getblockchaininfo fails
	chainSlow time.Duration // getblockchaininfo answers this late
	calls     map[string]int
}

func newLoopNode(t *testing.T, tip int64) *loopNode {
	n := &loopNode{tip: tip, tipHash: chainHash(tip), headers: tip, calls: map[string]int{}}
	n.srv = httptest.NewServer(n)
	t.Cleanup(n.srv.Close)
	return n
}

// setTip moves the node's chain to height tip, on the tests' chain.
func (n *loopNode) setTip(tip int64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.tip, n.tipHash = tip, chainHash(tip)
	if n.headers < tip {
		n.headers = tip
	}
}

// setHeaders sets the best header the node knows of.
func (n *loopNode) setHeaders(h int64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.headers = h
}

func (n *loopNode) set(f func(n *loopNode)) {
	n.mu.Lock()
	defer n.mu.Unlock()
	f(n)
}

func (n *loopNode) count(method string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.calls[method]
}

func (n *loopNode) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string `json:"method"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	n.mu.Lock()
	n.calls[req.Method]++
	tip, hash, headers, ibd, down, slow := n.tip, n.tipHash, n.headers, n.ibd, n.chainDown, n.chainSlow
	n.mu.Unlock()
	if req.Method == "getblockchaininfo" && slow > 0 {
		select {
		case <-time.After(slow):
		case <-r.Context().Done():
			return
		}
	}
	var result interface{}
	var rpcErr interface{}
	switch req.Method {
	case "getblocktemplate":
		result = map[string]interface{}{"version": 0x20000000, "previousblockhash": hash, "transactions": []interface{}{},
			"coinbasevalue": int64(50_0000_0000), "bits": "1902c9b9", "height": tip + 1, "curtime": time.Now().Unix(),
			"target": "0000000000000002c9b900000000000000000000000000000000000000000000"}
	case "getblockchaininfo":
		if down {
			rpcErr = map[string]interface{}{"code": -1, "message": "the node did not answer"}
		} else {
			result = map[string]interface{}{"chain": "main", "blocks": tip, "headers": max(headers, tip), "initialblockdownload": ibd}
		}
	case "validateaddress":
		result = map[string]interface{}{"isvalid": false} // the job manager reads the address itself
	default:
		rpcErr = map[string]interface{}{"code": -32601, "message": "Method not found"}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"result": result, "error": rpcErr, "id": "forge"})
}

// loopPool is Forge Pool's DATUM intake as far as a gateway can tell, with the real pool's rules
// for a job (forge-pool-v2 validateJob): one for a height above the pool's is answered "retry", one
// for a lower height or another block at its height is refused as a stale template.
type loopPool struct {
	srv   *httptest.Server
	clock *loopClock

	mu         sync.Mutex
	height     int64  // the height the pool mines: its tip + 1
	prev       string // the block it mines on; chainHash(height-1) unless a test set another
	snapHeight int64  // the height its TIDES snapshot names, when that is behind its node; else height
	jobs       []wire.JobRequest
	snapGets   int
	shares     []wire.Share
	delay      time.Duration // a job POST waits this long before it is answered
	slowAt     int64         // with delay: only a job for this height waits
	hang       bool          // a job POST is never answered: it waits until the gateway gives up
	down       bool          // everything answers 502
	release    chan struct{}
	inFlight   int // job POSTs being answered now
	given      int // job POSTs the gateway gave up on before they were answered
}

func newLoopPool(t *testing.T, clock *loopClock, height int64) *loopPool {
	p := &loopPool{clock: clock, height: height, prev: chainHash(height - 1), release: make(chan struct{})}
	p.srv = httptest.NewServer(p)
	t.Cleanup(p.srv.Close)
	t.Cleanup(func() { close(p.release) }) // before srv.Close: a hung answer must end first
	return p
}

// mines moves the pool's node to tip.
func (p *loopPool) mines(tip int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.height, p.prev = tip+1, chainHash(tip)
}

func (p *loopPool) set(f func(p *loopPool)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f(p)
}

// registered is the job POSTs the pool has seen so far.
func (p *loopPool) registered() []wire.JobRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]wire.JobRequest(nil), p.jobs...)
}

// waiting is how many job POSTs the pool is answering now, and how many the gateway gave up on.
func (p *loopPool) waiting() (now, givenUp int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inFlight, p.given
}

func (p *loopPool) gets() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.snapGets
}

func (p *loopPool) credited() []wire.Share {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]wire.Share(nil), p.shares...)
}

func (p *loopPool) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	down := p.down
	p.mu.Unlock()
	if down {
		http.Error(w, "<html><body>502 Bad Gateway</body></html>", http.StatusBadGateway)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/datum/v1/tides" {
		p.mu.Lock()
		p.snapGets++
		s := wire.Snapshot{Version: 7, Height: p.height, PrevHash: p.prev, Dust: 546, Work: map[string]float64{},
			Carry: map[string]int64{}, At: p.clock.Now()}
		if p.snapHeight != 0 {
			s.Height, s.PrevHash = p.snapHeight, chainHash(p.snapHeight-1)
		}
		p.mu.Unlock()
		json.NewEncoder(w).Encode(s)
		return
	}
	body, _ := io.ReadAll(r.Body)
	if _, err := wire.Verify(r, body, p.clock.Now()); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	switch r.URL.Path {
	case "/datum/v1/jobs":
		var req wire.JobRequest
		json.Unmarshal(body, &req)
		p.mu.Lock()
		p.jobs = append(p.jobs, req)
		n, delay, hang := len(p.jobs), p.delay, p.hang
		if p.slowAt != 0 && req.Height != p.slowAt {
			delay = 0
		}
		p.inFlight++
		p.mu.Unlock()
		defer p.set(func(p *loopPool) { p.inFlight-- })
		gaveUp := func() { p.set(func(p *loopPool) { p.given++ }) }
		if hang {
			select {
			case <-p.release:
			case <-r.Context().Done():
				gaveUp()
			}
			return
		}
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				gaveUp()
				return
			}
		}
		p.mu.Lock()
		height, prev := p.height, p.prev
		p.mu.Unlock()
		var resp wire.JobResponse
		switch {
		case req.Height > height:
			resp = wire.JobResponse{Error: fmt.Sprintf("the pool's node is still at height %d", height-1), Retry: true}
		case req.Height != height || req.PrevHash != prev:
			resp = wire.JobResponse{Error: fmt.Sprintf("stale template: the pool mines height %d on %s", height, prev)}
		default:
			resp = wire.JobResponse{JobID: fmt.Sprintf("pj%d", n), ShareDifficulty: 1024}
		}
		json.NewEncoder(w).Encode(resp)
	case "/datum/v1/shares":
		var b wire.ShareBatch
		json.Unmarshal(body, &b)
		p.mu.Lock()
		p.shares = append(p.shares, b.Shares...)
		p.mu.Unlock()
		resp := wire.ShareBatchResponse{ShareDifficulty: 1024}
		for range b.Shares {
			resp.Results = append(resp.Results, wire.ShareResult{Accepted: true})
		}
		json.NewEncoder(w).Encode(resp)
	default:
		http.NotFound(w, r)
	}
}

// loopNotice is a mining.notify a test miner received, with the job it names as the loop built it.
type loopNotice struct {
	id    string
	clean bool
	at    time.Time
	job   *mining.Job // from the loop's job history
}

// loopMiner is a miner logged in on one stratum port, keeping every mining.notify it receives.
type loopMiner struct {
	t     *testing.T
	notes chan loopNotice
}

func connectMiner(t *testing.T, addr, user string) *loopMiner {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	fmt.Fprintf(c, `{"id":1,"method":"mining.subscribe","params":["bmminer/2.0.0"]}`+"\n")
	fmt.Fprintf(c, `{"id":2,"method":"mining.authorize","params":["%s","x"]}`+"\n", user)
	m := &loopMiner{t: t, notes: make(chan loopNotice, 10000)}
	authorized := make(chan struct{})
	go func() {
		r := bufio.NewReader(c)
		var once sync.Once
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			var msg struct {
				ID     interface{}     `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal([]byte(line), &msg) != nil {
				continue
			}
			if id, ok := msg.ID.(float64); ok && id == 2 {
				once.Do(func() { close(authorized) })
			}
			if msg.Method != "mining.notify" {
				continue
			}
			var params []interface{}
			if json.Unmarshal(msg.Params, &params) != nil || len(params) < 9 {
				continue
			}
			id, _ := params[0].(string)
			clean, _ := params[8].(bool)
			jobHistoryMu.RLock()
			job := jobHistory[id]
			jobHistoryMu.RUnlock()
			m.notes <- loopNotice{id: id, clean: clean, at: time.Now(), job: job}
		}
	}()
	select {
	case <-authorized:
	case <-time.After(5 * time.Second):
		t.Fatalf("the miner on %s was not authorized", addr)
	}
	return m
}

// next is the next job the miner receives, within d.
func (m *loopMiner) next(d time.Duration) (loopNotice, bool) {
	select {
	case n := <-m.notes:
		return n, true
	case <-time.After(d):
		return loopNotice{}, false
	}
}

type loopRigOpts struct {
	tip   int64 // the node's chain height at the start
	tides bool  // TIDES mode, with a fake pool at the same tip
	// The gateway's RegisterFor and RetryEvery: Forge Solo's defaults (6 s, 1 min) when zero.
	registerFor, retryEvery time.Duration
}

// loopRig is the job loop with everything it talks to.
type loopRig struct {
	t      *testing.T
	clock  *loopClock
	node   *loopNode
	pool   *loopPool // nil in solo
	gw     *tidesgw.Gateway
	loop   *jobLoop
	miner  *loopMiner // on the main port
	rental *loopMiner // on the rental port
	payout string
	logs   *observer.ObservedLogs
	tick   chan time.Time // with run: the loop's poll
	blocks chan string    // with run: its ZMQ notices
}

func newLoopRig(t *testing.T, o loopRigOpts) *loopRig {
	t.Helper()
	savedLogger, savedJM, savedMain, savedRental := logger, jobManager, stratumServer, stratumRentalServer
	savedJob, savedGW, savedMode := getCurrentJob(), tidesGateway(), currentPayoutMode()
	savedURL, savedUser, savedPass := rpcURL, rpcUser, rpcPass
	savedDiff := getNetworkDifficulty()
	jobHistoryMu.Lock()
	savedHistory, savedOrder := jobHistory, jobHistoryOrder
	jobHistory, jobHistoryOrder = map[string]*mining.Job{}, nil
	jobHistoryMu.Unlock()
	t.Cleanup(func() {
		logger, jobManager, stratumServer, stratumRentalServer = savedLogger, savedJM, savedMain, savedRental
		setCurrentJob(savedJob)
		tidesGWPtr.Store(savedGW)
		payoutModeVal.Store(savedMode)
		tidesRefreshWanted.Store(false)
		rpcURL, rpcUser, rpcPass = savedURL, savedUser, savedPass
		setNetworkDifficulty(savedDiff)
		jobHistoryMu.Lock()
		jobHistory, jobHistoryOrder = savedHistory, savedOrder
		jobHistoryMu.Unlock()
	})

	core, logs := observer.New(zap.DebugLevel)
	logger = zap.New(core)
	r := &loopRig{t: t, clock: &loopClock{}, payout: testAddr(7), logs: logs}
	r.node = newLoopNode(t, o.tip)
	rpcURL, rpcUser, rpcPass = r.node.srv.URL, "u", "p"
	jobManager = mining.NewJobManager(r.node.srv.URL, "u", "p", "", "/jobloop/")
	if err := jobManager.SetPoolAddress(r.payout); err != nil {
		t.Fatal(err)
	}
	setCurrentJob(nil)
	tidesRefreshWanted.Store(false)

	cfg := func(rental bool) *stratum.ServerConfig {
		c := &stratum.ServerConfig{Host: "127.0.0.1", Port: 0, MaxConnections: 16, ExtraNonce1Size: 4, ExtraNonce2Size: 8,
			MinDiff: 1024, MaxDiff: 1e12, VardiffEnabled: true, TargetShareTime: 5, RetargetTime: 10, SoloOnly: true,
			CreditPayoutAddress: true, ServerName: "main"}
		if rental {
			c.MinDiff, c.IsRentalPort, c.ServerName = 500000, true, "rental"
		}
		return c
	}
	stratumServer = stratum.NewServer(cfg(false), zap.NewNop(), nil, nil)
	stratumRentalServer = stratum.NewServer(cfg(true), zap.NewNop(), nil, nil)
	for _, srv := range []*stratum.Server{stratumServer, stratumRentalServer} {
		srv.SetSoloPayoutAddress(r.payout)
		srv.SetLoginHandler(tidesLoginCheck)
		if err := srv.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(srv.Stop)
	}

	payoutModeVal.Store(stats.PayoutModeSolo)
	tidesGWPtr.Store(nil)
	if o.tides {
		r.pool = newLoopPool(t, r.clock, o.tip+1)
		_, key, _ := ed25519.GenerateKey(nil)
		r.gw = tidesgw.New(tidesgw.Config{PoolURL: r.pool.srv.URL, Key: key, Logger: logger, CreditTo: tidesPayoutAddress,
			MaxDifficulty: tidesMaxDifficulty, RegisterFor: o.registerFor, RetryEvery: o.retryEvery, Now: r.clock.Now})
		tidesGWPtr.Store(r.gw)
		payoutModeVal.Store(stats.PayoutModeTides)
	}

	r.miner = connectMiner(t, stratumServer.ListenAddr(), r.payout+".rig1")
	r.rental = connectMiner(t, stratumRentalServer.ListenAddr(), r.payout+".mrr")

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	r.loop = &jobLoop{stop: stop, now: r.clock.Now, newBlockWait: newBlockWait}
	return r
}

// step is one turn of the loop: the 1 s poll, or (zmq) a new block's notice. A registration the
// turn left running beside the loop is waited for and its answer taken, as the running loop does.
func (r *loopRig) step(zmq bool) {
	r.loop.turn(zmq)
	if p := r.loop.pending; p != nil {
		select {
		case a := <-p.answer:
			r.loop.finish(a)
		case <-time.After(10 * time.Second):
			r.t.Fatal("the pool did not answer the registration")
		}
	}
}

// run runs the loop as main does, on the rig's poll and ZMQ, until the test ends. The test then
// drives it with poll and zmq instead of step.
func (r *loopRig) run() {
	r.tick, r.blocks = make(chan time.Time, 1), make(chan string, 10)
	stop, done := make(chan struct{}), make(chan struct{})
	r.loop.tick, r.loop.blocks, r.loop.stop = r.tick, r.blocks, stop
	go func() {
		defer close(done)
		r.loop.run()
	}()
	r.t.Cleanup(func() {
		close(stop)
		<-done
	})
}

// poll is the loop's 1 s poll firing, unless the last one is still waiting to be taken.
func (r *loopRig) poll() {
	select {
	case r.tick <- time.Now():
	default:
	}
}

// zmq is a new block reaching the node, and ZMQ telling the running loop.
func (r *loopRig) zmq(tip int64) {
	r.node.setTip(tip)
	r.blocks <- chainHash(tip)
}

// waitUntil fails the test with code unless cond holds within d.
func waitUntil(t *testing.T, d time.Duration, code string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(d); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("%s: not so within %s", code, d)
		}
	}
}

// block is a new block reaching the node, and ZMQ telling the loop.
func (r *loopRig) block(tip int64) {
	r.node.setTip(tip)
	r.step(true)
}

// drain is every job the main port's miner received within d, waiting d after the last, with the
// rental port's checked to be the same.
func (r *loopRig) drain(d time.Duration) []loopNotice {
	var got []loopNotice
	for {
		n, ok := r.miner.next(d)
		if !ok {
			break
		}
		got = append(got, n)
	}
	for range got {
		if _, ok := r.rental.next(d); !ok {
			r.t.Fatalf("the rental port got fewer jobs than the main port's %d", len(got))
		}
	}
	return got
}

// sent waits for the next job on both ports and checks they are the same.
func (r *loopRig) sent(code string, d time.Duration) loopNotice {
	r.t.Helper()
	n, ok := r.miner.next(d)
	if !ok {
		r.t.Fatalf("%s: no job reached the miner within %s", code, d)
	}
	rn, ok := r.rental.next(d)
	if !ok || rn.id != n.id || rn.clean != n.clean {
		r.t.Fatalf("%s: the rental port got %+v, the main port %+v", code, rn, n)
	}
	if n.job == nil {
		r.t.Fatalf("%s: job %s is not in the loop's job history", code, n.id)
	}
	return n
}

// quiet checks that no job reaches the miners within d.
func (r *loopRig) quiet(code string, d time.Duration) {
	r.t.Helper()
	if n, ok := r.miner.next(d); ok {
		r.t.Fatalf("%s: a job went out (%s, height %d, clean %v)", code, n.id, n.job.Height, n.clean)
	}
}

// logged reports whether a log line containing msg was written.
func (r *loopRig) logged(msg string) bool {
	return r.logs.FilterMessageSnippet(msg).Len() > 0
}

// shareOn forwards a share found on job to the pool, as the share processor does, and flushes it.
func (r *loopRig) shareOn(job *mining.Job, nonce uint32) {
	tidesTakeShare(&stratum.Share{JobID: job.ID, MinerID: r.payout, WorkerName: "mrr", ActualDiff: 1e7,
		ExtraNonce1: "0a0b0c0d", ExtraNonce2: "0000000000000001", NTime: job.NTime, Nonce: fmt.Sprintf("%08x", nonce)}, false)
	r.gw.Flush()
}

package tidesgw

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/cashaddr"
	"github.com/BitcoincashII/forge-solo/internal/datum/wire"
	"github.com/BitcoincashII/forge-solo/internal/mining"
	"github.com/BitcoincashII/forge-solo/internal/stratum"
)

func addr(b byte) string {
	var h [20]byte
	for i := range h {
		h[i] = b
	}
	return cashaddr.Encode(cashaddr.MainnetPrefix, cashaddr.P2PKH, h)
}

var (
	me    = addr(1)
	other = addr(2)
)

const prevHash = "00000000000000011dabe720fd9b6256da61020714af5d1272687fe9c80d7215"

// fakePool is the pool's DATUM API as far as a gateway can tell. Every POST must carry a valid
// signature, checked with the same wire.Verify the real intake uses.
type fakePool struct {
	t   *testing.T
	srv *httptest.Server

	mu         sync.Mutex
	snap       wire.Snapshot
	snapGets   int
	jobs       []wire.JobRequest
	onJob      func(n int, req wire.JobRequest) wire.JobResponse
	shares     []wire.Share
	onShares   func(batch []wire.Share) wire.ShareBatchResponse
	down       bool
	badSigSeen bool
}

func newFakePool(t *testing.T) *fakePool {
	p := &fakePool{t: t, snap: wire.Snapshot{Version: 7, Height: 83361, PrevHash: prevHash, Dust: 546,
		Work: map[string]float64{}, Carry: map[string]int64{}, At: time.Now()}}
	p.onJob = func(n int, req wire.JobRequest) wire.JobResponse {
		return wire.JobResponse{JobID: "pooljob", ShareDifficulty: 2048}
	}
	p.onShares = func(batch []wire.Share) wire.ShareBatchResponse {
		r := wire.ShareBatchResponse{ShareDifficulty: 4096}
		for range batch {
			r.Results = append(r.Results, wire.ShareResult{Accepted: true})
		}
		return r
	}
	p.srv = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *fakePool) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	down := p.down
	p.mu.Unlock()
	if down {
		http.Error(w, "down", http.StatusBadGateway)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/datum/v1/tides" {
		p.mu.Lock()
		p.snapGets++
		s := p.snap
		p.mu.Unlock()
		json.NewEncoder(w).Encode(s)
		return
	}
	body, _ := io.ReadAll(r.Body)
	if _, err := wire.Verify(r, body, time.Now()); err != nil {
		p.mu.Lock()
		p.badSigSeen = true
		p.mu.Unlock()
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	switch r.URL.Path {
	case "/datum/v1/jobs":
		var req wire.JobRequest
		json.Unmarshal(body, &req)
		p.mu.Lock()
		p.jobs = append(p.jobs, req)
		n := len(p.jobs)
		p.mu.Unlock()
		json.NewEncoder(w).Encode(p.onJob(n, req))
	case "/datum/v1/shares":
		var b wire.ShareBatch
		json.Unmarshal(body, &b)
		p.mu.Lock()
		p.shares = append(p.shares, b.Shares...)
		p.mu.Unlock()
		json.NewEncoder(w).Encode(p.onShares(b.Shares))
	default:
		http.NotFound(w, r)
	}
}

func newGateway(t *testing.T, p *fakePool) *Gateway {
	_, key, _ := ed25519.GenerateKey(nil)
	return New(Config{PoolURL: p.srv.URL, Key: key, RegisterFor: 2 * time.Second, RetryEvery: time.Minute})
}

func template(txs ...mining.TxData) *mining.BlockTemplate {
	t := &mining.BlockTemplate{Version: 0x20000000, PreviousBlockHash: prevHash, Bits: "1902c9b9", Height: 83361,
		CurTime: time.Now().Unix(), CoinbaseValue: 50_0000_0000, Transactions: txs}
	for _, tx := range txs {
		t.CoinbaseValue += tx.Fee
	}
	return t
}

// A transaction whose txid is really its hash, as the pool's intake checks when it asks for data.
func tx(fee int64, seed byte) mining.TxData {
	raw := []byte{0x02, 0, 0, 0, 1, seed}
	id := txidOf(raw)
	return mining.TxData{Data: hex.EncodeToString(raw), TxID: id, Fee: fee}
}

func sha256d(b []byte) []byte {
	a := sha256.Sum256(b)
	c := sha256.Sum256(a[:])
	return c[:]
}

func txidOf(raw []byte) string {
	a := sha256d(raw)
	for i, j := 0, 31; i < j; i, j = i+1, j-1 {
		a[i], a[j] = a[j], a[i]
	}
	return hex.EncodeToString(a)
}

// The registered coinbase pays the snapshot's TIDES split exactly -- checked with the same
// ParseCoinbase/PaysExactly the pool's intake uses -- and the registration says what part of it
// is this install's.
func TestRegisterPaysTheSnapshotSplit(t *testing.T) {
	p := newFakePool(t)
	p.snap.Work = map[string]float64{me: 1, other: 3}
	g := newGateway(t, p)
	reg, err := g.Register(template(tx(1000, 1)), me, []byte("/Forge Solo/"))
	if err != nil {
		t.Fatalf("TIDES-GW-REGISTER: %v", err)
	}
	cb, err := wire.ParseCoinbase(reg.Coinb1, reg.Coinb2)
	if err != nil {
		t.Fatalf("TIDES-GW-LAYOUT: the registered coinbase is not in the DATUM layout: %v", err)
	}
	want, _ := wire.Payouts(&p.snap, 50_0000_1000, me)
	if err := cb.PaysExactly(want); err != nil {
		t.Fatalf("TIDES-GW-SPLIT: %v", err)
	}
	if reg.FinderSats != 12_5000_0250 || reg.Outputs != 2 || reg.PoolJobID != "pooljob" || reg.ShareDiff != 2048 {
		t.Fatalf("TIDES-GW-REG-FIELDS: %+v", reg)
	}
	if len(p.jobs) != 1 || len(p.jobs[0].TxIDs) != 1 || p.jobs[0].Finder != me || p.jobs[0].Snapshot != 7 {
		t.Fatalf("TIDES-GW-REQUEST: %+v", p.jobs)
	}
}

// With nobody in the pool's window the first finder takes the whole block.
func TestRegisterEmptyWindowPaysTheFinder(t *testing.T) {
	p := newFakePool(t)
	g := newGateway(t, p)
	reg, err := g.Register(template(), strings.ToUpper(me), nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.jobs[0].Finder != me {
		t.Fatalf("TIDES-GW-CANONICAL: the finder went to the pool as %q, not in canonical form", p.jobs[0].Finder)
	}
	if reg.FinderSats != 50_0000_0000 || reg.Outputs != 1 {
		t.Fatalf("TIDES-GW-EMPTY-WINDOW: finder gets %d of %d outputs", reg.FinderSats, reg.Outputs)
	}
}

// "Retry shortly" (the pool's node is a block behind) is retried until the pool takes the job.
func TestRegisterRetriesWhileThePoolCatchesUp(t *testing.T) {
	p := newFakePool(t)
	p.onJob = func(n int, req wire.JobRequest) wire.JobResponse {
		if n <= 3 {
			return wire.JobResponse{Error: "the pool's node is still at height 83359", Retry: true}
		}
		return wire.JobResponse{JobID: "late", ShareDifficulty: 1024}
	}
	g := newGateway(t, p)
	reg, err := g.Register(template(), me, nil)
	if err != nil || reg.PoolJobID != "late" {
		t.Fatalf("TIDES-GW-RETRY: %v %+v", err, reg)
	}
}

// ...but never past RegisterFor: the job loop is waiting.
func TestRegisterGivesUpAtItsDeadline(t *testing.T) {
	p := newFakePool(t)
	p.onJob = func(int, wire.JobRequest) wire.JobResponse { return wire.JobResponse{Error: "behind", Retry: true} }
	g := newGateway(t, p)
	start := time.Now()
	if _, err := g.Register(template(), me, nil); err == nil {
		t.Fatal("TIDES-GW-DEADLINE: a pool that only ever says retry was treated as success")
	}
	if el := time.Since(start); el > 4*time.Second {
		t.Fatalf("TIDES-GW-DEADLINE: registration held the job loop for %s", el)
	}
}

// A transaction the pool's node has not seen: the pool asks for its data, and the gateway sends it
// within the same registration.
func TestRegisterSendsTransactionDataWhenAsked(t *testing.T) {
	p := newFakePool(t)
	x := tx(500, 2)
	p.onJob = func(n int, req wire.JobRequest) wire.JobResponse {
		switch {
		case req.TxData == nil:
			return wire.JobResponse{Error: "transaction " + x.TxID + " is unknown to the pool's node; send its data"}
		case n == 2:
			return wire.JobResponse{Error: "the pool's node took transaction " + x.TxID, Retry: true}
		default:
			return wire.JobResponse{JobID: "withdata", ShareDifficulty: 1024}
		}
	}
	g := newGateway(t, p)
	reg, err := g.Register(template(x), me, nil)
	if err != nil || reg.PoolJobID != "withdata" {
		t.Fatalf("TIDES-GW-TXDATA: %v %+v", err, reg)
	}
	if got := p.jobs[1].TxData[x.TxID]; got != x.Data {
		t.Fatalf("TIDES-GW-TXDATA: the pool was sent %q for the transaction", got)
	}
}

// A transaction the pool's node refuses (a conflicting spend): register the block without the
// transactions, paying the subsidy alone, rather than leave TIDES.
func TestRegisterDropsTransactionsThePoolRefuses(t *testing.T) {
	p := newFakePool(t)
	p.onJob = func(n int, req wire.JobRequest) wire.JobResponse {
		if len(req.TxIDs) > 0 {
			return wire.JobResponse{Error: "the pool's node refuses transaction abc: txn-mempool-conflict"}
		}
		return wire.JobResponse{JobID: "bare", ShareDifficulty: 1024}
	}
	g := newGateway(t, p)
	reg, err := g.Register(template(tx(700, 3), tx(300, 4)), me, nil)
	if err != nil || reg.PoolJobID != "bare" {
		t.Fatalf("TIDES-GW-BARE: %v %+v", err, reg)
	}
	cb, _ := wire.ParseCoinbase(reg.Coinb1, reg.Coinb2)
	if len(reg.Txs) != 0 || reg.CoinbaseSats != 50_0000_0000 || cb.Value() != 50_0000_0000 {
		t.Fatalf("TIDES-GW-BARE-VALUE: %d txs, coinbase %d / %d; want none and the subsidy alone", len(reg.Txs), reg.CoinbaseSats, cb.Value())
	}
}

// A refusal for any other reason is final: the stratum falls back to solo.
func TestRegisterRefusalIsAnError(t *testing.T) {
	p := newFakePool(t)
	p.onJob = func(int, wire.JobRequest) wire.JobResponse { return wire.JobResponse{Error: "this gateway is blocked"} }
	g := newGateway(t, p)
	if _, err := g.Register(template(), me, nil); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("TIDES-GW-REFUSED: %v", err)
	}
	if len(p.jobs) != 1 {
		t.Fatalf("TIDES-GW-REFUSED: a final refusal was re-sent %d times", len(p.jobs))
	}
}

// Snapshots are fetched when stale or for a new height, and a recent one stands in when the pool
// cannot be asked.
func TestSnapshotCaching(t *testing.T) {
	p := newFakePool(t)
	g := newGateway(t, p)
	for i := 0; i < 3; i++ {
		if _, err := g.Register(template(), me, nil); err != nil {
			t.Fatal(err)
		}
	}
	if p.snapGets != 1 {
		t.Fatalf("TIDES-GW-SNAP-CACHE: %d snapshot fetches for three registrations on one height", p.snapGets)
	}
	next := template()
	next.Height++
	if _, err := g.Register(next, me, nil); err != nil {
		t.Fatal(err)
	}
	if p.snapGets != 2 {
		t.Fatalf("TIDES-GW-SNAP-HEIGHT: a new height did not fetch a new snapshot (%d fetches)", p.snapGets)
	}
}

func TestDue(t *testing.T) {
	p := newFakePool(t)
	now := time.Now()
	_, key, _ := ed25519.GenerateKey(nil)
	g := New(Config{PoolURL: p.srv.URL, Key: key, RetryEvery: time.Minute, Now: func() time.Time { return now }})
	if !g.Due(true) || !g.Due(false) {
		t.Fatal("TIDES-GW-DUE: a starting gateway must try the pool")
	}
	g.mu.Lock()
	g.lastTry = now
	g.mu.Unlock()
	g.Fallback(nil)
	if g.Due(true) {
		t.Fatal("TIDES-GW-DUE-NEWBLOCK: a fallen-back gateway held a new block's work back to try the pool")
	}
	if g.Due(false) {
		t.Fatal("TIDES-GW-DUE-BACKOFF: a fallen-back gateway retried before RetryEvery")
	}
	now = now.Add(61 * time.Second)
	if !g.Due(false) {
		t.Fatal("TIDES-GW-DUE-RETRY: a fallen-back gateway never retries the pool")
	}
	if g.Due(true) {
		t.Fatal("TIDES-GW-DUE-NEWBLOCK: past the backoff, a fallen-back gateway held a new block's work back to try the pool")
	}
}

func share(job string, diff float64, versionBits string) *stratum.Share {
	return &stratum.Share{JobID: job, MinerID: strings.ToUpper(me), WorkerName: "rig1", ActualDiff: diff,
		ExtraNonce1: "0A0B0C0D", ExtraNonce2: "0000000000000001", NTime: "66F8A1B2", Nonce: "DEADBEEF", VersionBits: versionBits}
}

// Only shares on a TIDES job that reach the pool's difficulty go to the pool, and they go in the
// form the pool checks: one canonical miner address, the whole 12-byte extranonce, and the whole
// rolled version however little of it the miner sent.
func TestForwardOnlyWhatThePoolCredits(t *testing.T) {
	p := newFakePool(t)
	g := newGateway(t, p)
	reg := &Registration{PoolJobID: "pj", ShareDiff: 2048, PrevHash: prevHash}
	g.Track("7", reg, "20000000")

	if g.Forward("7", share("7", 2047, "")) {
		t.Fatal("TIDES-GW-FWD-DIFF: a share below the pool's difficulty was queued")
	}
	if g.Forward("8", share("8", 1e9, "")) {
		t.Fatal("TIDES-GW-FWD-SOLO: a share on a job the pool never registered was queued")
	}
	if !g.Forward("7", share("7", 2048, "2000")) {
		t.Fatal("TIDES-GW-FWD: a share at the pool's difficulty was not queued")
	}
	if n := g.Flush(); n != 1 {
		t.Fatalf("TIDES-GW-FLUSH: flushed %d shares", n)
	}
	got := p.shares[0]
	if got.JobID != "pj" || got.Miner != me || got.Worker != "rig1" || got.Extranonce != "0a0b0c0d0000000000000001" ||
		got.NTime != "66f8a1b2" || got.Nonce != "deadbeef" {
		t.Fatalf("TIDES-GW-WIRE: %+v", got)
	}
	// "2000" is two bytes: only the first byte pair is rolled, onto the job's version.
	if want := hex.EncodeToString(stratum.RollVersion("20000000", "2000")); got.Version != want || len(got.Version) != 8 {
		t.Fatalf("TIDES-GW-VERSION: sent version %q, want %q (8 hex digits)", got.Version, want)
	}
	st := g.Status()
	if st.Forwarded != 1 || st.Accepted != 1 || st.ShareDifficulty != 4096 {
		t.Fatalf("TIDES-GW-COUNTS: %+v", st)
	}
}

// Shares on a block the pool has moved past are dropped, not sent; delivery failures and the
// pool's "resend" are retried; refusals are counted.
func TestFlushDropsStaleAndRetriesFailures(t *testing.T) {
	p := newFakePool(t)
	g := newGateway(t, p)
	g.Track("1", &Registration{PoolJobID: "old", ShareDiff: 1, PrevHash: "aa"}, "20000000")
	g.Forward("1", share("1", 5, ""))
	g.Track("2", &Registration{PoolJobID: "new", ShareDiff: 1, PrevHash: "bb"}, "20000000")
	g.Forward("2", share("2", 5, ""))
	if n := g.Flush(); n != 1 || p.shares[0].JobID != "new" {
		t.Fatalf("TIDES-GW-STALE: flushed %d, first %+v", n, p.shares)
	}
	if g.Status().Dropped != 1 {
		t.Fatalf("TIDES-GW-STALE-COUNT: %+v", g.Status())
	}

	p.mu.Lock()
	p.down = true
	p.mu.Unlock()
	g.Forward("2", share("2", 6, ""))
	if g.Flush() != 0 || g.Status().Queued != 1 {
		t.Fatalf("TIDES-GW-RETRY-QUEUE: a failed delivery lost the share: %+v", g.Status())
	}
	p.mu.Lock()
	p.down = false
	p.onShares = func(b []wire.Share) wire.ShareBatchResponse {
		return wire.ShareBatchResponse{Results: []wire.ShareResult{{Error: "the pool could not record the share; resend it"}}}
	}
	p.mu.Unlock()
	g.Flush()
	if g.Status().Queued != 1 {
		t.Fatal("TIDES-GW-RESEND: a share the pool asked to be resent was dropped")
	}
	p.mu.Lock()
	p.onShares = func(b []wire.Share) wire.ShareBatchResponse {
		return wire.ShareBatchResponse{Results: []wire.ShareResult{{Error: "duplicate"}}}
	}
	p.mu.Unlock()
	g.Flush()
	if st := g.Status(); st.Queued != 0 || st.Rejected != 1 || st.LastReject != "duplicate" {
		t.Fatalf("TIDES-GW-REJECT: %+v", st)
	}
	if p.badSigSeen {
		t.Fatal("TIDES-GW-SIG: the fake pool saw a request whose signature did not verify")
	}
}

// A block goes at once, alone, ahead of the queue -- before the stratum hands it to the local node.
func TestSendBlockGoesAtOnce(t *testing.T) {
	p := newFakePool(t)
	g := newGateway(t, p)
	g.Track("9", &Registration{PoolJobID: "blk", ShareDiff: 1, PrevHash: prevHash}, "20000000")
	g.Forward("9", share("9", 5, ""))
	g.Track("10", &Registration{PoolJobID: "other-tip", ShareDiff: 1, PrevHash: "cc"}, "20000000")
	g.Forward("10", share("10", 5, ""))
	p.onShares = func(b []wire.Share) wire.ShareBatchResponse {
		r := wire.ShareBatchResponse{Results: []wire.ShareResult{{Accepted: true, Block: true}}}
		for range b[1:] {
			r.Results = append(r.Results, wire.ShareResult{Accepted: true})
		}
		return r
	}
	blk := share("9", 1e12, "")
	blk.Nonce = "00000001"
	if err := g.SendBlock("9", blk); err != nil {
		t.Fatalf("TIDES-GW-BLOCK: %v", err)
	}
	// The block first, then the share queued on the same tip; not the one on another tip.
	if len(p.shares) != 2 || p.shares[0].Nonce != "00000001" || p.shares[1].JobID != "blk" {
		t.Fatalf("TIDES-GW-BLOCK-BATCH: sent %+v", p.shares)
	}
	st := g.Status()
	if st.Blocks != 1 || st.Accepted != 2 || st.Queued != 1 {
		t.Fatalf("TIDES-GW-BLOCK-COUNTS: %+v", st)
	}
}

// A pool that restarted numbers its snapshots afresh, so the gateway's cached one is unknown
// there: fetch the pool's current snapshot and register again, instead of falling back.
func TestRegisterRefetchesTheSnapshotAfterAPoolRestart(t *testing.T) {
	p := newFakePool(t)
	g := newGateway(t, p)
	if _, err := g.Register(template(), me, nil); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.snap.Version = 1 // the pool restarted
	p.onJob = func(n int, req wire.JobRequest) wire.JobResponse {
		if req.Snapshot != 1 {
			return wire.JobResponse{Error: "unknown or expired snapshot; fetch /datum/v1/tides again"}
		}
		return wire.JobResponse{JobID: "fresh", ShareDifficulty: 1024}
	}
	p.mu.Unlock()
	reg, err := g.Register(template(), me, nil)
	if err != nil || reg.PoolJobID != "fresh" || reg.Snapshot != 1 {
		t.Fatalf("TIDES-GW-POOL-RESTART: %v %+v", err, reg)
	}
}

// Miners stay on a registered job through a missed refresh or two, never longer.
func TestKeepIsBounded(t *testing.T) {
	p := newFakePool(t)
	now := time.Now()
	_, key, _ := ed25519.GenerateKey(nil)
	g := New(Config{PoolURL: p.srv.URL, Key: key, Now: func() time.Time { return now }})
	g.Track("5", &Registration{PoolJobID: "j", PrevHash: prevHash, At: now.Add(-30 * time.Second)}, "20000000")
	if !g.Keep("5") {
		t.Fatal("TIDES-GW-KEEP: a job registered 30 s ago was not kept through a failed refresh")
	}
	now = now.Add(20 * time.Second)
	if g.Keep("5") {
		t.Fatal("TIDES-GW-KEEP-BOUND: a job registered 50 s ago was kept; a silent pool would get uncredited work until the next block")
	}
	if g.Keep("6") {
		t.Fatal("TIDES-GW-KEEP-SOLO: a job the pool never registered was treated as keepable")
	}
}

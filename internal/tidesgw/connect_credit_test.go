package tidesgw

import (
	"bufio"
	"crypto/ed25519"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/datum/wire"
	"github.com/BitcoincashII/forge-solo/internal/mining"
	"github.com/BitcoincashII/forge-solo/internal/stratum"
	"go.uber.org/zap"
)

// creditPool is a fake Forge Pool that credits as its intake does: a job's share difficulty is the
// higher of its own for this gateway and the one the coinbase commits to (at most its
// share_diff_max), and every accepted share is credited that.
type creditPool struct {
	*fakePool
	mu       sync.Mutex
	own      float64 // the pool's own share difficulty for this gateway
	top      float64 // the pool's share_diff_max; 0: none
	jobDiff  map[string]float64
	credited float64
}

func newCreditPool(t *testing.T) *creditPool {
	c := &creditPool{fakePool: newFakePool(t), own: 1024, jobDiff: map[string]float64{}}
	c.snap.Work = map[string]float64{other: 1}
	c.onJob = func(n int, req wire.JobRequest) wire.JobResponse {
		c.mu.Lock()
		defer c.mu.Unlock()
		d := c.own
		if req.ShareDiffExp != nil {
			committed := math.Ldexp(1, *req.ShareDiffExp)
			if c.top > 0 {
				committed = math.Min(committed, c.top)
			}
			d = math.Max(d, committed)
		}
		id := fmt.Sprintf("pj%d", n)
		c.jobDiff[id] = d
		return wire.JobResponse{JobID: id, ShareDifficulty: d}
	}
	c.onShares = func(batch []wire.Share) wire.ShareBatchResponse {
		c.mu.Lock()
		defer c.mu.Unlock()
		r := wire.ShareBatchResponse{ShareDifficulty: c.own}
		for _, s := range batch {
			c.credited += c.jobDiff[s.JobID]
			r.Results = append(r.Results, wire.ShareResult{Accepted: true})
		}
		return r
	}
	return c
}

func (c *creditPool) setOwn(d float64) {
	c.mu.Lock()
	c.own = d
	c.mu.Unlock()
}

func (c *creditPool) setTop(d float64) {
	c.mu.Lock()
	c.top = d
	c.mu.Unlock()
}

func (c *creditPool) total() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.credited
}

// login logs a miner in on the stratum at addr as a rig or a marketplace's health check does:
// subscribe with its user agent, then authorize.
func login(t *testing.T, addr, ua, user string) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(c, `{"id":1,"method":"mining.subscribe","params":["%s"]}`+"\n", ua)
	fmt.Fprintf(c, `{"id":2,"method":"mining.authorize","params":["%s","x"]}`+"\n", user)
	r := bufio.NewReader(c)
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("login as %s: %v", ua, err)
		}
		if strings.Contains(line, `"id":2`) {
			break
		}
	}
	c.SetReadDeadline(time.Time{})
	go io.Copy(io.Discard, r)
	return c
}

// A rental's first job was registered while only MiningRigRentals' health checks were logged in, the
// rig connecting a moment later, and at a reconnect the job was registered before the rig's first
// share. Each job committed to nothing, and the pool credited the rig's shares on it at its own 1024
// while the rig worked at 500000 and more: 0.2% of the work. A job registered while a miner is
// logged in now commits to what it was given, and is credited in full.
func TestAJobRegisteredBeforeARentalsFirstShareCreditsItsWork(t *testing.T) {
	srv := stratum.NewServer(&stratum.ServerConfig{Host: "127.0.0.1", Port: 0, MaxConnections: 16, MaxConnectionsPerIP: 16,
		ExtraNonce1Size: 4, ExtraNonce2Size: 8, MinDiff: 500000, MaxDiff: 1e12, VardiffEnabled: true, TargetShareTime: 25,
		RetargetTime: 10, IsRentalPort: true, SoloOnly: true, CreditPayoutAddress: true}, zap.NewNop(), nil)
	srv.SetSoloPayoutAddress(me)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)

	const minerDiff, shares = 500000.0, 2000
	for _, ua := range []string{"infinite-hash-proxy/probe", "bosminer-plus-tuner 0.9.3-5a7fd334"} {
		conn := login(t, srv.ListenAddr(), ua, me+".mrr")
		p := newCreditPool(t)
		_, key, _ := ed25519.GenerateKey(nil)
		g := New(Config{PoolURL: p.srv.URL, Key: key, RegisterFor: 2 * time.Second, RetryEvery: time.Minute,
			MaxDifficulty: srv.MaxDifficulty})
		reg, err := g.Register(template(), me, []byte("/Forge Solo/"))
		if err != nil {
			t.Fatal(err)
		}
		g.Track("18", reg, "20000000")
		// The rig's work on that job: shares found at 500000, as a 4.4 PH/s rig finds them.
		rng := rand.New(rand.NewSource(1))
		for i := 0; i < shares; i++ {
			sh := share("18", minerDiff/(1-rng.Float64()), "")
			sh.Nonce = fmt.Sprintf("%08x", i)
			g.Forward("18", sh)
		}
		for g.Flush() > 0 {
		}
		if got := p.total() / (shares * minerDiff); got < 0.9 {
			t.Errorf("TIDES-CONNECT-CREDIT: %s logged in when the job was registered: the pool credited %.1f%% of the rig's work on it",
				ua, 100*got)
		}
		conn.Close()
		for end := time.Now().Add(3 * time.Second); srv.CountAuthorized() != 0; time.Sleep(10 * time.Millisecond) {
			if time.Now().After(end) {
				t.Fatal("the connection did not end")
			}
		}
	}
}

// A miner that logs in after the job in flight was registered, above the miners it was registered
// for, has each share on it credited at the pool's own difficulty until the next job. Undercommitted
// says so, and the stratum asks for a new job at once; the new job is not undercommitted, and a job
// committed as high as the network difficulty allows never is, so a miner given more than that does
// not ask for a new job at every login.
func TestALoginAboveTheJobsCommitmentIsSeen(t *testing.T) {
	p := newCreditPool(t)
	_, key, _ := ed25519.GenerateKey(nil)
	max := 0.0
	g := New(Config{PoolURL: p.srv.URL, Key: key, RegisterFor: 2 * time.Second, RetryEvery: time.Minute,
		MaxDifficulty: func() float64 { return max }})
	track := func(id string, tmpl func(...mining.TxData) *mining.BlockTemplate) {
		t.Helper()
		reg, err := g.Register(tmpl(), me, []byte("/Forge Solo/"))
		if err != nil {
			t.Fatal(err)
		}
		g.Track(id, reg, "20000000")
	}

	track("18", template) // no miner logged in: committed to nothing
	if g.Undercommitted("18") {
		t.Error("TIDES-UNDER-NONE: with no miner a job was said to be undercommitted")
	}
	max = 500000 // a rental logs in
	if !g.Undercommitted("18") {
		t.Error("TIDES-UNDER-LOGIN: a rental at 500000 logged in on a job credited 1024 a share, and it was not seen")
	}
	track("19", template)
	if g.Undercommitted("19") {
		t.Error("TIDES-UNDER-AFTER: the job registered for the rental is still said to be undercommitted")
	}

	// The pool's own difficulty already covers the rental.
	p.setOwn(1 << 22)
	max = 0
	track("20", template)
	max = 500000
	if g.Undercommitted("20") {
		t.Error("TIDES-UNDER-POOL: a job the pool credits at 2^22 was said to be undercommitted for a miner at 500000")
	}

	// A block whose difficulty is about 2^18: the job commits to 2^18, the most it may, while the
	// rental at 500000 would want 2^20.
	p.setOwn(1024)
	track("21", func(...mining.TxData) *mining.BlockTemplate { b := template(); b.Bits = "1b003fff"; return b })
	if g.Undercommitted("21") {
		t.Error("TIDES-UNDER-CAP: a job committed at the network difficulty's cap is said to be undercommitted: " +
			"a miner above the cap would ask for a new job at every login")
	}
	// A pool that credits no more than 2^16 whatever the job commits to: a new job would get no more.
	p.setTop(1 << 16)
	track("22", template)
	if g.Undercommitted("22") {
		t.Error("TIDES-UNDER-POOLCAP: a job committed to 2^20 that the pool credits at its 2^16 cap is said to be " +
			"undercommitted: every login would ask for a new job")
	}
	if g.Undercommitted("nope") {
		t.Error("TIDES-UNDER-UNKNOWN: a job the pool never registered was said to be undercommitted")
	}
}

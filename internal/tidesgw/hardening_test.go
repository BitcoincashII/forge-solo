package tidesgw

import (
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/cashaddr"
	"github.com/BitcoincashII/forge-solo/internal/datum/wire"
	"github.com/BitcoincashII/forge-solo/internal/mining"
)

// A slow pool holds the job loop, where a new block's work waits, for RegisterFor at most -- its
// fallbacks included. It held it for 10 s when honest but slow and 40 s when hostile.
func TestRegisterNeverOutlastsItsDeadline(t *testing.T) {
	within := func(code string, p *fakePool) {
		t.Helper()
		g := newGateway(t, p) // RegisterFor 2 s
		start := time.Now()
		_, err := g.Register(template(), me, nil)
		if el := time.Since(start); err == nil || el > 2600*time.Millisecond {
			t.Fatalf("%s: registration held the job loop for %s and gave %v", code, el.Round(time.Millisecond), err)
		}
	}

	slowSnapshot := newFakePool(t)
	slowSnapshot.delay = 3 * time.Second
	within("TIDES1-SNAPSHOT-DEADLINE", slowSnapshot)

	// The pool takes the job in the end, after the deadline.
	slowJob := newFakePool(t)
	slowJob.onJob = func(int, wire.JobRequest) wire.JobResponse {
		time.Sleep(3 * time.Second)
		return wire.JobResponse{JobID: "late", ShareDifficulty: 2048}
	}
	within("TIDES1-DEADLINE", slowJob)

	// A fast "start over" answer, then slow ones: the fallback gets what is left, not a deadline
	// of its own.
	restarted := newFakePool(t)
	restarted.onJob = func(n int, _ wire.JobRequest) wire.JobResponse {
		if n == 1 {
			return wire.JobResponse{Error: "unknown or expired snapshot"}
		}
		time.Sleep(1800 * time.Millisecond)
		return wire.JobResponse{Error: "behind", Retry: true}
	}
	within("TIDES1-FALLBACK-DEADLINE", restarted)
}

// What the pool sends is bounded: its snapshot's size, its split's outputs, and the block it makes.
func TestThePoolsSplitIsBounded(t *testing.T) {
	p := newFakePool(t)
	p.snapBody = []byte(`{"version":7,"height":83361,"pad":"` + strings.Repeat("x", 5<<20) + `"}`)
	g := newGateway(t, p)
	if _, err := g.Register(template(), me, nil); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("TIDES2-SNAPSHOT-SIZE: a 5 MB snapshot gave %v", err)
	}

	q := newFakePool(t)
	for i := 0; i < maxCoinbaseOutputs+500; i++ {
		var h [20]byte
		h[0], h[1], h[2] = byte(i), byte(i>>8), 0x5a
		q.snap.Work[cashaddr.Encode(cashaddr.MainnetPrefix, cashaddr.P2PKH, h)] = 1
	}
	g = newGateway(t, q)
	if _, err := g.Register(template(), me, nil); err == nil || !strings.Contains(err.Error(), "outputs") {
		t.Fatalf("TIDES2-OUTPUTS: a split of %d outputs gave %v", maxCoinbaseOutputs+500, err)
	}

	// Coinbase plus transactions past the block size limit: the block goes without them.
	old := maxBlockBytes
	maxBlockBytes = 1500
	t.Cleanup(func() { maxBlockBytes = old })
	r := newFakePool(t)
	r.snap.Work = map[string]float64{me: 1}
	g = newGateway(t, r)
	raw := make([]byte, 2000)
	raw[0] = 0x02
	big := mining.TxData{Data: hex.EncodeToString(raw), TxID: txidOf(raw), Fee: 700}
	reg, err := g.Register(template(big), me, nil)
	if err != nil || len(reg.Txs) != 0 || reg.CoinbaseSats != 50_0000_0000 {
		t.Fatalf("TIDES2-BLOCK-SIZE: a block that would not fit gave %v %+v", err, reg)
	}
}

// A redirect is never followed: the pool's address was checked to be https, and a redirect could
// take the request, signed headers and all, anywhere -- plain http too.
func TestRedirectsAreNotFollowed(t *testing.T) {
	var hits atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer elsewhere.Close()
	p := newFakePool(t)
	p.redirectTo = elsewhere.URL + "/datum/v1/tides"
	g := newGateway(t, p)
	if _, err := g.Register(template(), me, nil); err == nil {
		t.Fatal("TIDES3-REDIRECT: a redirected snapshot was taken")
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("TIDES3-REDIRECT: the redirect was followed (%d requests elsewhere)", n)
	}
}

// The pool's reason for refusing a share is shown as one short line, whatever it sends.
func TestThePoolsRejectTextIsClipped(t *testing.T) {
	g := newGateway(t, newFakePool(t))
	g.mu.Lock()
	g.tally([]queued{{}}, &wire.ShareBatchResponse{Results: []wire.ShareResult{{Error: strings.Repeat("e", 1<<20)}}})
	g.mu.Unlock()
	if got := g.Status().LastReject; len([]rune(got)) > maxReason+1 {
		t.Fatalf("TIDES5-REJECT-TEXT: the dashboard is sent a %d-character reason", len([]rune(got)))
	}
}

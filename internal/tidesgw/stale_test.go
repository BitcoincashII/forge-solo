package tidesgw

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/datum/wire"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// While this node catches up with the chain, the pool's snapshot names a height above the
// template's, and the pool would refuse the template as stale. The gateway says so without asking.
func TestRegisterAsksNothingWhileThePoolIsAhead(t *testing.T) {
	p := newFakePool(t)
	p.snap.Height = 84117
	g := newGateway(t, p)
	tm := template()
	tm.Height = 83945
	_, err := g.Register(tm, me, nil)
	var stale *StaleError
	if !errors.As(err, &stale) || stale.PoolHeight != 84117 || stale.Height != 83945 {
		t.Fatalf("TIDES-GW-BEHIND: %v", err)
	}
	if len(p.jobs) != 0 {
		t.Fatalf("TIDES-GW-BEHIND-NO-POST: the pool was asked to register %d jobs it would refuse", len(p.jobs))
	}
	if want := "this BCH2 node is catching up with the chain: it is at block 83944, Forge Pool at 84116"; err.Error() != want {
		t.Fatalf("TIDES-GW-BEHIND-WORDS: %q, want %q", err, want)
	}
}

// When the pool's snapshot is behind its node, the gateway asks, and the pool refuses the template
// as stale naming the height it mines, which the gateway reads. An answer that names none is still
// stale; any other refusal is not.
func TestAStaleRefusalNamesThePoolsHeight(t *testing.T) {
	p := newFakePool(t)
	answer := "stale template: the pool mines height 83362 on " + prevHash
	p.onJob = func(int, wire.JobRequest) wire.JobResponse { return wire.JobResponse{Error: answer} }
	g := newGateway(t, p)
	_, err := g.Register(template(), me, nil)
	var stale *StaleError
	if !errors.As(err, &stale) || len(p.jobs) != 1 {
		t.Fatalf("TIDES-GW-STALE-KIND: %v (%d registrations)", err, len(p.jobs))
	}
	if stale.PoolHeight != 83362 || stale.waitFor() != 83362 {
		t.Fatalf("TIDES-GW-STALE-READ: %+v", stale)
	}
	answer = "stale template: something new"
	_, err = g.Register(template(), me, nil)
	if !errors.As(err, &stale) || stale.PoolHeight != 0 || stale.waitFor() != 83361 || err.Error() != answer {
		t.Fatalf("TIDES-GW-STALE-UNREAD: %v", err)
	}
	answer = "this gateway is blocked"
	if _, err = g.Register(template(), me, nil); errors.As(err, &stale) || err.Error() != answer {
		t.Fatalf("TIDES-GW-STALE-ONLY: %v", err)
	}
	if got := (&StaleError{PoolHeight: 1001, Height: 1001}).Error(); got != "this BCH2 node and Forge Pool have different blocks at height 1000" {
		t.Fatalf("TIDES-GW-STALE-SIBLING: %q", got)
	}
}

// Fallen back because this node was behind the pool, the gateway tries the first new block whose
// template reaches the height the pool mines, and none below it. Any other fall back after that
// clears it: a pool that timed out must not hold a new block's work back again.
func TestDueAfterFallingBackBehindThePool(t *testing.T) {
	p := newFakePool(t)
	now := time.Now()
	_, key, _ := ed25519.GenerateKey(nil)
	g := New(Config{PoolURL: p.srv.URL, Key: key, RetryEvery: time.Minute, Now: func() time.Time { return now }})
	g.mu.Lock()
	g.lastTry = now
	g.mu.Unlock()
	behind := &StaleError{PoolHeight: 84117, Height: 83945}

	g.Fallback(behind)
	if g.DueAt(true, 84116) {
		t.Fatal("TIDES-GW-WAIT-BELOW: a block below the pool's height was tried")
	}
	if !g.DueAt(true, 84117) || !g.DueAt(true, 84200) {
		t.Fatal("TIDES-GW-WAIT-AT: the block that reached the pool's height was not tried")
	}
	if g.Due(true) || g.DueAt(false, 84117) {
		t.Fatal("TIDES-GW-WAIT-BACKOFF: without a new block at the pool's height, the pool was tried before RetryEvery")
	}
	g.Note(errors.New("a refresh failed"))
	if !g.DueAt(true, 84117) {
		t.Fatal("TIDES-GW-WAIT-NOTE: a failed refresh cleared the wait for the pool's height")
	}
	g.Fallback(errors.New("Post \"https://pool.bch2.org/datum/v1/jobs\": context deadline exceeded"))
	if g.DueAt(true, 84117) {
		t.Fatal("TIDES-GW-WAIT-ASSIGN: after a pool that timed out, a new block's work was held back to try it")
	}
	now = now.Add(61 * time.Second)
	if !g.Due(false) {
		t.Fatal("TIDES-GW-WAIT-RETRY: a fallen-back gateway no longer retries the pool")
	}
}

// The log and the dashboard say this node is behind the pool, not that the pool is unavailable,
// once; a pool that then stops answering is said to be unavailable.
func TestFallingBackBehindThePoolSaysSo(t *testing.T) {
	p := newFakePool(t)
	core, logs := observer.New(zap.InfoLevel)
	_, key, _ := ed25519.GenerateKey(nil)
	g := New(Config{PoolURL: p.srv.URL, Key: key, Logger: zap.New(core)})
	g.Fallback(&StaleError{PoolHeight: 84117, Height: 83945})
	if st := g.Status(); st.State != StateFallback || st.Reason != "this BCH2 node is catching up with the chain: it is at block 83944, Forge Pool at 84116" {
		t.Fatalf("TIDES-GW-WAIT-REASON: %s (%s)", st.State, st.Reason)
	}
	if logs.FilterMessageSnippet("unavailable").Len() != 0 || logs.FilterMessageSnippet("not on Forge Pool's block yet").Len() != 1 {
		t.Fatalf("TIDES-GW-WAIT-LOG: %v", logs.All())
	}
	g.Fallback(&StaleError{PoolHeight: 84118, Height: 84117})
	if logs.Len() != 1 {
		t.Fatalf("TIDES-GW-WAIT-LOG-ONCE: %d lines", logs.Len())
	}
	g.Fallback(errors.New("the pool did not answer"))
	if logs.FilterMessageSnippet("Forge Pool unavailable").Len() != 1 || !strings.Contains(g.Status().Reason, "did not answer") {
		t.Fatalf("TIDES-GW-WAIT-LOG-KIND: %v", logs.All())
	}

	// Forge Gateway with pool_only turns its miners away meanwhile, and says why.
	core, logs = observer.New(zap.InfoLevel)
	g = New(Config{PoolURL: p.srv.URL, Key: key, Logger: zap.New(core), PoolOnly: true})
	g.Fallback(&StaleError{PoolHeight: 84117, Height: 83945})
	if logs.FilterMessageSnippet("not on Forge Pool's block yet; miners are turned away until it is (pool_only)").Len() != 1 {
		t.Fatalf("TIDES-GW-WAIT-POOL-ONLY: %v", logs.All())
	}
}

// The dashboard is told when TIDES fell back because this node is not on the pool's block, so it
// does not say the pool is unavailable; and only then.
func TestStatusSaysWhenThisNodeIsBehind(t *testing.T) {
	g := newGateway(t, newFakePool(t))
	g.Fallback(&StaleError{PoolHeight: 84117, Height: 83945})
	st := g.Status()
	b, _ := json.Marshal(st)
	if !st.NodeBehind || !strings.Contains(string(b), `"node_behind":true`) {
		t.Fatalf("TIDES-GW-STATUS-BEHIND: %s", b)
	}
	g.Track("1", &Registration{PoolJobID: "pj1", Height: 84117, PrevHash: prevHash, At: time.Now()}, "20000000")
	if st := g.Status(); st.State != StateActive || st.NodeBehind {
		t.Fatalf("TIDES-GW-STATUS-ACTIVE: %+v", st)
	}
	g.Fallback(errors.New("the pool did not answer"))
	st = g.Status()
	b, _ = json.Marshal(st)
	if st.NodeBehind || strings.Contains(string(b), "node_behind") {
		t.Fatalf("TIDES-GW-STATUS-POOL: a pool that did not answer is shown as this node behind: %s", b)
	}
}

// Only a snapshot just fetched from the pool says the pool is ahead. When the pool cannot be asked,
// the snapshot kept from before may name a height it has since left; the gateway then asks the pool
// as before, and does not call this node behind.
func TestOnlyAFreshSnapshotSaysThePoolIsAhead(t *testing.T) {
	p := newFakePool(t)
	p.snap.Height = 84117
	g := newGateway(t, p)
	tm := template()
	tm.Height = 84117
	if _, err := g.Register(tm, me, nil); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.down = true
	p.mu.Unlock()
	tm.Height = 84000
	_, err := g.Register(tm, me, nil)
	var stale *StaleError
	if err == nil || errors.As(err, &stale) {
		t.Fatalf("TIDES-GW-BEHIND-ONLY-ASKED: with the pool down, a kept snapshot made this node behind: %v", err)
	}
}

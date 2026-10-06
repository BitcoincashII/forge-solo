package main

import (
	"strings"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/tidesgw"
)

// After a restart with this node behind the chain, the first registration carried a template the
// pool had moved past. The pool refused it as stale, and the gateway took that for a pool it could
// not reach: "Forge Pool unavailable", and solo for a whole minute, though the node caught up within
// two seconds (live: TIDES came back 59.5 s after the node reached the tip). Now nothing is
// registered while the pool's snapshot is ahead of the template, the reason says the node is
// catching up, and the template that reaches the pool's height is registered at once.
func TestJobLoopTIDESComesBackWhenTheNodeCatchesUp(t *testing.T) {
	r := newLoopRig(t, loopRigOpts{tip: 83944, tides: true})
	r.pool.mines(84116)

	r.step(false)
	first := r.sent("JOBLOOP-STALE-FIRST", 4*time.Second)
	if first.job.Tides || !first.clean || first.job.Height != 83945 {
		t.Fatalf("JOBLOOP-STALE-FIRST: clean %v, job %+v", first.clean, first.job)
	}
	behind := r.gw.Status()
	for _, tip := range []int64{83980, 84030, 84090, 84115} {
		r.block(tip)
		if n := r.sent("JOBLOOP-STALE-CATCHING-UP", 4*time.Second); n.job.Tides || !n.clean || n.job.Height != tip+1 {
			t.Fatalf("JOBLOOP-STALE-CATCHING-UP: at %d, clean %v, job %+v", tip, n.clean, n.job)
		}
	}
	r.block(84116)
	n := r.sent("JOBLOOP-STALE-RESUME", 4*time.Second)
	if st := r.gw.Status(); !n.job.Tides || !n.clean || n.job.Height != 84117 || st.State != tidesgw.StateActive {
		t.Fatalf("JOBLOOP-STALE-RESUME: at the pool's height the miners got %+v (clean %v), the gateway is %s (%s); want TIDES at once",
			n.job, n.clean, st.State, st.Reason)
	}
	if regs := r.pool.registered(); len(regs) != 1 || regs[0].Height != 84117 {
		t.Fatalf("JOBLOOP-STALE-NO-POST: the pool was asked to register %d jobs, want only the one at 84117", len(regs))
	}
	if g := r.pool.gets(); g > 2 {
		t.Fatalf("JOBLOOP-STALE-GETS: %d snapshot fetches for one catch-up", g)
	}
	if behind.State != tidesgw.StateFallback || !strings.Contains(behind.Reason, "catching up") ||
		!strings.Contains(behind.Reason, "83944") || !strings.Contains(behind.Reason, "84116") {
		t.Fatalf("JOBLOOP-STALE-REASON: while the node caught up the gateway was %s (%s)", behind.State, behind.Reason)
	}
	if r.logged("Forge Pool unavailable") || !r.logged("not on Forge Pool's block yet") {
		t.Fatal("JOBLOOP-STALE-WORDING: the log blames the pool, or does not say this node is behind it")
	}
	r.shareOn(n.job, 1)
	if got := r.pool.credited(); len(got) != 1 || got[0].JobID != r.gw.Registered(n.job.ID).PoolJobID {
		t.Fatalf("JOBLOOP-STALE-CREDIT: the pool was sent %+v", got)
	}
}

// The pool's snapshot can be a block behind its node: the gateway asks, and the pool refuses the
// template as stale, naming the height it mines. TIDES comes back with the first template at that
// height, not a minute later.
func TestJobLoopTIDESComesBackAfterAStaleRefusal(t *testing.T) {
	r := newLoopRig(t, loopRigOpts{tip: 1000, tides: true})
	r.pool.mines(1001)
	r.pool.set(func(p *loopPool) { p.snapHeight = 1001 })
	r.step(false)
	if n := r.sent("JOBLOOP-REFUSED-FIRST", 4*time.Second); n.job.Tides || n.job.Height != 1001 {
		t.Fatalf("JOBLOOP-REFUSED-FIRST: %+v", n.job)
	}
	refused := r.gw.Status()
	r.block(1001)
	if n := r.sent("JOBLOOP-REFUSED-RESUME", 4*time.Second); !n.job.Tides || !n.clean || n.job.Height != 1002 {
		t.Fatalf("JOBLOOP-REFUSED-RESUME: clean %v, job %+v, gateway %s", n.clean, n.job, r.gw.Status().State)
	}
	if !strings.Contains(refused.Reason, "catching up") {
		t.Fatalf("JOBLOOP-REFUSED-REASON: %s (%s)", refused.State, refused.Reason)
	}
	if regs := r.pool.registered(); len(regs) != 2 {
		t.Fatalf("JOBLOOP-REFUSED-REGS: %d registrations, want 2", len(regs))
	}
}

// This node and the pool can have different blocks at the same height (two found at once): the pool
// refuses the template as stale. The next block settles it, and TIDES comes back with it.
func TestJobLoopTIDESComesBackAfterADifferentBlockAtTheSameHeight(t *testing.T) {
	r := newLoopRig(t, loopRigOpts{tip: 1000, tides: true})
	r.pool.set(func(p *loopPool) { p.prev = strings.Repeat("ab", 32) })
	r.step(false)
	if n := r.sent("JOBLOOP-SIBLING-FIRST", 4*time.Second); n.job.Tides {
		t.Fatalf("JOBLOOP-SIBLING-FIRST: %+v", n.job)
	}
	refused := r.gw.Status()
	r.pool.mines(1001)
	r.block(1001)
	if n := r.sent("JOBLOOP-SIBLING-RESUME", 4*time.Second); !n.job.Tides || !n.clean || n.job.Height != 1002 {
		t.Fatalf("JOBLOOP-SIBLING-RESUME: clean %v, job %+v, gateway %s", n.clean, n.job, r.gw.Status().State)
	}
	if !strings.Contains(refused.Reason, "different blocks at height 1000") {
		t.Fatalf("JOBLOOP-SIBLING-REASON: %s (%s)", refused.State, refused.Reason)
	}
}

// A node that stays behind the pool (stuck, or the pool on a chain it will not follow): the miners
// mine solo all the while, with the periodic job as usual; the gateway asks the pool again once a
// minute, as for any fall back, fetching its snapshot and registering nothing while it is ahead.
func TestJobLoopANodeStuckBehindThePoolKeepsMiningSolo(t *testing.T) {
	r := newLoopRig(t, loopRigOpts{tip: 1000, tides: true})
	r.pool.mines(5000)
	r.step(false)
	if n := r.sent("JOBLOOP-STUCK-FIRST", 4*time.Second); n.job.Tides {
		t.Fatalf("JOBLOOP-STUCK-FIRST: %+v", n.job)
	}
	gets := r.pool.gets()
	for i := 0; i < 3; i++ {
		r.clock.Add(periodicJobEvery)
		r.step(false)
		if n := r.sent("JOBLOOP-STUCK-PERIODIC", 4*time.Second); n.job.Tides || n.clean {
			t.Fatalf("JOBLOOP-STUCK-PERIODIC: clean %v, job %+v", n.clean, n.job)
		}
	}
	if g, regs := r.pool.gets(), r.pool.registered(); g != gets || len(regs) != 0 {
		t.Fatalf("JOBLOOP-STUCK-QUIET: %d more snapshot fetches and %d registrations in 45 s behind the pool", g-gets, len(regs))
	}
	r.clock.Add(20 * time.Second) // a minute since the gateway last asked
	r.step(false)
	if n := r.sent("JOBLOOP-STUCK-RETRY-JOB", 4*time.Second); n.job.Tides {
		t.Fatalf("JOBLOOP-STUCK-RETRY-JOB: %+v", n.job)
	}
	if g, regs := r.pool.gets(), r.pool.registered(); g != gets+1 || len(regs) != 0 {
		t.Fatalf("JOBLOOP-STUCK-RETRY: the retry made %d snapshot fetches and %d registrations, want 1 and 0", g-gets, len(regs))
	}
	if st := r.gw.Status(); st.State != tidesgw.StateFallback || !strings.Contains(st.Reason, "catching up") {
		t.Fatalf("JOBLOOP-STUCK-REASON: %s (%s)", st.State, st.Reason)
	}
}

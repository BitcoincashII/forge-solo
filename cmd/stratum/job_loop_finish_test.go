package main

import (
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/stats"
)

// awaitAnswer is the pool's answer to registration p.
func awaitAnswer(t *testing.T, p *registration) poolAnswer {
	t.Helper()
	select {
	case a := <-p.answer:
		return a
	case <-time.After(5 * time.Second):
		t.Fatal("the pool did not answer the registration")
		return poolAnswer{}
	}
}

// The pool's answer for a block the miners have left is thrown away: no job is made of it, and
// the gateway never tracks it. Tracked, it would have had the gateway drop every share on the
// miners' new block as stale until the next job.
func TestJobLoopThrowsAwayAnAnswerForABlockTheMinersLeft(t *testing.T) {
	r := newLoopRig(t, loopRigOpts{tip: 1000, tides: true})
	r.step(false)
	r.sent("JOBLOOP-FINISH-FIRST", 3*time.Second)

	before, err := jobManager.GetBlockTemplate()
	if err != nil {
		t.Fatal(err)
	}
	p := r.loop.register(r.gw, before, false) // a refresh on block 1000
	a := awaitAnswer(t, p)
	if a.err != nil {
		t.Fatalf("JOBLOOP-FINISH-REGISTERED: %v", a.err)
	}
	r.pool.mines(1001)
	r.block(1001) // the block came before the loop took the answer
	nb := r.sent("JOBLOOP-FINISH-BLOCK", 3*time.Second)
	if !nb.job.Tides || nb.job.Height != 1002 {
		t.Fatalf("JOBLOOP-FINISH-BLOCK: %+v", nb.job)
	}

	r.loop.pending = p
	r.loop.finish(a)
	r.quiet("JOBLOOP-FINISH-TIP", 300*time.Millisecond)
	if got := getCurrentJob(); got.ID != nb.job.ID {
		t.Fatalf("JOBLOOP-FINISH-TIP: the current job is %s, not the block's %s", got.ID, nb.job.ID)
	}
	r.shareOn(nb.job, 1)
	if got := r.pool.credited(); len(got) != 1 || got[0].JobID != r.gw.Registered(nb.job.ID).PoolJobID {
		t.Fatalf("JOBLOOP-FINISH-TRACKED: the pool was sent %+v", got)
	}
}

// An answer that comes after a switch to solo is thrown away too: miners stay on solo work.
func TestJobLoopThrowsAwayAnAnswerAfterASwitchToSolo(t *testing.T) {
	r := newLoopRig(t, loopRigOpts{tip: 1000, tides: true})
	r.step(false)
	r.sent("JOBLOOP-FINISH-MODE-FIRST", 3*time.Second)
	payoutModeVal.Store(stats.PayoutModeSolo)
	r.step(false)
	if n := r.sent("JOBLOOP-FINISH-MODE-SOLO", 3*time.Second); n.job.Tides || !n.clean {
		t.Fatalf("JOBLOOP-FINISH-MODE-SOLO: %+v", n.job)
	}
	tmpl, err := jobManager.GetBlockTemplate()
	if err != nil {
		t.Fatal(err)
	}
	p := r.loop.register(r.gw, tmpl, false)
	a := awaitAnswer(t, p)
	r.loop.finish(a)
	r.quiet("JOBLOOP-FINISH-MODE", 300*time.Millisecond)
	if getCurrentJob().Tides {
		t.Fatal("JOBLOOP-FINISH-MODE: a TIDES job replaced the solo one after the switch to solo")
	}
}

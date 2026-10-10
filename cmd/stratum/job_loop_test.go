package main

import (
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/tidesgw"
	"go.uber.org/zap"
)

// On a healthy node in solo mode the loop sends a job at the first poll, nothing more while the
// tip stands still, the periodic job after 15 s without making miners drop their work, and a new
// block's job at once, with clean_jobs. Both ports get every job.
func TestJobLoopSoloOnAHealthyNode(t *testing.T) {
	r := newLoopRig(t, loopRigOpts{tip: 1000})

	r.step(false)
	first := r.sent("JOBLOOP-SOLO-FIRST", 2*time.Second)
	if !first.clean || first.job.Height != 1001 || first.job.OriginalPrevHash != chainHash(1000) || first.job.Tides ||
		first.job.PayTo != r.payout {
		t.Fatalf("JOBLOOP-SOLO-FIRST: clean %v, job %+v", first.clean, first.job)
	}
	r.step(false)
	r.step(true) // a second notice of the same block
	r.quiet("JOBLOOP-SOLO-SAME-TIP", 300*time.Millisecond)

	r.clock.Add(periodicJobEvery)
	r.step(false)
	if p := r.sent("JOBLOOP-SOLO-PERIODIC", 2*time.Second); p.clean || p.job.Height != 1001 || p.job.Tides {
		t.Fatalf("JOBLOOP-SOLO-PERIODIC: clean %v, job %+v", p.clean, p.job)
	}

	r.block(1001)
	if nb := r.sent("JOBLOOP-SOLO-NEW-BLOCK", 2*time.Second); !nb.clean || nb.job.Height != 1002 || nb.job.OriginalPrevHash != chainHash(1001) {
		t.Fatalf("JOBLOOP-SOLO-NEW-BLOCK: clean %v, job %+v", nb.clean, nb.job)
	}
	r.step(false)
	r.quiet("JOBLOOP-SOLO-COUNT", 300*time.Millisecond)
}

// With a healthy pool in TIDES mode: every job is registered with the pool first, one registration
// per new block and per periodic job; a new block's job has clean_jobs and the periodic one does
// not; the gateway is active; and a share on the miners' job is credited to the payout address
// under the pool's job, while one on the job before the block is not sent.
func TestJobLoopTIDESWithAHealthyPool(t *testing.T) {
	r := newLoopRig(t, loopRigOpts{tip: 1000, tides: true})

	r.step(false)
	first := r.sent("JOBLOOP-TIDES-FIRST", 3*time.Second)
	if !first.clean || !first.job.Tides || first.job.Height != 1001 || first.job.OriginalPrevHash != chainHash(1000) {
		t.Fatalf("JOBLOOP-TIDES-FIRST: clean %v, job %+v", first.clean, first.job)
	}
	if st := r.gw.Status(); st.State != tidesgw.StateActive {
		t.Fatalf("JOBLOOP-TIDES-ACTIVE: the gateway is %s (%s)", st.State, st.Reason)
	}
	r.step(false)
	r.quiet("JOBLOOP-TIDES-SAME-TIP", 300*time.Millisecond)
	if n := len(r.pool.registered()); n != 1 {
		t.Fatalf("JOBLOOP-TIDES-REGS: %d registrations for one job", n)
	}

	r.clock.Add(periodicJobEvery)
	r.step(false)
	if p := r.sent("JOBLOOP-TIDES-PERIODIC", 3*time.Second); p.clean || !p.job.Tides || p.job.Height != 1001 {
		t.Fatalf("JOBLOOP-TIDES-PERIODIC: clean %v, job %+v", p.clean, p.job)
	}
	if n := len(r.pool.registered()); n != 2 {
		t.Fatalf("JOBLOOP-TIDES-REGS: %d registrations for two jobs", n)
	}

	r.pool.mines(1001)
	r.block(1001)
	nb := r.sent("JOBLOOP-TIDES-NEW-BLOCK", 3*time.Second)
	if !nb.clean || !nb.job.Tides || nb.job.Height != 1002 || nb.job.OriginalPrevHash != chainHash(1001) {
		t.Fatalf("JOBLOOP-TIDES-NEW-BLOCK: clean %v, job %+v", nb.clean, nb.job)
	}
	regs := r.pool.registered()
	if len(regs) != 3 || regs[2].Height != 1002 || regs[2].PrevHash != chainHash(1001) {
		t.Fatalf("JOBLOOP-TIDES-REGS: %d registrations, the last %+v", len(regs), regs[len(regs)-1])
	}
	r.step(false)
	r.quiet("JOBLOOP-TIDES-COUNT", 300*time.Millisecond)

	r.shareOn(first.job, 1) // on the block before: not sent
	r.shareOn(nb.job, 2)
	want, _ := tidesgw.CanonicalAddress(r.payout)
	if got := r.pool.credited(); len(got) != 1 || got[0].JobID != "pj3" || got[0].Miner != want {
		t.Fatalf("JOBLOOP-TIDES-CREDIT: the pool was sent %+v, want one share on pj3 for %s", got, want)
	}
}

// main runs the loop as run: it takes ZMQ notices and the poll until shutdown, logs each notice, and
// logs them again on the poll once a catch-up's quiet is over.
func TestJobLoopRunsOnZMQAndThePoll(t *testing.T) {
	r := newLoopRig(t, loopRigOpts{tip: 1000})
	tick, blocks, stop, done := make(chan time.Time), make(chan string, 10), make(chan struct{}), make(chan struct{})
	r.loop.tick, r.loop.blocks, r.loop.stop = tick, blocks, stop
	go func() {
		defer close(done)
		r.loop.run()
	}()
	tick <- time.Now()
	if n := r.sent("JOBLOOP-RUN-POLL", 2*time.Second); !n.clean || n.job.Height != 1001 {
		t.Fatalf("JOBLOOP-RUN-POLL: %+v", n.job)
	}
	r.node.setTip(1001)
	blocks <- chainHash(1001)
	if n := r.sent("JOBLOOP-RUN-ZMQ", 2*time.Second); !n.clean || n.job.Height != 1002 {
		t.Fatalf("JOBLOOP-RUN-ZMQ: %+v", n.job)
	}
	if !r.logged("New block job broadcast") || r.logs.FilterField(zap.String("source", "ZMQ")).Len() != 1 {
		t.Fatal("JOBLOOP-RUN-SOURCE: the new block's job is not logged as coming from ZMQ")
	}
	if r.logs.FilterMessage("⚡ ZMQ triggered job refresh").Len() != 1 {
		t.Fatal("JOBLOOP-RUN-ZMQ-LOGGED: the ZMQ notice is not logged")
	}
	quietNotices.Store(true) // as a catch-up leaves it
	r.clock.Add(catchUpEvery)
	tick <- time.Now()
	waitUntil(t, 2*time.Second, "JOBLOOP-RUN-ZMQ-AGAIN", func() bool { return !quietNotices.Load() })
	close(stop)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("JOBLOOP-RUN-STOP: the loop did not stop at shutdown")
	}
}

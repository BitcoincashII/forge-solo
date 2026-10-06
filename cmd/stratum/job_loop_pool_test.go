package main

import (
	"strings"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/tidesgw"
)

// A pool that stopped answering held a new block's work for up to 12 s while the miners hashed on
// the block before: up to 6 s behind a periodic refresh already waiting for the pool, then up to
// 6 s for the block's own registration. The refresh now runs beside the loop and is given up when
// a block comes, and the block's work goes out solo once it has waited about 2 s.
func TestJobLoopANewBlockNeverWaitsLongForAHungPool(t *testing.T) {
	r := newLoopRig(t, loopRigOpts{tip: 1000, tides: true})
	r.run()
	r.poll()
	if n := r.sent("JOBLOOP-HANG-FIRST", 3*time.Second); !n.job.Tides {
		t.Fatalf("JOBLOOP-HANG-FIRST: %+v", n.job)
	}

	r.pool.set(func(p *loopPool) { p.hang = true })
	r.clock.Add(periodicJobEvery)
	r.poll() // the periodic refresh, which the pool never answers
	waitUntil(t, 2*time.Second, "JOBLOOP-HANG-REFRESH", func() bool { now, _ := r.pool.waiting(); return now == 1 })
	for i := 0; i < 3; i++ {
		r.poll()
		time.Sleep(50 * time.Millisecond)
	}
	r.quiet("JOBLOOP-HANG-KEEPS", 200*time.Millisecond) // miners keep the job they have meanwhile
	if n := len(r.pool.registered()); n != 2 {
		t.Fatalf("JOBLOOP-HANG-ONE-AT-A-TIME: %d registrations while one refresh waited for the pool, want 2", n)
	}

	r.pool.mines(1001)
	start := time.Now()
	r.zmq(1001)
	n := r.sent("JOBLOOP-HANG-NEW-BLOCK", 15*time.Second)
	if took := n.at.Sub(start); took > 3*time.Second {
		t.Fatalf("JOBLOOP-HANG-WAITED: the new block's work reached the miners %s after the block", took.Round(10*time.Millisecond))
	}
	if !n.clean || n.job.Tides || n.job.Height != 1002 {
		t.Fatalf("JOBLOOP-HANG-SOLO: clean %v, job %+v", n.clean, n.job)
	}
	// The refresh for the block before is given up, not left to answer later.
	waitUntil(t, time.Second, "JOBLOOP-HANG-DROPPED", func() bool { _, given := r.pool.waiting(); return given >= 1 })

	// The block's registration gives up at RegisterFor and TIDES falls back to solo, without
	// another job: the miners have solo work for the block already.
	waitUntil(t, 8*time.Second, "JOBLOOP-HANG-FALLBACK", func() bool { return r.gw.Status().State == tidesgw.StateFallback })
	r.quiet("JOBLOOP-HANG-NO-EXTRA", 300*time.Millisecond)
	// The next block's work goes out at once, without asking the pool.
	start = time.Now()
	r.zmq(1002)
	n = r.sent("JOBLOOP-HANG-NEXT", 3*time.Second)
	if took := n.at.Sub(start); took > time.Second || !n.clean || n.job.Tides || n.job.Height != 1003 {
		t.Fatalf("JOBLOOP-HANG-NEXT: after %s, clean %v, job %+v", took, n.clean, n.job)
	}
}

// A pool that takes 3 s to register a block's work (slow, or answering "retry" while its node
// takes the block): the miners get solo work for the block after about 2 s, then the pool's job
// as soon as it comes, with clean_jobs. TIDES stays active, and the share on that job is credited.
func TestJobLoopASlowPoolsBlockIsMinedSoloThenTIDES(t *testing.T) {
	r := newLoopRig(t, loopRigOpts{tip: 1000, tides: true})
	r.run()
	r.poll()
	r.sent("JOBLOOP-SLOW-FIRST", 3*time.Second)

	r.pool.set(func(p *loopPool) { p.delay = 3 * time.Second })
	r.pool.mines(1001)
	start := time.Now()
	r.zmq(1001)
	solo := r.sent("JOBLOOP-SLOW-SOLO", 6*time.Second)
	took := solo.at.Sub(start)
	if solo.job.Tides || !solo.clean || solo.job.Height != 1002 {
		t.Fatalf("JOBLOOP-SLOW-SOLO: the first job for the block came after %s: %+v (clean %v); want solo work after about 2 s",
			took.Round(10*time.Millisecond), solo.job, solo.clean)
	}
	if took < 1500*time.Millisecond || took > 2800*time.Millisecond {
		t.Fatalf("JOBLOOP-SLOW-WAIT: solo work went out %s after the block, want about 2 s", took)
	}
	back := r.sent("JOBLOOP-SLOW-BACK", 6*time.Second)
	if !back.job.Tides || !back.clean || back.job.Height != 1002 {
		t.Fatalf("JOBLOOP-SLOW-BACK: clean %v, job %+v", back.clean, back.job)
	}
	if st := r.gw.Status(); st.State != tidesgw.StateActive || r.logged("Forge Pool unavailable") {
		t.Fatalf("JOBLOOP-SLOW-ACTIVE: the gateway is %s (%s)", st.State, st.Reason)
	}
	if n := len(r.pool.registered()); n != 2 {
		t.Fatalf("JOBLOOP-SLOW-REGS: %d registrations, want 2", n)
	}
	r.quiet("JOBLOOP-SLOW-COUNT", 300*time.Millisecond)
	r.shareOn(back.job, 1)
	if got := r.pool.credited(); len(got) != 1 || got[0].JobID != r.gw.Registered(back.job.ID).PoolJobID {
		t.Fatalf("JOBLOOP-SLOW-CREDIT: the pool was sent %+v", got)
	}
}

// A pool that registers a block's work within 2 s: the block is mined in TIDES from its first job,
// with no solo job in between, as before.
func TestJobLoopAPoolUnderTwoSecondsKeepsTheBlockTIDES(t *testing.T) {
	r := newLoopRig(t, loopRigOpts{tip: 1000, tides: true})
	r.run()
	r.poll()
	r.sent("JOBLOOP-QUICK-FIRST", 3*time.Second)
	r.pool.set(func(p *loopPool) { p.delay = time.Second })
	r.pool.mines(1001)
	start := time.Now()
	r.zmq(1001)
	n := r.sent("JOBLOOP-QUICK", 4*time.Second)
	if took := n.at.Sub(start); !n.job.Tides || !n.clean || n.job.Height != 1002 || took < 900*time.Millisecond || took > 1900*time.Millisecond {
		t.Fatalf("JOBLOOP-QUICK: after %s, clean %v, job %+v", took, n.clean, n.job)
	}
	r.quiet("JOBLOOP-QUICK-ONE", 2*time.Second)
}

// A refresh still at the pool when a block comes is given up, and nothing comes of it: the
// block's work goes out at once, no job for the block before follows it, and shares on the
// block's work are credited (tracking the late answer would have had the gateway drop them).
func TestJobLoopARefreshForTheBlockBeforeComesToNothing(t *testing.T) {
	r := newLoopRig(t, loopRigOpts{tip: 1000, tides: true})
	r.run()
	r.poll()
	r.sent("JOBLOOP-OLD-TIP-FIRST", 3*time.Second)

	r.pool.set(func(p *loopPool) { p.delay, p.slowAt = 1500*time.Millisecond, 1001 }) // only the block before is slow
	r.clock.Add(periodicJobEvery)
	r.poll()
	waitUntil(t, 2*time.Second, "JOBLOOP-OLD-TIP-REFRESH", func() bool { now, _ := r.pool.waiting(); return now == 1 })
	r.pool.mines(1001)
	start := time.Now()
	r.zmq(1001)
	n := r.sent("JOBLOOP-OLD-TIP-NEW", 5*time.Second)
	if n.job.Height != 1002 || !n.job.Tides || !n.clean {
		t.Fatalf("JOBLOOP-OLD-TIP-ORDER: the first job after the block was %+v (clean %v)", n.job, n.clean)
	}
	if took := n.at.Sub(start); took > time.Second {
		t.Fatalf("JOBLOOP-OLD-TIP-WAITED: the block's work waited %s behind the refresh", took)
	}
	r.quiet("JOBLOOP-OLD-TIP-JOB", 2*time.Second)
	r.shareOn(n.job, 1)
	if got := r.pool.credited(); len(got) != 1 || got[0].JobID != r.gw.Registered(n.job.ID).PoolJobID {
		t.Fatalf("JOBLOOP-OLD-TIP-TRACKED: the pool was sent %+v", got)
	}
}

// Fallen back to solo, the loop tries the pool again every RetryEvery. A pool that hangs on that
// retry held a new block's work for up to 6 s; the retry now runs beside the loop and is given up
// when the block comes, whose work goes out at once.
func TestJobLoopAHungRetryNeverHoldsANewBlock(t *testing.T) {
	r := newLoopRig(t, loopRigOpts{tip: 1000, tides: true})
	r.run()
	r.pool.set(func(p *loopPool) { p.down = true })
	r.poll()
	if n := r.sent("JOBLOOP-RETRY-FIRST", 3*time.Second); n.job.Tides || r.gw.Status().State != tidesgw.StateFallback {
		t.Fatalf("JOBLOOP-RETRY-FIRST: %+v, gateway %s", n.job, r.gw.Status().State)
	}
	r.pool.set(func(p *loopPool) { p.down, p.hang = false, true })
	r.clock.Add(61 * time.Second)
	r.poll() // the retry is due; the pool never answers it
	waitUntil(t, 2*time.Second, "JOBLOOP-RETRY-ASKED", func() bool { now, _ := r.pool.waiting(); return now == 1 })
	start := time.Now()
	r.zmq(1001)
	n := r.sent("JOBLOOP-RETRY-NEW-BLOCK", 10*time.Second)
	if took := n.at.Sub(start); took > time.Second {
		t.Fatalf("JOBLOOP-RETRY-HOLDS: the new block's work waited %s for the retry", took.Round(10*time.Millisecond))
	}
	if !n.clean || n.job.Tides || n.job.Height != 1002 {
		t.Fatalf("JOBLOOP-RETRY-SOLO: clean %v, job %+v", n.clean, n.job)
	}
	waitUntil(t, time.Second, "JOBLOOP-RETRY-DROPPED", func() bool { _, given := r.pool.waiting(); return given >= 1 })
}

// A refresh on the same block that the pool fails, now that it runs beside the loop: the miners
// stay on their TIDES job (the pool holds it until the tip moves), the dashboard says the last
// refresh failed, and the pool is asked again only with the next periodic job, not at every poll.
// Once the job is KeepFor old, the miners move to solo work with clean_jobs.
func TestJobLoopAFailedRefreshKeepsTheMinersOnTheirJob(t *testing.T) {
	r := newLoopRig(t, loopRigOpts{tip: 1000, tides: true, registerFor: 300 * time.Millisecond})
	r.step(false)
	first := r.sent("JOBLOOP-KEEP-FIRST", 3*time.Second)
	if !first.job.Tides {
		t.Fatalf("JOBLOOP-KEEP-FIRST: %+v", first.job)
	}
	r.pool.set(func(p *loopPool) { p.hang = true }) // every registration fails at RegisterFor

	r.clock.Add(periodicJobEvery)
	r.step(false)
	r.quiet("JOBLOOP-KEEP-JOB", 300*time.Millisecond)
	if st := r.gw.Status(); st.State != tidesgw.StateActive || !strings.HasPrefix(st.Reason, "last refresh failed") {
		t.Fatalf("JOBLOOP-KEEP-NOTE: the gateway is %s (%s) after one failed refresh", st.State, st.Reason)
	}
	if got := getCurrentJob(); got.ID != first.job.ID {
		t.Fatalf("JOBLOOP-KEEP-JOB: the current job is %s, not the TIDES job %s", got.ID, first.job.ID)
	}
	for i := 0; i < 5; i++ {
		r.clock.Add(time.Second)
		r.step(false)
	}
	r.quiet("JOBLOOP-KEEP-QUIET", 200*time.Millisecond)
	if n := len(r.pool.registered()); n != 2 {
		t.Fatalf("JOBLOOP-KEEP-AGAIN: %d registrations within 5 s of a failed refresh, want 2 (the first job and the refresh)", n)
	}

	r.clock.Add(periodicJobEvery - 5*time.Second) // 30 s: still kept
	r.step(false)
	r.quiet("JOBLOOP-KEEP-SECOND", 300*time.Millisecond)
	if n := len(r.pool.registered()); n != 3 || r.gw.Status().State != tidesgw.StateActive {
		t.Fatalf("JOBLOOP-KEEP-SECOND: %d registrations, gateway %s", n, r.gw.Status().State)
	}
	r.clock.Add(periodicJobEvery) // 45 s: KeepFor
	r.step(false)
	n := r.sent("JOBLOOP-KEEP-FALLBACK", 3*time.Second)
	if n.job.Tides || !n.clean || n.job.Height != 1001 || r.gw.Status().State != tidesgw.StateFallback {
		t.Fatalf("JOBLOOP-KEEP-FALLBACK: %+v (clean %v), gateway %s", n.job, n.clean, r.gw.Status().State)
	}
}

// A pool that is down: solo work at once, the gateway falls back, and its reason is one line.
func TestJobLoopAPoolThatIsDownGivesSoloWorkAtOnce(t *testing.T) {
	r := newLoopRig(t, loopRigOpts{tip: 1000, tides: true})
	r.run()
	r.pool.set(func(p *loopPool) { p.down = true })
	start := time.Now()
	r.poll()
	n := r.sent("JOBLOOP-DOWN", 3*time.Second)
	if took := n.at.Sub(start); took > time.Second || n.job.Tides || !n.clean {
		t.Fatalf("JOBLOOP-DOWN: after %s, clean %v, job %+v", took, n.clean, n.job)
	}
	if st := r.gw.Status(); st.State != tidesgw.StateFallback || st.Reason == "" {
		t.Fatalf("JOBLOOP-DOWN-STATE: %s (%s)", st.State, st.Reason)
	}
}

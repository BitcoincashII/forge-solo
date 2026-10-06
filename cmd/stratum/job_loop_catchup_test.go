package main

import (
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/stats"
)

// Live on Linux the node left initial sync 25,500 blocks behind the chain and connected them at
// about 1,000 a second (its initial sync flag was already off, as after any restart): the loop sent
// 2,500 new-block jobs in 26 s, a hundred a second, each with clean_jobs, all for blocks long
// buried. A miner connected then would have had every share in flight refused as stale. Now, while
// the node is two or more blocks behind the headers it knows, a new block's work goes out at most
// every 5 s, and the work for the block that brings it level at once.
func TestJobLoopANodeCatchingUpSendsAJobEveryFiveSeconds(t *testing.T) {
	r := newLoopRig(t, loopRigOpts{tip: 77258})
	r.node.setHeaders(84189)
	r.step(false)
	if n := r.sent("JOBLOOP-CATCHUP-FIRST", 2*time.Second); !n.clean || n.job.Height != 77259 {
		t.Fatalf("JOBLOOP-CATCHUP-FIRST: the miners had no work, and got %+v (clean %v)", n.job, n.clean)
	}
	for tip := int64(77259); tip < 77359; tip++ {
		r.block(tip)
	}
	if got := r.drain(300 * time.Millisecond); len(got) != 0 {
		t.Fatalf("JOBLOOP-CATCHUP-FLOOD: %d jobs for 100 blocks connected in a second while catching up, want none for 5 s", len(got))
	}

	r.clock.Add(5 * time.Second)
	r.block(77400)
	if n := r.sent("JOBLOOP-CATCHUP-EVERY", 2*time.Second); !n.clean || n.job.Height != 77401 {
		t.Fatalf("JOBLOOP-CATCHUP-EVERY: %+v (clean %v)", n.job, n.clean)
	}
	for tip := int64(77401); tip < 77450; tip++ {
		r.block(tip)
	}
	r.clock.Add(4 * time.Second)
	r.block(80000)
	if got := r.drain(300 * time.Millisecond); len(got) != 0 {
		t.Fatalf("JOBLOOP-CATCHUP-EVERY-ONLY: %d jobs within 5 s of the last", len(got))
	}

	// The block that brings the node level goes out at once, 4 s after the last job.
	r.block(84189)
	if n := r.sent("JOBLOOP-CATCHUP-LEVEL", 2*time.Second); !n.clean || n.job.Height != 84190 || n.job.OriginalPrevHash != chainHash(84189) {
		t.Fatalf("JOBLOOP-CATCHUP-LEVEL: %+v (clean %v)", n.job, n.clean)
	}
	// Then as before: a block whose header came first is one behind, and goes out at once.
	r.node.setHeaders(84191)
	r.block(84190)
	if n := r.sent("JOBLOOP-CATCHUP-GAP-ONE", 2*time.Second); !n.clean || n.job.Height != 84191 {
		t.Fatalf("JOBLOOP-CATCHUP-GAP-ONE: %+v (clean %v)", n.job, n.clean)
	}
	r.block(84191)
	if n := r.sent("JOBLOOP-CATCHUP-AFTER", 2*time.Second); !n.clean || n.job.Height != 84192 {
		t.Fatalf("JOBLOOP-CATCHUP-AFTER: %+v (clean %v)", n.job, n.clean)
	}
	if r.logs.FilterMessageSnippet("catching up with the chain").Len() != 1 || r.logs.FilterMessageSnippet("has caught up").Len() != 1 {
		t.Fatalf("JOBLOOP-CATCHUP-LOG: the catch-up is not said once, and its end once: %v", r.logs.All())
	}
}

// The node is asked about its headers only when a new block comes within 5 s of the last block's
// work: at the tip, where blocks come minutes apart, never.
func TestJobLoopAtTheTipTheNodeIsNotAskedAboutItsHeaders(t *testing.T) {
	r := newLoopRig(t, loopRigOpts{tip: 1000})
	r.step(false)
	r.sent("JOBLOOP-ASK-FIRST", 2*time.Second)
	for tip := int64(1001); tip < 1004; tip++ {
		r.clock.Add(10 * time.Minute)
		r.block(tip)
		r.sent("JOBLOOP-ASK-BLOCK", 2*time.Second)
	}
	if n := r.node.count("getblockchaininfo"); n != 0 {
		t.Fatalf("JOBLOOP-ASK-TIP: the node was asked about its headers %d times at the tip", n)
	}
}

// A header the node never connects (a block withheld, or one it found invalid) leaves it behind its
// headers for good. Nothing stops: a block that comes minutes after the last goes out at once, one
// that comes within 5 s of the last waits at most until 5 s after it, and once the node has looked
// behind for 10 minutes nothing waits at all.
func TestJobLoopAHeaderNeverConnectedNeverHoldsWorkLong(t *testing.T) {
	r := newLoopRig(t, loopRigOpts{tip: 1000})
	r.node.setHeaders(1_000_000)
	r.step(false)
	r.sent("JOBLOOP-BOGUS-FIRST", 2*time.Second)
	r.clock.Add(10 * time.Minute)
	r.block(1001)
	if n := r.sent("JOBLOOP-BOGUS-AT-ONCE", 2*time.Second); n.job.Height != 1002 {
		t.Fatalf("JOBLOOP-BOGUS-AT-ONCE: %+v", n.job)
	}
	r.clock.Add(2 * time.Second)
	r.block(1002)
	if got := r.drain(200 * time.Millisecond); len(got) != 0 {
		t.Fatalf("JOBLOOP-BOGUS-QUICK: %d jobs", len(got))
	}
	r.clock.Add(3 * time.Second)
	r.step(false)
	if n := r.sent("JOBLOOP-BOGUS-BOUNDED", 2*time.Second); !n.clean || n.job.Height != 1003 {
		t.Fatalf("JOBLOOP-BOGUS-BOUNDED: the block that waited went out as %+v (clean %v)", n.job, n.clean)
	}

	// Two blocks a second apart every 30 s: the second waits while the node has looked behind for
	// under 10 minutes, then never.
	tip := int64(1002)
	held := 0
	for i := 0; i < 24; i++ {
		r.clock.Add(29 * time.Second)
		tip++
		r.block(tip)
		r.sent("JOBLOOP-BOGUS-SPACED", 2*time.Second)
		r.clock.Add(time.Second)
		tip++
		r.block(tip)
		if got := r.drain(100 * time.Millisecond); len(got) == 0 {
			held++
			r.clock.Add(4 * time.Second)
			r.step(false)
			r.sent("JOBLOOP-BOGUS-RELEASED", 2*time.Second)
		} else if i < 15 {
			t.Fatalf("JOBLOOP-BOGUS-EARLY: a quick block went out at once %s into the catch-up", time.Duration(i)*34*time.Second)
		}
	}
	if held < 16 || held > 18 {
		t.Fatalf("JOBLOOP-BOGUS-LIMIT: quick blocks waited %d times in 13.6 minutes, want about 10 minutes' worth (17)", held)
	}
}

// A node that does not answer about its headers counts as caught up: every block's work goes out at
// once, as before.
func TestJobLoopANodeThatDoesNotSayItsHeadersHoldsNothing(t *testing.T) {
	r := newLoopRig(t, loopRigOpts{tip: 1000})
	r.node.set(func(n *loopNode) { n.headers, n.chainDown = 5000, true })
	r.step(false)
	r.sent("JOBLOOP-CHAIN-DOWN-FIRST", 2*time.Second)
	for tip := int64(1001); tip < 1011; tip++ {
		r.block(tip)
	}
	if got := r.drain(300 * time.Millisecond); len(got) != 10 {
		t.Fatalf("JOBLOOP-CHAIN-DOWN: %d jobs for 10 blocks, want 10", len(got))
	}
}

// A node that fails to answer once during a catch-up: that block's work goes out, and the catch-up
// goes on, not said over again.
func TestJobLoopANodeThatFailsToAnswerOnceMidCatchUp(t *testing.T) {
	r := newLoopRig(t, loopRigOpts{tip: 1000})
	r.node.setHeaders(5000)
	r.step(false)
	r.sent("JOBLOOP-CHAIN-FLAKY-FIRST", 2*time.Second)
	r.block(1001)
	r.node.set(func(n *loopNode) { n.chainDown = true })
	r.block(1002)
	if n := r.sent("JOBLOOP-CHAIN-FLAKY-SENT", 2*time.Second); n.job.Height != 1003 {
		t.Fatalf("JOBLOOP-CHAIN-FLAKY-SENT: %+v", n.job)
	}
	r.node.set(func(n *loopNode) { n.chainDown = false })
	r.block(1003)
	if got := r.drain(200 * time.Millisecond); len(got) != 0 {
		t.Fatalf("JOBLOOP-CHAIN-FLAKY-HELD: %d jobs", len(got))
	}
	if r.logs.FilterMessageSnippet("catching up with the chain").Len() != 1 || r.logs.FilterMessageSnippet("has caught up").Len() != 0 {
		t.Fatalf("JOBLOOP-CHAIN-FLAKY: one failed answer ended the catch-up in the log: %v", r.logs.All())
	}
}

// A node slow to say what headers it has holds nothing for long: after a second its block's work
// goes out.
func TestJobLoopANodeSlowToSayItsHeadersHoldsLittle(t *testing.T) {
	r := newLoopRig(t, loopRigOpts{tip: 1000})
	r.node.set(func(n *loopNode) { n.headers, n.chainSlow = 5000, 3*time.Second })
	r.step(false)
	r.sent("JOBLOOP-CHAIN-SLOW-FIRST", 2*time.Second)
	start := time.Now()
	r.block(1001)
	n := r.sent("JOBLOOP-CHAIN-SLOW", 4*time.Second)
	if took := n.at.Sub(start); took > 2*time.Second || n.job.Height != 1002 {
		t.Fatalf("JOBLOOP-CHAIN-SLOW: the block's work went out %s after it: %+v", took, n.job)
	}
}

// A catch-up later on, after the node was seen level or not seen behind for a while, is held as the
// first was: the 10 minutes count from its own start.
func TestJobLoopALaterCatchUpIsHeldToo(t *testing.T) {
	r := newLoopRig(t, loopRigOpts{tip: 1000})
	r.node.setHeaders(3000)
	r.step(false)
	r.sent("JOBLOOP-CATCHUP-AGAIN-FIRST", 2*time.Second)
	r.block(1001)
	if got := r.drain(200 * time.Millisecond); len(got) != 0 {
		t.Fatalf("JOBLOOP-CATCHUP-AGAIN-FIRST: %d jobs", len(got))
	}
	// Twenty minutes on, with no word from the node in between, it is catching up again.
	r.clock.Add(20 * time.Minute)
	r.block(1500)
	r.sent("JOBLOOP-CATCHUP-AGAIN-SENT", 2*time.Second)
	r.block(1501)
	if got := r.drain(200 * time.Millisecond); len(got) != 0 {
		t.Fatalf("JOBLOOP-CATCHUP-AGAIN: a second catch-up was not held: %d jobs", len(got))
	}
}

// Only a new block's work can wait. A job on the same block that is due at once (a new payout
// address, a miner logging in above the TIDES job's difficulty, a switch of payout mode) goes out at
// once a second after a block, even with the node far behind its headers, and the node is not
// asked about them for it.
func TestJobLoopOnlyANewBlocksWorkWaitsForACatchUp(t *testing.T) {
	r := newLoopRig(t, loopRigOpts{tip: 1000})
	r.node.setHeaders(1_000_000)
	r.step(false)
	r.sent("JOBLOOP-SAME-BLOCK-FIRST", 2*time.Second)
	r.clock.Add(10 * time.Minute)
	r.block(1001)
	r.sent("JOBLOOP-SAME-BLOCK-NEW", 2*time.Second)
	asked := r.node.count("getblockchaininfo")
	r.clock.Add(time.Second)
	if err := jobManager.SetPoolAddress(testAddr(8)); err != nil {
		t.Fatal(err)
	}
	r.step(false)
	if n := r.sent("JOBLOOP-SAME-BLOCK-ADDRESS", 2*time.Second); !n.clean || n.job.Height != 1002 || n.job.PayTo == r.payout {
		t.Fatalf("JOBLOOP-SAME-BLOCK-ADDRESS: %+v (clean %v)", n.job, n.clean)
	}
	if got := r.node.count("getblockchaininfo"); got != asked {
		t.Fatalf("JOBLOOP-SAME-BLOCK-ASKED: the node was asked about its headers %d times for an address change", got-asked)
	}
}

func TestJobLoopOnlyANewBlocksWorkWaitsForACatchUpInTIDES(t *testing.T) {
	r := newLoopRig(t, loopRigOpts{tip: 1000, tides: true})
	r.node.setHeaders(1_000_000)
	r.step(false)
	r.sent("JOBLOOP-SAME-BLOCK-TIDES-FIRST", 3*time.Second)
	r.clock.Add(10 * time.Minute)
	r.pool.mines(1001)
	r.block(1001)
	if n := r.sent("JOBLOOP-SAME-BLOCK-TIDES-NEW", 3*time.Second); !n.job.Tides {
		t.Fatalf("JOBLOOP-SAME-BLOCK-TIDES-NEW: %+v", n.job)
	}
	asked := r.node.count("getblockchaininfo")
	r.clock.Add(time.Second)
	tidesRefreshWanted.Store(true) // a miner logged in above the job's difficulty
	r.step(false)
	if n := r.sent("JOBLOOP-SAME-BLOCK-LOGIN", 3*time.Second); !n.job.Tides || n.clean || n.job.Height != 1002 {
		t.Fatalf("JOBLOOP-SAME-BLOCK-LOGIN: %+v (clean %v)", n.job, n.clean)
	}
	r.clock.Add(time.Second)
	payoutModeVal.Store(stats.PayoutModeSolo)
	r.step(false)
	if n := r.sent("JOBLOOP-SAME-BLOCK-MODE", 3*time.Second); n.job.Tides || !n.clean || n.job.Height != 1002 {
		t.Fatalf("JOBLOOP-SAME-BLOCK-MODE: %+v (clean %v)", n.job, n.clean)
	}
	if got := r.node.count("getblockchaininfo"); got != asked {
		t.Fatalf("JOBLOOP-SAME-BLOCK-TIDES-ASKED: the node was asked about its headers %d times for jobs on the same block", got-asked)
	}
}

// A node stuck behind its headers makes no new blocks: the periodic job goes on every 15 s.
func TestJobLoopANodeStuckBehindItsHeadersGetsThePeriodicJob(t *testing.T) {
	r := newLoopRig(t, loopRigOpts{tip: 1000})
	r.node.setHeaders(2000)
	r.step(false)
	r.sent("JOBLOOP-STUCK-NODE-FIRST", 2*time.Second)
	for i := 0; i < 3; i++ {
		r.clock.Add(periodicJobEvery)
		r.step(false)
		if n := r.sent("JOBLOOP-STUCK-NODE-PERIODIC", 2*time.Second); n.clean || n.job.Height != 1001 {
			t.Fatalf("JOBLOOP-STUCK-NODE-PERIODIC: %+v (clean %v)", n.job, n.clean)
		}
	}
}

// In TIDES mode the catch-up asks nothing of the pool: solo work every 5 s, no registration and no
// more snapshot fetches, and the pool's job with the block that brings the node level.
func TestJobLoopTIDESWhileTheNodeCatchesUp(t *testing.T) {
	r := newLoopRig(t, loopRigOpts{tip: 77258, tides: true})
	r.node.setHeaders(84189)
	r.pool.mines(84189)
	r.step(false)
	if n := r.sent("JOBLOOP-CATCHUP-TIDES-FIRST", 4*time.Second); n.job.Tides || n.job.Height != 77259 {
		t.Fatalf("JOBLOOP-CATCHUP-TIDES-FIRST: %+v", n.job)
	}
	gets := r.pool.gets()
	for tip := int64(77259); tip < 77359; tip++ {
		r.block(tip)
	}
	if got := r.drain(300 * time.Millisecond); len(got) != 0 {
		t.Fatalf("JOBLOOP-CATCHUP-TIDES-FLOOD: %d jobs for 100 blocks in a second", len(got))
	}
	r.clock.Add(5 * time.Second)
	r.block(80000)
	if n := r.sent("JOBLOOP-CATCHUP-TIDES-EVERY", 2*time.Second); n.job.Tides || n.job.Height != 80001 {
		t.Fatalf("JOBLOOP-CATCHUP-TIDES-EVERY: %+v", n.job)
	}
	if regs := r.pool.registered(); len(regs) != 0 || r.pool.gets() != gets {
		t.Fatalf("JOBLOOP-CATCHUP-TIDES-QUIET: %d registrations and %d snapshot fetches while catching up", len(regs), r.pool.gets()-gets)
	}
	r.block(84189)
	if n := r.sent("JOBLOOP-CATCHUP-TIDES-LEVEL", 4*time.Second); !n.job.Tides || !n.clean || n.job.Height != 84190 {
		t.Fatalf("JOBLOOP-CATCHUP-TIDES-LEVEL: %+v (clean %v), gateway %s (%s)", n.job, n.clean, r.gw.Status().State, r.gw.Status().Reason)
	}
}

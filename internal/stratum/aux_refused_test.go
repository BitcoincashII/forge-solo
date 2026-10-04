package stratum

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// waitFound waits until the server has reported want 1175 blocks found.
func waitFound(t *testing.T, found *atomic.Int32, want int32, code string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for found.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("%s: %d 1175 blocks reported found, want %d", code, found.Load(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func notCredited(t *testing.T, cp *captureProcessor, code string) {
	t.Helper()
	select {
	case sh := <-cp.ch:
		t.Fatalf("%s: the refused share was credited: %+v", code, sh)
	case <-time.After(50 * time.Millisecond):
	}
}

// A share on the job of the BCH2 tip before the current one is refused as stale, but the 1175 block
// it solves is still a 1175 block: its proof does not depend on the BCH2 tip. It was answered
// "Job not found" and never sent (shown on a regtest 1175 node, which took the same proof sent by
// hand). It is sent once, credited to the miner an accepted share would be, and the share stays
// refused and uncredited.
func TestA1175SolutionOnTheLastTipsJobIsStillSent(t *testing.T) {
	shortAuxRetries(t)
	node := &fake1175{}
	s, cp, found, finders := auxServer(t, node, 0)
	s.config.CreditPayoutAddress = true
	s.SetSoloPayoutAddress(otherAddress)
	c := perJobClient(t)
	last, cur := auxJob("a"), auxJob("b")
	cur.PrevBlockHash = strings.Repeat("11", 32) // a new BCH2 block; the 1175 work is the same
	s.jobHistory.Store("a", last)
	s.jobHistory.Store("b", cur)
	s.currentJob.Store(cur)
	c.mu.Lock()
	c.Difficulty = jobLow
	c.mu.Unlock()

	nonce, _ := mineShare(t, s, last, "0000000000000001", jobLow, 0.5)
	if r := submitShare(s, c, "a", "0000000000000001", nonce); r.Error != ErrJobNotFound {
		t.Fatalf("AUX-STALE-REFUSED: a share on the last tip's job got %+v, want %v", r.Error, ErrJobNotFound)
	}
	waitFound(t, found, 1, "AUX-STALE")
	if f := <-finders; f != otherAddress {
		t.Fatalf("AUX-STALE-FINDER: the 1175 block is credited to %q, want the payout address %q", f, otherAddress)
	}
	notCredited(t, cp, "AUX-STALE-UNCREDITED")

	if r := submitShare(s, c, "a", "0000000000000001", nonce); r.Error != ErrJobNotFound {
		t.Fatalf("AUX-STALE-REFUSED: sent again, the share got %+v", r.Error)
	}
	time.Sleep(100 * time.Millisecond)
	if n, _ := node.counts(); n != 1 {
		t.Fatalf("AUX-STALE-ONCE: the same 1175 solution was sent %d times", n)
	}
}

// A share over the intake rate limit is refused unless it solves a BCH2 block. One that solves a
// 1175 block was refused unseen by the 1175 node; it is now sent, the share still refused.
func TestA1175SolutionOverTheRateLimitIsStillSent(t *testing.T) {
	shortAuxRetries(t)
	node := &fake1175{}
	s, cp, found, _ := auxServer(t, node, 0)
	s.config.MaxSharesPerSecond = 1
	c := perJobClient(t)
	j := auxJob("a")
	s.jobHistory.Store("a", j)
	s.currentJob.Store(j)
	c.mu.Lock()
	c.Difficulty = jobLow
	c.mu.Unlock()

	nonce, _ := mineShare(t, s, j, "0000000000000001", jobLow, 0.5) // below the BCH2 target
	submitAt(s, c, "ff", "0000000000000002", j.NTime, nonce)        // fills the second's one submit
	if r := submitShare(s, c, "a", "0000000000000001", nonce); r.Error != ErrRateLimited {
		t.Fatalf("AUX-OVERRATE-REFUSED: an over-rate share got %+v, want %v", r.Error, ErrRateLimited)
	}
	waitFound(t, found, 1, "AUX-OVERRATE")
	notCredited(t, cp, "AUX-OVERRATE-UNCREDITED")
}

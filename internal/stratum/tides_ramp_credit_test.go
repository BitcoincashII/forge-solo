package stratum

import (
	"fmt"
	"io"
	"math"
	"math/rand"
	"net"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/datum/gateway"
)

// tidesCredit is what the pool credited a rental's shares, and the work in them, over its first
// rampWindow seconds ([0]) and over the whole run ([1]).
type tidesCredit struct{ credited, worked [2]float64 }

// rampWindow is the start of a rental's connection: its login, its first ramp from the floor and
// its first jobs at the level the ramp set.
const rampWindow = 120.0

func (r *tidesCredit) add(sec, credited, worked float64) {
	r.credited[1] += credited
	r.worked[1] += worked
	if sec < rampWindow {
		r.credited[0] += credited
		r.worked[0] += worked
	}
}

// runTidesRental runs a rental on the rental port, as shipped, under TIDES for until seconds from
// its login, with nothing remembered for it: a new order, or any order after a stratum restart. The
// stratum's own login (authorize, the login handler), vardiff (addShareSample, adjustVardiffAt) and
// MaxDifficulty run; the rest is modelled as cmd/stratum and the pool do it:
//   - the job loop sends a job every 15 s, registered at 2^ShareDiffExp(MaxDifficulty), and one at
//     its next 1 s tick when a login finds the job in flight committed below what a job registered
//     now would commit, and below the pool's own 1024 (tidesLoginCheck, Gateway.Undercommitted);
//   - the pool credits a share each time it meets its job's share difficulty, the job's commitment
//     or 1024 where that is higher, at that difficulty: a share found at d is credited d on average
//     where the job's is at or above d, and only the job's where it is below;
//   - the rig finds shares at random at its hashrate against the difficulty its job went out under,
//     and works a new difficulty from its next job.
//
// With healthChecks, MiningRigRentals' health checks log in every 6.5 s (about 550 an hour) under
// the order's name from the rig's address, from a minute before the rig, and stay 10 s.
func runTidesRental(t *testing.T, hashrate float64, healthChecks bool, seed int64, until float64) tidesCredit {
	t.Helper()
	s := rentalPortServer()
	defer s.Stop()
	s.config.SoloOnly, s.config.CreditPayoutAddress = true, true
	s.SetSoloPayoutAddress(testPayout)
	const (
		host       = "198.51.100.20"
		poolOwn    = 1024.0
		jobEvery   = 15.0
		checkEvery = 6.5
		checkStays = 10.0
	)
	rnd := rand.New(rand.NewSource(seed))
	base := time.Now()
	sec := -60 - rnd.Float64()*jobEvery
	now := func() time.Time { return base.Add(time.Duration(sec * float64(time.Second))) }

	commitNow := func() float64 {
		if e, ok := gateway.ShareDiffExp(s.maxDifficultyAt(now())); ok {
			return math.Ldexp(1, e)
		}
		return 0
	}
	var committed, lastJob float64
	asked := math.Inf(1) // when the job loop sends the job a login asked for
	register := func() { committed, lastJob, asked = commitNow(), sec, math.Inf(1) }
	s.SetLoginHandler(func() {
		if want := commitNow(); want > committed && want > poolOwn {
			asked = math.Min(asked, math.Floor(sec)+1)
		}
	})
	login := func(c *Client) {
		t.Helper()
		s.clients.Store(c.ID, c)
		resp, auth := s.authorize(c, &Request{ID: 2, Method: MethodAuthorize,
			Params: []byte(`["` + testPayout + `.mrr","x"]`)})
		if resp.Result != true {
			t.Fatalf("TIDES-RAMP-LOGIN: %+v", resp.Error)
		}
		if auth.sendWork {
			if c.Conn != nil {
				s.sendCurrentDifficulty(c)
			}
			s.noteLogin()
		}
	}

	type check struct {
		c    *Client
		gone float64
	}
	var checks []check
	nextCheck, n := math.Inf(1), 0
	if healthChecks {
		nextCheck = sec + rnd.Float64()*checkEvery
	}
	expire := func() {
		kept := checks[:0]
		for _, h := range checks {
			if h.gone <= sec {
				s.clients.Delete(h.c.ID)
			} else {
				kept = append(kept, h)
			}
		}
		checks = kept
	}

	poolSide, minerSide := net.Pipe()
	defer poolSide.Close()
	defer minerSide.Close()
	go io.Copy(io.Discard, minerSide)
	var rig *Client
	var jobDiff float64 // the difficulty the rig's job went out under
	sent := func() float64 {
		rig.mu.RLock()
		defer rig.mu.RUnlock()
		return rig.LastDifficultySent
	}

	var r tidesCredit
	register()
	for sec < until {
		next := math.Min(math.Min(lastJob+jobEvery, asked), math.Min(nextCheck, until))
		if rig == nil {
			next = math.Min(next, 0)
		} else if at := sec + rnd.ExpFloat64()*jobDiff*(1<<32)/hashrate; at < next {
			sec = at
			r.add(sec, math.Min(jobDiff, math.Max(poolOwn, committed)), jobDiff)
			rig.mu.Lock()
			k := rig.addShareSample(now(), jobDiff)
			rig.ProvenDifficulty, rig.ProvenAt = jobDiff, now()
			rig.mu.Unlock()
			rig.ValidShares.Add(1)
			if k >= VardiffMinShares {
				s.adjustVardiffAt(rig, now())
			}
			continue
		}
		sec = next
		expire()
		switch {
		case rig == nil && sec == 0:
			rig = &Client{ID: "rig", Conn: poolSide, IP: host + ":36018", UserAgent: "bosminer-plus-tuner 0.9.3-5a7fd334",
				ConnectedAt: now(), firstJobAt: now()}
			login(rig)
			jobDiff = sent()
		case sec == nextCheck:
			n++
			c := &Client{ID: fmt.Sprintf("check%d", n), IP: fmt.Sprintf("%s:%d", host, 40000+n),
				UserAgent: "infinite-hash-proxy/probe", ConnectedAt: now()}
			login(c)
			checks = append(checks, check{c, sec + checkStays})
			nextCheck += checkEvery
		case sec == asked || sec == lastJob+jobEvery:
			register()
			if rig != nil {
				jobDiff = sent()
			}
		}
	}
	return r
}

// A rental that connects with nothing remembered for it (a new order, or any order after a stratum
// restart) opens at the rental port's floor, and firstRamp lifts it in one step to the level its
// floor shares measure: a 4.5 PH/s rig from 500000 to about 26M at target_time 25. Until its first
// share at that level its last share was at the floor, and it counted at provenAhead times that,
// 2M: each job registered in the 25 s or so before that share committed to 2^22, and the pool
// credited the rig's shares at 26M on it 2^22 each, 16% of their work. MiningRigRentals' health
// checks hid it, as each logged in at the level the ramp remembered and asked for a job committed
// to it; a rental without them (NiceHash, and the rest) lost it on every connect. The level
// firstRamp set now counts from the moment it is set.
func TestTIDESCreditsARentalFromItsConnectThroughItsFirstRamp(t *testing.T) {
	const seeds, until = 200, 600.0
	for _, healthChecks := range []bool{false, true} {
		for _, h := range []float64{1e15, 4.5e15, 10e15} {
			var sum tidesCredit
			for seed := int64(1); seed <= seeds; seed++ {
				r := runTidesRental(t, h, healthChecks, seed, until)
				for i := range sum.credited {
					sum.credited[i] += r.credited[i]
					sum.worked[i] += r.worked[i]
				}
			}
			early, all := sum.credited[0]/sum.worked[0], sum.credited[1]/sum.worked[1]
			lost := (sum.worked[0] - sum.credited[0]) / seeds * (1 << 32) / h
			t.Logf("health checks %v, %4.1f PH/s: credited %.1f%% of the first %.0f s (%.1f rig-seconds lost), %.2f%% of %.0f s",
				healthChecks, h/1e15, 100*early, rampWindow, lost, 100*all, until)
			if early < 0.95 {
				t.Errorf("TIDES-RAMP-CREDIT: a %.1f PH/s rental (health checks: %v) was credited %.1f%% of its work in its first %.0f s",
					h/1e15, healthChecks, 100*early, rampWindow)
			}
			if all < 0.99 {
				t.Errorf("TIDES-RAMP-CREDIT-ALL: a %.1f PH/s rental (health checks: %v) was credited %.2f%% of its work in its first %.0f s",
					h/1e15, healthChecks, 100*all, until)
			}
		}
	}
}

// firstRamped is a fresh connection on s, logged in at the floor, that has mined there at hashrate
// until firstRamp lifted it; the share that did is its last. It returns when that share came.
func firstRamped(t *testing.T, s *Server, hashrate float64) (*rampMiner, time.Time) {
	t.Helper()
	m := newRampMiner(t, s, 1)
	s.clients.Store(m.c.ID, m.c)
	for i := 0; i < 1000; i++ {
		m.sec += m.rnd.ExpFloat64() * m.jobDiff * (1 << 32) / hashrate
		at := m.base.Add(time.Duration(m.sec * float64(time.Second)))
		m.c.mu.Lock()
		m.c.ProvenDifficulty, m.c.ProvenAt = m.jobDiff, at
		m.c.mu.Unlock()
		m.share()
		m.c.mu.RLock()
		done := m.c.FirstRampDone
		m.c.mu.RUnlock()
		if done {
			return m, at
		}
	}
	t.Fatal("TIDES-COUNT-RAMP-SETUP: firstRamp never lifted the connection")
	return nil, time.Time{}
}

// The level firstRamp sets is proven: every share that measured it met the floor, at the rate that
// level stands for. It counts in MaxDifficulty from the moment it is set, as far as the connection
// stays at it. Any other raise counts only as far as provenAhead times the last share.
func TestTheLevelAFirstRampSetsCounts(t *testing.T) {
	s := rentalPortServer()
	t.Cleanup(s.Stop)
	floor := s.config.AbsoluteMinDiff
	m, last := firstRamped(t, s, 4.5e15)
	level := m.difficulty()
	if level <= 2*provenAhead*floor {
		t.Fatalf("TIDES-COUNT-RAMP-SETUP: firstRamp lifted a 4.5 PH/s rental to %g only", level)
	}
	if got := s.maxDifficultyAt(last); got != level {
		t.Errorf("TIDES-COUNT-RAMP: firstRamp lifted a 4.5 PH/s rental from %g to %g; before its first share there it counts %g",
			floor, level, got)
	}
	m.c.mu.Lock()
	m.c.Difficulty, m.c.LastDifficultySent = level/3, level/3
	m.c.mu.Unlock()
	if got := s.maxDifficultyAt(last); got != level/3 {
		t.Errorf("TIDES-COUNT-RAMP-LOWERED: lowered to %g after its first ramp to %g, the connection counts %g", level/3, level, got)
	}

	// A connection given 4M whose shares prove only the floor (a share is accepted at the port's
	// floor, and credited what it proves), timed as if they met 4M: vardiff raises it.
	at := last.Add(time.Hour)
	c := &Client{ID: "cheap", IP: "203.0.113.8:4000", MinerID: testPayout, WorkerName: "rig2", Authorized: true,
		Difficulty: 8 * floor, LastDifficultySent: 8 * floor, ProvenDifficulty: floor, ProvenAt: at}
	poolSide, minerSide := net.Pipe()
	t.Cleanup(func() { poolSide.Close(); minerSide.Close() })
	go io.Copy(io.Discard, minerSide)
	c.Conn = poolSide
	s.clients.Delete(m.c.ID)
	s.clients.Store(c.ID, c)
	c.mu.Lock()
	for i := 0; i < VardiffMinShares+2; i++ {
		c.addShareSample(at.Add(time.Duration(i-VardiffMinShares-2)*100*time.Millisecond), 8*floor)
	}
	c.mu.Unlock()
	s.adjustVardiffAt(c, at)
	c.mu.RLock()
	raised := c.Difficulty
	c.mu.RUnlock()
	if raised <= 8*floor {
		t.Fatalf("TIDES-COUNT-RAMP-SETUP: vardiff left the connection at %g", raised)
	}
	if got := s.maxDifficultyAt(at); got != provenAhead*floor {
		t.Errorf("TIDES-COUNT-RAMP-ONLY: raised from %g to %g, its shares proving %g, the connection counts %g, want %g",
			8*floor, raised, floor, got, provenAhead*floor)
	}
}

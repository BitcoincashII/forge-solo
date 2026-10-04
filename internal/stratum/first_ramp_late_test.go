package stratum

import (
	"fmt"
	"io"
	"math"
	"math/rand"
	"net"
	"testing"
	"time"
)

// rampMiner is a miner on one connection, run against the vardiff handleSubmit runs
// (addShareSample, then adjustVardiffAt). It finds shares at random at the rate its hashrate gives
// against the difficulty its job went out under; a job goes out every 10 seconds, and the miner
// applies mining.set_difficulty from the next job on.
type rampMiner struct {
	s       *Server
	c       *Client
	rnd     *rand.Rand
	base    time.Time // when the connection's first job went out
	sec     float64   // seconds since then
	nextJob float64
	jobDiff float64
	changes []diffChange
}

// newRampMiner is a fresh connection at its floor, its first job just sent.
func newRampMiner(t *testing.T, s *Server, seed int64) *rampMiner {
	t.Helper()
	poolSide, minerSide := net.Pipe()
	t.Cleanup(func() { poolSide.Close(); minerSide.Close() })
	go io.Copy(io.Discard, minerSide) // drain mining.set_difficulty
	base := time.Unix(1_790_000_000, 0)
	floor := s.config.AbsoluteMinDiff
	c := &Client{ID: "late", Conn: poolSide, IP: "203.0.113.7:4000", MinerID: testPayout, WorkerName: "rig1",
		Authorized: true, Difficulty: floor, ConnectedAt: base.Add(-time.Second), firstJobAt: base}
	return &rampMiner{s: s, c: c, rnd: rand.New(rand.NewSource(seed)), base: base, nextJob: 10, jobDiff: floor,
		changes: []diffChange{{0, floor}}}
}

// resume puts the connection at diff, as a remembered level or a d= password does.
func (m *rampMiner) resume(diff float64) {
	m.c.mu.Lock()
	m.c.Difficulty = diff
	m.c.mu.Unlock()
	m.jobDiff = diff
	m.changes = []diffChange{{0, diff}}
}

func (m *rampMiner) difficulty() float64 {
	m.c.mu.RLock()
	defer m.c.mu.RUnlock()
	return m.c.Difficulty
}

// note records the difficulty if it changed.
func (m *rampMiner) note() {
	if d := m.difficulty(); d != m.changes[len(m.changes)-1].diff {
		m.changes = append(m.changes, diffChange{m.sec, d})
	}
}

// mine runs the miner at hashrate (H/s; 0: not hashing) until second until.
func (m *rampMiner) mine(hashrate, until float64) {
	for m.sec < until {
		next := math.Min(m.nextJob, until)
		if hashrate > 0 {
			if at := m.sec + m.rnd.ExpFloat64()*m.jobDiff*(1<<32)/hashrate; at < next {
				m.sec = at
				m.share()
				continue
			}
		}
		m.sec = next
		if next == m.nextJob {
			m.nextJob += 10
			m.jobDiff = m.difficulty()
		}
	}
}

// share is an accepted share, as handleSubmit records it.
func (m *rampMiner) share() {
	at := m.base.Add(time.Duration(m.sec * float64(time.Second)))
	m.c.mu.Lock()
	n := m.c.addShareSample(at, m.jobDiff)
	m.c.mu.Unlock()
	m.c.ValidShares.Add(1)
	if n >= VardiffMinShares {
		m.s.adjustVardiffAt(m.c, at)
	}
	m.note()
}

// firstChangeAfter is the first difficulty the connection was set to at or after second sec, or
// the one in force if there was none.
func (m *rampMiner) firstChangeAfter(sec float64) float64 {
	for _, ch := range m.changes[1:] {
		if ch.sec >= sec {
			return ch.diff
		}
	}
	return m.difficulty()
}

// A miner that starts hashing a while after its first job (a reboot warming up, an order filled
// later, rigs that join a proxy later) was held at its floor for good: its shares were measured
// against all the time since the first job, and once the record of its shares was full that time
// only grew. A 100 TH/s miner stayed at 1024 on the main port, 23 shares a second, and a 5 PH/s
// order stayed at 500000 on the rental port.
func TestFirstRampLiftsAMinerThatStartsHashingLate(t *testing.T) {
	cases := []struct {
		name     string
		s        func() *Server
		hashrate float64
	}{
		{"main port, 100 TH/s", mainPortServer, 100e12},
		{"rental port, 5 PH/s", rentalPortServer, 5e15},
	}
	for _, k := range cases {
		for _, idle := range []float64{0, 300, 480, 1200} {
			for seed := int64(1); seed <= 3; seed++ {
				s := k.s()
				want := level(s, k.hashrate)
				m := newRampMiner(t, s, seed)
				m.mine(0, idle)
				m.mine(k.hashrate, idle+600)
				name := fmt.Sprintf("%s, hashing from %.0f s after its first job, seed %d", k.name, idle, seed)
				if got := m.difficulty(); got < want/3 || got > 3*want {
					t.Errorf("RAMP-LATE: %s: %.4g after ten minutes of hashing, its level is %.4g", name, got, want)
				}
				// One step, not the ordinary ramp's +50% at a time.
				if first := m.firstChangeAfter(idle); first < want/4 {
					t.Errorf("RAMP-LATE-ONE-STEP: %s: it first left its floor for %.4g, its level is %.4g", name, first, want)
				}
			}
		}
	}
}

// The idle rescue puts a resumed connection that has sent no share back to its floor. A miner that
// then started hashing was held there for good, as above.
func TestFirstRampAfterTheIdleRescue(t *testing.T) {
	for _, start := range []float64{420, 900} {
		s := mainPortServer()
		want := level(s, 100e12)
		m := newRampMiner(t, s, 1)
		m.resume(want)
		s.clients.Store(m.c.ID, m.c)
		m.mine(0, 240)
		s.resetIdleDifficulties(m.base.Add(240 * time.Second))
		m.note()
		if d := m.difficulty(); d != s.config.AbsoluteMinDiff {
			t.Fatalf("RAMP-IDLE-SETUP: the idle rescue left the connection at %.4g", d)
		}
		m.mine(0, start)
		m.mine(100e12, start+600)
		if got := m.difficulty(); got < want/3 || got > 3*want {
			t.Errorf("RAMP-IDLE-RESET: hashing from %.0f s, reset to the floor at 240 s: %.4g after ten minutes, its level is %.4g",
				start, got, want)
		}
	}
}

// A connection that resumed a remembered level, or was given one with d=, still has its one escape
// from the floor. Walked down to the floor while its hashrate was low, it was held there for good
// once the hashrate came back.
func TestVardiffClimbsBackFromTheFloor(t *testing.T) {
	cases := []struct {
		name               string
		s                  func() *Server
		before, dip, after float64
	}{
		{"main port", mainPortServer, 10e12, 0.5e12, 100e12},
		{"rental port", rentalPortServer, 1e15, 50e12, 5e15},
	}
	for _, k := range cases {
		s := k.s()
		m := newRampMiner(t, s, 1)
		m.resume(level(s, k.before))
		m.mine(k.before, 1200)
		m.mine(k.dip, 3000)
		if d := m.difficulty(); d != s.config.AbsoluteMinDiff {
			t.Fatalf("RAMP-DIP-SETUP: %s: the dip left the miner at %.4g, not at its floor", k.name, d)
		}
		m.mine(k.after, 4200)
		if got, want := m.difficulty(), level(s, k.after); got < want/3 || got > 3*want {
			t.Errorf("RAMP-CLIMBS-BACK: %s: twenty minutes after its hashrate came back the miner is at %.4g, its level is %.4g",
				k.name, got, want)
		}
	}
}

// Where the time since the first job says the miner is slower than its latest shares do, the step
// is the ordinary one, and the escape from the floor is kept for later.
func TestFirstRampGoesNoLowerThanTheOrdinaryStep(t *testing.T) {
	s := mainPortServer()
	floor := s.config.AbsoluteMinDiff
	every := time.Duration(float64(s.config.TargetShareTime) / 1.4 * float64(time.Second)) // 1.4 times the target rate
	first := time.Now().Add(-time.Hour)
	c := rampingClient(t, s, first.Add(-5*every))
	for i := 0; i < VardiffMinShares; i++ {
		c.ShareSamples = append(c.ShareSamples, shareSample{at: first.Add(time.Duration(i) * every), diff: floor})
	}
	s.adjustVardiffAt(c, c.ShareSamples[len(c.ShareSamples)-1].at)
	c.mu.RLock()
	got, ramped := c.Difficulty, c.FirstRampDone
	c.mu.RUnlock()
	if math.Abs(got/(1.4*floor)-1) > 0.01 {
		t.Errorf("RAMP-ORDINARY-STEP: a miner at 1.4 times the target rate went from %v to %.4g, want %.4g", floor, got, 1.4*floor)
	}
	if ramped {
		t.Error("RAMP-ESCAPE-KEPT: the escape from the floor was used on a step no bigger than the ordinary one")
	}
}

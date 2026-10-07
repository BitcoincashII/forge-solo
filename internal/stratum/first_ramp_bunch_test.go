package stratum

import (
	"fmt"
	"io"
	"math"
	"net"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// mainPortServer has the main port's shipped vardiff (docker/stratum/config.template.yaml).
func mainPortServer() *Server {
	return NewServer(&ServerConfig{
		MinDiff: 1024, MaxDiff: 1e12, VardiffEnabled: true, TargetShareTime: 5, RetargetTime: 10,
		VariancePercent: 0.25, SoloOnly: true,
	}, zap.NewNop(), nil)
}

// rampingClient is a connection at its floor whose first job went out at jobAt, with no shares yet.
func rampingClient(t *testing.T, s *Server, jobAt time.Time) *Client {
	t.Helper()
	poolSide, minerSide := net.Pipe()
	t.Cleanup(func() { poolSide.Close(); minerSide.Close() })
	go io.Copy(io.Discard, minerSide)
	return &Client{ID: "bunched", Conn: poolSide, IP: "203.0.113.5:4000", MinerID: testPayout, Authorized: true,
		Difficulty: s.config.AbsoluteMinDiff, ConnectedAt: jobAt.Add(-time.Second), firstJobAt: jobAt}
}

// level is the difficulty at which hashrate (H/s) finds a share every TargetShareTime seconds.
func level(s *Server, hashrate float64) float64 {
	return hashrate * float64(s.config.TargetShareTime) / (1 << 32)
}

// firstRamp lifts a connection off its floor by the rate its first shares measure. Those were timed
// by when the stratum read them, so shares that arrived together (the segment carrying the first
// ones lost and resent, or the stratum held up for a moment) measured a rate hundreds of
// thousands of times the real one: the main port set a 200 TH/s miner to 5e8 (one share every 3
// hours), and the rental port set its floor of 500000 to 2.5e11, above the network difficulty, so
// the miner sent none of the blocks it found. While the record holds every share since the first
// job, the step is checked against the rate over that whole time, and waits for more shares where
// the two disagree.
func TestFirstRampIsBoundedByTheTimeSinceTheFirstJob(t *testing.T) {
	cases := []struct {
		name     string
		s        *Server
		hashrate float64
	}{
		{"main port, 200 TH/s", mainPortServer(), 200e12},
		{"main port, 1 PH/s", mainPortServer(), 1e15},
		{"rental port, 1 PH/s", rentalPortServer(), 1e15},
		{"rental port, 10 PH/s", rentalPortServer(), 10e15},
	}
	for _, k := range cases {
		floor := k.s.config.AbsoluteMinDiff
		every := time.Duration(floor * (1 << 32) / k.hashrate * float64(time.Second))
		jobAt := time.Now().Add(-time.Hour)
		// One found every `every` after the job. The first ten were held 200 ms past the tenth (a
		// lost segment resent), so they and every share found meanwhile are read together, 10
		// microseconds apart; later ones are read as they are found.
		together := jobAt.Add(time.Duration(VardiffMinShares)*every + 200*time.Millisecond)
		want := level(k.s, k.hashrate)
		// ramp gives c its shares one at a time, as handleSubmit does, until its difficulty changes.
		ramp := func(c *Client) float64 {
			for i := 1; i <= 3*maxShareSamples; i++ {
				at := jobAt.Add(time.Duration(i) * every)
				if !at.After(together) {
					at = together.Add(time.Duration(i) * 10 * time.Microsecond)
				}
				c.mu.Lock()
				n := c.addShareSample(at, floor)
				c.mu.Unlock()
				if n >= VardiffMinShares {
					k.s.adjustVardiffAt(c, at)
				}
				c.mu.RLock()
				d := c.Difficulty
				c.mu.RUnlock()
				if d != floor {
					return d
				}
			}
			return floor
		}

		got := ramp(rampingClient(t, k.s, jobAt))
		if got > 1.1*want {
			t.Errorf("RAMP-BUNCHED: %s: shares read together set the difficulty to %.4g, %.2fx the miner's level %.4g",
				k.name, got, got/want, want)
		}
		if want > 2*floor && got <= floor {
			t.Errorf("RAMP-BUNCHED-ESCAPES: %s: the miner stayed at its floor %.4g, its level is %.4g", k.name, floor, want)
		}

		// Shares on a connection that was never sent a job are checked against the time since it connected.
		noJob := rampingClient(t, k.s, jobAt)
		noJob.firstJobAt = time.Time{}
		if got := ramp(noJob); got > 1.1*want {
			t.Errorf("RAMP-BUNCHED-NO-JOB: %s: shares read together on a connection sent no job set the difficulty to %.4g, %.2fx the miner's level %.4g",
				k.name, got, got/want, want)
		}
	}
}

// A rental of about 1 PH/s at the rental port's floor sends a share every 2 seconds. Where its
// first minute or two of shares arrive together, they are still within VardiffSampleTime when the
// record has dropped its first share and firstRamp no longer checks the step against the time
// since the first job; measured over that time, they set such a rental 2.5 times its level. It
// leaves its floor in one step to its level. The shares read together still count among its latest
// ones for a while after that step, so it can be raised once more, as before, but not far.
func TestFirstRampOfARentalWhoseFirstSharesArriveTogether(t *testing.T) {
	s := rentalPortServer()
	const hashrate = 1e15
	floor, want := s.config.AbsoluteMinDiff, level(s, hashrate)
	for _, bunch := range []int{VardiffMinShares, VardiffSampleShares, 2 * VardiffSampleShares} {
		c := rampingClient(t, s, time.Now().Add(-time.Hour))
		at := func(sec float64) time.Time { return c.firstJobAt.Add(time.Duration(sec * float64(time.Second))) }
		difficulty := func() float64 {
			c.mu.RLock()
			defer c.mu.RUnlock()
			return c.Difficulty
		}
		read := func(sec, foundAt float64) {
			c.mu.Lock()
			n := c.addShareSample(at(sec), foundAt)
			c.mu.Unlock()
			if n >= VardiffMinShares {
				s.adjustVardiffAt(c, at(sec))
			}
		}
		// The miner hashes from its first job and finds a share each time its work reaches the
		// difficulty of the job it works on; a job goes out every 10 s. Its first `bunch` shares, and
		// every one found until 200 ms after the last of them, are read together.
		var held []float64
		holding, heldUntil := true, math.Inf(1)
		jobDiff, nextJob, work, sec := floor, 10.0, 0.0, 0.0
		left, first, high := math.Inf(1), 0.0, 0.0
		for sec < 900 {
			next := nextJob
			if holding && heldUntil < next {
				next = heldUntil
			}
			if found := sec + math.Max(0, jobDiff*(1<<32)-work)/hashrate; found < next {
				sec, work = found, 0
				if holding {
					if held = append(held, jobDiff); len(held) == bunch {
						heldUntil = sec + 0.2
					}
				} else {
					read(sec, jobDiff)
				}
			} else {
				work += (next - sec) * hashrate
				sec = next
				if holding && sec == heldUntil {
					for i, d := range held {
						read(sec+float64(i)*10e-6, d)
					}
					holding = false
				}
				if sec == nextJob {
					jobDiff, nextJob = difficulty(), nextJob+10
				}
			}
			if d := difficulty(); d != floor && math.IsInf(left, 1) {
				left, first = sec, d
			}
			if !math.IsInf(left, 1) {
				high = math.Max(high, difficulty())
			}
		}
		name := fmt.Sprintf("1 PH/s at the rental floor, its first %d shares read together", bunch)
		if math.IsInf(left, 1) || left > 300 {
			t.Errorf("RAMP-BUNCHED-RENTAL-LEAVES: %s: still at its floor %.4g after %.0f s, its level is %.4g", name, floor, math.Min(left, sec), want)
			continue
		}
		if first > 1.1*want {
			t.Errorf("RAMP-BUNCHED-RENTAL-STEP: %s: its first step, %.0f s after its first job, set %.4g, %.2fx its level %.4g",
				name, left, first, first/want, want)
		}
		if high > 1.5*want {
			t.Errorf("RAMP-BUNCHED-RENTAL-AFTER: %s: set as high as %.4g, %.2fx its level %.4g, in its first 15 minutes",
				name, high, high/want, want)
		}
	}
}

// The bound counts from the first job a connection was sent: later jobs do not move it.
func TestFirstJobTimeIsKept(t *testing.T) {
	s, _ := perJobServer()
	c := perJobClient(t)
	before := time.Now()
	s.sendJob(c, soloTestJob("1"))
	c.mu.RLock()
	first := c.firstJobAt
	c.mu.RUnlock()
	if first.Before(before) || first.After(time.Now()) {
		t.Fatalf("RAMP-FIRST-JOB: the first job's time is %v, want the moment it went out", first)
	}
	time.Sleep(5 * time.Millisecond)
	s.sendJob(c, soloTestJob("2"))
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.firstJobAt.Equal(first) {
		t.Fatalf("RAMP-FIRST-JOB-KEPT: a later job moved the first job's time from %v to %v", first, c.firstJobAt)
	}
}

// One step goes at most firstRampMaxStep times the floor, however fast the shares say the miner
// is: once the record is full it can be all one bunch. The ordinary ramp does the rest.
func TestFirstRampStepIsCapped(t *testing.T) {
	s := mainPortServer()
	now := time.Now()
	c := rampingClient(t, s, now.Add(-5500*time.Microsecond))
	for i := 0; i < VardiffMinShares; i++ { // one every half millisecond: 10000 times the rate at the floor
		c.ShareSamples = append(c.ShareSamples, shareSample{at: now.Add(time.Duration(i-VardiffMinShares) * 500 * time.Microsecond)})
	}
	s.adjustVardiffAt(c, c.ShareSamples[len(c.ShareSamples)-1].at)
	c.mu.RLock()
	got := c.Difficulty
	c.mu.RUnlock()
	if floor := s.config.AbsoluteMinDiff; got > floor*firstRampMaxStep {
		t.Fatalf("RAMP-STEP-CAP: one step took the floor %v to %.4g, %.0fx", floor, got, got/floor)
	}
}

// sharesTogether sends job to the miner m (logged in as rig1), finds n shares on it at the floor and
// writes them in one go. It returns how long finding them took and, once all are answered, the
// difficulty the stratum has for the miner.
func sharesTogether(t *testing.T, s *Server, m *testMiner, job *Job, n int) (time.Duration, float64) {
	t.Helper()
	s.BroadcastJob(job)
	for !strings.Contains(m.line(t), MethodNotify) {
	}
	start := time.Now()
	var lines strings.Builder
	for k := 1; k <= n; k++ {
		en2 := fmt.Sprintf("%016x", k)
		for i := uint32(0); ; i++ {
			nonce := fmt.Sprintf("%08x", i)
			ok, _, _, err := s.validateShare(job, m.en1, en2, job.NTime, nonce, "", s.config.MinDiff)
			if err != nil {
				t.Fatal(err)
			}
			if ok {
				fmt.Fprintf(&lines, `{"id":%d,"method":"mining.submit","params":["rig1","%s","%s","%s","%s"]}`+"\n",
					100+k, job.ID, en2, job.NTime, nonce)
				break
			}
		}
	}
	mined := time.Since(start)
	m.c.Write([]byte(lines.String()))
	for answered := 0; answered < n; {
		if strings.Contains(m.line(t), `"result":true`) {
			answered++
		}
	}
	var got float64
	s.clients.Range(func(_, v interface{}) bool {
		c := v.(*Client)
		c.mu.RLock()
		got = c.Difficulty
		c.mu.RUnlock()
		return false
	})
	return mined, got
}

// The same over a real connection: a miner writes its first twelve shares in one go.
func TestFirstSharesSentTogetherDoNotOvershoot(t *testing.T) {
	s := tcpServer(t, nil)
	m := dialTestMiner(t, s, "rig1")
	const shares = 12
	mined, got := sharesTogether(t, s, m, soloTestJob("1"), shares)
	// The miner's real rate: twelve shares at the floor in the time it took to find them.
	want := s.config.MinDiff * float64(s.config.TargetShareTime) / (mined.Seconds() / shares)
	if got > 2*want {
		t.Fatalf("RAMP-BUNCHED-TCP: twelve shares written together set the difficulty to %.4g, %.0fx the miner's level %.4g",
			got, got/want, want)
	}
}

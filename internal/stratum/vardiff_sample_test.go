package stratum

import (
	"io"
	"math"
	"math/rand"
	"net"
	"sort"
	"testing"
	"time"

	"go.uber.org/zap"
)

// measuredShareTime counts each share as the work it was found against, in shares at the current
// difficulty, over the latest VardiffSampleShares shares.
func TestMeasuredShareTime(t *testing.T) {
	base := time.Unix(1_790_000_000, 0)
	// samples: one share at base, then one per (seconds, found-against) step.
	samples := func(steps ...[2]float64) []shareSample {
		out := []shareSample{{at: base}}
		at := base
		for _, st := range steps {
			at = at.Add(time.Duration(st[0] * float64(time.Second)))
			out = append(out, shareSample{at: at, diff: st[1]})
		}
		return out
	}
	repeat := func(n int, step [2]float64) [][2]float64 {
		out := make([][2]float64, n)
		for i := range out {
			out[i] = step
		}
		return out
	}
	long := append(repeat(10, [2]float64{1000, 8}), repeat(VardiffSampleShares, [2]float64{5, 8})...)
	cases := []struct {
		name    string
		samples []shareSample
		current float64
		want    float64
	}{
		{"all at the current difficulty", samples(repeat(9, [2]float64{5, 8})...), 8, 5},
		{"difficulty not known: the current one", samples(repeat(9, [2]float64{5, 0})...), 8, 5},
		{"found at half the current difficulty", samples(repeat(9, [2]float64{2, 4})...), 8, 4},
		{"found at twice the current difficulty", samples(repeat(9, [2]float64{10, 16})...), 8, 5},
		{"before and after a raise", samples([2]float64{2, 4}, [2]float64{2, 4}, [2]float64{4, 8}, [2]float64{4, 8}), 8, 4},
		{"only the latest VardiffSampleShares count", samples(long...), 8, 5},
		{"one share: nothing to measure", samples(), 8, 0},
		{"no time between them", samples(repeat(9, [2]float64{0, 8})...), 8, 0},
		{"no current difficulty", samples(repeat(9, [2]float64{5, 8})...), 0, 0},
	}
	for _, k := range cases {
		if got := measuredShareTime(k.samples, k.current); math.Abs(got-k.want) > 1e-9 {
			t.Errorf("SHARE-TIME: %s: %g s, want %g s", k.name, got, k.want)
		}
	}
}

// After a raise, the shares found before it are still in the sample. Counted at the new difficulty
// they say the miner is faster than it is, and the next step overshot its level.
func TestVardiffDoesNotOvershootAfterARaise(t *testing.T) {
	s := newRampTestServer(t)
	const d1 = 1 << 20
	level := 2.1 * d1 // where this miner finds a share every TargetShareTime seconds
	d2 := 1.5 * d1    // where the last adjustment put it
	every := func(d float64) time.Duration {
		return time.Duration(float64(s.config.TargetShareTime) * d / level * float64(time.Second))
	}
	at := time.Now().Add(-time.Hour)
	samples := []shareSample{{at: at, diff: d1}}
	for i := 0; i < 7; i++ { // found at d1, before the raise
		at = at.Add(every(d1))
		samples = append(samples, shareSample{at: at, diff: d1})
	}
	for i := 0; i < 2; i++ { // found at d2, since
		at = at.Add(every(d2))
		samples = append(samples, shareSample{at: at, diff: d2})
	}
	c, done := rampClient(t, s, d2, 1)
	defer done()
	c.ShareSamples, c.FirstRampDone, c.DifficultyChangedAt = samples, true, at.Add(-time.Hour)

	s.adjustVardiffAt(c, at)
	c.mu.RLock()
	got := c.Difficulty
	c.mu.RUnlock()
	if math.Abs(got/level-1) > 0.01 {
		t.Fatalf("OVERSHOOT: raised from %g to %g; the miner's level is %g", d2, got, level)
	}
}

// diffChange is a difficulty vardiff set, and the simulated second it set it at.
type diffChange struct{ sec, diff float64 }

// simulateVardiff runs the vardiff handleSubmit runs (addShareSample, then adjustVardiffAt) on a
// simulated miner, for seconds, and returns every difficulty it was set to. The miner finds shares
// at random, at the rate hashrate (H/s) gives at the difficulty its job went out under; a job goes
// out every 10 seconds, and the miner applies mining.set_difficulty from the next job on, as the
// stratum spec says and as the Braiins OS rental of 2026-10-02 did.
func simulateVardiff(t *testing.T, s *Server, seed int64, start float64, hashrate func(sec float64) float64, seconds float64) []diffChange {
	t.Helper()
	poolSide, minerSide := net.Pipe()
	defer poolSide.Close()
	defer minerSide.Close()
	go io.Copy(io.Discard, minerSide) // drain mining.set_difficulty

	base := time.Unix(1_790_000_000, 0)
	c := &Client{ID: "sim", Conn: poolSide, MinerID: "sim", Authorized: true, Difficulty: start,
		FirstRampDone: true, DifficultyChangedAt: base.Add(-time.Hour)}
	rnd := rand.New(rand.NewSource(seed))
	const jobEvery = 10.0
	changes := []diffChange{{0, start}}
	jobDiff, nextJob, sec := start, 0.0, 0.0
	for sec < seconds {
		wait := rnd.ExpFloat64() * jobDiff * (1 << 32) / hashrate(sec)
		if sec+wait >= nextJob { // a new job first: the miner moves to it, at the difficulty now in force
			sec, nextJob = nextJob, nextJob+jobEvery
			c.mu.RLock()
			jobDiff = c.Difficulty
			c.mu.RUnlock()
			continue
		}
		sec += wait
		at := base.Add(time.Duration(sec * float64(time.Second)))
		c.mu.Lock()
		n := c.addShareSample(at, jobDiff)
		c.mu.Unlock()
		if n >= VardiffMinShares {
			s.adjustVardiffAt(c, at)
		}
		c.mu.RLock()
		d := c.Difficulty
		c.mu.RUnlock()
		if d != changes[len(changes)-1].diff {
			changes = append(changes, diffChange{sec, d})
		}
	}
	return changes
}

// diffAt is the difficulty in force at sec.
func diffAt(changes []diffChange, sec float64) float64 {
	d := changes[0].diff
	for _, ch := range changes {
		if ch.sec > sec {
			break
		}
		d = ch.diff
	}
	return d
}

// rentalPortServer has the rental port's shipped vardiff (docker/stratum/config.template.yaml).
func rentalPortServer() *Server {
	return NewServer(&ServerConfig{
		MinDiff: 500000, MaxDiff: 1e12, VardiffEnabled: true, TargetShareTime: 5, RetargetTime: 10,
	}, zap.NewNop(), nil, nil)
}

func median(xs []float64) float64 {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	return s[len(s)/2]
}

// A steady miner stays near its level. With each share counted at the current difficulty, over 10
// shares, the 5 PH/s rental of 2026-10-02 swung between 0.5x and 3.4x of its level, a dozen changes
// in 7 minutes; this simulation of the same miner on that code ranged about 0.3x to 3.3x in the
// typical hour, with about 130 changes.
func TestVardiffHoldsASteadyMinerNearItsLevel(t *testing.T) {
	s := rentalPortServer()
	hashrate := 5e15
	level := hashrate * float64(s.config.TargetShareTime) / (1 << 32)
	var lows, highs, perHour []float64
	for seed := int64(1); seed <= 20; seed++ {
		changes := simulateVardiff(t, s, seed, level, func(float64) float64 { return hashrate }, 3900)
		low, high, n := math.Inf(1), 0.0, 0
		for sec := 300.0; sec < 3900; sec += 1 {
			d := diffAt(changes, sec) / level
			low, high = math.Min(low, d), math.Max(high, d)
		}
		for _, ch := range changes {
			if ch.sec >= 300 {
				n++
			}
		}
		lows, highs, perHour = append(lows, low), append(highs, high), append(perHour, float64(n))
		if low < 0.45 || high > 2.5 {
			t.Errorf("STEADY-RANGE: seed %d: a steady miner's difficulty ranged %.2fx to %.2fx of its level over an hour", seed, low, high)
		}
	}
	t.Logf("over 20 simulated hours: typical range %.2fx to %.2fx of the level, %.0f changes an hour",
		median(lows), median(highs), median(perHour))
	if median(lows) < 0.6 || median(highs) > 1.9 {
		t.Errorf("STEADY-TYPICAL: a steady miner's difficulty typically ranged %.2fx to %.2fx of its level", median(lows), median(highs))
	}
	if median(perHour) > 40 {
		t.Errorf("STEADY-CHANGES: a steady miner's difficulty typically changed %.0f times an hour", median(perHour))
	}
}

// A longer sample is slower to follow a real change: it must still follow one within minutes.
func TestVardiffFollowsAHashrateDrop(t *testing.T) {
	s := rentalPortServer()
	before, after := 5e15, 1e15
	level := func(h float64) float64 { return h * float64(s.config.TargetShareTime) / (1 << 32) }
	var took []float64
	for seed := int64(1); seed <= 20; seed++ {
		changes := simulateVardiff(t, s, seed, level(before), func(sec float64) float64 {
			if sec < 600 {
				return before
			}
			return after
		}, 2400)
		settled := math.Inf(1)
		for _, ch := range changes {
			if ch.sec >= 600 && ch.diff <= 1.43*level(after) {
				settled = ch.sec - 600
				break
			}
		}
		took = append(took, settled)
		if settled > 900 {
			t.Errorf("DROP-SETTLES: seed %d: %.0f s after the miner fell to a fifth, its difficulty was not yet near its new level", seed, settled)
		}
	}
	t.Logf("a fall to a fifth of the hashrate: typically near the new level in %.0f s", median(took))
	if median(took) > 600 {
		t.Errorf("DROP-TYPICAL: typically %.0f s to follow a fall to a fifth of the hashrate", median(took))
	}
}

// The record of a client's shares is bounded, and keeps the latest.
func TestShareSamplesAreBounded(t *testing.T) {
	c := &Client{}
	base := time.Unix(1_790_000_000, 0)
	n := 0
	for i := 0; i < maxShareSamples+50; i++ {
		n = c.addShareSample(base.Add(time.Duration(i)*time.Second), 1)
	}
	if n != maxShareSamples || len(c.ShareSamples) != maxShareSamples ||
		!c.ShareSamples[len(c.ShareSamples)-1].at.Equal(base.Add(time.Duration(maxShareSamples+49)*time.Second)) {
		t.Fatalf("SAMPLES-BOUND: %d samples on record (reported %d), want the latest %d", len(c.ShareSamples), n, maxShareSamples)
	}
}

// adjustVardiffAt judges every interval at the time it is given, including the cooldown after a
// cut for rejections: past the cooldown, a miner that proves it can work harder is raised past the
// ceiling that cut left.
func TestVardiffCooldownIsJudgedAtTheTimeGiven(t *testing.T) {
	s := newRampTestServer(t)
	const d = 1e6
	c, done := rampClient(t, s, d, float64(s.config.TargetShareTime)/2) // twice as fast as the target
	defer done()
	now := time.Now().Add(DifficultyReductionCooldown + time.Minute)
	for i := range c.ShareSamples { // the same shares, ending at now
		c.ShareSamples[i].at = c.ShareSamples[i].at.Add(DifficultyReductionCooldown + time.Minute)
	}
	c.FirstRampDone, c.DifficultyChangedAt = true, time.Now().Add(-time.Hour)
	c.DifficultyReducedFrom, c.DifficultyReducedAt = 1.25*d, time.Now() // ceiling 0.8 * 1.25d = d

	s.adjustVardiffAt(c, now)
	c.mu.RLock()
	got := c.Difficulty
	c.mu.RUnlock()
	if got != 1.5*d {
		t.Fatalf("CEILING-COOLDOWN-NOW: %v after the cooldown, the difficulty went from %g to %g, want %g", DifficultyReductionCooldown+time.Minute, d, got, 1.5*d)
	}
}

// retarget_time is counted at the time adjustVardiffAt is given: no change until it has passed
// since the last one.
func TestVardiffRetargetIsJudgedAtTheTimeGiven(t *testing.T) {
	s := newRampTestServer(t)
	const d = 1e6
	c, done := rampClient(t, s, d, float64(s.config.TargetShareTime)/2) // twice as fast as the target
	defer done()
	changed := time.Unix(1_790_000_000, 0)
	last := changed.Add(time.Duration(s.config.RetargetTime) * time.Second / 2)
	for i := range c.ShareSamples { // the same shares, ending halfway to the next retarget
		c.ShareSamples[i].at = last.Add(-time.Duration(len(c.ShareSamples)-1-i) * time.Duration(s.config.TargetShareTime) * time.Second / 2)
	}
	c.FirstRampDone, c.DifficultyChangedAt = true, changed

	s.adjustVardiffAt(c, last)
	c.mu.RLock()
	early := c.Difficulty
	c.mu.RUnlock()
	s.adjustVardiffAt(c, changed.Add(time.Duration(s.config.RetargetTime)*time.Second))
	c.mu.RLock()
	due := c.Difficulty
	c.mu.RUnlock()
	if early != d || due == d {
		t.Fatalf("RETARGET-AT-TIME: %g halfway to the retarget (want %g unchanged), %g once due (want a change)", early, d, due)
	}
}

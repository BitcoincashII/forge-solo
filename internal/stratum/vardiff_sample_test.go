package stratum

import (
	"io"
	"math"
	"math/rand"
	"net"
	"os"
	"sort"
	"testing"
	"time"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

// measuredShareTime counts each share as the work it was found against, in shares at the current
// difficulty, over the shares of the last VardiffSampleTime or the latest VardiffSampleShares,
// whichever are more.
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
	// 40 shares 2 s apart, then 30 shares 6 s apart: the latest 30 span 174 s, and VardiffSampleTime
	// reaches 31 more of the earlier ones (240 s over 60 shares).
	recent := append(repeat(40, [2]float64{2, 8}), repeat(VardiffSampleShares, [2]float64{6, 8})...)
	// The same, the earlier ones found below half the current difficulty, on the climb to it.
	climb := append(repeat(40, [2]float64{2, 3}), repeat(VardiffSampleShares, [2]float64{6, 8})...)
	// 10 shares 39 s apart, then 19 shares 10 s apart: VardiffSampleTime holds 21 of them, and the
	// latest 30 span 580 s.
	slow := append(repeat(10, [2]float64{39, 8}), repeat(19, [2]float64{10, 8})...)
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
		{"every share of the last VardiffSampleTime counts", samples(recent...), 8, 4},
		{"the time window stops at a share found on the climb", samples(climb...), 8, 6},
		{"fewer than VardiffSampleShares in VardiffSampleTime: the latest VardiffSampleShares count", samples(slow...), 8, 20},
		{"one share: nothing to measure", samples(), 8, 0},
		{"no time between them", samples(repeat(9, [2]float64{0, 8})...), 8, 0},
		{"no current difficulty", samples(repeat(9, [2]float64{5, 8})...), 0, 0},
	}
	for _, k := range cases {
		if got := measuredShareTime(k.samples, k.current, VardiffSampleTime); math.Abs(got-k.want) > 1e-9 {
			t.Errorf("SHARE-TIME: %s: %g s, want %g s", k.name, got, k.want)
		}
	}
	// No window: the latest VardiffSampleShares alone.
	if got := measuredShareTime(samples(recent...), 8, 0); math.Abs(got-6) > 1e-9 {
		t.Errorf("SHARE-TIME-LATEST: with no window, %g s, want 6 s, the latest %d shares alone", got, VardiffSampleShares)
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
// stratum spec says and as Braiins OS does.
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

// rentalPortServer has the rental port's shipped vardiff, read from docker/stratum/config.template.yaml
// (which the Windows and Linux configs are tested to match), so every rental simulation runs at what
// ships.
func rentalPortServer() *Server {
	v := shippedRentalVardiff()
	return NewServer(&ServerConfig{
		MinDiff: v.MinDiff, MaxDiff: v.MaxDiff, VardiffEnabled: v.Enabled, TargetShareTime: v.TargetTime,
		RetargetTime: v.RetargetTime,
	}, zap.NewNop(), nil, nil)
}

type rentalVardiff struct {
	Enabled      bool    `yaml:"enabled"`
	MinDiff      float64 `yaml:"min_diff"`
	MaxDiff      float64 `yaml:"max_diff"`
	TargetTime   int     `yaml:"target_time"`
	RetargetTime int     `yaml:"retarget_time"`
}

// shippedRentalVardiff is stratum_rental.vardiff in the shipped template.
func shippedRentalVardiff() rentalVardiff {
	b, err := os.ReadFile("../../docker/stratum/config.template.yaml")
	if err != nil {
		panic(err)
	}
	var cfg struct {
		Rental struct {
			Vardiff rentalVardiff `yaml:"vardiff"`
		} `yaml:"stratum_rental"`
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		panic(err)
	}
	if cfg.Rental.Vardiff.TargetTime <= 0 || cfg.Rental.Vardiff.MinDiff <= 0 {
		panic("stratum_rental.vardiff in the template has no target_time or min_diff")
	}
	return cfg.Rental.Vardiff
}

func median(xs []float64) float64 {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	return s[len(s)/2]
}

// A steady miner stays near its level. With each share counted at the current difficulty, over 10
// shares, this simulation of a steady 5 PH/s miner typically ranged about 0.3x to 3.3x of its level
// in an hour, with about 130 changes.
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

// A longer sample is slower to follow a real change: it must still follow one within a bounded
// number of target share times. Vardiff counts shares, so the time it takes grows with target_time:
// at the rental port's 25 s a fall to a fifth typically takes about half an hour (at 5 s, about 6
// minutes). The difficulty meanwhile stays inside MiningRigRentals' range, which is set by the rig's
// advertised hashrate, and the slower shares are credited in full.
func TestVardiffFollowsAHashrateDrop(t *testing.T) {
	s := rentalPortServer()
	before, after := 5e15, 1e15
	level := func(h float64) float64 { return h * float64(s.config.TargetShareTime) / (1 << 32) }
	tt := float64(s.config.TargetShareTime)
	var took []float64
	for seed := int64(1); seed <= 20; seed++ {
		changes := simulateVardiff(t, s, seed, level(before), func(sec float64) float64 {
			if sec < 600 {
				return before
			}
			return after
		}, 600+360*tt)
		settled := math.Inf(1)
		for _, ch := range changes {
			if ch.sec >= 600 && ch.diff <= 1.43*level(after) {
				settled = ch.sec - 600
				break
			}
		}
		took = append(took, settled)
		if settled > 180*tt {
			t.Errorf("DROP-SETTLES: seed %d: %.0f s after the miner fell to a fifth, its difficulty was not yet near its new level", seed, settled)
		}
	}
	t.Logf("a fall to a fifth of the hashrate: typically near the new level in %.0f s", median(took))
	if median(took) > 120*tt {
		t.Errorf("DROP-TYPICAL: typically %.0f s to follow a fall to a fifth of the hashrate", median(took))
	}
}

// A steady miner on the main port changes difficulty a few times an hour, not every few minutes,
// whether it took its first step off the floor or resumed a remembered level. Over its latest 30
// shares alone, two and a half minutes at the main port's target_time, luck took a steady miner
// over the edge of the variance window about 20 times an hour, and to 1.65x its level.
func TestVardiffHoldsASteadyMainPortMinerNearItsLevel(t *testing.T) {
	s := mainPortServer()
	hashrate := 100e12
	lvl := level(s, hashrate)
	for _, k := range []struct {
		name string
		run  func(seed int64) []diffChange
	}{
		{"after its first step", func(seed int64) []diffChange {
			return simulateVardiff(t, s, seed, lvl, func(float64) float64 { return hashrate }, 3900)
		}},
		{"resumed at its level", func(seed int64) []diffChange {
			m := newRampMiner(t, s, seed)
			m.resume(lvl)
			m.mine(hashrate, 3900)
			return m.changes
		}},
	} {
		var lows, highs, perHour []float64
		for seed := int64(1); seed <= 20; seed++ {
			changes := k.run(seed)
			low, high, n := math.Inf(1), 0.0, 0
			for sec := 300.0; sec < 3900; sec++ {
				d := diffAt(changes, sec) / lvl
				low, high = math.Min(low, d), math.Max(high, d)
			}
			for _, ch := range changes {
				if ch.sec >= 300 {
					n++
				}
			}
			lows, highs, perHour = append(lows, low), append(highs, high), append(perHour, float64(n))
		}
		t.Logf("%s, over 20 simulated hours: typical range %.2fx to %.2fx of the level, %.0f changes an hour",
			k.name, median(lows), median(highs), median(perHour))
		if median(perHour) > 12 {
			t.Errorf("STEADY-MAIN-CHANGES: a steady miner on the main port, %s, typically changed difficulty %.0f times an hour",
				k.name, median(perHour))
		}
		if median(lows) < 0.7 || median(highs) > 1.45 {
			t.Errorf("STEADY-MAIN-RANGE: a steady miner on the main port, %s, typically ranged %.2fx to %.2fx of its level",
				k.name, median(lows), median(highs))
		}
	}
}

// The longer sample still follows a real change within a few minutes: a miner on the main port
// whose hashrate halves or doubles is near its new level within about four minutes, and within ten
// whatever its luck.
func TestVardiffFollowsAHalvingAndADoublingOnTheMainPort(t *testing.T) {
	s := mainPortServer()
	const before, at = 100e12, 1800.0
	for _, k := range []struct {
		name   string
		factor float64
	}{{"halves", 0.5}, {"doubles", 2}} {
		after := before * k.factor
		var took []float64
		for seed := int64(1); seed <= 40; seed++ {
			changes := simulateVardiff(t, s, seed, level(s, before), func(sec float64) float64 {
				if sec < at {
					return before
				}
				return after
			}, at+900)
			near := math.Inf(1)
			for sec := at; sec < at+900; sec++ {
				if r := diffAt(changes, sec) / level(s, after); r >= 1/1.3 && r <= 1.3 {
					near = sec - at
					break
				}
			}
			took = append(took, near)
			if near > 600 {
				t.Errorf("FOLLOW-MAIN-SETTLES: seed %d: %.0f s after the miner's hashrate %s, its difficulty was not yet near its new level",
					seed, near, k.name)
			}
		}
		t.Logf("hashrate %s: typically near the new level in %.0f s", k.name, median(took))
		if median(took) > 240 {
			t.Errorf("FOLLOW-MAIN-TYPICAL: typically %.0f s to follow a miner whose hashrate %s", median(took), k.name)
		}
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

package stratum

import (
	"io"
	"math"
	"net"
	"testing"
	"time"

	"go.uber.org/zap"
)

// These tests pin the difficulty-floor invariant:
//
//	the pool must NEVER judge a share against a HARDER target than the one it
//	assigned to the miner.
//
// When that invariant broke on the main pool, 55 miners produced correct work that was
// refused, silently, with no signal anywhere except the miners' own reject counters --
// and the automatic vardiff remedy could not fire, because it is gated on exactly the
// condition the bug creates. The failure is invisible from the pool side, so it has to be
// held down by tests rather than caught by observation.

func newFloorTestServer(t *testing.T, minDiff, absMinDiff float64) *Server {
	t.Helper()
	return NewServer(&ServerConfig{
		MinDiff:         minDiff,
		AbsoluteMinDiff: absMinDiff,
		RentalMinDiff:   500000,
		MaxDiff:         1e12,
		VardiffEnabled:  true,
		TargetShareTime: 10,
		RetargetTime:    1,
	}, zap.NewNop(), nil, nil)
}

// TestFloorOrderingInvariant checks the one ordering that makes the whole guarantee work:
// the ASSIGNMENT floor may never exceed the JUDGING floor.
func TestFloorOrderingInvariant(t *testing.T) {
	cases := []struct {
		name       string
		minDiff    float64
		absMinDiff float64
		wantMin    float64
		wantAbs    float64
	}{
		// The shipped Umbrel config: min_diff 1024, absolute_min_diff unset. Unset must
		// mean "same as min_diff", so a published app's assignment floor does not move.
		{"shipped config", 1024, 0, 1024, 1024},
		// An operator who raised min_diff keeps BOTH floors raised; the assignment floor
		// is not silently dropped out from under them.
		{"operator raised min_diff", 32768, 0, 32768, 32768},
		// An explicitly lowered assignment floor is honoured (safe only because
		// shareFloorFor judges at min(assigned, MinDiff)).
		{"explicit lower assignment floor", 1024, 256, 1024, 256},
		// Above MinDiff it must be clamped DOWN, never accepted as-is.
		{"absolute above min is clamped", 1024, 4096, 1024, 1024},
		{"both unset use defaults", 0, 0, 32768, 32768},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newFloorTestServer(t, tc.minDiff, tc.absMinDiff)
			if s.config.MinDiff != tc.wantMin {
				t.Errorf("MinDiff = %v, want %v", s.config.MinDiff, tc.wantMin)
			}
			if s.config.AbsoluteMinDiff != tc.wantAbs {
				t.Errorf("AbsoluteMinDiff = %v, want %v", s.config.AbsoluteMinDiff, tc.wantAbs)
			}
			if s.config.AbsoluteMinDiff > s.config.MinDiff {
				t.Fatalf("ordering invariant broken: assignment floor %v > judging floor %v",
					s.config.AbsoluteMinDiff, s.config.MinDiff)
			}
		})
	}
}

// TestEveryAssignableDifficultyIsJudgeable is the core invariant test: for every value the
// pool can possibly ASSIGN, the floor it JUDGES at must be no harder.
func TestEveryAssignableDifficultyIsJudgeable(t *testing.T) {
	// Deliberately run with a lowered assignment floor, the configuration in which the
	// invariant actually has work to do.
	s := newFloorTestServer(t, 1024, 256)

	assignable := []struct {
		name string
		diff float64
	}{
		{"non-rental vardiff floor", s.vardiffFloor(false)},
		{"rental vardiff floor", s.vardiffFloor(true)},
		{"manual diff clamped to assignment floor", s.config.AbsoluteMinDiff},
		{"just below judging floor", s.config.MinDiff - 1},
		{"exactly the judging floor", s.config.MinDiff},
		{"ramped above judging floor", s.config.MinDiff * 64},
		{"configured maximum", s.config.MaxDiff},
		// The post-rejection ceiling in adjustVardiff (DifficultyReducedFrom * 0.8) is
		// applied AFTER the floor clamp and can legitimately land below the floor.
		{"post-rejection ceiling undershoot", s.config.AbsoluteMinDiff * 0.8},
		{"deep undershoot", 1},
	}

	for _, a := range assignable {
		got := s.shareFloorFor(a.diff)
		if got > a.diff {
			t.Errorf("%s: assigned %v but judged at %v -- pool would refuse correct work",
				a.name, a.diff, got)
		}
		want := math.Min(a.diff, s.config.MinDiff)
		if got != want {
			t.Errorf("%s: shareFloorFor(%v) = %v, want min(assigned, MinDiff) = %v",
				a.name, a.diff, got, want)
		}
	}

	// A zero/unset assigned difficulty must fall back to the pool floor, never to 0:
	// judging at 0 would accept anything.
	if got := s.shareFloorFor(0); got != s.config.MinDiff {
		t.Errorf("shareFloorFor(0) = %v, want MinDiff %v", got, s.config.MinDiff)
	}
	if got := s.shareFloorFor(-1); got != s.config.MinDiff {
		t.Errorf("shareFloorFor(-1) = %v, want MinDiff %v", got, s.config.MinDiff)
	}
}

// TestRentalFloorStaysSeparate: lowering the ordinary assignment floor must not drag the
// rental floor down with it. NiceHash/MRR require 500k+ and misbehave if offered less.
func TestRentalFloorStaysSeparate(t *testing.T) {
	s := newFloorTestServer(t, 1024, 256)

	if got := s.vardiffFloor(false); got != s.config.AbsoluteMinDiff {
		t.Errorf("non-rental floor = %v, want AbsoluteMinDiff %v", got, s.config.AbsoluteMinDiff)
	}
	if got := s.vardiffFloor(true); got != s.config.RentalMinDiff {
		t.Errorf("rental floor = %v, want RentalMinDiff %v", got, s.config.RentalMinDiff)
	}
	if s.vardiffFloor(true) <= s.vardiffFloor(false) {
		t.Fatalf("rental floor %v collapsed onto the ordinary floor %v",
			s.vardiffFloor(true), s.vardiffFloor(false))
	}

	// Same separation through the client-typed accessor.
	if got := s.getMinDiffForClient(&Client{}); got != s.config.AbsoluteMinDiff {
		t.Errorf("getMinDiffForClient(plain) = %v, want %v", got, s.config.AbsoluteMinDiff)
	}
	if got := s.getMinDiffForClient(&Client{RentalService: RentalNiceHash}); got != s.config.RentalMinDiff {
		t.Errorf("getMinDiffForClient(rental) = %v, want %v", got, s.config.RentalMinDiff)
	}

	// A rental assigned its (high) floor is still judged at the pool floor -- never
	// harder than what it was told to work at.
	rentalFloor := s.vardiffFloor(true)
	if got := s.shareFloorFor(rentalFloor); got > rentalFloor {
		t.Errorf("rental judged at %v, harder than its assigned %v", got, rentalFloor)
	}
}

// TestVardiffCeilingUndershootStaysJudgeable drives the real adjustVardiff path into the
// case that was broken in this copy: the post-rejection ceiling (DifficultyReducedFrom *
// 0.8) is applied after the floor clamp and hands the miner a target BELOW the floor. The
// sub-floor difficulty itself is intentional -- backing off is the right response to
// rejections -- but the share floor must follow it down. Otherwise the miner is told one
// number and judged by a harder one, and cannot escape: the automatic remedy is gated on
// client.Difficulty > floor, which the undershoot itself makes false.
func TestVardiffCeilingUndershootStaysJudgeable(t *testing.T) {
	s := newFloorTestServer(t, 1024, 1024) // shipped config: both floors 1024

	poolSide, minerSide := net.Pipe()
	defer poolSide.Close()
	defer minerSide.Close()
	go io.Copy(io.Discard, minerSide) // drain mining.set_difficulty

	now := time.Now()
	shareTimes := make([]time.Time, 0, VardiffMinShares)
	for i := 0; i < VardiffMinShares; i++ {
		// 100s between shares against a 10s target -> ratio far below 1, so vardiff wants
		// to REDUCE difficulty and we reach the ceiling check with a live adjustment.
		shareTimes = append(shareTimes, now.Add(time.Duration(i-VardiffMinShares)*100*time.Second))
	}

	client := &Client{
		ID:                    "undershoot",
		Conn:                  poolSide,
		MinerID:               "test-undershoot",
		Authorized:            true,
		Difficulty:            2048,
		DifficultyChangedAt:   now.Add(-time.Hour), // older than RetargetTime
		DifficultyReducedFrom: 1200,                // > floor, so ceiling 960 < floor 1024
		DifficultyReducedAt:   now,
		ShareTimes:            shareTimes,
		ConnectedAt:           now.Add(-time.Hour),
	}

	s.adjustVardiff(client)

	client.mu.RLock()
	assigned := client.Difficulty
	client.mu.RUnlock()

	if assigned >= s.config.MinDiff {
		t.Fatalf("adjustVardiff settled at %v (>= MinDiff %v); the sub-floor ceiling case "+
			"this test exists to pin was not reached, so it is no longer covered -- fix the "+
			"setup rather than deleting the test", assigned, s.config.MinDiff)
	}

	floor := s.shareFloorFor(assigned)
	if floor > assigned {
		t.Fatalf("assigned %v but judging at %v: this miner's correct work is refused and "+
			"vardiff cannot recover it", assigned, floor)
	}
	if floor != assigned {
		t.Errorf("shareFloorFor(%v) = %v, want the assigned value itself", assigned, floor)
	}
}

// TestGracePeriodConstant guards the grace window against being "simplified" to zero.
// A difficulty RAISE invalidates work already in flight -- the miner computed those shares
// against the target it held when it began them -- so the previous, lower target must stay
// acceptable for a while after the change. Removing this measured 252 rejects in 2 minutes
// on the main pool.
func TestGracePeriodConstant(t *testing.T) {
	if difficultyGracePeriod <= 0 {
		t.Fatalf("difficultyGracePeriod = %v; in-flight work would be rejected on every raise",
			difficultyGracePeriod)
	}
	// Must comfortably exceed a single share interval at the default target share time.
	if difficultyGracePeriod < 30*time.Second {
		t.Errorf("difficultyGracePeriod = %v, too short to cover work in flight across a "+
			"difficulty raise", difficultyGracePeriod)
	}
}

// TestGracePeriodSelectsPreviousDifficulty pins the grace-window decision itself, mirroring
// the expression in handleSubmit: a RAISE keeps the previous (lower) target judgeable for
// difficultyGracePeriod; a LOWER, or an expired window, uses the current target.
func TestGracePeriodSelectsPreviousDifficulty(t *testing.T) {
	s := newFloorTestServer(t, 1024, 1024)

	// Fixed clock: the decision is time-dependent, and a test that sleeps to observe it is
	// slow and flaky. Drives the SAME function handleSubmit calls.
	now := time.Now()
	justRaised := now.Add(-time.Second)
	longAgo := now.Add(-2 * difficultyGracePeriod)

	cases := []struct {
		name               string
		assigned, previous float64
		changedAt          time.Time
		want               float64
	}{
		{"raise inside the window keeps the in-flight target", 8192, 2048, justRaised, 2048},
		{"raise after the window uses the new target", 8192, 2048, longAgo, 8192},
		{"lower never widens the window upward", 2048, 8192, justRaised, 2048},
		{"no previous difficulty recorded yet", 2048, 0, justRaised, 2048},
		{"equal previous is not a raise", 4096, 4096, justRaised, 4096},
		{"exactly at the window boundary has expired", 8192, 2048,
			now.Add(-difficultyGracePeriod), 8192},
	}

	for _, c := range cases {
		got := s.effectiveJudgingDifficulty(c.assigned, c.previous, c.changedAt, now)
		if got != c.want {
			t.Errorf("%s: effective = %v, want %v", c.name, got, c.want)
		}
		// The invariant this exists to protect: never judge above what the miner was told.
		if got > c.assigned {
			t.Errorf("%s: effective %v exceeds assigned %v", c.name, got, c.assigned)
		}
		if floor := s.shareFloorFor(got); floor > got {
			t.Errorf("%s: judged at %v, harder than the in-flight target %v", c.name, floor, got)
		}
	}
}

// The ordering invariant, exercised through the SAME function the server calls. Restating
// the rules inside the test would keep it green after the production guards were deleted.
//
// Note this app defaults the ASSIGNMENT floor to MinDiff rather than to a hardcoded 1024:
// it already ships min_diff 1024, and an operator who raised it must not have the
// assignment floor silently dropped out from under them.
func TestNormalizeDifficultyFloorsKeepsAssignmentBelowJudging(t *testing.T) {
	cases := []struct {
		name    string
		in      ServerConfig
		wantMin float64
		wantAbs float64
	}{
		{"shipped config", ServerConfig{MinDiff: 1024}, 1024, 1024},
		{"both unset", ServerConfig{}, 32768, 32768},
		{"explicit lower assignment floor is honoured",
			ServerConfig{MinDiff: 32768, AbsoluteMinDiff: 1024}, 32768, 1024},
		{"assignment floor above judging floor is clamped down",
			ServerConfig{MinDiff: 1024, AbsoluteMinDiff: 9999}, 1024, 1024},
		// The reason this function exists: a negative value from a config typo used to
		// survive every guard, because they tested == 0. vardiffFloor would then hand back
		// a negative assignment floor while shareFloorFor, treating non-positive as unset,
		// judged at MinDiff -- the miner told a negative target and refused above it.
		{"negative assignment floor", ServerConfig{MinDiff: 1024, AbsoluteMinDiff: -1}, 1024, 1024},
		{"negative judging floor", ServerConfig{MinDiff: -5, AbsoluteMinDiff: 1024}, 32768, 1024},
		{"both negative", ServerConfig{MinDiff: -1, AbsoluteMinDiff: -1}, 32768, 32768},
	}

	for _, c := range cases {
		cfg := c.in
		normalizeDifficultyFloors(&cfg)

		if cfg.MinDiff != c.wantMin || cfg.AbsoluteMinDiff != c.wantAbs {
			t.Errorf("%s: got MinDiff=%v AbsoluteMinDiff=%v, want %v / %v",
				c.name, cfg.MinDiff, cfg.AbsoluteMinDiff, c.wantMin, c.wantAbs)
		}
		if cfg.AbsoluteMinDiff <= 0 || cfg.MinDiff <= 0 {
			t.Errorf("%s: non-positive floor survived (%v / %v)", c.name, cfg.AbsoluteMinDiff, cfg.MinDiff)
		}
		if cfg.AbsoluteMinDiff > cfg.MinDiff {
			t.Errorf("%s: assignment floor %v above judging floor %v",
				c.name, cfg.AbsoluteMinDiff, cfg.MinDiff)
		}
		s := &Server{config: &cfg}
		if got := s.shareFloorFor(cfg.AbsoluteMinDiff); got > cfg.AbsoluteMinDiff {
			t.Errorf("%s: miner assigned %v would be judged at %v -- 100%% rejection",
				c.name, cfg.AbsoluteMinDiff, got)
		}
	}
}

package stratum

import (
	"io"
	"net"
	"testing"
	"time"

	"go.uber.org/zap"
)

// newRampTestServer mirrors the SHIPPED docker/stratum/config.template.yaml values, because
// the defect firstRamp exists to fix is a property of those exact numbers: min_diff 1024,
// target_time 10, retarget_time 30.
func newRampTestServer(t *testing.T) *Server {
	t.Helper()
	return NewServer(&ServerConfig{
		MinDiff:         1024,
		MaxDiff:         1e12,
		RentalMinDiff:   500000,
		VardiffEnabled:  true,
		TargetShareTime: 10,
		RetargetTime:    30,
	}, zap.NewNop(), nil, nil)
}

// rampClient builds a client sitting at the floor that has just submitted
// VardiffMinShares shares at the given interval.
func rampClient(t *testing.T, s *Server, startDiff, shareIntervalSec float64) (*Client, func()) {
	t.Helper()
	poolSide, minerSide := net.Pipe()
	go io.Copy(io.Discard, minerSide) // drain mining.set_difficulty

	now := time.Now()
	shareTimes := make([]time.Time, 0, VardiffMinShares)
	for i := 0; i < VardiffMinShares; i++ {
		offset := time.Duration(float64(i-VardiffMinShares) * shareIntervalSec * float64(time.Second))
		shareTimes = append(shareTimes, now.Add(offset))
	}
	c := &Client{
		ID:          "ramp",
		Conn:        poolSide,
		MinerID:     "test-ramp",
		Authorized:  true,
		Difficulty:  startDiff,
		ShareTimes:  shareTimes,
		ConnectedAt: now.Add(-time.Minute),
		// DifficultyChangedAt is deliberately left zero: that is a real fresh connection,
		// and it is why the first adjustment is not held off by RetargetTime.
	}
	return c, func() { poolSide.Close(); minerSide.Close() }
}

// A large miner starting at the floor must escape in ONE adjustment.
//
// Without firstRamp this is +50% per 30s retarget: 1 PH/s of rented hashpower needs ~20
// steps (~10 minutes) to reach the difficulty its rate warrants, and spends that whole
// window submitting ~227 shares/s into a 100/s intake cap -- over half its work refused.
// Marketplace hashpower is denied the remembered-difficulty resume by design
// (isManyMinerMarketplace), so it repeats that window on every reconnect.
func TestFirstRampEscapesTheFloorInOneAdjustment(t *testing.T) {
	s := newRampTestServer(t)

	// 1 PH/s at difficulty 1024 produces a share every ~1/227 s.
	const shareInterval = 1.0 / 227.0
	c, done := rampClient(t, s, s.config.MinDiff, shareInterval)
	defer done()

	s.adjustVardiff(c)

	c.mu.RLock()
	assigned := c.Difficulty
	ramped := c.FirstRampDone
	c.mu.RUnlock()

	if !ramped {
		t.Fatal("FirstRampDone was not set; the firstRamp branch was not reached")
	}
	// One clamped step would have landed at 1536. Anything near that means the clamp is
	// still in force and a marketplace connection is still stuck in the ten-minute climb.
	if assigned <= s.config.MinDiff*2 {
		t.Fatalf("assigned %v after firstRamp; still within one clamped step of the floor %v",
			assigned, s.config.MinDiff)
	}
	// The measured target: ~1024 * (10 / (1/227)) = ~2.32M.
	if assigned < 1e6 || assigned > 4e6 {
		t.Errorf("assigned %v, want roughly the 2.3M its measured rate warrants", assigned)
	}
	// The invariant the whole difficulty design rests on must survive the jump.
	if floor := s.shareFloorFor(assigned); floor > assigned {
		t.Errorf("assigned %v but judged at %v: correct work would be refused", assigned, floor)
	}
}

// firstRamp is once per connection. The second adjustment is the ordinary clamped ramp,
// so a noisy measurement cannot yo-yo the assignment.
func TestFirstRampAppliesOnlyOnce(t *testing.T) {
	s := newRampTestServer(t)
	const shareInterval = 1.0 / 227.0
	c, done := rampClient(t, s, s.config.MinDiff, shareInterval)
	defer done()

	s.adjustVardiff(c)
	c.mu.RLock()
	afterFirst := c.Difficulty
	c.mu.RUnlock()

	// Re-arm: fresh samples, and an adjustment window that has elapsed.
	c2, done2 := rampClient(t, s, afterFirst, shareInterval)
	defer done2()
	c2.FirstRampDone = true

	s.adjustVardiff(c2)
	c2.mu.RLock()
	afterSecond := c2.Difficulty
	c2.mu.RUnlock()

	if afterSecond > afterFirst*1.5001 {
		t.Errorf("second adjustment went from %v to %v, more than the +50%% clamp allows — "+
			"firstRamp is not limited to one adjustment", afterFirst, afterSecond)
	}
}

// A Bitaxe-class miner at the floor must be left exactly where it is. Its measured ratio
// sits inside the variance window, so no adjustment happens at all -- firstRamp must not
// change that.
func TestFirstRampLeavesSmallMinersAtTheFloor(t *testing.T) {
	s := newRampTestServer(t)

	// ~0.5 TH/s at difficulty 1024: a share every ~8.8s against a 10s target.
	c, done := rampClient(t, s, s.config.MinDiff, 8.8)
	defer done()

	s.adjustVardiff(c)

	c.mu.RLock()
	assigned := c.Difficulty
	c.mu.RUnlock()

	if assigned != s.config.MinDiff {
		t.Errorf("small miner moved from the floor %v to %v", s.config.MinDiff, assigned)
	}
}

// firstRamp must never ramp DOWN out of the floor. A miner slower than the target is
// already at the lowest difficulty it can be assigned.
func TestFirstRampNeverGoesBelowTheFloor(t *testing.T) {
	s := newRampTestServer(t)

	// A share every 100s against a 10s target: the ratio is far below 1.
	c, done := rampClient(t, s, s.config.MinDiff, 100)
	defer done()

	s.adjustVardiff(c)

	c.mu.RLock()
	assigned := c.Difficulty
	ramped := c.FirstRampDone
	c.mu.RUnlock()

	if assigned < s.config.MinDiff {
		t.Errorf("assigned %v, below the floor %v", assigned, s.config.MinDiff)
	}
	if ramped {
		t.Error("FirstRampDone was consumed by a downward adjustment; the one escape must be saved for an upward one")
	}
}

// A client that is ALREADY above the floor -- e.g. one that resumed a remembered level --
// does not get the unclamped step. firstRamp is an escape from the floor, not a general
// bypass of the ramp clamp.
func TestFirstRampDoesNotApplyAboveTheFloor(t *testing.T) {
	s := newRampTestServer(t)

	const shareInterval = 1.0 / 227.0
	c, done := rampClient(t, s, s.config.MinDiff*4, shareInterval)
	defer done()

	s.adjustVardiff(c)

	c.mu.RLock()
	assigned := c.Difficulty
	ramped := c.FirstRampDone
	c.mu.RUnlock()

	if ramped {
		t.Error("FirstRampDone set for a client that was not at the floor")
	}
	if assigned > s.config.MinDiff*4*1.5001 {
		t.Errorf("assigned %v: more than the +50%% clamp above the starting %v",
			assigned, s.config.MinDiff*4)
	}
}

// variance_percent is a shipped config knob that nothing read: the code used the hardcoded
// VardiffVariancePercent, so an operator whose miner settled at the wrong difficulty could
// turn the one dial the config offered them and watch nothing happen.
//
// A miner 20% off target sits INSIDE the default 30% dead-band (no adjustment) and OUTSIDE
// a configured 10% one (adjust), so the same input must produce different behaviour.
func TestVariancePercentIsHonoured(t *testing.T) {
	// 12.5s between shares against a 10s target: ratio 0.8, i.e. 20% off.
	const interval = 12.5

	wide := NewServer(&ServerConfig{
		MinDiff: 1024, MaxDiff: 1e12, RentalMinDiff: 500000,
		VardiffEnabled: true, TargetShareTime: 10, RetargetTime: 30,
	}, zap.NewNop(), nil, nil) // VariancePercent unset -> default 30%
	c1, done1 := rampClient(t, wide, 4096, interval)
	defer done1()
	wide.adjustVardiff(c1)
	c1.mu.RLock()
	unchanged := c1.Difficulty
	c1.mu.RUnlock()
	if unchanged != 4096 {
		t.Errorf("with the default 30 percent dead-band a 20-percent-off miner was adjusted to %v; it should sit inside the band", unchanged)
	}

	narrow := NewServer(&ServerConfig{
		MinDiff: 1024, MaxDiff: 1e12, RentalMinDiff: 500000,
		VardiffEnabled: true, TargetShareTime: 10, RetargetTime: 30,
		VariancePercent: 0.10,
	}, zap.NewNop(), nil, nil)
	c2, done2 := rampClient(t, narrow, 4096, interval)
	defer done2()
	narrow.adjustVardiff(c2)
	c2.mu.RLock()
	adjusted := c2.Difficulty
	c2.mu.RUnlock()
	if adjusted == 4096 {
		t.Error("with a configured 10 percent dead-band the same 20-percent-off miner was NOT adjusted; variance_percent is being ignored")
	}
}

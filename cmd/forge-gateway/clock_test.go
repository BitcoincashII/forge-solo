package main

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// The fake pool takes the gateway's jobs when the clocks agree: the gateway mines into the TIDES
// window.
func TestTheFakePoolTakesJobs(t *testing.T) {
	n := newFakeNode(t, "u", "p")
	p := newFakePool(t, n)
	h := startAs(t, "GW-FAKE-POOL", poolConfig(t, n, p), testPassword)
	var v statusView
	if !eventually(10*time.Second, func() bool { v = h.a.view(time.Now()); return v.State == stateActive }) {
		t.Fatalf("GW-FAKE-POOL: the state is %s %q, mode %s", v.State, v.StateReason, v.Mode)
	}
}

// A PC clock that is off makes Forge Pool refuse every signed request (401, "request time is ...
// off the pool's clock"): the pool was reached, and only setting the clock right helps. The status
// page says so, in words, not as the pool's JSON; the miners mine solo meanwhile; and once the clock
// is right the gateway goes back to the pool by itself, with no save and no restart.
func TestAClockThatIsOffIsSaidSo(t *testing.T) {
	setVar(t, &poolRetryEvery, 200*time.Millisecond)
	setVar(t, &stateLogEvery, 20*time.Millisecond)
	n := newFakeNode(t, "u", "p")
	p := newFakePool(t, n)
	p.skew.Store(int64(-7 * time.Hour)) // the PC's clock is 7 hours fast
	h := startAs(t, "GW-CLOCK", poolConfig(t, n, p), testPassword)
	var v statusView
	if !eventually(10*time.Second, func() bool { v = h.a.view(time.Now()); return v.State == stateClockOff }) {
		t.Fatalf("GW-CLOCK-STATE: the state is %s %q, pool %s %q", v.State, v.StateReason, v.Pool.State, v.Pool.Reason)
	}
	windows := runtime.GOOS == "windows"
	if want := clockReason(7*time.Hour, windows, "solo"); v.StateReason != want || v.Mode != "solo" {
		t.Errorf("GW-CLOCK-REASON: %q (mode %s)\nwant %q", v.StateReason, v.Mode, want)
	}
	if !strings.Contains(v.StateReason, "clock is about 7 hours off. Forge Pool refuses requests until it is right") {
		t.Errorf("GW-CLOCK-REASON: %q", v.StateReason)
	}
	if want := clockShort(7*time.Hour, windows); v.Pool.Reason != want {
		t.Errorf("GW-CLOCK-CARD: the Forge Pool card says %q, want %q", v.Pool.Reason, want)
	}
	if v.Job == nil || v.Job.Tides {
		t.Errorf("GW-CLOCK-SOLO: the miners are not on solo work meanwhile: %+v", v.Job)
	}
	if !eventually(time.Second, func() bool {
		return h.logs.FilterLevelExact(zap.WarnLevel).FilterMessageSnippet("state: clock_off: ").Len() == 1
	}) {
		t.Error("GW-CLOCK-LOG: the change of state was not logged once, at Warn")
	}
	a := h.a
	p.skew.Store(0) // the clock was set right
	if !eventually(10*time.Second, func() bool { v = h.a.view(time.Now()); return v.State == stateActive }) {
		t.Fatalf("GW-CLOCK-RECOVERS: the clocks agree, and the state is still %s %q", v.State, v.StateReason)
	}
	if h.a != a || v.Mode != "tides" || p.jobs.Load() == 0 {
		t.Errorf("GW-CLOCK-RECOVERS: mode %s, %d jobs registered", v.Mode, p.jobs.Load())
	}
}

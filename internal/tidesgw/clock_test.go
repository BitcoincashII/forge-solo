package tidesgw

import (
	"crypto/ed25519"
	"errors"
	"testing"
	"time"
)

// Forge Pool's refusal of a request whose time is off its clock is read from the gateway's reason,
// with how far off, in round words; nothing else is taken for it.
func TestClockRefusalIsReadFromTheReason(t *testing.T) {
	for _, tc := range []struct {
		reason  string
		refused bool
		far     string
	}{
		{`/datum/v1/jobs: 401 Unauthorized: {"error":"request time is -6h59m59s off the pool's clock"}`, true, "about 7 hours"},
		{`/datum/v1/jobs: 401 Unauthorized: {"error":"request time is 3m5s off the pool's clock"}`, true, "about 3 minutes"},
		{`/datum/v1/jobs: 401 Unauthorized: {"error":"request time is 49h0m0s off the pool's clock"}`, true, "about 2 days"},
		{`/datum/v1/jobs: 401 Unauthorized: {"error":"request time is 1h2m0s off the pool's clock"}`, true, "about an hour"},
		{`/datum/v1/jobs: 401 Unauthorized: {"error":"request time is soon off the pool's clock"}`, true, ""},
		{`/datum/v1/jobs: 401 Unauthorized: {"error":"bad signature"}`, false, ""},
		{`/datum/v1/jobs: 502 Bad Gateway: request time is 7h0m0s off the pool's clock`, false, ""},
		{"the pool did not answer", false, ""},
	} {
		off, refused := ClockRefusal(tc.reason)
		if refused != tc.refused || HowFar(off) != tc.far {
			t.Errorf("TIDES-CLOCK-READ: %q: refused %v, %q", tc.reason, refused, HowFar(off))
		}
	}
}

// The gateway's status says when the pool refuses this computer's clock, and how far off it is,
// for the dashboard to say how to set it right. A pool that is down, or a node behind the pool's
// block, is not that.
func TestStatusSaysThePoolRefusesTheClock(t *testing.T) {
	g := newGateway(t, newFakePool(t))
	g.Fallback(errors.New(`/datum/v1/jobs: 401 Unauthorized: {"error":"request time is -6h59m59s off the pool's clock"}`))
	if st := g.Status(); st.State != StateFallback || !st.ClockRefused || st.ClockOff != "about 7 hours" {
		t.Errorf("TIDES-CLOCK-STATUS: %+v", st)
	}
	for _, err := range []error{errors.New("the pool did not answer"), &StaleError{PoolHeight: 101, Height: 100}} {
		g.Fallback(err)
		if st := g.Status(); st.ClockRefused || st.ClockOff != "" {
			t.Errorf("TIDES-CLOCK-STATUS-OTHER: %v read as a clock refusal: %+v", err, st)
		}
	}
}

// Against a pool whose clock is 7 hours ahead of this computer's, the pool's own check (wire.Verify)
// refuses the registration, and the gateway's status, once the stratum falls back on it, says so.
func TestThePoolRefusesAClockThatIsOff(t *testing.T) {
	p := newFakePool(t)
	_, key, _ := ed25519.GenerateKey(nil)
	g := New(Config{PoolURL: p.srv.URL, Key: key, RegisterFor: 2 * time.Second, RetryEvery: time.Minute,
		Now: func() time.Time { return time.Now().Add(-7 * time.Hour) }})
	_, err := g.Register(template(), me, nil)
	if err == nil {
		t.Fatal("TIDES-CLOCK-POOL: the pool took a request signed 7 hours off its clock")
	}
	g.Fallback(err)
	if st := g.Status(); !st.ClockRefused || st.ClockOff != "about 7 hours" {
		t.Errorf("TIDES-CLOCK-POOL: the pool answered %v, and the status is %+v", err, st)
	}
}

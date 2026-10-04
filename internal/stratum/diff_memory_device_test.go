package stratum

import (
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"
)

// Two devices that log in under one label (the bare payout address, or "rig" on both) shared one
// remembered difficulty: a small rig that reconnected after an S19 opened at the S19's level, and a
// rig whose firmware does not suggest a difficulty stayed there until it had found ten shares, hours
// at that level. The level is remembered for the address a device connects from as well.
func TestRememberedDifficultyBelongsToOneDevice(t *testing.T) {
	s := newSoloServer(t, testPayout)
	floor := s.config.AbsoluteMinDiff
	loginFrom := func(ip string) *Client {
		t.Helper()
		poolSide, minerSide := net.Pipe()
		t.Cleanup(func() { poolSide.Close(); minerSide.Close() })
		go io.Copy(io.Discard, minerSide)
		c := &Client{ID: ip, Conn: poolSide, IP: ip + ":4028", Difficulty: floor, ConnectedAt: time.Now()}
		params, _ := json.Marshal([]string{testPayout, "x"})
		if r := s.handleAuthorize(c, &Request{ID: 1, Method: MethodAuthorize, Params: params}); r.Result != true {
			t.Fatalf("login from %s refused: %+v", ip, r.Error)
		}
		return c
	}
	diffOf := func(c *Client) float64 {
		c.mu.RLock()
		defer c.mu.RUnlock()
		return c.Difficulty
	}

	// The S19 works its way up: vardiff raises it from 100000 by half, and remembers the level.
	s19 := loginFrom("192.168.1.20")
	now := time.Now()
	s19.mu.Lock()
	s19.Difficulty, s19.FirstRampDone = 100000, true
	for i := 0; i < VardiffMinShares; i++ { // twice as fast as the target
		s19.ShareSamples = append(s19.ShareSamples, shareSample{at: now.Add(time.Duration(i-VardiffMinShares) * 5 * time.Second)})
	}
	s19.mu.Unlock()
	s.adjustVardiffAt(s19, now)
	if got := diffOf(s19); got != 150000 {
		t.Fatalf("DIFFMEM-SETUP: the S19 went from 100000 to %v, want 150000", got)
	}

	if got := diffOf(loginFrom("192.168.1.21")); got != floor {
		t.Fatalf("DIFFMEM-OTHER-DEVICE: a second device with the same label opened at %v, want the floor %v", got, floor)
	}
	again := loginFrom("192.168.1.20")
	if got := diffOf(again); got != 150000 {
		t.Fatalf("DIFFMEM-SAME-DEVICE: the S19 reconnecting opened at %v, want its own 150000", got)
	}

	// The idle reset puts the device back to the floor for its next connection too.
	again.ConnectedAt = now.Add(-time.Hour)
	s.clients.Store(again.ID, again)
	s.resetIdleDifficulties(now)
	s.clients.Delete(again.ID)
	if got := diffOf(loginFrom("192.168.1.20")); got != floor {
		t.Fatalf("DIFFMEM-IDLE: after an idle reset the device reconnected at %v, want the floor %v", got, floor)
	}
}

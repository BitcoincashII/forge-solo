package stratum

import (
	"strings"
	"testing"
	"time"
)

// authorizedNow waits until s has n clients logged in.
func authorizedNow(t *testing.T, s *Server, n int64) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); s.CountAuthorized() != n; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("%d clients logged in, want %d", s.CountAuthorized(), n)
		}
	}
}

// MiningRigRentals' health checks log in under the order's own name, two at a time, all through a
// rental. They were counted as miners: for one rented rig the dashboard showed 2 or 3 workers and
// "3 miner(s) authorized and submitting shares", the public stats 3 rentals, and with the health
// checks alone, before the rig arrived, a miner that should be checked for hashing. A health check
// is not a miner until it sends a share.
func TestHealthChecksAreNotCountedAsMiners(t *testing.T) {
	s, rl, _ := rentalLogServer(t)
	addr := rl.Addr().String()
	user := testPayout + ".mrr"
	rig := marketLogin(t, addr, rigUA, user)
	marketLogin(t, addr, probeUA, user)
	marketLogin(t, addr, probeUA, user)
	authorizedNow(t, s, 3)

	if c, a := s.CountMiners(); c != 1 || a != 1 {
		t.Errorf("PROBE-COUNT-MINERS: a rig and two health checks count as %d connected, %d logged in; want 1 and 1", c, a)
	}
	if r := s.GetRentalStats(); r.TotalRentals != 1 || r.OtherRentals != 0 {
		t.Errorf("PROBE-COUNT-RENTALS: a rig and two health checks count as %d rentals (%d other), want 1", r.TotalRentals, r.OtherRentals)
	}
	if w := s.AuthorizedWorkers(); len(w) != 1 {
		t.Errorf("PROBE-COUNT-WORKERS: a rig and two health checks list %d workers, want 1", len(w))
	}

	// The rig reconnects: for a moment only the health checks are there.
	leave(t, rig)
	authorizedNow(t, s, 2)
	if c, a := s.CountMiners(); c != 0 || a != 0 {
		t.Errorf("PROBE-COUNT-ALONE: health checks alone count as %d connected, %d logged in; want none", c, a)
	}
	if r := s.GetRentalStats(); r.TotalRentals != 0 {
		t.Errorf("PROBE-COUNT-ALONE-RENTALS: health checks alone count as %d rentals", r.TotalRentals)
	}

	// One that sends shares is a miner, whatever its user agent.
	s.clients.Range(func(_, v interface{}) bool {
		v.(*Client).ValidShares.Store(1)
		return false
	})
	if c, a := s.CountMiners(); c != 1 || a != 1 {
		t.Errorf("PROBE-COUNT-HASHING: a health-check user agent that sent shares counts as %d connected, %d logged in; want 1 and 1", c, a)
	}
	if w := s.AuthorizedWorkers(); len(w) != 1 {
		t.Errorf("PROBE-COUNT-HASHING-WORKERS: it lists %d workers, want 1", len(w))
	}
}

// CountMiners counts every connection that is not a health check, logged in or not: a miner
// refused at login is the one the dashboard must still see connecting.
func TestCountMinersCountsAMinerNotLoggedIn(t *testing.T) {
	s, rl, _ := rentalLogServer(t)
	c, r := dialLine(t, rl.Addr().String(), `{"id":1,"method":"mining.subscribe","params":["`+rigUA+`"]}`)
	t.Cleanup(func() { c.Close() })
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if line, err := r.ReadString('\n'); err != nil || !strings.Contains(line, `"id":1`) {
		t.Fatalf("no subscribe answer: %q %v", line, err)
	}
	if conns, auth := s.CountMiners(); conns != 1 || auth != 0 {
		t.Errorf("PROBE-COUNT-CONNECTED: a miner subscribed but not logged in counts as %d connected, %d logged in; want 1 and 0", conns, auth)
	}
}

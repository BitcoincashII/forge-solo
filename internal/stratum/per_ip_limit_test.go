package stratum

import "testing"

func serverWithPerIPLimit(limit int) *Server {
	s := NewServerForTest()
	s.config = &ServerConfig{MaxConnections: 256, MaxConnectionsPerIP: limit}
	return s
}

// One remote host must not be able to take the whole connection pool.
func TestPerIPLimitBoundsASingleHost(t *testing.T) {
	s := serverWithPerIPLimit(3)
	const host = "198.51.100.7"

	for i := 1; i <= 3; i++ {
		if !s.reserveIPSlot(host) {
			t.Fatalf("connection %d from one host was refused below the limit", i)
		}
	}
	if s.reserveIPSlot(host) {
		t.Error("a 4th connection from the same host was allowed past a limit of 3")
	}
	// A different host is unaffected -- the limit is per source, not global.
	if !s.reserveIPSlot("203.0.113.9") {
		t.Error("a different host was refused because another host hit its limit")
	}
	// Releasing one frees exactly one.
	s.releaseIPSlot(host)
	if !s.reserveIPSlot(host) {
		t.Error("releasing a slot did not free capacity")
	}
	if s.reserveIPSlot(host) {
		t.Error("releasing one slot freed more than one")
	}
}

// The counter map must not become another thing that only ever grows.
func TestPerIPLimitDoesNotRetainIdleHosts(t *testing.T) {
	s := serverWithPerIPLimit(4)
	for _, h := range []string{"198.51.100.1", "198.51.100.2", "198.51.100.3"} {
		if !s.reserveIPSlot(h) {
			t.Fatalf("reserve for %s failed", h)
		}
	}
	for _, h := range []string{"198.51.100.1", "198.51.100.2", "198.51.100.3"} {
		s.releaseIPSlot(h)
	}
	if n := len(s.ipConns); n != 0 {
		t.Errorf("map retained %d entries for hosts with nothing open: %v", n, s.ipConns)
	}
}

// A miner on the same machine, and the container healthcheck, must never be refused.
func TestPerIPLimitExemptsLoopback(t *testing.T) {
	s := serverWithPerIPLimit(1)
	for _, h := range []string{"127.0.0.1", "::1"} {
		for i := 0; i < 5; i++ {
			if !s.reserveIPSlot(h) {
				t.Fatalf("loopback %s refused at connection %d", h, i+1)
			}
		}
	}
	if len(s.ipConns) != 0 {
		t.Errorf("loopback connections were counted: %v", s.ipConns)
	}
}

// 0 means no limit, so an operator can opt out.
func TestPerIPLimitZeroDisables(t *testing.T) {
	s := serverWithPerIPLimit(0)
	for i := 0; i < 500; i++ {
		if !s.reserveIPSlot("198.51.100.50") {
			t.Fatalf("limit 0 refused connection %d; it must disable the cap", i+1)
		}
	}
}

// The counter keys on host, so two ports from one machine share a budget.
func TestHostOfStripsThePort(t *testing.T) {
	for addr, want := range map[string]string{
		"198.51.100.7:54321": "198.51.100.7",
		"[2001:db8::1]:3333": "2001:db8::1",
		"198.51.100.7":       "198.51.100.7",
	} {
		if got := hostOf(addr); got != want {
			t.Errorf("hostOf(%q) = %q, want %q", addr, got, want)
		}
	}
}

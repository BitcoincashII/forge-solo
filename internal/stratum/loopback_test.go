package stratum

import "testing"

// A local health probe must not be logged as a miner that failed to handshake.
//
// The container healthcheck runs `nc -z localhost 3333`, which opens a socket and closes
// it without speaking stratum. The disconnect path guarded against that with
// strings.HasPrefix(ip, "127.0.0.1") -- IPv4 only. `localhost` resolves to ::1 first on a
// dual-stack container, so every probe slipped past and logged a warning: ~84 an hour,
// ~2000 a day. That volume buries the case the warning exists for, a real miner failing
// to complete the handshake, which is the one thing it is supposed to surface.
func TestLoopbackDetectionCoversBothIPFamilies(t *testing.T) {
	loopback := []string{
		"127.0.0.1:40420",        // IPv4, the case the old check caught
		"127.0.0.53:9",           // anywhere in 127.0.0.0/8
		"[::1]:40420",            // IPv6 -- what the healthcheck actually produces
		"[::ffff:127.0.0.1]:333", // v4-mapped v6
		"::1",                    // bare, no port
	}
	for _, a := range loopback {
		if !isLoopback(a) {
			t.Errorf("isLoopback(%q) = false; a local health probe will be logged as an "+
				"external client that failed to subscribe", a)
		}
	}

	external := []string{
		"144.202.73.66:46368", // the Dallas relay -- real miners arrive this way
		"8.8.8.8:1234",
		"[2001:db8::1]:3333",
		"garbage", // unparseable must NOT be treated as local
		"",        // nor empty
	}
	for _, a := range external {
		if isLoopback(a) {
			t.Errorf("isLoopback(%q) = true; a genuine miner that failed to subscribe would "+
				"be silently dropped from the logs", a)
		}
	}
}

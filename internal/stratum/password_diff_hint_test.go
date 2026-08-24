package stratum

import (
	"encoding/json"
	"io"
	"net"
	"testing"

	"go.uber.org/zap"
)

func TestParsePasswordDiffHint(t *testing.T) {
	tests := []struct {
		name     string
		password string
		want     float64
	}{
		{"plain hint", "d=2000000", 2000000},
		{"uppercase key", "D=500000", 500000},
		{"comma separated among other terms", "x,d=131072,stats", 131072},
		{"semicolon separated", "id=rig1;d=65536", 65536},
		{"space separated", "d=4096 mc=BCH2", 4096},
		{"fractional", "d=1024.5", 1024.5},
		{"padded", "  d=8192  ", 8192},

		{"no hint", "x", 0},
		{"empty", "", 0},
		{"key only", "d=", 0},
		{"not a number", "d=high", 0},
		// A zero or negative pin must read as "no opinion", never as an assignment:
		// difficulty 0 would make every nonce a valid share and flood the pool.
		{"zero", "d=0", 0},
		{"negative", "d=-5", 0},
		// "id=..." ends in "d=" only by coincidence of substring matching; it must not
		// be read as a hint. This is why the parser matches per-term, not by index.
		{"id term is not a diff term", "id=12345", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parsePasswordDiffHint(tc.password); got != tc.want {
				t.Errorf("parsePasswordDiffHint(%q) = %v, want %v", tc.password, got, tc.want)
			}
		})
	}
}

func authorizeWithPassword(t *testing.T, s *Server, password string) *Client {
	t.Helper()
	poolSide, minerSide := net.Pipe()
	t.Cleanup(func() { poolSide.Close(); minerSide.Close() })
	go io.Copy(io.Discard, minerSide)

	c := &Client{ID: "hint", Conn: poolSide}
	params, err := json.Marshal([]string{
		"bitcoincashii:qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq.worker",
		password,
	})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	resp := s.handleAuthorize(c, &Request{ID: 1, Method: MethodAuthorize, Params: params})
	if resp.Result != true {
		t.Fatalf("authorize rejected: %+v", resp.Error)
	}
	return c
}

func newHintTestServer(t *testing.T) *Server {
	t.Helper()
	return NewServer(&ServerConfig{
		MinDiff:         1024,
		MaxDiff:         1e12,
		RentalMinDiff:   500000,
		VardiffEnabled:  true,
		TargetShareTime: 10,
		RetargetTime:    30,
		SoloOnly:        true, // the shipped home-app configuration
	}, zap.NewNop(), nil, nil)
}

// Rented hashpower declares its size in the password. Honouring it is the difference
// between opening at the right difficulty and ramping to it while shares are refused.
func TestAuthorizeHonoursPasswordDiffHint(t *testing.T) {
	s := newHintTestServer(t)
	c := authorizeWithPassword(t, s, "d=2000000")

	c.mu.RLock()
	diff, manual := c.Difficulty, c.ManualDiff
	c.mu.RUnlock()

	if diff != 2000000 {
		t.Errorf("opening difficulty = %v, want the hinted 2000000", diff)
	}
	// The hint must not masquerade as an operator-set manual difficulty: that field turns
	// vardiff OFF for the connection, and a wrong hint would then never self-correct.
	if manual != 0 {
		t.Errorf("ManualDiff = %v, want 0 so vardiff stays live for a hinted client", manual)
	}
}

// A hint below the assignment floor is raised to the floor, exactly like every other
// assignment path. "d=1" must not turn one connection into a share flood.
func TestAuthorizePasswordHintIsClampedToTheFloor(t *testing.T) {
	s := newHintTestServer(t)
	c := authorizeWithPassword(t, s, "d=1")

	c.mu.RLock()
	diff := c.Difficulty
	c.mu.RUnlock()

	if diff < s.config.AbsoluteMinDiff {
		t.Errorf("opening difficulty = %v, below the assignment floor %v", diff, s.config.AbsoluteMinDiff)
	}
}

// An absurd pin is capped at MaxDiff rather than accepted, so a client cannot park itself
// where it will never submit a share.
func TestAuthorizePasswordHintIsClampedToMaxDiff(t *testing.T) {
	s := newHintTestServer(t)
	c := authorizeWithPassword(t, s, "d=99999999999999")

	c.mu.RLock()
	diff := c.Difficulty
	c.mu.RUnlock()

	if diff > s.config.MaxDiff {
		t.Errorf("opening difficulty = %v, above MaxDiff %v", diff, s.config.MaxDiff)
	}
}

// The ordinary case must be untouched: no hint means the normal floor/resume path.
func TestAuthorizeWithoutHintUsesTheFloor(t *testing.T) {
	s := newHintTestServer(t)
	c := authorizeWithPassword(t, s, "x")

	c.mu.RLock()
	diff := c.Difficulty
	c.mu.RUnlock()

	if diff != s.config.AbsoluteMinDiff {
		t.Errorf("opening difficulty = %v, want the floor %v", diff, s.config.AbsoluteMinDiff)
	}
}

// A real password must still be captured as the settings proof-of-control secret, and a
// difficulty hint must still NOT be (it is not a secret -- it is printed in order forms).
func TestPasswordHintIsNotStoredAsASecret(t *testing.T) {
	s := newHintTestServer(t)
	c := authorizeWithPassword(t, s, "d=65536")

	c.mu.RLock()
	minerID := c.MinerID
	c.mu.RUnlock()

	if _, ok := s.authPasswords.Load(minerID); ok {
		t.Error("a d= hint was stored as the settings password; it is public, not a secret")
	}
}

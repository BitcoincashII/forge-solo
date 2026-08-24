package stratum

import (
	"encoding/json"
	"io"
	"net"
	"testing"

	"go.uber.org/zap"
)

const testPayout = "bitcoincashii:qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq"

func newSoloServer(t *testing.T, payout string) *Server {
	t.Helper()
	s := NewServer(&ServerConfig{
		MinDiff: 1024, MaxDiff: 1e12, RentalMinDiff: 500000,
		VardiffEnabled: true, TargetShareTime: 10, RetargetTime: 30,
		SoloOnly: true,
	}, zap.NewNop(), nil, nil)
	s.SetSoloPayoutAddress(payout)
	return s
}

func authorize(t *testing.T, s *Server, username string) (*Client, *Response) {
	t.Helper()
	poolSide, minerSide := net.Pipe()
	t.Cleanup(func() { poolSide.Close(); minerSide.Close() })
	go io.Copy(io.Discard, minerSide)

	c := &Client{ID: "c", Conn: poolSide}
	params, err := json.Marshal([]string{username, "x"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return c, s.handleAuthorize(c, &Request{ID: 1, Method: MethodAuthorize, Params: params})
}

// THE bug behind "node syncs 100% but will not hash".
//
// The app's Settings page, README and store listing all tell the user the worker username
// is "any label". The stratum refused every label that was not a CashAddr, and an
// unauthorized client is sent no difficulty and no job (handleMessage gates both on the
// authorize result, and BroadcastJob skips !Authorized). The rig sits connected at 0 H/s on
// a fully synced node with nothing in the UI to explain it.
func TestSoloAuthorizeAcceptsAnyWorkerLabel(t *testing.T) {
	s := newSoloServer(t, testPayout)

	for _, username := range []string{"rig1", "bitaxe", "worker1", "x", "umbrel", "S19-garage", "rig1.worker1"} {
		c, resp := authorize(t, s, username)
		if resp.Result != true {
			t.Errorf("authorize(%q) refused: %+v", username, resp.Error)
			continue
		}
		c.mu.RLock()
		minerID := c.MinerID
		c.mu.RUnlock()
		// The stats key must be the payout address: the dashboard looks a miner up by the
		// configured address, so any other value renders every tile empty.
		if minerID != testPayout {
			t.Errorf("authorize(%q) credited to %q, want the payout address %q", username, minerID, testPayout)
		}
	}
}

// An address-style username must keep working exactly as before.
func TestSoloAuthorizeStillAcceptsAddressUsernames(t *testing.T) {
	s := newSoloServer(t, testPayout)
	c, resp := authorize(t, s, testPayout+".rig1")
	if resp.Result != true {
		t.Fatalf("address username refused: %+v", resp.Error)
	}
	c.mu.RLock()
	minerID, worker := c.MinerID, c.WorkerName
	c.mu.RUnlock()
	if minerID != testPayout {
		t.Errorf("MinerID = %q, want %q", minerID, testPayout)
	}
	if worker != "rig1" {
		t.Errorf("WorkerName = %q, want %q", worker, "rig1")
	}
}

// A rented-hashpower worker label also begins "braiins". The probe branch must not claim
// it: crediting a whole rental to the fake miner "probe" blanks the dashboard for its
// entire duration.
func TestSoloAuthorizeBraiinsLabelIsNotSwallowedByTheProbeBranch(t *testing.T) {
	s := newSoloServer(t, testPayout)
	c, resp := authorize(t, s, "braiins-rental-01")
	if resp.Result != true {
		t.Fatalf("braiins worker label refused: %+v", resp.Error)
	}
	c.mu.RLock()
	minerID := c.MinerID
	c.mu.RUnlock()
	if minerID == "probe" {
		t.Error("a braiins worker label was credited to the probe identity instead of the payout address")
	}
	if minerID != testPayout {
		t.Errorf("MinerID = %q, want the payout address", minerID)
	}
}

// With no payout address there is no stats key to use, so the old rejection stands. Mining
// is paused in that state anyway (the job loop builds nothing), and the dashboard banner
// tells the user to set an address.
func TestSoloAuthorizeStillRejectsLabelsWithNoPayoutAddress(t *testing.T) {
	s := newSoloServer(t, "")
	_, resp := authorize(t, s, "rig1")
	if resp.Result == true {
		t.Error("a plain label authorized with no payout address configured; there is no address to credit it to")
	}
}

// The connectivity probe a marketplace runs before an order must still be accepted on an
// install that has no payout address yet.
func TestBraiinsProbeStillAcceptedWithNoPayoutAddress(t *testing.T) {
	s := newSoloServer(t, "")
	c, resp := authorize(t, s, "braiinstest")
	if resp.Result != true {
		t.Fatalf("braiins probe refused: %+v", resp.Error)
	}
	c.mu.RLock()
	minerID := c.MinerID
	c.mu.RUnlock()
	if minerID != "probe" {
		t.Errorf("MinerID = %q, want the probe identity", minerID)
	}
}

// A pool (non-solo) deployment must keep rejecting non-address usernames: there the
// username IS the payout identity.
func TestNonSoloStillRejectsNonAddressUsernames(t *testing.T) {
	s := NewServer(&ServerConfig{
		MinDiff: 1024, MaxDiff: 1e12, RentalMinDiff: 500000,
		VardiffEnabled: true, TargetShareTime: 10, RetargetTime: 30,
		SoloOnly: false,
	}, zap.NewNop(), nil, nil)
	s.SetSoloPayoutAddress(testPayout)

	_, resp := authorize(t, s, "rig1")
	if resp.Result == true {
		t.Error("a non-solo pool authorized a non-address username; it would mine to an unpayable identity")
	}
}

// An overlong label from an unauthenticated connection must be bounded before it reaches a
// database column.
func TestSoloAuthorizeBoundsTheWorkerLabel(t *testing.T) {
	s := newSoloServer(t, testPayout)
	long := ""
	for i := 0; i < 500; i++ {
		long += "a"
	}
	c, resp := authorize(t, s, long)
	if resp.Result != true {
		t.Fatalf("long label refused: %+v", resp.Error)
	}
	c.mu.RLock()
	worker := c.WorkerName
	c.mu.RUnlock()
	if len(worker) > maxWorkerLabel {
		t.Errorf("WorkerName is %d chars, want at most %d", len(worker), maxWorkerLabel)
	}
}

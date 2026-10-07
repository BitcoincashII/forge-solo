package stratum

import (
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/cashaddr"
	"go.uber.org/zap"
)

// A REAL address: checksum-valid, so normalizeMinerAddress accepts it. The previous
// placeholder was 42 q's, which has no valid checksum -- it only worked because the
// checksum was not being verified.
const testPayout = "bitcoincashii:qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzse6qye33q"

func newSoloServer(t *testing.T, payout string) *Server {
	t.Helper()
	s := NewServer(&ServerConfig{
		MinDiff: 1024, MaxDiff: 1e12, RentalMinDiff: 500000,
		VardiffEnabled: true, TargetShareTime: 10, RetargetTime: 30,
		SoloOnly: true,
	}, zap.NewNop(), nil)
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
	}, zap.NewNop(), nil)
	s.SetSoloPayoutAddress(testPayout)

	_, resp := authorize(t, s, "rig1")
	if resp.Result == true {
		t.Error("a non-solo pool authorized a non-address username; it would mine to an unpayable identity")
	}
}

// A one-character typo in an address used as the username must NOT become the minerID.
// It used to: authorize succeeded and every share was recorded under an address the
// dashboard API then refused, so all tiles read zero forever with nothing to explain it.
// In solo the miner is now credited to the configured payout address instead, and the
// mistyped string survives only as a worker label.
func TestSoloAuthorizeRejectsMistypedAddressAsMinerID(t *testing.T) {
	s := newSoloServer(t, testPayout)

	// Flip one character of a valid address; the checksum no longer holds.
	typo := testPayout[:len(testPayout)-1] + "p"
	if typo == testPayout {
		t.Fatal("typo fixture did not change the address")
	}
	c, resp := authorize(t, s, typo)
	if resp.Result != true {
		t.Fatalf("authorize refused: %+v", resp.Error)
	}
	c.mu.RLock()
	minerID := c.MinerID
	c.mu.RUnlock()
	if minerID == typo {
		t.Error("a checksum-invalid address became the minerID; the dashboard will refuse it and every tile will read zero")
	}
	if minerID != testPayout {
		t.Errorf("MinerID = %q, want the configured payout address", minerID)
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

// Forge Solo pays one address. A rental that logs in with an address of its own -- MRR's worker
// field holds "bitcoincashii:q….mrr" -- is a label there like any other: keyed to its own
// address, it sat at 0 H/s on the dashboard for its whole length, and a block it found would
// have been recorded under an address the coinbase never paid.
func TestSoloCreditsAnotherAddressToThePayoutAddress(t *testing.T) {
	var h [20]byte
	h[0] = 7
	other := cashaddr.Encode(cashaddr.MainnetPrefix, cashaddr.P2PKH, h)
	s := newSoloServer(t, testPayout)
	s.config.CreditPayoutAddress = true

	for _, tc := range []struct{ username, worker string }{
		{other + ".mrr", "mrr"},
		{other, shortAddress(other)}, // a bare address: its short form tells two rigs apart
		{testPayout + ".rig1", "rig1"},
	} {
		c, resp := authorize(t, s, tc.username)
		if resp.Result != true {
			t.Errorf("authorize(%q) refused: %+v", tc.username, resp.Error)
			continue
		}
		c.mu.RLock()
		minerID, worker := c.MinerID, c.WorkerName
		c.mu.RUnlock()
		if minerID != testPayout {
			t.Errorf("authorize(%q) credited to %q, want the payout address %q", tc.username, minerID, testPayout)
		}
		if worker != tc.worker {
			t.Errorf("authorize(%q): worker %q, want %q", tc.username, worker, tc.worker)
		}
	}
}

// Forge Gateway can serve several people: without CreditPayoutAddress an address username is
// credited to itself.
func TestAddressUsernameCreditedToItselfWithoutCreditPayoutAddress(t *testing.T) {
	var h [20]byte
	h[0] = 7
	other := cashaddr.Encode(cashaddr.MainnetPrefix, cashaddr.P2PKH, h)
	s := newSoloServer(t, testPayout)
	c, resp := authorize(t, s, other+".rig1")
	if resp.Result != true {
		t.Fatalf("address username refused: %+v", resp.Error)
	}
	c.mu.RLock()
	minerID, worker := c.MinerID, c.WorkerName
	c.mu.RUnlock()
	if minerID != other || worker != "rig1" {
		t.Errorf("credited to %q worker %q, want %q worker rig1", minerID, worker, other)
	}
}

// A TIDES job commits to a share difficulty above MaxDifficulty, so it must cover every miner
// that is really working, including one whose vardiff step is decided but not yet sent. It counts
// only what a miner has proven with a recent share, and at most provenAhead times that: a
// connection that merely claims a difficulty (d=1000000000000 in its password) made every job
// commit 2^41, above the network difficulty, so the install's other miners went uncredited.
func TestMaxDifficultyCoversEveryProvenMiner(t *testing.T) {
	s := newSoloServer(t, testPayout)
	if got := s.MaxDifficulty(); got != 0 {
		t.Fatalf("no miners: %g, want 0", got)
	}
	now := time.Now()
	s.clients.Store("bitaxe", &Client{ID: "bitaxe", Authorized: true, Difficulty: 1024, LastDifficultySent: 1024,
		ProvenDifficulty: 1024, ProvenAt: now})
	s.clients.Store("rental", &Client{ID: "rental", Authorized: true, Difficulty: 750000, LastDifficultySent: 500000,
		ProvenDifficulty: 500000, ProvenAt: now})
	s.clients.Store("probe", &Client{ID: "probe", Authorized: false, Difficulty: 1e9, LastDifficultySent: 1e9,
		ProvenDifficulty: 1e9, ProvenAt: now})
	s.clients.Store("claim", &Client{ID: "claim", Authorized: true, Difficulty: 1e12, LastDifficultySent: 1e12})
	s.clients.Store("gone quiet", &Client{ID: "gone quiet", Authorized: true, Difficulty: 5e6, LastDifficultySent: 5e6,
		ProvenDifficulty: 5e6, ProvenAt: now.Add(-provenFor - time.Second)})
	if got := s.MaxDifficulty(); got != 750000 {
		t.Fatalf("MaxDifficulty = %g, want 750000 (the rental's next difficulty; not the unauthorized probe, "+
			"the unproven claim or the miner with no share for %s)", got, provenFor)
	}
	s.clients.Store("ramping", &Client{ID: "ramping", Authorized: true, Difficulty: 1e9, LastDifficultySent: 1e9,
		ProvenDifficulty: 1e6, ProvenAt: now})
	if got := s.MaxDifficulty(); got != provenAhead*1e6 {
		t.Fatalf("MaxDifficulty = %g, want %g: a difficulty far above the last share counts only up to %gx it",
			got, provenAhead*1e6, provenAhead)
	}
}

// Forge Solo stopped its rental server twice on the way out, and Stop closed its channel each
// time: every shutdown ended in "panic: close of closed channel". Stop is idempotent.
func TestStopTwiceDoesNotPanic(t *testing.T) {
	s := newSoloServer(t, testPayout)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("second Stop panicked: %v", r)
		}
	}()
	s.Stop()
	s.Stop()
}

// A connected miner that is quiet (a rental between shares) must not hold Stop up: Stop waited
// up to 30 s for miners to hang up, which they never do by themselves, and Docker killed the
// stratum at its 10 s stop timeout with the TIDES shares still queued.
func TestStopClosesQuietMinersPromptly(t *testing.T) {
	s := NewServer(&ServerConfig{Host: "127.0.0.1", Port: 0, MaxConnections: 10, MaxConnectionsPerIP: 10,
		MinDiff: 1024, MaxDiff: 1e12, VardiffEnabled: true, TargetShareTime: 10, RetargetTime: 30, SoloOnly: true},
		zap.NewNop(), nil)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", s.ListenAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Subscribed, then quiet: its handler blocks reading the next message, which is what held
	// Stop up. (A connection that never sends is dropped at shutdown by the first-message wait.)
	if _, err := conn.Write([]byte(`{"id":1,"method":"mining.subscribe","params":["test-miner/1.0"]}` + "\n")); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 4096)
	if n, err := conn.Read(buf); err != nil || !strings.Contains(string(buf[:n]), `"id":1`) {
		t.Fatalf("no subscribe answer: %q %v", buf[:n], err)
	}
	if s.clientCount.Load() != 1 {
		t.Fatalf("%d clients connected, want the one miner", s.clientCount.Load())
	}
	time.Sleep(200 * time.Millisecond) // quiet
	start := time.Now()
	s.Stop()
	// An absolute bound, not one derived from shutdownGrace: Docker gives the whole process 10 s
	// by default, and Forge Solo stops two ports and then sends its queued TIDES shares.
	if took := time.Since(start); took > 4*time.Second {
		t.Fatalf("Stop took %v with one quiet miner connected", took)
	}
	if n := s.clientCount.Load(); n != 0 {
		t.Fatalf("%d miners still connected after Stop", n)
	}
	// Whatever the server sent before closing, the connection must end.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		if _, err := conn.Read(buf); err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				t.Fatal("the miner's connection is still open after Stop")
			}
			break
		}
	}
}

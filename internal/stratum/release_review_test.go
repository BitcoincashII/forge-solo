package stratum

// Regressions found by the 1.0.12 pre-release review. Each test fails on the code before its fix.

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/cashaddr"
	"go.uber.org/zap"
)

// otherAddress is a checksum-valid mainnet address that is not testPayout.
var otherAddress = cashaddr.Encode(cashaddr.MainnetPrefix, cashaddr.P2PKH, [20]byte{0xaa, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 0xbb})

// connected authorizes username and registers the client the way handleClient does, so the
// server sees it as connected.
func connected(t *testing.T, s *Server, id, username string) *Client {
	t.Helper()
	c, resp := authorize(t, s, username)
	if resp.Result != true {
		t.Fatalf("%s refused: %+v", username, resp.Error)
	}
	c.ID = id
	s.clients.Store(c.ID, c)
	return c
}

func minerOf(c *Client) (string, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.MinerID, c.WorkerName
}

// One proof of work, resubmitted under spellings of the version bits that all build the same
// header, is one share: it was accepted 7 times, each credited and in TIDES mode forwarded.
func TestDuplicateShareUnderEverySpellingOfOneVersion(t *testing.T) {
	s, cp := perJobServer()
	c := perJobClient(t)
	j := soloTestJob("a")
	s.jobHistory.Store("a", j)
	c.mu.Lock()
	c.Difficulty = jobLow
	c.mu.Unlock()
	nonce, _ := mineShare(t, s, j, "0000000000000001", jobLow, 1e9)
	accepted := 0
	for _, vb := range []string{"", "00000000", "0000", "zz", "0x20000000", "20000000 "} {
		p := []string{"rig", "a", "0000000000000001", j.NTime, nonce}
		if vb != "" {
			p = append(p, vb)
		}
		params, _ := json.Marshal(p)
		if r := s.handleSubmit(c, &Request{ID: 1, Params: params}); r.Result == true {
			accepted++
			<-cp.ch
		}
	}
	if accepted != 1 {
		t.Fatalf("one share accepted %d times", accepted)
	}
}

// The two ports broadcast the same job: their first miners must not share an extranonce1, or
// they search the same headers (and in TIDES mode, so would the first miners of two installs).
func TestTwoServersHandOutDifferentExtranonce1(t *testing.T) {
	mk := func() *Server {
		return NewServer(&ServerConfig{ExtraNonce1Size: 4, ExtraNonce2Size: 8, MinDiff: 1024}, zap.NewNop(), nil, nil)
	}
	main, rental := mk(), mk()
	a, b := &Client{ID: "a"}, &Client{ID: "b"}
	main.handleSubscribe(a, &Request{ID: 1, Params: json.RawMessage(`["bitaxe/2.0"]`)})
	rental.handleSubscribe(b, &Request{ID: 1, Params: json.RawMessage(`["xminer-1.2.6"]`)})
	if a.ExtraNonce1 == b.ExtraNonce1 || len(a.ExtraNonce1) != 8 || len(b.ExtraNonce1) != 8 {
		t.Fatalf("extranonce1 %q and %q", a.ExtraNonce1, b.ExtraNonce1)
	}
}

// A payout-address change reaches the miners already connected: they stayed on the old one.
func TestPayoutChangeReachesConnectedMiners(t *testing.T) {
	s, _ := perJobServer()
	s.config.CreditPayoutAddress = true
	s.SetSoloPayoutAddress(testPayout)
	c := connected(t, s, "c1", "rig1")
	s.SetSoloPayoutAddress(otherAddress)
	if id, w := minerOf(c); id != otherAddress || w != "rig1" {
		t.Fatalf("after the change: %q / %q, want %q / rig1", id, w, otherAddress)
	}
}

// A miner that logged in with its own address before any payout address was set joins the
// payout address once one is, labelled by its address (or its worker suffix).
func TestAddressUsernameJoinsThePayoutOnceSet(t *testing.T) {
	s, _ := perJobServer()
	s.config.CreditPayoutAddress = true
	withSuffix := connected(t, s, "c1", otherAddress+".rig1")
	bare := connected(t, s, "c2", otherAddress)
	s.SetSoloPayoutAddress(testPayout)
	if id, w := minerOf(withSuffix); id != testPayout || w != "rig1" {
		t.Fatalf("address.rig1: %q / %q, want %q / rig1", id, w, testPayout)
	}
	if id, w := minerOf(bare); id != testPayout || w != shortAddress(otherAddress) {
		t.Fatalf("bare address: %q / %q, want %q / %q", id, w, testPayout, shortAddress(otherAddress))
	}
}

// A share is credited to the payout address in effect when it arrives, even for a miner whose
// own record is stale (one authorizing while the change is applied).
func TestShareIsCreditedToThePayoutInEffect(t *testing.T) {
	s, cp := perJobServer()
	s.config.CreditPayoutAddress = true
	s.SetSoloPayoutAddress(otherAddress)
	c := perJobClient(t) // authorized under testPayout, and not in s.clients: not re-keyed
	j := soloTestJob("a")
	s.jobHistory.Store("a", j)
	c.mu.Lock()
	c.Difficulty = jobLow
	c.mu.Unlock()
	nonce, _ := mineShare(t, s, j, "0000000000000001", jobLow, 1e9)
	if r := submitShare(s, c, "a", "0000000000000001", nonce); r.Result != true {
		t.Fatalf("share refused: %+v", r.Error)
	}
	select {
	case sh := <-cp.ch:
		if sh.MinerID != otherAddress {
			t.Fatalf("credited to %q, want the payout in effect %q", sh.MinerID, otherAddress)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the share never reached the processor")
	}
}

// Every worker label is bounded. With the payout address as the username, a 60 KB label went
// through uncapped and made TIDES share batches the pool refuses, stalling every miner's shares.
func TestWorkerLabelIsBoundedOnEveryPath(t *testing.T) {
	s, _ := perJobServer()
	s.config.CreditPayoutAddress = true
	s.SetSoloPayoutAddress(testPayout)
	for _, user := range []string{
		testPayout + "." + strings.Repeat("x", 500),
		otherAddress + "." + strings.Repeat("y", 500),
		strings.Repeat("z", 500),
	} {
		c := connected(t, s, "c", user)
		if _, w := minerOf(c); len(w) > maxWorkerLabel {
			t.Errorf("%.20s…: label of %d bytes", user, len(w))
		}
	}
}

// Broadcasting while miners authorize is race-free (run with -race): BroadcastJob read
// client.Authorized without the lock handleAuthorize writes it under.
func TestBroadcastWhileMinersAuthorize(t *testing.T) {
	s, _ := perJobServer()
	s.SetSoloPayoutAddress(testPayout)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 1; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			s.BroadcastJob(soloTestJob(fmt.Sprintf("%x", i)))
		}
	}()
	for k := 0; k < 20; k++ {
		poolSide, minerSide := net.Pipe()
		go io.Copy(io.Discard, minerSide)
		c := &Client{ID: fmt.Sprintf("c%d", k), Conn: poolSide}
		s.clients.Store(c.ID, c)
		params, _ := json.Marshal([]string{"rig", "x"})
		s.handleAuthorize(c, &Request{ID: 1, Method: MethodAuthorize, Params: params})
		poolSide.Close()
		minerSide.Close()
	}
	close(stop)
	wg.Wait()
}

package stratum

// Regressions found by the 1.0.12 pre-release review. Each test fails on the code before its fix.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
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

// tcpServer is a started solo server on a free loopback port, crediting cp.
func tcpServer(t *testing.T, sp ShareProcessor) *Server {
	t.Helper()
	s := NewServer(&ServerConfig{Host: "127.0.0.1", Port: 0, MaxConnections: 10, MaxConnectionsPerIP: 10,
		ExtraNonce1Size: 4, ExtraNonce2Size: 8, MinDiff: 1e-6, AbsoluteMinDiff: 1e-6, MaxDiff: 1e12,
		TargetShareTime: 10, RetargetTime: 30, VardiffEnabled: true, SoloOnly: true, CreditPayoutAddress: true},
		zap.NewNop(), sp, nil)
	s.SetSoloPayoutAddress(testPayout)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	return s
}

type testMiner struct {
	c   net.Conn
	r   *bufio.Reader
	en1 string
}

// dialTestMiner subscribes and authorizes as user.
func dialTestMiner(t *testing.T, s *Server, user string) *testMiner {
	t.Helper()
	c, err := net.Dial("tcp", s.ListenAddr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	m := &testMiner{c: c, r: bufio.NewReader(c)}
	m.send(t, `{"id":1,"method":"mining.subscribe","params":["cgminer/4.12"]}`)
	for {
		line := m.line(t)
		if strings.Contains(line, `"id":1`) {
			var resp struct {
				Result []json.RawMessage `json:"result"`
			}
			json.Unmarshal([]byte(line), &resp)
			json.Unmarshal(resp.Result[1], &m.en1)
			break
		}
	}
	m.send(t, `{"id":2,"method":"mining.authorize","params":["`+user+`","x"]}`)
	for !strings.Contains(m.line(t), `"id":2`) {
	}
	return m
}

func (m *testMiner) send(t *testing.T, s string) {
	t.Helper()
	if _, err := m.c.Write([]byte(s + "\n")); err != nil {
		t.Fatal(err)
	}
}

func (m *testMiner) line(t *testing.T) string {
	t.Helper()
	m.c.SetReadDeadline(time.Now().Add(3 * time.Second))
	l, err := m.r.ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return l
}

// shareFor finds a nonce with work on job for the miner's extranonce1, at the 1e-6 floor.
func shareFor(t *testing.T, s *Server, job *Job, en1 string) string {
	t.Helper()
	for n := uint32(0); n < 1<<24; n++ {
		nonce := fmt.Sprintf("%08x", n)
		ok, _, _, err := s.validateShare(job, en1, "0000000000000001", job.NTime, nonce, "", 1e-6)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			return nonce
		}
	}
	t.Fatal("no share found")
	return ""
}

// A share sent while Stop gives the miners their moment is processed: it was read and thrown
// away, and a block found in the last moments before an update or restart was lost with it.
func TestShareDuringStopGraceIsProcessed(t *testing.T) {
	cp := &captureProcessor{ch: make(chan *Share, 16)}
	s := tcpServer(t, cp)
	m := dialTestMiner(t, s, "rig1")
	job := soloTestJob("1")
	s.BroadcastJob(job)
	time.Sleep(100 * time.Millisecond)
	nonce := shareFor(t, s, job, m.en1)
	go s.Stop()
	time.Sleep(300 * time.Millisecond) // inside the 2 s grace
	m.send(t, `{"id":9,"method":"mining.submit","params":["rig1","1","0000000000000001","`+job.NTime+`","`+nonce+`"]}`)
	select {
	case sh := <-cp.ch:
		if sh.JobID != "1" {
			t.Fatalf("processed %+v", sh)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a share submitted during Stop's grace was never processed")
	}
}

// slowProcessor takes its time over each share, like a block share waiting for the pool: longer
// than Stop's grace for the miners plus its wait for their handlers (3 s), within inflightGrace.
type slowProcessor struct{ done atomic.Int32 }

func (p *slowProcessor) ProcessShare(context.Context, *Share) error {
	time.Sleep(3500 * time.Millisecond)
	p.done.Add(1)
	return nil
}

// Stop waits for shares still being processed: exiting under one lost the block it carried.
func TestStopWaitsForSharesBeingProcessed(t *testing.T) {
	sp := &slowProcessor{}
	s := tcpServer(t, sp)
	m := dialTestMiner(t, s, "rig1")
	job := soloTestJob("1")
	s.BroadcastJob(job)
	time.Sleep(100 * time.Millisecond)
	nonce := shareFor(t, s, job, m.en1)
	m.send(t, `{"id":9,"method":"mining.submit","params":["rig1","1","0000000000000001","`+job.NTime+`","`+nonce+`"]}`)
	for !strings.Contains(m.line(t), `"id":9`) {
	}
	s.Stop()
	if sp.done.Load() != 1 {
		t.Fatal("Stop returned while the accepted share was still being processed")
	}
}

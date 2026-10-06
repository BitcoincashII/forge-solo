package main

import (
	"bufio"
	"crypto/ed25519"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/mining"
	"github.com/BitcoincashII/forge-solo/internal/stats"
	"github.com/BitcoincashII/forge-solo/internal/stratum"
	"github.com/BitcoincashII/forge-solo/internal/tidesgw"
	"go.uber.org/zap"
)

// minerLogin logs a miner in on addr: subscribe with its user agent, then authorize.
func minerLogin(t *testing.T, addr, ua, user string) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	fmt.Fprintf(c, `{"id":1,"method":"mining.subscribe","params":["%s"]}`+"\n", ua)
	fmt.Fprintf(c, `{"id":2,"method":"mining.authorize","params":["%s","x"]}`+"\n", user)
	r := bufio.NewReader(c)
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("login as %s: %v", ua, err)
		}
		if strings.Contains(line, `"id":2`) {
			break
		}
	}
	c.SetReadDeadline(time.Time{})
	go io.Copy(io.Discard, r)
	return c
}

// The TIDES job in flight was registered for the miners logged in then. A rental that logs in
// above them had each share on it credited at the pool's own 1024 until the next job, up to 15 s of
// its work. Its login now asks the job loop for a new job at once, registered at its difficulty;
// a login on a job already committed to it asks for nothing.
func TestALoginOnAnUndercommittedTIDESJobAsksForANewJobAtOnce(t *testing.T) {
	const payout = "bitcoincashii:qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzse6qye33q"
	savedMain, savedRental, savedLogger, savedJob := stratumServer, stratumRentalServer, logger, getCurrentJob()
	savedGW, savedMode := tidesGateway(), currentPayoutMode()
	t.Cleanup(func() {
		stratumServer, stratumRentalServer, logger = savedMain, savedRental, savedLogger
		setCurrentJob(savedJob)
		tidesGWPtr.Store(savedGW)
		payoutModeVal.Store(savedMode)
		tidesRefreshWanted.Store(false)
	})
	logger = zap.NewNop()

	srv := stratum.NewServer(&stratum.ServerConfig{Host: "127.0.0.1", Port: 0, MaxConnections: 8, ExtraNonce1Size: 4,
		ExtraNonce2Size: 8, MinDiff: 500000, MaxDiff: 1e12, IsRentalPort: true, SoloOnly: true, CreditPayoutAddress: true},
		zap.NewNop(), nil, nil)
	srv.SetSoloPayoutAddress(payout)
	srv.SetLoginHandler(tidesLoginCheck)
	stratumServer, stratumRentalServer = nil, srv
	_, key, _ := ed25519.GenerateKey(nil)
	g := tidesgw.New(tidesgw.Config{PoolURL: "http://127.0.0.1:9", Key: key, MaxDifficulty: tidesMaxDifficulty})
	tidesGWPtr.Store(g)
	payoutModeVal.Store(stats.PayoutModeTides)
	tidesRefreshWanted.Store(false)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)

	// Job 18 was registered while no miner was logged in: it commits to nothing, the pool's 1024.
	g.Track("18", &tidesgw.Registration{PoolJobID: "pj18", ShareDiff: 1024, Bits: "1902c9b9"}, "20000000")
	setCurrentJob(&mining.Job{ID: "18", Tides: true})
	minerLogin(t, srv.ListenAddr(), "bosminer-plus-tuner 0.9.3-5a7fd334", payout+".mrr")
	for end := time.Now().Add(3 * time.Second); !tidesRefreshWanted.Load(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("TIDES-LOGIN-ASKS: a rental at 500000 logged in on a job credited 1024 a share, and no new job was asked for")
		}
	}
	now := time.Now()
	if !periodicJobDue(now, now) {
		t.Fatal("TIDES-LOGIN-DUE: the job loop was asked for a new job and does not make one")
	}
	if periodicJobDue(now, now) {
		t.Fatal("TIDES-LOGIN-ONCE: one login asked the job loop for more than one new job")
	}
	if !periodicJobDue(now.Add(-periodicJobEvery), now) {
		t.Fatal("TIDES-LOGIN-PERIODIC: the periodic job is no longer due after its interval")
	}

	// Job 19, registered for the rental: committed to 2^20. Another login asks for nothing.
	g.Track("19", &tidesgw.Registration{PoolJobID: "pj19", ShareDiff: 1 << 20, Committed: 1 << 20, Bits: "1902c9b9"}, "20000000")
	setCurrentJob(&mining.Job{ID: "19", Tides: true})
	tidesLoginCheck()
	if tidesRefreshWanted.Load() {
		t.Fatal("TIDES-LOGIN-COMMITTED: a login on a job committed to its difficulty asked for a new job")
	}
	// Solo mode, or a solo job meanwhile: nothing to ask for.
	setCurrentJob(&mining.Job{ID: "18", Tides: true})
	payoutModeVal.Store(stats.PayoutModeSolo)
	tidesLoginCheck()
	if tidesRefreshWanted.Load() {
		t.Fatal("TIDES-LOGIN-SOLO: a login in solo mode asked for a new TIDES job")
	}
}

// Both stratum ports run the check at each login, and the job loop takes its request.
func TestTheLoginCheckIsWired(t *testing.T) {
	for file, wants := range map[string][]string{
		"main.go": {
			"stratumServer.SetLoginHandler(tidesLoginCheck)",
			"stratumRentalServer.SetLoginHandler(tidesLoginCheck)",
		},
		"job_loop.go": {"needPeriodicUpdate := periodicJobDue(l.lastJobTime, l.now())"},
	} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range wants {
			if !strings.Contains(string(raw), want) {
				t.Errorf("TIDES-LOGIN-WIRED: %s no longer has %q", file, want)
			}
		}
	}
}

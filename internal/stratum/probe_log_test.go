package stratum

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap/zaptest/observer"
)

const probeUA = "infinite-hash-proxy/probe"

// marketLogin logs a client in on addr as MiningRigRentals' rig and its health checks do:
// mining.configure, then subscribe with its user agent, then authorize. It returns once the login
// is answered.
func marketLogin(t *testing.T, addr, ua, user string) net.Conn {
	t.Helper()
	c, r := dialLine(t, addr,
		`{"id":1,"method":"mining.configure","params":[["version-rolling"],{"version-rolling.mask":"1fffe000","version-rolling.min-bit-count":16}]}`,
		`{"id":2,"method":"mining.subscribe","params":["`+ua+`"]}`,
		`{"id":3,"method":"mining.authorize","params":["`+user+`","x"]}`)
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("login as %s: %v", ua, err)
		}
		if strings.Contains(line, `"id":3`) {
			break
		}
	}
	c.SetReadDeadline(time.Time{})
	go io.Copy(io.Discard, r)
	t.Cleanup(func() { c.Close() })
	return c
}

// leave ends a connection as MiningRigRentals' health checks do, cleanly: the stratum reads the
// end of it. (Closed with answers still unread, a connection ends in a reset instead.)
func leave(t *testing.T, c net.Conn) {
	t.Helper()
	if err := c.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
}

// A marketplace's check that the pool works is told from a miner by its user agent, and only
// those: a rented rig relayed by a marketplace, or a home miner behind a proxy, is a miner.
func TestHealthChecksAreToldByTheirUserAgent(t *testing.T) {
	for ua, want := range map[string]bool{
		"infinite-hash-proxy/probe": true,
		"MiningRigRentals/Test/1.0": true,
		"miningrigrentals/test":     true,
		"Some-Proxy/PROBE":          true,
		"bosminer-plus-tuner 0.9.3": false,
		"xminer-1.2.6":              false,
		"MiningRigRentals/1.0":      false,
		"stratum-proxy/1.0":         false,
		"probe-miner/2.0":           false,
		"":                          false,
	} {
		if got := isHealthCheck(ua); got != want {
			t.Errorf("PROBE-UA: %q is a health check: %v, want %v", ua, got, want)
		}
	}
}

// rentalLogServer is the rental port, as shipped, on a listener whose clients come from the
// internet, with a job to hand out.
func rentalLogServer(t *testing.T) (*Server, *remoteListener, *observer.ObservedLogs) {
	t.Helper()
	s, rl, logs := observedServer(t, &ServerConfig{MaxConnections: 64, MaxConnectionsPerIP: 32, ExtraNonce1Size: 4,
		ExtraNonce2Size: 8, MinDiff: 500000, MaxDiff: 1e12, VardiffEnabled: true, TargetShareTime: 25, RetargetTime: 10,
		IsRentalPort: true, SoloOnly: true, CreditPayoutAddress: true})
	s.currentJob.Store(&Job{ID: "1", PrevBlockHash: strings.Repeat("00", 32), Version: "20000000", NBits: "1903444b",
		NTime: "6ac3a18a", CoinBase1: "01", CoinBase2: "02", CreatedAt: time.Now()})
	return s, rl, logs
}

// withUA is the lines logged with user agent ua and a message starting with msg.
func withUA(logs *observer.ObservedLogs, msg, ua string) []observer.LoggedEntry {
	var out []observer.LoggedEntry
	for _, e := range logs.All() {
		if u, _ := e.ContextMap()["user_agent"].(string); u == ua && strings.HasPrefix(e.Message, msg) {
			out = append(out, e)
		}
	}
	return out
}

// During a MiningRigRentals rental its health checks log in under the order's own name about 550
// times an hour, hold the connection about 10 s, send no share and close it. Each wrote five lines,
// 85% of the stratum's log, so the 20 MB kept on Windows and Linux held under three days of a
// rental, and the rig's own login and disconnect lines, which named neither its address nor its
// user agent, could be told from the health checks' only by how long they had been connected.
func TestHealthChecksDoNotFillTheLog(t *testing.T) {
	s, rl, logs := rentalLogServer(t)
	addr := rl.Addr().String()
	user := testPayout + ".mrr"
	rig := marketLogin(t, addr, rigUA, user)
	const probes = 10
	for i := 0; i < probes; i++ {
		leave(t, marketLogin(t, addr, probeUA, user))
	}
	settled(t, s, rl, probes+1, 1)

	for _, e := range logs.All() {
		said := strings.HasPrefix(e.Message, "Marketplace health check logged in and closed again") ||
			strings.HasPrefix(e.Message, "Marketplace health checks logged in and closed again")
		if u, _ := e.ContextMap()["user_agent"].(string); u == probeUA && !said {
			t.Errorf("PROBE-LOG-QUIET: a health check logged %q", e.Message)
		}
		if e.Message == "External client connected" || e.Message == "mining.configure received" {
			t.Errorf("PROBE-LOG-DEBUG: %q is logged above debug", e.Message)
		}
	}
	if n := logs.FilterMessageSnippet("Marketplace health check logged in and closed again").Len(); n != 1 {
		t.Errorf("PROBE-LOG-FIRST: the first health check was said %d times, want once", n)
	}
	login := withUA(logs, "Miner authorized", rigUA)
	if len(login) != 1 {
		t.Fatalf("PROBE-LOG-LOGIN: %d login lines for the rig", len(login))
	}
	if ip, _ := login[0].ContextMap()["ip"].(string); !strings.HasPrefix(ip, "8.8.8.8:") {
		t.Errorf("PROBE-LOG-LOGIN-FIELDS: the rig's login line gives the address %q", ip)
	}
	if _, ok := login[0].ContextMap()["valid_shares"]; !ok {
		t.Errorf("PROBE-LOG-LOGIN-FIELDS: the rig's login line has no valid_shares: %v", login[0].ContextMap())
	}

	// Ten minutes on, one line counts the others.
	s.flushHealthChecks(time.Now().Add(healthCheckSummaryEvery), false)
	sum := logs.FilterMessage("Marketplace health checks logged in and closed again").All()
	if len(sum) != 1 || sum[0].ContextMap()["count"] != int64(probes-1) {
		t.Fatalf("PROBE-LOG-SUMMARY: the health checks since the first were summed up as %v", sum)
	}
	if s.flushHealthChecks(time.Now().Add(2*healthCheckSummaryEvery), false); logs.FilterMessage("Marketplace health checks logged in and closed again").Len() != 1 {
		t.Error("PROBE-LOG-SUMMARY-ONCE: with no health check since, the count was logged again")
	}

	leave(t, rig)
	settled(t, s, rl, probes+1, 0)
	gone := withUA(logs, "Client disconnected", rigUA)
	if len(gone) != 1 {
		t.Fatalf("PROBE-LOG-DISCONNECT: %d disconnect lines for the rig", len(gone))
	}
	if ip, _ := gone[0].ContextMap()["ip"].(string); !strings.HasPrefix(ip, "8.8.8.8:") {
		t.Errorf("PROBE-LOG-DISCONNECT-FIELDS: the rig's disconnect line gives the address %q", ip)
	}
}

// A health check is counted, not logged, only when it went as health checks do: it sent no share
// and closed the connection itself. One the stratum closed, or one that sent shares, is logged in
// full, as a miner is. And the stratum, stopping, says how many it counted since its last line.
func TestAHealthCheckOutOfTheOrdinaryIsLoggedInFull(t *testing.T) {
	s, rl, logs := rentalLogServer(t)
	addr := rl.Addr().String()
	user := testPayout + ".mrr"

	bad := marketLogin(t, addr, probeUA, user)
	bad.Write([]byte(strings.Repeat("garbage\n", maxBadLines+1)))
	settled(t, s, rl, 1, 0)
	if gone := withUA(logs, "Client disconnected", probeUA); len(gone) != 1 || gone[0].ContextMap()["reason"] != "it sent lines that are not stratum" {
		t.Errorf("PROBE-LOG-CLOSED-BY-STRATUM: a health check the stratum closed was logged as %v", gone)
	}

	hashing := marketLogin(t, addr, probeUA, user)
	s.clients.Range(func(_, v interface{}) bool {
		v.(*Client).ValidShares.Store(3)
		return true
	})
	leave(t, hashing)
	settled(t, s, rl, 2, 0)
	if n := len(withUA(logs, "Client disconnected", probeUA)); n != 2 {
		t.Errorf("PROBE-LOG-HASHING: a health-check user agent that sent shares was not logged as a miner (%d lines)", n)
	}

	leave(t, marketLogin(t, addr, probeUA, user)) // the first routine one: said at once
	leave(t, marketLogin(t, addr, probeUA, user)) // counted
	settled(t, s, rl, 4, 0)
	s.Stop()
	sum := logs.FilterMessage("Marketplace health checks logged in and closed again").All()
	if len(sum) != 1 || sum[0].ContextMap()["count"] != int64(1) {
		t.Errorf("PROBE-LOG-STOP-FLUSH: stopping, the stratum summed up the health checks it counted as %v", sum)
	}
}

// The count is logged by the stratum's cleanup round while it runs, not only when it stops: the
// health checks end with the rental, and nothing else would say how many came.
func TestHealthChecksAreSummedUpWhileTheStratumRuns(t *testing.T) {
	defer func(every time.Duration) { shareCleanupEvery = every }(shareCleanupEvery)
	shareCleanupEvery = 20 * time.Millisecond
	s, rl, logs := rentalLogServer(t)
	addr := rl.Addr().String()
	user := testPayout + ".mrr"
	leave(t, marketLogin(t, addr, probeUA, user)) // said at once
	leave(t, marketLogin(t, addr, probeUA, user)) // counted
	settled(t, s, rl, 2, 0)

	// Ten minutes since the first one was said.
	s.probes.mu.Lock()
	s.probes.since = s.probes.since.Add(-healthCheckSummaryEvery)
	s.probes.mu.Unlock()
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		sum := logs.FilterMessage("Marketplace health checks logged in and closed again").All()
		if len(sum) == 1 && sum[0].ContextMap()["count"] == int64(1) {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("PROBE-LOG-TICK: ten minutes after the first health check, with the stratum running, the one since was summed up as %v", sum)
		}
	}
}

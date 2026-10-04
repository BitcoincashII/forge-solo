package stratum

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// observedServer is a solo stratum whose log a test reads, on a listener that reports its
// connections as coming from the internet.
func observedServer(t *testing.T, cfg *ServerConfig) (*Server, *remoteListener, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zapcore.InfoLevel)
	s := NewServer(cfg, zap.New(core), nil, nil)
	s.SetSoloPayoutAddress(testPayout)
	rl := serveOn(t, s)
	rl.internet.Store(true)
	return s, rl, logs
}

// settled waits until s has accepted `accepted` connections and has `open` of them still open.
func settled(t *testing.T, s *Server, rl *remoteListener, accepted, open int64) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); rl.accepted.Load() < accepted || s.clientCount.Load() != open; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("%d of %d connections accepted, %d open, want %d", rl.accepted.Load(), accepted, s.clientCount.Load(), open)
		}
	}
}

func dialLine(t *testing.T, addr string, lines ...string) (net.Conn, *bufio.Reader) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		c.Write([]byte(l + "\n"))
	}
	return c, bufio.NewReader(c)
}

// What a client can put in the log is limited. mining.suggest_difficulty wrote a line for every
// suggestion, outside the connection's budget: one connection sending them wrote 30 MB in minutes.
func TestSuggestionsWriteABudgetedLog(t *testing.T) {
	s, _ := perJobServer()
	core, logs := observer.New(zap.InfoLevel)
	s.logger = zap.New(core)
	c := perJobClient(t)
	for i := 0; i < 200; i++ {
		s.handleMessage(c, []byte(fmt.Sprintf(`{"id":%d,"method":"mining.suggest_difficulty","params":[%d]}`, i, 1+i%2)))
	}
	if n := logs.FilterMessage("Miner suggested difficulty accepted").Len(); n == 0 || n > clientLogBudget {
		t.Fatalf("LOG-SUGGEST: 200 suggestions wrote %d lines, want 1..%d", n, clientLogBudget)
	}
}

// With no payout address set, a refused login wrote the whole username, up to 64 KB, every time.
func TestRefusedLoginsWriteABudgetedClippedLog(t *testing.T) {
	s := newSoloServer(t, "")
	core, logs := observer.New(zap.InfoLevel)
	s.logger = zap.New(core)
	long := strings.Repeat("x", 60000)
	login := func(c *Client, user string) {
		params, _ := json.Marshal([]string{user, "x"})
		s.handleMessage(c, []byte(`{"id":2,"method":"mining.authorize","params":`+string(params)+`}`))
	}
	c := perJobClient(t)
	for i := 0; i < 100; i++ {
		login(c, long)
	}
	refused := logs.FilterMessage("Rejected connection with invalid address").All()
	if len(refused) == 0 || len(refused) > clientLogBudget {
		t.Fatalf("LOG-REJECT: 100 refused logins wrote %d lines, want 1..%d", len(refused), clientLogBudget)
	}
	for _, e := range refused {
		if u, _ := e.ContextMap()["username"].(string); len(u) > maxWorkerLabel+len("…") {
			t.Fatalf("LOG-REJECT-CLIP: a refused login logged a %d-byte username", len(u))
		}
	}

	login(perJobClient(t), "braiins"+long)
	probes := logs.FilterMessage("Braiins probe connection accepted").All()
	if len(probes) != 1 {
		t.Fatalf("LOG-PROBE: %d probe lines, want 1", len(probes))
	}
	if u, _ := probes[0].ContextMap()["username"].(string); len(u) > maxWorkerLabel+len("…") {
		t.Fatalf("LOG-PROBE-CLIP: a probe login logged a %d-byte username", len(u))
	}
}

// Every connection came with a fresh budget, and the lines about it opening and closing were
// outside any: opening and closing connections in a loop rotated the whole log away in minutes.
// All the lines clients cause on a port share its budget.
func TestConnectionChurnWritesABudgetedLog(t *testing.T) {
	s, rl, logs := observedServer(t, &ServerConfig{MaxConnections: 64, ExtraNonce1Size: 4, ExtraNonce2Size: 8, SoloOnly: true})
	addr := rl.Addr().String()
	const each = 100
	for i := 0; i < each; i++ {
		c, _ := dialLine(t, addr) // opens and closes
		c.Close()
		c, r := dialLine(t, addr, "GET / HTTP/1.1") // not stratum
		r.ReadString('\n')
		c.Close()
		c, r = dialLine(t, addr, `{"id":1,"method":"mining.extranonce.subscribe","params":[]}`) // a message, not a login
		r.ReadString('\n')
		c.Close()
		c, r = dialLine(t, addr, `{"id":1,"method":"mining.subscribe","params":[]}`) // subscribes only
		r.ReadString('\n')
		c.Close()
		c, r = dialLine(t, addr, `{"id":1,"method":"mining.subscribe","params":[]}`,
			`{"id":2,"method":"mining.authorize","params":["rig1","x"]}`) // logs in
		c.SetReadDeadline(time.Now().Add(3 * time.Second))
		for {
			line, err := r.ReadString('\n')
			if err != nil || strings.Contains(line, `"id":2`) {
				break
			}
		}
		c.Close()
	}
	settled(t, s, rl, 5*each, 0)
	if n := logs.Len(); n > serverLogBudget {
		t.Fatalf("LOG-CHURN: %d connections opened and closed wrote %d lines, budget %d", 5*each, n, serverLogBudget)
	}
}

// Connections refused by the per-address cap each wrote a warning.
func TestRefusedConnectionsWriteABudgetedLog(t *testing.T) {
	s, rl, logs := observedServer(t, &ServerConfig{MaxConnections: 64, MaxConnectionsPerIP: 2, ExtraNonce1Size: 4, ExtraNonce2Size: 8, SoloOnly: true})
	addr := rl.Addr().String()
	for i := 0; i < 2; i++ {
		c, _ := dialLine(t, addr)
		t.Cleanup(func() { c.Close() })
	}
	settled(t, s, rl, 2, 2)
	for i := 0; i < 300; i++ {
		c, _ := dialLine(t, addr)
		c.Close()
	}
	settled(t, s, rl, 302, 2)
	if n := logs.FilterMessage("refused connection: per-IP limit reached").Len(); n == 0 || n > serverLogBudget {
		t.Fatalf("LOG-REFUSED: 300 refused connections wrote %d lines, want 1..%d", n, serverLogBudget)
	}
}

// The lines a connection's budget left out are counted even when it writes nothing more: the line
// about it ending says how many.
func TestLinesLeftOutAreCountedWhenTheConnectionEnds(t *testing.T) {
	s, logs := reasonServer(t)
	c := loggedIn(t, s)
	var b strings.Builder
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&b, `{"id":%d,"method":"mining.ping","params":[]}`+"\n", 100+i)
	}
	c.Write([]byte(b.String()))
	time.Sleep(300 * time.Millisecond)
	c.Close()
	disconnectReason(t, logs, "LOG-LEFT-AT-END")
	e := logs.FilterMessage("Client disconnected").All()[0]
	if n, _ := e.ContextMap()["suppressed_since_last"].(int64); n < 30 {
		t.Fatalf("LOG-LEFT-AT-END: the connection's last line says %d lines were left out, want the 30 or more its budget dropped", n)
	}
}

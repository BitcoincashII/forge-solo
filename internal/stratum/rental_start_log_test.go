package stratum

import (
	"bufio"
	"fmt"
	"io"
	"maps"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// port3333Server is port 3333 as shipped, on a listener whose clients come from the internet, with
// a job to hand out.
func port3333Server(t *testing.T) (*Server, *remoteListener, *observer.ObservedLogs) {
	t.Helper()
	s, rl, logs := observedServer(t, &ServerConfig{MaxConnections: 256, MaxConnectionsPerIP: 128, ExtraNonce1Size: 4,
		ExtraNonce2Size: 8, MinDiff: 1024, MaxDiff: 1e12, VardiffEnabled: true, TargetShareTime: 5, RetargetTime: 10,
		SoloOnly: true, CreditPayoutAddress: true})
	s.currentJob.Store(&Job{ID: "1", PrevBlockHash: strings.Repeat("00", 32), Version: "20000000", NBits: "1903444b",
		NTime: "6ac3a18a", CoinBase1: "01", CoinBase2: "02", CreatedAt: time.Now()})
	return s, rl, logs
}

// s21UA is the user agent the rented Antminer S21 XPs gave.
const s21UA = "Antminer S21 XP/Tue Apr 15 16:46:21 CST 2025"

// leftOutMsg is the line that sums up the lines the log budgets left out.
const leftOutMsg = "Log lines left out to keep the log from filling"

// refusedLogin opens a connection to addr, logs in as user with user agent ua and sends refused
// shares on a job the stratum does not know. It returns once they are answered, with the address the
// stratum sees the connection come from. It does not use t, so a rig's goroutine can call it.
func refusedLogin(addr, ua, user string, refused int) (net.Conn, string, error) {
	c, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, `{"id":1,"method":"mining.subscribe","params":["%s"]}`+"\n", ua)
	fmt.Fprintf(&b, `{"id":2,"method":"mining.authorize","params":["%s","x"]}`+"\n", user)
	for i := 0; i < refused; i++ {
		fmt.Fprintf(&b, `{"id":%d,"method":"mining.submit","params":["%s","ee","%016x","6ac3a18a","00000000"]}`+"\n", 3+i, user, i)
	}
	c.Write([]byte(b.String()))
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	r := bufio.NewReader(c)
	last := fmt.Sprintf(`"id":%d,`, 2+refused)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			c.Close()
			return nil, "", err
		}
		if strings.Contains(line, last) {
			break
		}
	}
	c.SetReadDeadline(time.Time{})
	go io.Copy(io.Discard, r)
	return c, fmt.Sprintf("8.8.8.8:%d", c.LocalAddr().(*net.TCPAddr).Port), nil
}

// rentalStart replays a rental's start as MiningRigRentals brought one on: rigs, all through one
// address, each connect attempts times within the minute, log in under the order's name and have a
// share refused, and every connection but a rig's last closes again. It returns the address the
// stratum saw each connection come from, once every one has closed.
func rentalStart(t *testing.T, s *Server, rl *remoteListener, rigs, attempts int) []string {
	t.Helper()
	addr := rl.Addr().String()
	var (
		mu     sync.Mutex
		seen   []string
		open   []net.Conn
		failed error
		wg     sync.WaitGroup
	)
	for i := 0; i < rigs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for a := 0; a < attempts; a++ {
				c, from, err := refusedLogin(addr, s21UA, testPayout+".mrr", 1)
				mu.Lock()
				if err != nil {
					if failed == nil {
						failed = err
					}
					mu.Unlock()
					return
				}
				seen = append(seen, from)
				if a == attempts-1 {
					open = append(open, c)
				}
				mu.Unlock()
				if a < attempts-1 {
					c.Close()
				}
			}
		}()
	}
	wg.Wait()
	if failed != nil {
		t.Fatalf("LOGB-RENTAL-SETUP: a rig could not log in: %v", failed)
	}
	for _, c := range open {
		c.Close()
	}
	settled(t, s, rl, int64(rigs*attempts), 0)
	return seen
}

// linesFrom counts, for each connection, the lines with message msg logged about it.
func linesFrom(logs *observer.ObservedLogs, msg string) map[string]int {
	got := map[string]int{}
	for _, e := range logs.FilterMessage(msg).All() {
		ip, _ := e.ContextMap()["ip"].(string)
		got[ip]++
	}
	return got
}

// without is how many of the connections in seen have no line in got.
func without(seen []string, got map[string]int) int {
	n := 0
	for _, ip := range seen {
		if got[ip] == 0 {
			n++
		}
	}
	return n
}

// A rental's start, as one came on a Windows PC: the 16 rigs of an order, all through
// MiningRigRentals' one address, connected about 50 times in a minute. One budget of 120 lines a
// minute held every line clients caused on the port; the logins used it up and the rest of the
// minute's lines were left out, a refused share among them (the dashboard counted two, the log
// showed one). A rental of 32 rigs reconnecting as often writes every line too.
func TestARentalStartIsLoggedInFull(t *testing.T) {
	for _, tc := range []struct {
		name           string
		rigs, attempts int
	}{
		{"16 rigs, as seen", 16, 3},
		{"32 rigs", 32, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, rl, logs := port3333Server(t)
			seen := rentalStart(t, s, rl, tc.rigs, tc.attempts)
			if n := without(seen, linesFrom(logs, "Job not found")); n > 0 {
				t.Errorf("LOGB-RENTAL-REFUSED: %d of %d refused shares left no line", n, len(seen))
			}
			if n := without(seen, linesFrom(logs, "Client disconnected")); n > 0 {
				t.Errorf("LOGB-RENTAL-GONE: %d of %d miners that had logged in left no line when they disconnected", n, len(seen))
			}
			if n := without(seen, linesFrom(logs, "Miner authorized")); n > 0 {
				t.Errorf("LOGB-RENTAL-LOGIN: %d of %d logins left no line", n, len(seen))
			}
			if n := without(seen, linesFrom(logs, "Client subscribed")); n > 0 {
				t.Errorf("LOGB-RENTAL-SUBSCRIBE: %d of %d connections left no line when they subscribed", n, len(seen))
			}
		})
	}
}

// churnNeverLoggingIn opens and closes 4*rounds connections on addr that never log in, as a scanner
// or a broken client does: one sends nothing, one is not stratum, one only asks for extranonce
// updates, one only subscribes. Each causes a line, and the last two (subscribed, then ended before
// logging in): 5 a round.
func churnNeverLoggingIn(t *testing.T, addr string, rounds int) {
	t.Helper()
	for i := 0; i < rounds; i++ {
		c, _ := dialLine(t, addr)
		c.Close()
		c, r := dialLine(t, addr, "GET / HTTP/1.1")
		r.ReadString('\n')
		c.Close()
		c, r = dialLine(t, addr, `{"id":1,"method":"mining.extranonce.subscribe","params":[]}`)
		r.ReadString('\n')
		c.Close()
		c, r = dialLine(t, addr, `{"id":1,"method":"mining.subscribe","params":[]}`)
		r.ReadString('\n')
		c.Close()
	}
}

// causedByChurn is the lines churnNeverLoggingIn causes, by message.
func causedByChurn(rounds int) map[string]int64 {
	return map[string]int64{
		"External client disconnected without subscribing":              int64(2 * rounds),
		"Closed a connection that did not start with a stratum message": int64(rounds),
		"Client subscribed": int64(rounds),
		"External client disconnected before logging in": int64(rounds),
	}
}

// Connections that never log in are held to the port's budget, and no longer use up the lines of
// the miners that do: a miner's refused share and its disconnect are still logged.
func TestAFloodThatNeverLogsInLeavesTheMinersLines(t *testing.T) {
	s, rl, logs := port3333Server(t)
	addr := rl.Addr().String()
	const rounds = 60
	churnNeverLoggingIn(t, addr, rounds)
	settled(t, s, rl, 4*rounds, 0)
	if n := logs.Len(); n > serverLogBudget {
		t.Fatalf("LOGB-FLOOD-BOUND: %d connections that never logged in wrote %d lines, budget %d", 4*rounds, n, serverLogBudget)
	}

	c, from, err := refusedLogin(addr, s21UA, testPayout+".mrr", 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	settled(t, s, rl, 4*rounds+1, 0)
	if linesFrom(logs, "Job not found")[from] != 1 {
		t.Errorf("LOGB-FLOOD-REFUSED: after a flood of connections that never logged in, a miner's refused share left no line")
	}
	if linesFrom(logs, "Client disconnected")[from] != 1 {
		t.Errorf("LOGB-FLOOD-GONE: after a flood of connections that never logged in, a miner's disconnect left no line")
	}

	// Stopping, the stratum sums up what was left out since its last cleanup round.
	caused := causedByChurn(rounds)
	caused["Client subscribed"]++ // the miner's
	var want int64
	for msg, n := range caused {
		want += n - int64(logs.FilterMessage(msg).Len())
	}
	s.Stop()
	if _, n := summedUp(logs, overPort); n != want {
		t.Errorf("LOGB-SUMMARY-STOP: stopping, the stratum summed up %d lines left out, want %d", n, want)
	}
}

// The reasons the lines summing up lines left out give, by how they start.
const (
	overOwn    = "one connection's own messages"
	overPort   = "lines about connections, besides"
	overMiners = "miners' logins, refused shares, difficulty changes and disconnects came"
)

// summedUp adds up the lines that sum up lines left out for the reason that starts with reason: how
// many, and how many of each message.
func summedUp(logs *observer.ObservedLogs, reason string) (map[string]int64, int64) {
	by := map[string]int64{}
	var n int64
	for _, e := range logs.FilterMessage(leftOutMsg).All() {
		m := e.ContextMap()
		if r, _ := m["reason"].(string); !strings.HasPrefix(r, reason) {
			continue
		}
		c, _ := m["count"].(int64)
		n += c
		lines, _ := m["lines"].(map[string]int64)
		for k, v := range lines {
			by[k] += v
		}
	}
	return by, n
}

// awaitSummedUp waits for the lines summing up lines left out for reason to count want.
func awaitSummedUp(logs *observer.ObservedLogs, reason string, want int64) (map[string]int64, int64) {
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		by, n := summedUp(logs, reason)
		if n >= want || time.Now().After(end) {
			return by, n
		}
	}
}

// Lines left out are summed up in a line of their own at the next cleanup round: how many, why, and
// which messages. They were only counted on the next line written, whatever connection that was
// about: the 54 left out at a rental's start showed as suppressed_since_last on a rig's disconnect
// a minute later.
func TestLinesLeftOutAreSummedUpPromptly(t *testing.T) {
	defer func(every time.Duration) { shareCleanupEvery = every }(shareCleanupEvery)
	shareCleanupEvery = 20 * time.Millisecond
	s, rl, logs := port3333Server(t)
	addr := rl.Addr().String()
	const rounds = 60
	churnNeverLoggingIn(t, addr, rounds)
	settled(t, s, rl, 4*rounds, 0)

	want := map[string]int64{}
	var wantN int64
	for msg, n := range causedByChurn(rounds) {
		if left := n - int64(logs.FilterMessage(msg).Len()); left > 0 {
			want[msg] = left
			wantN += left
		}
	}
	if wantN == 0 {
		t.Fatal("LOGB-SUMMARY-SETUP: the flood left nothing out")
	}
	by, n := awaitSummedUp(logs, overPort, wantN)
	if n == 0 {
		t.Errorf("LOGB-SUMMARY: %d lines about connections that never logged in were left out and no line says so", wantN)
	} else if n != wantN || !maps.Equal(by, want) {
		t.Errorf("LOGB-SUMMARY-COUNT: the lines left out were summed up as %d %v, want %d %v", n, by, wantN, want)
	}
	// Once: the cleanup rounds since say nothing more.
	time.Sleep(10 * shareCleanupEvery)
	if _, again := summedUp(logs, overPort); again != n {
		t.Errorf("LOGB-SUMMARY-ONCE: the %d lines left out were summed up again, %d in all", n, again)
	}

	// A minute on, the next lines written are about their own connection only.
	s.logs.mu.Lock()
	s.logs.windowStart = s.logs.windowStart.Add(-time.Minute)
	s.logs.mu.Unlock()
	c, _, err := refusedLogin(addr, s21UA, testPayout+".mrr", 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	settled(t, s, rl, 4*rounds+1, 0)
	for _, e := range logs.All() {
		if v, ok := e.ContextMap()["suppressed_since_last"]; ok {
			t.Errorf("LOGB-RIDES: %q carries suppressed_since_last %v, lines other connections caused", e.Message, v)
		}
	}
}

// A connection's own lines left out are summed up too, and the line saying it ended still says how
// many of them there were.
func TestAConnectionsOwnLinesLeftOutAreSummedUp(t *testing.T) {
	defer func(every time.Duration) { shareCleanupEvery = every }(shareCleanupEvery)
	shareCleanupEvery = 20 * time.Millisecond
	s, rl, logs := port3333Server(t)
	c, from, err := refusedLogin(rl.Addr().String(), s21UA, testPayout+".mrr", 1)
	if err != nil {
		t.Fatal(err)
	}
	// Subscribed, logged in and refused a share: 3 lines of its 20. 40 pings write 17 more.
	var b strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, `{"id":%d,"method":"mining.ping","params":[]}`+"\n", 100+i)
	}
	c.Write([]byte(b.String()))
	leave(t, c)
	settled(t, s, rl, 1, 0)

	const left = 40 - (clientLogBudget - 3)
	by, n := awaitSummedUp(logs, overOwn, left)
	if n != left || by["Ignoring unsupported stratum method"] != left {
		t.Errorf("LOGB-SUMMARY-OWN: a connection's %d lines left out by its own budget were summed up as %d %v", left, n, by)
	}
	gone := logs.FilterMessage("Client disconnected").All()
	if len(gone) != 1 || gone[0].ContextMap()["ip"] != from || gone[0].ContextMap()["suppressed_since_last"] != int64(left) {
		t.Errorf("LOGB-OWN-AT-END: the connection's disconnect line is %v, want it to say %d of its lines were left out", gone, left)
	}
}

// A connection's count of its own lines left out is given with its next line. When the port's
// budget leaves that line out, the count waits for the one after: it is not lost, nor mixed with
// lines other connections caused.
func TestAConnectionsCountWaitsWhenThePortLeavesItsLineOut(t *testing.T) {
	s, _ := perJobServer()
	core, logs := observer.New(zap.InfoLevel)
	s.logger = zap.New(core)
	c := perJobClient(t)
	ping := func(id int) {
		s.handleMessage(c, []byte(fmt.Sprintf(`{"id":%d,"method":"mining.ping","params":[]}`, id)))
	}
	for i := 0; i < clientLogBudget+5; i++ {
		ping(i)
	}
	// A minute on, other connections have used up the port's budget.
	c.logs.mu.Lock()
	c.logs.windowStart = c.logs.windowStart.Add(-time.Minute)
	c.logs.mu.Unlock()
	s.logs.mu.Lock()
	s.logs.windowStart, s.logs.n = time.Now(), serverLogBudget
	s.logs.mu.Unlock()
	ping(100)
	// And a minute after that, it has room again.
	s.logs.mu.Lock()
	s.logs.windowStart = s.logs.windowStart.Add(-time.Minute)
	s.logs.mu.Unlock()
	ping(101)
	lines := logs.FilterMessage("Ignoring unsupported stratum method").All()
	if len(lines) != clientLogBudget+1 {
		t.Fatalf("LOGB-OWN-SETUP: %d lines written, want %d", len(lines), clientLogBudget+1)
	}
	if n := lines[clientLogBudget].ContextMap()["suppressed_since_last"]; n != int64(5) {
		t.Errorf("LOGB-OWN-KEPT: the connection's next line says %v of its lines were left out, want its own 5", n)
	}
}

// Logging in costs a stranger nothing, so the miners' budget is a budget too: past its burst, a
// flood of logins, refused shares and disconnects is held to the rate.
func TestAFloodOfLoginsIsBounded(t *testing.T) {
	s, rl, logs := port3333Server(t)
	addr := rl.Addr().String()
	start := time.Now()
	const logins, each = 150, 5 // a login, 3 refused shares and a disconnect
	for i := 0; i < logins; i++ {
		c, _, err := refusedLogin(addr, s21UA, testPayout+".mrr", each-2)
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
	}
	settled(t, s, rl, logins, 0)
	elapsed := time.Since(start)
	miners := logs.FilterMessage("Miner authorized").Len() + logs.FilterMessage("Job not found").Len() +
		logs.FilterMessage("Client disconnected").Len()
	if miners < minerLogBurst {
		t.Fatalf("LOGB-LOGINS-SETUP: the flood wrote %d lines, short of the burst, %d", miners, minerLogBurst)
	}
	if limit := minerLogBurst + int(elapsed.Minutes()*minerLogRate) + 1; miners > limit {
		t.Errorf("LOGB-LOGINS-BOUND: %d logins with %d refused shares each wrote %d lines in %v, budget %d", logins, each-2, miners, elapsed, limit)
	}
	if subscribed := logs.FilterMessage("Client subscribed").Len(); subscribed > serverLogBudget {
		t.Errorf("LOGB-LOGINS-SUBSCRIBED: %d subscribe lines, budget %d", subscribed, serverLogBudget)
	}
	s.Stop()
	if _, n := summedUp(logs, overMiners); n != int64(logins*each-miners) {
		t.Errorf("LOGB-SUMMARY-MINERS: %d of the miners' lines were left out, summed up as %d", logins*each-miners, n)
	}
}

package main

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/cashaddr"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// unreachablePool is a Forge Pool nothing answers at: the gateway mines solo meanwhile.
const unreachablePool = "http://127.0.0.1:1"

// fakeHash is the hash of the block at height h on the chain the fake node has.
func fakeHash(h int64) string { return fmt.Sprintf("%064x", 0x2000000+h) }

// fakeNode is a BCH2 node's RPC as far as the gateway uses it.
type fakeNode struct {
	srv  *httptest.Server
	quit chan struct{}

	mu       sync.Mutex
	logins   map[string]string // user: password, the logins it takes
	blocks   int64
	headers  int64
	ibd      bool
	slowGBT  map[string]time.Duration // getblocktemplate answers this late for this user
	inGBT    map[string]int           // getblocktemplate calls under way, by user
	calls    map[string]int
	notJSON  bool // answers an HTML page, as something that is not a node would
	hangInfo bool // getblockchaininfo never answers
	forbid   bool // answers 403 to everything, as a node does to an address its rpcallowip leaves out
}

func newFakeNode(t *testing.T, user, pass string) *fakeNode {
	n := &fakeNode{logins: map[string]string{user: pass}, blocks: 100, headers: 100, quit: make(chan struct{}),
		slowGBT: map[string]time.Duration{}, inGBT: map[string]int{}, calls: map[string]int{}}
	n.srv = httptest.NewServer(n)
	t.Cleanup(n.srv.Close)
	t.Cleanup(func() { close(n.quit) }) // first: a slow answer ends at once
	return n
}

func (n *fakeNode) url() string { return n.srv.URL }

func (n *fakeNode) login() (string, string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for u, p := range n.logins {
		return u, p
	}
	return "", ""
}

// setLogin makes the node take this login only, as a node does with a new cookie.
func (n *fakeNode) setLogin(user, pass string) {
	n.set(func(n *fakeNode) { n.logins = map[string]string{user: pass} })
}

func (n *fakeNode) set(f func(n *fakeNode)) {
	n.mu.Lock()
	defer n.mu.Unlock()
	f(n)
}

func (n *fakeNode) get(f func(n *fakeNode) int) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return f(n)
}

func (n *fakeNode) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u, p, _ := r.BasicAuth()
	n.mu.Lock()
	want, known := n.logins[u]
	notJSON, hang, forbid := n.notJSON, n.hangInfo, n.forbid
	n.mu.Unlock()
	// The node checks the address a request comes from before the login (httpserver.cpp).
	if forbid {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if !known || p != want {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if notJSON {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html><body>It works!</body></html>"))
		return
	}
	var req struct {
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	n.mu.Lock()
	n.calls[req.Method]++
	slow := n.slowGBT[u]
	if req.Method == "getblocktemplate" {
		n.inGBT[u]++
	}
	n.mu.Unlock()
	if req.Method == "getblocktemplate" {
		defer n.set(func(n *fakeNode) { n.inGBT[u]-- })
		if slow > 0 {
			select {
			case <-time.After(slow):
			case <-n.quit:
				return
			case <-r.Context().Done():
				return
			}
		}
	}
	if req.Method == "getblockchaininfo" && hang {
		select {
		case <-n.quit:
		case <-r.Context().Done():
		}
		return
	}
	n.mu.Lock()
	blocks, headers, ibd := n.blocks, n.headers, n.ibd
	n.mu.Unlock()
	var result, rpcErr interface{}
	switch req.Method {
	case "getblocktemplate":
		result = map[string]interface{}{"version": 0x20000000, "previousblockhash": fakeHash(blocks), "transactions": []interface{}{},
			"coinbasevalue": int64(50_0000_0000), "bits": "1902c9b9", "height": blocks + 1, "curtime": time.Now().Unix(),
			"target": "0000000000000002c9b900000000000000000000000000000000000000000000"}
	case "getblockchaininfo":
		result = map[string]interface{}{"chain": "main", "blocks": blocks, "headers": headers, "initialblockdownload": ibd}
	case "validateaddress":
		var addr string
		if len(req.Params) > 0 {
			json.Unmarshal(req.Params[0], &addr)
		}
		if a, err := cashaddr.Decode(addr, cashaddr.MainnetPrefix); err == nil {
			result = map[string]interface{}{"isvalid": true, "scriptPubKey": hex.EncodeToString(a.Script())}
		} else {
			result = map[string]interface{}{"isvalid": false}
		}
	case "submitblock":
		result = nil
	case "getblockhash":
		result = fakeHash(blocks)
	default:
		rpcErr = map[string]interface{}{"code": -32601, "message": "Method not found"}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"result": result, "error": rpcErr, "id": "forge-gateway"})
}

// freeAddr is a 127.0.0.1 address with a port the system has just given out and taken back: the
// config takes no port 0.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// nodeConfig is a config for the fake node with a user login, the miners and the status page on
// free 127.0.0.1 ports, and an unreachable pool.
func nodeConfig(t *testing.T, n *fakeNode) string {
	return nodeConfigAt(n, freeAddr(t), freeAddr(t))
}

// nodeConfigAt is nodeConfig with the miners at stratum and the status page at status.
func nodeConfigAt(n *fakeNode, stratum, status string) string {
	user, pass := n.login()
	return `{"node":{"rpc_url":"` + n.url() + `","rpc_user":"` + user + `","rpc_password":"` + pass + `"},` +
		`"mining":{"payout_address":"` + testPayout + `"},"stratum":{"listen":"` + stratum + `"},` +
		`"status":{"listen":"` + status + `"},"pool":{"url":"` + unreachablePool + `"}}`
}

// freshTestConfig is the tray app's fresh config with the miners and the status page on free ports
// and an unreachable pool.
func freshTestConfig(t *testing.T) string {
	s := freshConfig(t)
	s = strings.Replace(s, `"listen": "0.0.0.0:3333"`, `"listen": "`+freeAddr(t)+`"`, 1)
	s = strings.Replace(s, `"listen": "127.0.0.1:3090"`, `"listen": "`+freeAddr(t)+`"`, 1)
	return strings.Replace(s, `"log_level": "info"`, `"pool": {"url": "`+unreachablePool+`"},`+"\n"+`  "log_level": "info"`, 1)
}

// gwConfig is a config for the fake node with every part given: node is the node section's body,
// mining the mining section's.
func gwConfig(t *testing.T, node, mining string) string {
	return `{"node":{` + node + `},"mining":{` + mining + `},"stratum":{"listen":"` + freeAddr(t) + `"},` +
		`"status":{"listen":"` + freeAddr(t) + `"},"pool":{"url":"` + unreachablePool + `"}}`
}

// harness is the gateway running in this process, as run runs it.
type harness struct {
	t       *testing.T
	a       *app
	path    string
	logs    *observer.ObservedLogs
	stop    chan struct{}
	ended   chan struct{}
	err     error
	stopped sync.Once
}

// testPassword is the settings password the tests start the gateway with: 64 hex characters, as
// the Windows tray app makes.
const testPassword = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// startGateway runs the gateway with the config cfg until the test ends, as the console does.
func startGateway(t *testing.T, cfg string) *harness {
	t.Helper()
	return startGatewayPW(t, cfg, "")
}

// startGatewayPW is startGateway with SETTINGS_PASSWORD set to password, as the tray app starts it.
func startGatewayPW(t *testing.T, cfg, password string) *harness {
	t.Helper()
	return startAs(t, "GW-START", cfg, password)
}

// runBriefly is run with the config at path, which is to end at once: an error that ends the
// gateway. It fails with code when the gateway runs instead.
func runBriefly(t *testing.T, code, path string) error {
	t.Helper()
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- run(path, stop, false) }()
	select {
	case err := <-done:
		return err
	case <-time.After(15 * time.Second):
		close(stop)
		<-done
		t.Fatalf("%s: the gateway started and ran", code)
		return nil
	}
}

// startAs is startGatewayPW, failing with code when the gateway does not start.
func startAs(t *testing.T, code, cfg, password string) *harness {
	t.Helper()
	t.Setenv("SETTINGS_PASSWORD", password)
	h := &harness{t: t, path: writeConfig(t, cfg), stop: make(chan struct{}), ended: make(chan struct{})}
	core, logs := observer.New(zap.DebugLevel)
	h.logs = logs
	got := make(chan *app, 1)
	oldStarted, oldWrap := started, wrapLog
	started = func(a *app) { got <- a }
	wrapLog = func(*zap.Logger) *zap.Logger { return zap.New(core) }
	t.Cleanup(func() { started, wrapLog = oldStarted, oldWrap })
	go func() {
		h.err = run(h.path, h.stop, false)
		close(h.ended)
	}()
	select {
	case h.a = <-got:
	case <-h.ended:
		t.Fatalf("%s: the gateway did not start: %v", code, h.err)
	case <-time.After(30 * time.Second):
		t.Fatalf("%s: the gateway did not start within 30s", code)
	}
	t.Cleanup(func() { h.close(60 * time.Second) })
	return h
}

// close stops the gateway and waits up to within for run to return: false if it did not.
func (h *harness) close(within time.Duration) bool {
	h.stopped.Do(func() { close(h.stop) })
	select {
	case <-h.ended:
		return true
	case <-time.After(within):
		return false
	}
}

// setUp waits until the engine in use has a job loop.
func (h *harness) setUp(within time.Duration) *engine {
	h.t.Helper()
	var e *engine
	if !eventually(within, func() bool { e = h.a.eng.Load(); return e != nil && e.loop.Load() != nil }) {
		h.t.Fatalf("the gateway has no job loop after %s", within)
	}
	return e
}

// rewrite replaces the config file with cfg and applies it, as a save does.
func (h *harness) rewrite(cfg string) {
	h.t.Helper()
	if err := writeFile(h.path, cfg); err != nil {
		h.t.Fatal(err)
	}
	h.a.reloadFromFile("a test")
}

func writeFile(path, content string) error { return os.WriteFile(path, []byte(content), 0o600) }

// eventually reports whether cond became true within d.
func eventually(d time.Duration, cond func() bool) bool {
	end := time.Now().Add(d)
	for {
		if cond() {
			return true
		}
		if time.Now().After(end) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// setVar sets *p to v for the rest of the test.
func setVar[T any](t *testing.T, p *T, v T) {
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

// miner is a stratum client.
type miner struct {
	t      *testing.T
	conn   net.Conn
	msgs   chan stratumMsg
	closed chan struct{}
	id     int
}

type stratumMsg struct {
	ID     json.RawMessage   `json:"id"`
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
	Result json.RawMessage   `json:"result"`
}

// notify is a mining.notify.
type notify struct {
	job, coinb1, coinb2 string
	clean               bool
}

func dialMiner(t *testing.T, addr string) *miner {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	m := &miner{t: t, conn: c, msgs: make(chan stratumMsg, 1000), closed: make(chan struct{})}
	t.Cleanup(func() { c.Close() })
	go func() {
		defer close(m.closed)
		sc := bufio.NewScanner(c)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var msg stratumMsg
			if json.Unmarshal(sc.Bytes(), &msg) == nil {
				select {
				case m.msgs <- msg:
				default:
				}
			}
		}
	}()
	return m
}

func (m *miner) send(method string, params ...interface{}) {
	m.id++
	b, _ := json.Marshal(map[string]interface{}{"id": m.id, "method": method, "params": params})
	m.conn.Write(append(b, '\n'))
}

// login subscribes and authorizes as user.
func (m *miner) login(user string) {
	m.send("mining.subscribe", "gateway-test/1.0")
	m.send("mining.authorize", user, "x")
}

// next waits for a mining.notify that ok accepts, reading past the others.
func (m *miner) next(within time.Duration, ok func(notify) bool) (notify, bool) {
	end := time.After(within)
	for {
		select {
		case msg := <-m.msgs:
			if msg.Method != "mining.notify" || len(msg.Params) < 9 {
				continue
			}
			var n notify
			json.Unmarshal(msg.Params[0], &n.job)
			json.Unmarshal(msg.Params[2], &n.coinb1)
			json.Unmarshal(msg.Params[3], &n.coinb2)
			json.Unmarshal(msg.Params[8], &n.clean)
			if ok == nil || ok(n) {
				return n, true
			}
		case <-end:
			return notify{}, false
		}
	}
}

// tagged reports whether a job's coinbase carries tag.
func tagged(tag string) func(notify) bool {
	h := hex.EncodeToString([]byte(tag))
	return func(n notify) bool { return strings.Contains(n.coinb1+n.coinb2, h) }
}

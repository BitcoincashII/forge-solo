package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/tidesgw"
	"go.uber.org/zap"
)

// Every row of the state table, and the first that applies wins where two do.
func TestStateTable(t *testing.T) {
	const url = "http://127.0.0.1:8342"
	synced := &healthResult{ci: &chainInfo{Chain: "main", Blocks: 100, Headers: 100}}
	refused := &healthResult{err: errUnauthorized, unauthorized: true}
	forbidden := &healthResult{err: errForbidden, forbidden: true}
	const forbiddenWhy = "Your node refuses this computer (HTTP 403): it answers RPC only from the addresses in the rpcallowip lines of its config file. " +
		"For a node on another computer, add rpcallowip=<this computer's address> and rpcbind=<the node's address> to that file and restart the node."
	down := &healthResult{err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}}
	missing := &fs.PathError{Op: "open", Path: "/n/.cookie", Err: fs.ErrNotExist}
	behind := tidesgw.Status{NodeBehind: true, Reason: "this BCH2 node is catching up with the chain: it is at block 99, Forge Pool at 101"}
	unreachable := tidesgw.Status{State: tidesgw.StateFallback, Reason: "the pool did not answer"}
	setUp := func(f func(in *stateInput)) stateInput {
		in := stateInput{engine: true, rpcURL: url, health: synced, mode: "tides"}
		f(&in)
		return in
	}
	cases := []struct {
		name, state, reason string
		in                  stateInput
	}{
		{"1 not set up", stateUnconfigured, "Set your node and payout address in Settings.",
			stateInput{problem: "Set your node and payout address in Settings."}},
		{"1 problem over login", stateUnconfigured, "Set your payout address in Settings.",
			setUp(func(in *stateInput) { in.problem, in.loginErr = "Set your payout address in Settings.", missing })},
		{"2 no engine", stateStarting, "Forge Gateway is starting.", stateInput{}},
		{"3 cookie missing", stateNodeLogin, "Forge Gateway cannot read the node's cookie file /n/.cookie: it does not exist. Check that the node is running and that the cookie file in Settings is the node's.",
			setUp(func(in *stateInput) {
				in.cookie, in.cookiePath, in.loginErr, in.health = true, "/n/.cookie", missing, nil
			})},
		{"3 not a cookie", stateNodeLogin, "Forge Gateway cannot read the node's cookie file /n/.cookie: it does not hold one line, user:password. Check that the node is running and that the cookie file in Settings is the node's.",
			setUp(func(in *stateInput) {
				in.cookie, in.cookiePath, in.loginErr, in.health = true, "/n/.cookie", &notCookieError{"/n/.cookie"}, nil
			})},
		{"3 login over unreachable", stateNodeLogin, "",
			setUp(func(in *stateInput) { in.cookie, in.loginErr, in.health = true, missing, down })},
		{"4 user refused", stateNodeLogin, "Your node refused the RPC login. Check the RPC user and password in Settings: they must be the rpcuser and rpcpassword in the node's config file.",
			setUp(func(in *stateInput) { in.health = refused })},
		{"4b cookie refused", stateNodeLogin, "Your node refused the login from its cookie file. Check that the cookie file in Settings is the running node's.",
			setUp(func(in *stateInput) { in.cookie, in.health = true, refused })},
		{"4 node over pool", stateNodeLogin, "",
			setUp(func(in *stateInput) { in.health, in.mode, in.pool = refused, "solo", unreachable })},
		{"4c refuses this computer", stateNodeForbidden, forbiddenWhy, setUp(func(in *stateInput) { in.health = forbidden })},
		{"4c refuses this computer, cookie login", stateNodeForbidden, forbiddenWhy,
			setUp(func(in *stateInput) { in.cookie, in.health = true, forbidden })},
		{"4c refuses an address by name", stateNodeForbidden, forbiddenWhy +
			" A node with no rpcallowip line also refuses an address given by name, such as localhost: for a node on this computer, enter http://127.0.0.1:8342 in Settings.",
			setUp(func(in *stateInput) { in.health, in.rpcURL = forbidden, "http://localhost:8342" })},
		{"4c node over pool", stateNodeForbidden, "",
			setUp(func(in *stateInput) { in.health, in.mode, in.pool = forbidden, "solo", unreachable })},
		{"5 unreachable", stateNodeUnreachable, "Forge Gateway cannot reach your node at " + url + ": nothing answers at that address. Check that the node is running with server=1 and that the RPC address in Settings is right.",
			setUp(func(in *stateInput) { in.health = down })},
		{"5 unreachable over syncing", stateNodeUnreachable, "",
			setUp(func(in *stateInput) { in.health, in.pool = down, behind })},
		{"6 syncing", stateNodeSyncing, "Your node is still syncing: block 50 of 100. Mining starts when it is done.",
			setUp(func(in *stateInput) {
				in.health = &healthResult{ci: &chainInfo{Chain: "main", Blocks: 50, Headers: 100, InitialBlockDownload: true}}
			})},
		{"7 behind the pool", stateNodeSyncing, "This BCH2 node is catching up with the chain: it is at block 99, Forge Pool at 101. Your miners mine solo until your node is on Forge Pool's block.",
			setUp(func(in *stateInput) { in.pool, in.mode = behind, "solo" })},
		{"7 behind the pool, pool only", stateNodeSyncing, "This BCH2 node is catching up with the chain: it is at block 99, Forge Pool at 101. Miners are turned away until your node is on Forge Pool's block.",
			setUp(func(in *stateInput) { in.pool, in.mode, in.poolOnly = behind, "waiting", true })},
		{"8 solo", statePoolUnreachable, "Forge Pool cannot be reached (the pool did not answer). Your miners mine solo on your node meanwhile: a block found now pays your payout address in full. Forge Gateway tries the pool again every minute.",
			setUp(func(in *stateInput) { in.pool, in.mode = unreachable, "solo" })},
		{"8 solo, no reason", statePoolUnreachable, "Forge Pool cannot be reached. Your miners mine solo on your node meanwhile: a block found now pays your payout address in full. Forge Gateway tries the pool again every minute.",
			setUp(func(in *stateInput) { in.mode = "solo" })},
		{"9 waiting", statePoolUnreachable, "Forge Pool cannot be reached (the pool did not answer). Pool only is on, so miners are turned away until it is back, and fail over to their backup pool.",
			setUp(func(in *stateInput) { in.pool, in.mode, in.poolOnly = unreachable, "waiting", true })},
		{"10 starting", stateStarting, "Waiting for the first block template from your node and the first job Forge Pool registers.",
			setUp(func(in *stateInput) { in.mode = "starting" })},
		{"10 before the first check", stateStarting, "Waiting for the first block template from your node and the first job Forge Pool registers.",
			setUp(func(in *stateInput) { in.mode, in.health = "starting", nil })},
		{"11 active", stateActive, "Mining into Forge Pool's TIDES window.", setUp(func(*stateInput) {})},
		{"12 otherwise", stateStarting, "Waiting for the first block template from your node and the first job Forge Pool registers.",
			setUp(func(in *stateInput) { in.mode = "off" })},
	}
	for _, k := range cases {
		state, reason := stateOf(k.in)
		if state != k.state || (k.reason != "" && reason != k.reason) {
			t.Errorf("GW-STATE-TABLE %s: %s %q\nwant %s %q", k.name, state, reason, k.state, k.reason)
		}
	}
}

// What the status page says of a node check that failed: the kind of failure, in a few words,
// the same on Windows and Linux.
func TestBriefNodeError(t *testing.T) {
	check := func(url string, timeout time.Duration) string {
		n := &node{url: url, user: "u", pass: "p", http: &http.Client{Timeout: timeout, Transport: &http.Transport{}}}
		_, err := n.chainInfo()
		if err == nil {
			t.Fatalf("GW-NODE-BRIEF: %s answered", url)
		}
		return briefNodeError(err)
	}
	if got := check("http://"+freeAddr(t), 5*time.Second); got != "nothing answers at that address" {
		t.Errorf("GW-NODE-BRIEF-CLOSED: %q", got)
	}
	silent := holdingListener(t)
	if got := check("http://"+silent, 100*time.Millisecond); got != "it did not answer in time" {
		t.Errorf("GW-NODE-BRIEF-TIMEOUT: %q", got)
	}
	if got := check("http://forge-gateway-test.invalid:8342", 10*time.Second); got != "the name forge-gateway-test.invalid does not resolve" {
		t.Errorf("GW-NODE-BRIEF-DNS: %q", got)
	}
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html><body>It works!</body></html>"))
	}))
	defer web.Close()
	if got := check(web.URL, 5*time.Second); got != "what answers there is not a BCH2 node" {
		t.Errorf("GW-NODE-BRIEF-NOT-A-NODE: %q", got)
	}
	long := briefNodeError(errors.New(strings.Repeat("x", 150) + "\n  " + strings.Repeat("y", 150)))
	if want := strings.Repeat("x", 150) + " " + strings.Repeat("y", 49) + "…"; long != want {
		t.Errorf("GW-NODE-BRIEF-LONG: %q", long)
	}
}

// A node answering 401 refuses the login; one answering 403 refuses this computer, whatever the
// login. The node card says which, in words with no key of the config file in them.
func TestNodeRefusals(t *testing.T) {
	for _, tc := range []struct {
		code   int
		want   error
		user   string // the node card with a user login
		cookie string // with a cookie login
	}{
		{http.StatusUnauthorized, errUnauthorized, "it refused the login: check the RPC user and password in Settings", "it refused the login from its cookie file"},
		{http.StatusForbidden, errForbidden, "it refuses this computer (HTTP 403)", "it refuses this computer (HTTP 403)"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.code) }))
		_, err := newNode(srv.URL, "u", "p").chainInfo()
		srv.Close()
		if !errors.Is(err, tc.want) {
			t.Errorf("GW-NODE-%d: the node's %d is %v, want %v", tc.code, tc.code, err, tc.want)
		}
		if got := nodeErrorShown(err, false); got != tc.user {
			t.Errorf("GW-NODE-ERROR-ROW: %d with a user login: %q", tc.code, got)
		}
		if got := nodeErrorShown(err, true); got != tc.cookie {
			t.Errorf("GW-NODE-ERROR-ROW: %d with a cookie login: %q", tc.code, got)
		}
	}
	if got := nodeErrorShown(&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")}, false); got != "nothing answers at that address" {
		t.Errorf("GW-NODE-ERROR-ROW: a closed port: %q", got)
	}
}

// holdingListener takes connections on 127.0.0.1 and never answers them, as a hung node does.
func holdingListener(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var held []net.Conn
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			held = append(held, c)
		}
	}()
	t.Cleanup(func() {
		l.Close()
		<-done
		for _, c := range held {
			c.Close()
		}
	})
	return l.Addr().String()
}

// A node that takes the connection and never answers shows as unreachable within one check, not
// after the 30 s a block submit may wait, and the change is logged.
func TestAHungNodeShowsSoon(t *testing.T) {
	setVar(t, &nodeCheckEvery, 50*time.Millisecond)
	setVar(t, &nodeCheckTimeout, 200*time.Millisecond)
	setVar(t, &stateLogEvery, 20*time.Millisecond)
	n := newFakeNode(t, "u", "p")
	h := startGateway(t, nodeConfig(t, n))
	h.setUp(10 * time.Second)
	n.set(func(n *fakeNode) { n.hangInfo = true })
	var v statusView
	ok := eventually(time.Second, func() bool {
		v = h.a.view(time.Now())
		return v.State == stateNodeUnreachable
	})
	if !ok || !strings.Contains(v.StateReason, ": it did not answer in time.") || v.Node.Error != "it did not answer in time" {
		t.Fatalf("GW-HEALTH-TIMEOUT: within 1s the state is %s %q, node error %q", v.State, v.StateReason, v.Node.Error)
	}
	if !eventually(time.Second, func() bool {
		return h.logs.FilterLevelExact(zap.WarnLevel).FilterMessageSnippet("state: node_unreachable: Forge Gateway cannot reach your node at").Len() == 1
	}) {
		t.Fatal("GW-STATE-LOG: the change of state was not logged once, at Warn")
	}
}

// /api/status gives the state, why, whether the gateway is set up, and what the node check saw.
func TestStatusFields(t *testing.T) {
	n := newFakeNode(t, "u", "p")
	h := startGateway(t, nodeConfig(t, n))
	h.setUp(10 * time.Second)
	if !eventually(5*time.Second, func() bool { e := h.a.eng.Load(); return e.health.good.Load() != nil }) {
		t.Fatal("GW-STATUS-FIELDS: no node check worked")
	}
	r, err := http.Get("http://" + h.a.statusAddr + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var got map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"configured", "state", "state_reason", "mode", "payout_address", "pool", "stratum", "workers", "blocks"} {
		if _, ok := got[k]; !ok {
			t.Errorf("GW-STATUS-FIELDS: no %q in %v", k, got)
		}
	}
	if got["configured"] != true || got["state"] == "" || got["state_reason"] == "" {
		t.Errorf("GW-STATUS-FIELDS: configured %v, state %v, reason %v", got["configured"], got["state"], got["state_reason"])
	}
	node, _ := got["node"].(map[string]interface{})
	for k, want := range map[string]interface{}{"rpc_url": n.url(), "chain": "main", "blocks": 100.0, "headers": 100.0, "syncing": false, "error": ""} {
		if node[k] != want {
			t.Errorf("GW-STATUS-NODE: node.%s = %v, want %v", k, node[k], want)
		}
	}
	for _, k := range []string{"template_height", "template_age_seconds", "network_difficulty"} {
		if _, ok := node[k]; !ok {
			t.Errorf("GW-STATUS-NODE: no node.%s", k)
		}
	}
}

// The page's header shows the state the API gives, and why; no em-dash stands between the pool's
// state and its reason.
func TestStatusPageShowsTheState(t *testing.T) {
	page, err := os.ReadFile("status.html")
	if err != nil {
		t.Fatal(err)
	}
	s := string(page)
	for _, want := range []string{
		`{unconfigured:"Not set up", node_unreachable:"Node unreachable", node_login:"Node login failed", node_forbidden:"Node refuses this computer", node_syncing:"Node syncing", pool_unreachable:(s.mode==="waiting"?"Waiting for pool":"Solo fallback"), starting:"Starting", active:"TIDES"}[s.state]`,
		`$("why").textContent = s.state_reason`,
		`.badge.bad{color:var(--bad)}`,
		"esc(p.state)+(p.reason?`: <span class=\"err\">${esc(p.reason)}</span>`:\"\")",
		`["Chain",`, `["Blocks",`, `n.error||n.template_error`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("GW-PAGE-STATE: status.html has no %s", want)
		}
	}
	if strings.Contains(s, "WHY[") || strings.Contains(s, "— ${esc(p.reason)}") {
		t.Error("GW-PAGE-STATE: the page still says why from the mode, or puts an em-dash before the pool's reason")
	}
}

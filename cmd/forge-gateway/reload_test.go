package main

import (
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// A node started again writes a new cookie and refuses the old one. 1.0.0 then had to be
// restarted by hand; the gateway now reads the new cookie by itself and goes on.
func TestANewCookieIsFollowed(t *testing.T) {
	setVar(t, &nodeCheckEvery, 50*time.Millisecond)
	n := newFakeNode(t, "__cookie__", "first")
	cookie := filepath.Join(t.TempDir(), ".cookie")
	if err := writeFile(cookie, "__cookie__:first\n"); err != nil {
		t.Fatal(err)
	}
	h := startGateway(t, gwConfig(t, `"rpc_url":"`+n.url()+`","rpc_cookie_file":"`+filepath.ToSlash(cookie)+`"`,
		`"payout_address":"`+testPayout+`"`))
	if e := h.setUp(10 * time.Second); e.node.pass != "first" {
		t.Fatalf("GW-COOKIE-RELOAD: the gateway logged in with %q", e.node.pass)
	}
	if err := writeFile(cookie, "__cookie__:second\n"); err != nil {
		t.Fatal(err)
	}
	n.setLogin("__cookie__", "second")
	ok := eventually(2*time.Second, func() bool {
		e := h.a.eng.Load()
		if e == nil || e.node == nil || e.node.pass != "second" || e.loop.Load() == nil {
			return false
		}
		r := e.health.last.Load()
		return r != nil && r.err == nil
	})
	if !ok {
		t.Fatalf("GW-COOKIE-RELOAD: the gateway did not take the node's new cookie within 2s (checks every %s)", nodeCheckEvery)
	}
	if h.logs.FilterMessage("the node's cookie changed (the node restarted): using the new one").Len() == 0 {
		t.Fatal("GW-COOKIE-RELOAD-LOG: the new cookie was not logged")
	}
}

// When settings are applied again, the job loop before stops handing out work, even a job it was
// building when it was told: a miner must never get work from settings that were replaced. Here
// the old loop waits on a slow node when it is told, past applyWait, and gets a new block after.
func TestTheOldLoopHandsOutNothing(t *testing.T) {
	setVar(t, &applyWait, 100*time.Millisecond)
	n := newFakeNode(t, "old", "p1")
	n.set(func(n *fakeNode) { n.logins["new"] = "p2" })
	h := startGateway(t, gwConfig(t, `"rpc_url":"`+n.url()+`","rpc_user":"old","rpc_password":"p1"`,
		`"payout_address":"`+testPayout+`","coinbase_tag":"OLD-TAG-1"`))
	h.setUp(10 * time.Second)
	m := dialMiner(t, h.a.srv.ListenAddr())
	m.login("rig1")
	if _, ok := m.next(10*time.Second, tagged("OLD-TAG-1")); !ok {
		t.Fatal("GW-RELOAD-STOPS-OLD: no first job")
	}
	n.set(func(n *fakeNode) { n.slowGBT["old"] = 1500 * time.Millisecond })
	if !eventually(5*time.Second, func() bool { return n.get(func(n *fakeNode) int { return n.inGBT["old"] }) > 0 }) {
		t.Fatal("GW-RELOAD-STOPS-OLD: the old loop never asked the slow node")
	}
	n.set(func(n *fakeNode) { n.blocks++ })
	h.rewrite(gwConfigAt(h, `"rpc_url":"`+n.url()+`","rpc_user":"new","rpc_password":"p2"`, `"payout_address":"`+testPayout+`","coinbase_tag":"NEW-TAG-1"`))
	if _, ok := m.next(5*time.Second, tagged("NEW-TAG-1")); !ok {
		t.Fatal("GW-RELOAD-STOPS-OLD: no job from the new settings")
	}
	// The old loop's slow call ends 1.5 s after it began, with a new block to make a job for.
	if j, ok := m.next(3*time.Second, tagged("OLD-TAG-1")); ok {
		t.Fatalf("GW-RELOAD-STOPS-OLD: the replaced loop handed out job %s after the new settings' first", j.job)
	}
}

// Each engine has a job manager of its own, whose IDs start at 1. The stratum and the pool gateway
// know jobs by ID, and the stratum drops the lowest first: the new settings' jobs must follow the
// old ones, never repeat their IDs.
func TestJobIDsGoOnAcrossSettings(t *testing.T) {
	n := newFakeNode(t, "u", "p")
	h := startGateway(t, gwConfig(t, `"rpc_url":"`+n.url()+`","rpc_user":"u","rpc_password":"p"`,
		`"payout_address":"`+testPayout+`","coinbase_tag":"FIRST-1"`))
	h.setUp(10 * time.Second)
	m := dialMiner(t, h.a.srv.ListenAddr())
	m.login("rig1")
	var highest uint64
	for i := 0; i < 2; i++ {
		j, ok := m.next(10*time.Second, tagged("FIRST-1"))
		if !ok {
			break
		}
		id, err := strconv.ParseUint(j.job, 16, 64)
		if err != nil {
			t.Fatalf("GW-RELOAD-JOBIDS: job ID %q is not hex: the stratum orders jobs by it", j.job)
		}
		highest = max(highest, id)
		n.set(func(n *fakeNode) { n.blocks++ })
	}
	if highest == 0 {
		t.Fatal("GW-RELOAD-JOBIDS: no first job")
	}
	h.rewrite(gwConfigAt(h, `"rpc_url":"`+n.url()+`","rpc_user":"u","rpc_password":"p"`, `"payout_address":"`+testPayout+`","coinbase_tag":"SECOND-2"`))
	j, ok := m.next(10*time.Second, tagged("SECOND-2"))
	if !ok {
		t.Fatal("GW-RELOAD-JOBIDS: no job from the new settings")
	}
	if id, err := strconv.ParseUint(j.job, 16, 64); err != nil || id <= highest {
		t.Fatalf("GW-RELOAD-JOBIDS: the new settings' first job is %q, after job %x of the old ones", j.job, highest)
	}
}

// pool_only is applied like the rest: the pool gateway says, at its next fall back, that miners
// are turned away rather than mining solo.
func TestPoolOnlyIsApplied(t *testing.T) {
	n := newFakeNode(t, "u", "p")
	h := startGateway(t, nodeConfig(t, n))
	h.setUp(10 * time.Second)
	if !eventually(10*time.Second, func() bool { return h.logs.FilterMessageSnippet("mining SOLO until it is back").Len() > 0 }) {
		t.Fatal("GW-RELOAD-POOLONLY: the gateway did not fall back to solo first")
	}
	h.rewrite(gwConfigAt(h, `"rpc_url":"`+n.url()+`","rpc_user":"u","rpc_password":"p"`, `"payout_address":"`+testPayout+`","pool_only":true`))
	if !eventually(10*time.Second, func() bool {
		return h.logs.FilterMessageSnippet("miners are turned away until it is back (pool_only)").Len() > 0
	}) {
		t.Fatal("GW-RELOAD-POOLONLY: after pool_only was turned on, the pool gateway did not say miners are turned away")
	}
}

// A stop while an apply waits for a job loop that will not end at once is not held up by it.
func TestAStopDoesNotWaitForAHungApply(t *testing.T) {
	setVar(t, &applyWait, 20*time.Second)
	setVar(t, &statusStopWait, 2*time.Second)
	setVar(t, &engineStopWait, time.Second)
	n := newFakeNode(t, "old", "p1")
	n.set(func(n *fakeNode) { n.logins["new"] = "p2" })
	h := startGateway(t, gwConfig(t, `"rpc_url":"`+n.url()+`","rpc_user":"old","rpc_password":"p1"`, `"payout_address":"`+testPayout+`"`))
	h.setUp(10 * time.Second)
	n.set(func(n *fakeNode) { n.slowGBT["old"] = 9 * time.Second })
	if !eventually(5*time.Second, func() bool { return n.get(func(n *fakeNode) int { return n.inGBT["old"] }) > 0 }) {
		t.Fatal("GW-SHUTDOWN-APPLY: the old loop never asked the slow node")
	}
	h.rewrite(gwConfigAt(h, `"rpc_url":"`+n.url()+`","rpc_user":"new","rpc_password":"p2"`, `"payout_address":"`+testPayout+`"`))
	time.Sleep(200 * time.Millisecond) // the apply is waiting for the old loop now
	began := time.Now()
	if !h.close(statusStopWait + time.Second) {
		t.Fatalf("GW-SHUTDOWN-APPLY: the gateway took longer than %s to stop while an apply waited for a hung job loop", statusStopWait+time.Second)
	}
	t.Logf("stopped in %s", time.Since(began))
}

// gwConfigAt is gwConfig on h's own listen addresses.
func gwConfigAt(h *harness, node, mining string) string {
	return `{"node":{` + node + `},"mining":{` + mining + `},"stratum":{"listen":"` + h.a.start.Stratum.Listen + `"},` +
		`"status":{"listen":"` + h.a.start.Status.Listen + `"},"pool":{"url":"` + unreachablePool + `"}}`
}

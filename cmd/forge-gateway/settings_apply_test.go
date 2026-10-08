package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// A save takes effect in the running gateway: the same stratum server, the same listener, the
// same miner connection, which gets a clean job built from the new settings within seconds.
func TestASaveTakesEffectWithoutARestart(t *testing.T) {
	n := newFakeNode(t, "u", "p")
	h := startGatewayPW(t, gwConfig(t, `"rpc_url":"`+n.url()+`","rpc_user":"u","rpc_password":"p"`, `"payout_address":"`+testPayout+`","coinbase_tag":"OLD-TAG-2"`), testPassword)
	h.setUp(10 * time.Second)
	srv, listen := h.a.srv, h.a.srv.ListenAddr()
	m := dialMiner(t, listen)
	m.login("rig1")
	if _, ok := m.next(10*time.Second, tagged("OLD-TAG-2")); !ok {
		t.Fatal("GW-RELOAD: no first job")
	}
	code, v := h.save(form(`"rpc_url":"`+n.url()+`","rpc_user":"u","rpc_password":"p"`, `"payout_address":"`+testPayout+`","coinbase_tag":"NEW-TAG-2"`))
	if code != 200 || v["message"] != "Saved. Forge Gateway now works with these settings: your miners get new work from them within seconds." {
		t.Fatalf("GW-RELOAD: HTTP %d %v", code, v)
	}
	j, ok := m.next(5*time.Second, tagged("NEW-TAG-2"))
	if !ok || !j.clean {
		t.Fatalf("GW-RELOAD: within 5s the miner got no clean job with the new tag (%+v)", j)
	}
	select {
	case <-m.closed:
		t.Fatal("GW-RELOAD: the miner's connection was closed")
	default:
	}
	if h.a.srv != srv || h.a.srv.ListenAddr() != listen {
		t.Fatal("GW-RELOAD: the stratum server was replaced")
	}
	select {
	case <-h.ended:
		t.Fatalf("GW-RELOAD: the gateway stopped: %v", h.err)
	default:
	}
}

// A new payout address closes the miners' connections once: a miner that logged in with a worker
// name is credited to the payout address of its login, and only a new login moves it to the new
// one. One that logged in with its own address stays credited to it.
func TestANewPayoutAddressReconnectsTheMiners(t *testing.T) {
	n := newFakeNode(t, "u", "p")
	h := startGatewayPW(t, gwConfig(t, `"rpc_url":"`+n.url()+`","rpc_user":"u","rpc_password":"p"`, `"payout_address":"`+testPayout+`"`), testPassword)
	h.setUp(10 * time.Second)
	listen := h.a.srv.ListenAddr()
	rig := dialMiner(t, listen)
	rig.login("rig1")
	own := dialMiner(t, listen)
	own.login(testMinerAddr + ".own")
	for _, m := range []*miner{rig, own} {
		if _, ok := m.next(10*time.Second, nil); !ok {
			t.Fatal("GW-RELOAD-PAYOUT: no first job")
		}
	}
	code, v := h.save(form(`"rpc_url":"`+n.url()+`","rpc_user":"u","rpc_password":"p"`, `"payout_address":"`+testPayout2+`"`))
	want := "Saved. Forge Gateway now works with these settings. The payout address changed, so your miners reconnect once: those that log in with a worker name are then credited to the new address."
	if code != 200 || v["message"] != want {
		t.Fatalf("GW-RELOAD-PAYOUT: HTTP %d %v", code, v)
	}
	for name, m := range map[string]*miner{"rig1": rig, "own address": own} {
		select {
		case <-m.closed:
		case <-time.After(5 * time.Second):
			workers := h.a.srv.AuthorizedWorkers()
			t.Fatalf("GW-RELOAD-PAYOUT: the %s miner was not reconnected within 5s; credited now: %+v", name, workers)
		}
	}
	said := "the payout address changed: 2 miners reconnect, so that those logged in with a worker name are credited to " + testPayout2
	if !eventually(2*time.Second, func() bool { return h.logs.FilterMessageSnippet(said).Len() == 1 }) {
		t.Fatalf("GW-RELOAD-PAYOUT: the reconnect was not logged; the log says %v", h.logs.FilterMessageSnippet("payout address changed").All())
	}
	if h.a.srv.ListenAddr() != listen {
		t.Fatal("GW-RELOAD-PAYOUT: the listener changed")
	}
	rig2 := dialMiner(t, listen)
	rig2.login("rig1")
	own2 := dialMiner(t, listen)
	own2.login(testMinerAddr + ".own")
	if _, ok := rig2.next(10*time.Second, paysTo(testPayout2)); !ok {
		t.Fatal("GW-RELOAD-PAYOUT: the solo work does not pay the new payout address")
	}
	credited := map[string]string{}
	eventually(5*time.Second, func() bool {
		for _, w := range h.a.srv.AuthorizedWorkers() {
			credited[w.WorkerName] = w.MinerID
		}
		return len(credited) >= 2
	})
	if credited["rig1"] != testPayout2 || credited["own"] != testMinerAddr {
		t.Fatalf("GW-RELOAD-PAYOUT: credited %v; want rig1 to %s and own to %s", credited, testPayout2, testMinerAddr)
	}
}

// A node check that found a new cookie applies the settings in the file, never those its engine
// held: a save made a moment before must not be replaced by the settings from before it.
func TestACookieCheckDoesNotUndoASave(t *testing.T) {
	setVar(t, &nodeCheckEvery, 50*time.Millisecond)
	n := newFakeNode(t, "__cookie__", "first")
	cookie := filepath.Join(t.TempDir(), ".cookie")
	if err := writeFile(cookie, "__cookie__:first\n"); err != nil {
		t.Fatal(err)
	}
	h := startGatewayPW(t, gwConfig(t, `"rpc_url":"`+n.url()+`","rpc_cookie_file":"`+filepath.ToSlash(cookie)+`"`, `"payout_address":"`+testPayout+`"`), testPassword)
	h.setUp(10 * time.Second)
	entered, release := make(chan struct{}), make(chan struct{})
	var once, freed sync.Once
	free := func() { freed.Do(func() { close(release) }) }
	t.Cleanup(free)
	setVar(t, &beforeReapply, func() {
		once.Do(func() { close(entered) })
		<-release
	})
	// The node restarts with a new cookie, and takes a user login too.
	if err := writeFile(cookie, "__cookie__:second\n"); err != nil {
		t.Fatal(err)
	}
	n.set(func(n *fakeNode) { n.logins = map[string]string{"__cookie__": "second", "u": "p"} })
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("GW-RELOAD-STALE: the check did not find the new cookie")
	}
	code, v := h.save(form(`"rpc_url":"`+n.url()+`","rpc_user":"u","rpc_password":"p"`, `"payout_address":"`+testPayout+`"`))
	if code != 200 {
		t.Fatalf("GW-RELOAD-STALE: HTTP %d %v", code, v)
	}
	free()
	userLogin := func() bool {
		e := h.a.eng.Load()
		h.a.pendMu.Lock()
		pending := h.a.pend != nil
		h.a.pendMu.Unlock()
		return !pending && e != nil && e.node != nil && e.node.user == "u" && e.loop.Load() != nil
	}
	if !eventually(2*time.Second, userLogin) {
		e := h.a.eng.Load()
		t.Fatalf("GW-RELOAD-STALE: within 2s the engine in use logs in as %q, not with the saved user login", e.node.user)
	}
	time.Sleep(300 * time.Millisecond)
	if !userLogin() {
		t.Fatalf("GW-RELOAD-STALE: the cookie login was applied again after the save (engine user %q)", h.a.eng.Load().node.user)
	}
	if node, _ := h.fileConfig(); node.RPCUser != "u" {
		t.Fatalf("GW-RELOAD-STALE: the file has %+v", node)
	}
}

// At the stop, a save under way is still written and answered; one that comes after the stop
// began finds the port closed, so "nothing was saved" on the page is true.
func TestAStopAnswersTheSaveUnderWay(t *testing.T) {
	setVar(t, &statusStopWait, 5*time.Second)
	n := newFakeNode(t, "u", "p")
	h := startGatewayPW(t, freshTestConfig(t), testPassword)
	inRename, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	setVar(t, &renameFile, func(from, to string) error {
		once.Do(func() { close(inRename) })
		<-release
		return os.Rename(from, to)
	})
	type result struct {
		code int
		v    map[string]interface{}
		err  error
	}
	first := make(chan result, 1)
	good := form(`"rpc_url":"`+n.url()+`","rpc_user":"u","rpc_password":"p"`, `"payout_address":"`+testPayout+`"`)
	go func() {
		code, v, _, err := h.try("POST", "/api/settings", good, map[string]string{"Content-Type": "application/json", "X-Forge-Password": testPassword})
		first <- result{code, v, err}
	}()
	select {
	case <-inRename:
	case <-time.After(10 * time.Second):
		t.Fatal("GW-SHUTDOWN-SAVE: the save never reached the rename")
	}
	stopped := make(chan bool, 1)
	go func() { stopped <- h.close(30 * time.Second) }()
	time.Sleep(300 * time.Millisecond) // the stop has begun: the status page no longer listens
	_, _, _, lateErr := h.try("POST", "/api/settings", form(`"rpc_user":"late","rpc_password":"p"`, `"payout_address":"`+testPayout+`"`),
		map[string]string{"Content-Type": "application/json", "X-Forge-Password": testPassword})
	close(release)
	r := <-first
	if r.err != nil || r.code != 200 {
		t.Fatalf("GW-SHUTDOWN-SAVE: the save under way at the stop got %d %v %v", r.code, r.v, r.err)
	}
	if node, _ := h.fileConfig(); node.RPCUser != "u" {
		t.Fatalf("GW-SHUTDOWN-SAVE: the save under way is not in the file: %+v", node)
	}
	if lateErr == nil {
		t.Fatal("GW-SHUTDOWN-SAVE: a save sent after the stop began was answered")
	}
	if !<-stopped {
		t.Fatal("GW-SHUTDOWN-SAVE: the gateway did not stop")
	}
	if strings.Contains(h.fileNow(), `"late"`) {
		t.Fatal("GW-SHUTDOWN-SAVE: the save sent after the stop began changed the file")
	}
}

// The status page has every element the tray app's test drives and the texts of the Settings card.
func TestStatusPageSettings(t *testing.T) {
	raw, err := os.ReadFile("status.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(raw)
	for _, id := range []string{"rpcUrl", "loginUser", "loginCookie", "rpcUser", "rpcPass", "cookieFile", "payout", "cbTag", "poolOnly",
		"gwPw", "saveBtn", "saveStatus", "settingsReadOnly", "setupBanner", "settings"} {
		if !strings.Contains(page, `id="`+id+`"`) {
			t.Errorf("GW-PAGE: no element %s", id)
		}
	}
	card := between(page, `<section id="settings">`, `</section>`)
	if card == "" {
		t.Fatal("GW-PAGE: no Settings card")
	}
	if strings.ContainsRune(card, '\u2014') {
		t.Error("GW-PAGE: an em-dash in the Settings card")
	}
	text := regexp.MustCompile(`<[^>]+>`).ReplaceAllString(card, "")
	for _, want := range []string{
		"Your node's RPC address", "Log in with the RPC user and password", "Log in with the node's cookie file", "RPC user", "RPC password",
		"Cookie file", "BCH2 payout address (required)", "Coinbase tag (optional, up to 24 characters)", "Pool only",
		"Forge Gateway's settings password (needed to save)", "Save settings",
		"Forge Gateway builds every block from your own BCH2 node. In the node's config file set server=1 and rpcuser and rpcpassword (or use its .cookie file), then restart the node. A node on this computer answers at http://127.0.0.1:8342.",
		"The node writes a new cookie each time it starts; Forge Gateway reads the new one by itself.",
		"Turn miners away while Forge Pool cannot be reached, instead of mining solo, so they fail over to their backup pool. Set it on a gateway that serves other people's addresses: a solo block pays only your payout address.",
		"Other programs and accounts on this computer can reach Forge Gateway, so a change here needs its settings password. With the Windows tray app: right-click the Forge Gateway icon in the notification area of the taskbar (if it is not there, click the ^ arrow beside it first) and choose Copy Settings Password, then paste it here. Otherwise it is the SETTINGS_PASSWORD Forge Gateway was started with. This browser remembers it once a save succeeds.",
		"Saved settings take effect at once: Forge Gateway reconnects to your node and to Forge Pool, and your miners get new work within seconds. A new payout address makes your miners reconnect once. Shares already credited to a wrong but valid payout address stay with it, so check it.",
		"Forge Gateway's saved settings could not be loaded, so nothing can be saved yet. Reload the page in a minute.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("GW-PAGE: the Settings card does not say %q", want)
		}
	}
	banner := regexp.MustCompile(`<[^>]+>`).ReplaceAllString(between(page, `id="setupBanner" hidden>`, `</div>`), "")
	if banner != "Forge Gateway is not set up yet: enter your node's RPC address and login and your BCH2 payout address in Settings below, then save. It starts mining as soon as they are saved." ||
		!strings.Contains(page, `<a href="#settings">Settings</a>`) {
		t.Errorf("GW-PAGE: the banner is %q", banner)
	}
	for _, want := range []string{
		`placeholder="http://127.0.0.1:8342"`, `type="password" autocomplete="new-password"`, `placeholder="C:\...\.cookie"`,
		`placeholder="bitcoincashii:q..."`, `id="cbTag" type="text" maxlength="24"`, `id="poolOnly"`, `#gwPw{-webkit-text-security:disc}`,
		`id="gwPw" type="text" autocomplete="off" data-lpignore="true" data-1p-ignore="true" data-bwignore="true" data-form-type="other"`,
		`"saved: leave empty to keep it"`, `"the node's rpcpassword"`, `"X-Forge-Password":pw`, `"Content-Type":"application/json"`,
		`const PW_KEY = "forgeGatewayPassword"`, `fetch("/api/settings"`,
		`"Enter Forge Gateway's settings password above the Save button first. Nothing was saved."`,
		`/^(bitcoincashii:|bitcoincash:)/i`,
		`"That is an address, not Forge Gateway's settings password. Nothing was saved. "+PW_WHERE`,
		`"Copy it again: right-click the Forge Gateway icon in the notification area, then Copy Settings Password."`,
		`"That is not Forge Gateway's settings password. Nothing was saved. "`,
		`"It is "+pwLength+" characters, only 0–9 and a–f; what was entered was "+pw.length+" characters. "`,
		`"Error: "+e+(/nothing was (saved|changed)/i.test(e)?"":" Nothing was saved.")`,
		`"Forge Gateway is not answering right now (it may be restarting), so nothing was saved. Your entries are still here: press Save again in a minute."`,
		`"Forge Gateway did not answer properly (HTTP "+status+"), so this save may or may not have gone through. Reload the page in a minute to see what is saved."`,
		`"This Forge Gateway was started without a settings password, so these settings can only be read here. To change them, edit "+(d.config_path||"its config file")+", then restart Forge Gateway."`,
		`$("setupBanner").hidden = s.configured !== false`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("GW-PAGE: status.html has no %s", want)
		}
	}
}

func between(s, from, to string) string {
	i := strings.Index(s, from)
	if i < 0 {
		return ""
	}
	s = s[i+len(from):]
	j := strings.Index(s, to)
	if j < 0 {
		return ""
	}
	return s[:j]
}

// Every text the states and Settings give is plain: no em-dash, and an en-dash only in 0–9 and a–f.
func TestTextsArePlain(t *testing.T) {
	for _, file := range []string{"state.go", "settings.go"} {
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		ast.Inspect(f, func(x ast.Node) bool {
			lit, ok := x.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatalf("%s: %v", file, err)
			}
			n++
			if strings.ContainsRune(s, '\u2014') || strings.ContainsRune(strings.NewReplacer("0–9", "", "a–f", "").Replace(s), '\u2013') {
				t.Errorf("GW-PLAIN: %s: %q", file, s)
			}
			return true
		})
		if n < 20 {
			t.Fatalf("GW-PLAIN: only %d texts found in %s", n, file)
		}
	}
}

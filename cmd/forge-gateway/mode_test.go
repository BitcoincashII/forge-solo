package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// SETTINGS_PASSWORD guards the settings of every program and account on the computer, so a short
// one is refused at start; one with spaces around is taken without them.
func TestSettingsPasswordLength(t *testing.T) {
	p := writeConfig(t, freshTestConfig(t))
	t.Setenv("SETTINGS_PASSWORD", " 0123456789abcde ")
	err := runBriefly(t, "GW-PW-SHORT", p)
	if exitCode(err) != 3 || err.Error() != "SETTINGS_PASSWORD is shorter than 16 characters: use a long random one (the Windows tray app makes 64 hex characters)" {
		t.Fatalf("GW-PW-SHORT: 15 characters: exit %d, %v", exitCode(err), err)
	}
	t.Setenv("SETTINGS_PASSWORD", " 0123456789abcdef\t")
	if pw, err := settingsPasswordFromEnv(); err != nil || pw != "0123456789abcdef" {
		t.Fatalf("GW-PW-SHORT: 16 characters with spaces around: %q, %v", pw, err)
	}
	t.Setenv("SETTINGS_PASSWORD", "   ")
	if pw, err := settingsPasswordFromEnv(); err != nil || pw != "" {
		t.Fatalf("GW-PW-SHORT: only spaces is no password: %q, %v", pw, err)
	}
}

// Without a password (the console, the service) the start is 1.0.0's: a config without a payout
// address is refused, with 1.0.0's text, and exit code 3.
func TestWithoutAPasswordAFreshConfigIsRefused(t *testing.T) {
	t.Setenv("SETTINGS_PASSWORD", "")
	p := writeConfig(t, freshTestConfig(t))
	err := runBriefly(t, "GW-MODE-STRICT", p)
	if exitCode(err) != 3 || err.Error() != p+": mining.payout_address is required: the BCH2 address your shares are credited to" {
		t.Fatalf("GW-MODE-STRICT: exit %d, %v", exitCode(err), err)
	}
}

// With a password (the tray app), a fresh config starts the gateway "not set up": the status page
// says so and what to do, and miners are turned away until Settings are saved.
func TestWithAPasswordAFreshConfigRuns(t *testing.T) {
	h := startAs(t, "GW-MODE-START", freshTestConfig(t), testPassword)
	v := getStatus(t, h)
	if v["configured"] != false || v["state"] != "unconfigured" || v["state_reason"] != "Set your node and payout address in Settings." || v["mode"] != "off" {
		t.Fatalf("GW-MODE-START: configured %v, state %v, reason %v, mode %v", v["configured"], v["state"], v["state_reason"], v["mode"])
	}
	c, err := net.Dial("tcp", h.a.srv.ListenAddr())
	if err != nil {
		t.Fatalf("GW-MODE-START-LISTEN: the stratum port does not listen: %v", err)
	}
	defer c.Close()
	c.Write([]byte(`{"id":1,"method":"mining.subscribe","params":["probe/1.0"]}` + "\n"))
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err = bufio.NewReader(c).ReadString('\n')
	if err == nil || strings.Contains(err.Error(), "timeout") {
		t.Fatalf("GW-MODE-START-TURNED-AWAY: a miner was let in before the gateway was set up (%v)", err)
	}
	select {
	case <-h.ended:
		t.Fatalf("GW-MODE-START: the gateway stopped: %v", h.err)
	default:
	}
}

// Settings need the status page: with a password, status.listen "off" is a config mistake.
func TestWithAPasswordTheStatusPageCannotBeOff(t *testing.T) {
	t.Setenv("SETTINGS_PASSWORD", testPassword)
	err := runBriefly(t, "GW-MODE-OFF", writeConfig(t, freshConfigWithStatus(t, "off")))
	if exitCode(err) != 3 || err.Error() != "status.listen is off, but Settings needs the status page: set it to 127.0.0.1:3090" {
		t.Fatalf("GW-MODE-OFF: exit %d, %v", exitCode(err), err)
	}
}

// freshConfigWithStatus is freshTestConfig with status.listen set to listen.
func freshConfigWithStatus(t *testing.T, listen string) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(freshTestConfig(t)), &m); err != nil {
		t.Fatal(err)
	}
	m["status"] = json.RawMessage(`{"listen":"` + listen + `"}`)
	b, _ := json.Marshal(m)
	return string(b)
}

// In the console, a node that refuses the login at start ends the gateway, as 1.0.0 did.
func TestInTheConsoleARefusedLoginEndsIt(t *testing.T) {
	t.Setenv("SETTINGS_PASSWORD", "")
	n := newFakeNode(t, "u", "p")
	cfg := gwConfig(t, `"rpc_url":"`+n.url()+`","rpc_user":"u","rpc_password":"wrong"`, `"payout_address":"`+testPayout+`"`)
	err := runBriefly(t, "GW-LOGIN-CONSOLE", writeConfig(t, cfg))
	if !errors.Is(err, errUnauthorized) || exitCode(err) != 1 {
		t.Fatalf("GW-LOGIN-CONSOLE: %v (exit %d)", err, exitCode(err))
	}
}

// Started by the tray app, a node that refuses the login is shown, never a reason to exit: the
// user corrects it in Settings.
func TestWithAPasswordARefusedLoginStays(t *testing.T) {
	n := newFakeNode(t, "u", "p")
	h := startAs(t, "GW-LOGIN-STAYS", gwConfig(t, `"rpc_url":"`+n.url()+`","rpc_user":"u","rpc_password":"wrong"`, `"payout_address":"`+testPayout+`"`), testPassword)
	var v statusView
	if !eventually(10*time.Second, func() bool { v = h.a.view(time.Now()); return v.State == stateNodeLogin }) {
		t.Fatalf("GW-LOGIN-STAYS: the state is %s %q", v.State, v.StateReason)
	}
	if v.StateReason != "Your node refused the RPC login. Check the RPC user and password in Settings: they must be the rpcuser and rpcpassword in the node's config file." {
		t.Fatalf("GW-LOGIN-STAYS: %q", v.StateReason)
	}
	select {
	case <-h.ended:
		t.Fatalf("GW-LOGIN-STAYS: the gateway stopped: %v", h.err)
	default:
	}
}

// A cookie file that is not there (a node that is stopped has none) is shown, and read as soon as
// the node writes it, with no save.
func TestAMissingCookieIsWaitedFor(t *testing.T) {
	setVar(t, &nodeCheckEvery, 50*time.Millisecond)
	n := newFakeNode(t, "__cookie__", "c1")
	cookie := filepath.Join(t.TempDir(), ".cookie")
	h := startGatewayPW(t, gwConfig(t, `"rpc_url":"`+n.url()+`","rpc_cookie_file":"`+filepath.ToSlash(cookie)+`"`, `"payout_address":"`+testPayout+`"`), testPassword)
	var v statusView
	want := "Forge Gateway cannot read the node's cookie file " + filepath.ToSlash(cookie) + ": it does not exist. Check that the node is running and that the cookie file in Settings is the node's."
	if !eventually(5*time.Second, func() bool { v = h.a.view(time.Now()); return v.State == stateNodeLogin }) || v.StateReason != want {
		t.Fatalf("GW-COOKIE-MISSING: %s %q\nwant %q", v.State, v.StateReason, want)
	}
	if err := writeFile(cookie, "__cookie__:c1\n"); err != nil {
		t.Fatal(err)
	}
	if !eventually(3*time.Second, func() bool { v = h.a.view(time.Now()); return v.State != stateNodeLogin && v.State != stateStarting }) {
		t.Fatalf("GW-COOKIE-MISSING: the cookie file was written, and the state is still %s %q", v.State, v.StateReason)
	}
}

// The status page's first answer already says whether the gateway is set up, before any apply:
// the tray app chooses the page it opens by it, and a fresh install must open Settings.
func TestTheFirstAnswerSaysNotSetUp(t *testing.T) {
	t.Setenv("SETTINGS_PASSWORD", testPassword)
	release := make(chan struct{})
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	setVar(t, &beforeApply, func() { <-release })
	type answer struct {
		v      map[string]interface{}
		engine bool
		err    error
	}
	first := make(chan answer, 1)
	setVar(t, &started, func(a *app) {
		ans := answer{engine: a.eng.Load() != nil}
		r, err := http.Get("http://" + a.statusAddr + "/api/status")
		if err == nil {
			err = json.NewDecoder(r.Body).Decode(&ans.v)
			r.Body.Close()
		}
		ans.err = err
		first <- ans
		free()
	})
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- run(writeConfig(t, freshTestConfig(t)), stop, false) }()
	t.Cleanup(func() {
		free()
		close(stop)
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("the gateway did not stop")
		}
	})
	select {
	case ans := <-first:
		if ans.err != nil || ans.engine {
			t.Fatalf("GW-CONFIGURED-EARLY: %v (an engine already: %v)", ans.err, ans.engine)
		}
		if ans.v["configured"] != false || ans.v["state"] != "unconfigured" {
			t.Fatalf("GW-CONFIGURED-EARLY: the first answer is configured %v, state %v", ans.v["configured"], ans.v["state"])
		}
	case err := <-done:
		t.Fatalf("GW-CONFIGURED-EARLY: the gateway ended: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("GW-CONFIGURED-EARLY: the gateway did not start")
	}
}

// getStatus is /api/status, decoded.
func getStatus(t *testing.T, h *harness) map[string]interface{} {
	t.Helper()
	r, err := http.Get("http://" + h.a.statusAddr + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var v map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

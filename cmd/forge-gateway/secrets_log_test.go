package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap/zaptest/observer"
)

// The secrets the tests below hand the gateway, each one of a kind no log line may ever hold.
const (
	secretPass1  = "rpc-secret-one-7f3a91"            // the node's RPC password
	secretPass2  = "rpc-secret-two-c4d2e8"            // a new one, saved in Settings
	secretCookie = "cookie-secret-55b1a0"             // the password in the node's .cookie
	secretTried  = "tried-but-wrong-9e8d7c6b5a493827" // a settings password someone tried
	secretInURL  = "url-secret-0a1b2c"                // a password written into the RPC address
	secretShort  = "short-secret-15"                  // a SETTINGS_PASSWORD too short to use
)

var allSecrets = []string{secretPass1, secretPass2, secretCookie, "__cookie__:" + secretCookie, testPassword, secretTried, secretInURL, secretShort}

// leaks is every secret in s.
func leaks(s string) []string {
	var out []string
	for _, x := range allSecrets {
		if strings.Contains(s, x) {
			out = append(out, x)
		}
	}
	return out
}

// noSecretLogged fails with code when a log entry, its message or any of its fields, holds a secret.
func noSecretLogged(t *testing.T, code string, logs *observer.ObservedLogs) {
	t.Helper()
	n := 0
	for _, e := range logs.All() {
		n++
		line := e.Message + " " + fmt.Sprint(e.ContextMap())
		if l := leaks(line); len(l) > 0 {
			t.Errorf("%s: the log holds %v: %s", code, l, line)
		}
	}
	if n < 5 {
		t.Fatalf("%s: setup: only %d log entries were seen", code, n)
	}
}

// An RPC address is shown and logged without a login written into it, and Settings refuses one.
func TestAnRPCAddressIsShownWithoutItsLogin(t *testing.T) {
	for in, want := range map[string]string{
		"http://u:" + secretInURL + "@127.0.0.1:8342":   "http://127.0.0.1:8342",
		"https://u:" + secretInURL + "@node.lan:8342/x": "https://node.lan:8342/x",
		"http://127.0.0.1:8342":                         "http://127.0.0.1:8342",
		"http://u:" + secretInURL + "@[::1:8342":        "http://[::1:8342",
	} {
		if got := shownURL(in); got != want {
			t.Errorf("GW-SECRET-URL: %q is shown as %q, want %q", in, got, want)
		}
	}
	if goodRPCURL("http://u:"+secretInURL+"@127.0.0.1:8342") || !goodRPCURL("http://127.0.0.1:8342") {
		t.Error("GW-SET-VALID-URL-LOGIN: Settings takes an RPC address with a login in it, or refuses a plain one")
	}
}

// No secret reaches the gateway's log: not the node's RPC password, the cookie's contents, the
// settings password, a wrong one someone tried, or a password written into the RPC address. In the
// console and with Settings, at the start, at every save and apply, and in their errors.
func TestNoSecretReachesTheLog(t *testing.T) {
	setVar(t, &nodeCheckEvery, 50*time.Millisecond)
	setVar(t, &stateLogEvery, 20*time.Millisecond)

	t.Run("console", func(t *testing.T) {
		n := newFakeNode(t, "gwuser", secretPass1)
		url := strings.Replace(n.url(), "http://", "http://gwuser:"+secretInURL+"@", 1)
		h := startGateway(t, gwConfig(t, `"rpc_url":"`+url+`","rpc_user":"gwuser","rpc_password":"`+secretPass1+`"`, `"payout_address":"`+testPayout+`"`))
		h.setUp(10 * time.Second)
		time.Sleep(200 * time.Millisecond) // a few node checks
		for _, path := range []string{"/api/status", "/api/settings"} {
			_, v, _ := h.call("GET", path, "", nil)
			if l := leaks(fmt.Sprint(v)); len(l) > 0 {
				t.Errorf("GW-SECRET-ANSWER: %s holds %v", path, l)
			}
		}
		h.close(30 * time.Second)
		noSecretLogged(t, "GW-SECRET-LOG-CONSOLE", h.logs)
	})

	t.Run("console errors", func(t *testing.T) {
		t.Setenv("SETTINGS_PASSWORD", "")
		n := newFakeNode(t, "gwuser", "the-right-one")
		err := runBriefly(t, "GW-SECRET-ERROR", writeConfig(t, gwConfig(t, `"rpc_url":"`+n.url()+`","rpc_user":"gwuser","rpc_password":"`+secretPass1+`"`, `"payout_address":"`+testPayout+`"`)))
		if err == nil || len(leaks(err.Error())) > 0 {
			t.Errorf("GW-SECRET-ERROR: a refused login ends the console with %v", err)
		}
		t.Setenv("SETTINGS_PASSWORD", secretShort)
		err = runBriefly(t, "GW-SECRET-ERROR", writeConfig(t, freshTestConfig(t)))
		if err == nil || len(leaks(err.Error())) > 0 {
			t.Errorf("GW-SECRET-ERROR: a short SETTINGS_PASSWORD ends it with %v", err)
		}
	})

	t.Run("settings", func(t *testing.T) {
		n := newFakeNode(t, "gwuser", secretPass1)
		h := startGatewayPW(t, gwConfig(t, `"rpc_url":"`+n.url()+`","rpc_user":"gwuser","rpc_password":"`+secretPass1+`"`, `"payout_address":"`+testPayout+`"`), testPassword)
		h.setUp(10 * time.Second)
		save := func(what, node string, want int) {
			t.Helper()
			if code, v := h.save(form(node, `"payout_address":"`+testPayout+`","coinbase_tag":"gw","pool_only":false`)); code != want {
				t.Fatalf("setup: %s: %d %v", what, code, v)
			}
			time.Sleep(150 * time.Millisecond) // its apply and a few node checks
		}
		n.setLogin("gwuser", secretPass2)
		save("a new password", `"rpc_url":"`+n.url()+`","rpc_user":"gwuser","rpc_password":"`+secretPass2+`"`, 200)
		save("the saved password kept", `"rpc_url":"`+n.url()+`","rpc_user":"gwuser","rpc_password":""`, 200)
		cookie := filepath.Join(t.TempDir(), ".cookie")
		if err := writeFile(cookie, "__cookie__:"+secretCookie+"\n"); err != nil {
			t.Fatal(err)
		}
		n.setLogin("__cookie__", secretCookie)
		save("a cookie login", `"rpc_url":"`+n.url()+`","rpc_cookie_file":"`+filepath.ToSlash(cookie)+`"`, 200)
		save("a password with a line break", `"rpc_url":"`+n.url()+`","rpc_user":"gwuser","rpc_password":"`+secretPass1+`\n"`, 400)
		save("a password in the RPC address", `"rpc_url":"`+strings.Replace(n.url(), "http://", "http://gwuser:"+secretInURL+"@", 1)+
			`","rpc_user":"gwuser","rpc_password":"`+secretPass1+`"`, 400)
		// A wrong settings password, with a form that holds a node password.
		code, _, _ := h.call("POST", "/api/settings", form(`"rpc_url":"`+n.url()+`","rpc_user":"gwuser","rpc_password":"`+secretPass1+`"`, `"payout_address":"`+testPayout+`"`),
			map[string]string{"Content-Type": "application/json", "X-Forge-Password": secretTried})
		if code != 401 {
			t.Fatalf("setup: a wrong settings password: %d", code)
		}
		// A save that cannot be written.
		saved := renameFile
		renameFile = func(string, string) error { return errors.New("the file is held by another program") }
		save("a save that cannot be written", `"rpc_url":"`+n.url()+`","rpc_user":"gwuser","rpc_password":"`+secretPass1+`"`, 500)
		renameFile = saved
		// A config file that cannot be read when it is read again.
		good, err := os.ReadFile(h.path)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeFile(h.path, `{"node":{"rpc_user":"gwuser","rpc_password":"`+secretPass2+`",`); err != nil {
			t.Fatal(err)
		}
		h.a.reloadFromFile("a test")
		if err := writeFile(h.path, string(good)); err != nil {
			t.Fatal(err)
		}
		// The node refusing the login, then this computer.
		n.setLogin("someone", "else")
		time.Sleep(200 * time.Millisecond)
		n.set(func(n *fakeNode) { n.forbid = true })
		time.Sleep(200 * time.Millisecond)
		h.close(30 * time.Second)
		noSecretLogged(t, "GW-SECRET-LOG-SETTINGS", h.logs)
	})
}

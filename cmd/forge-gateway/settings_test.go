package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/cashaddr"
)

// More made-up addresses: a miner's own, and a second payout address.
var (
	testMinerAddr = cashaddr.Encode(cashaddr.MainnetPrefix, cashaddr.P2PKH, [20]byte{9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9})
	testPayout2   = cashaddr.Encode(cashaddr.MainnetPrefix, cashaddr.P2PKH, [20]byte{7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7})
)

// call makes a request to the gateway's status server, on a connection of its own.
func (h *harness) call(method, path, body string, hdr map[string]string) (int, map[string]interface{}, http.Header) {
	h.t.Helper()
	code, v, hd, err := h.try(method, path, body, hdr)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	return code, v, hd
}

func (h *harness) try(method, path, body string, hdr map[string]string) (int, map[string]interface{}, http.Header, error) {
	req, err := http.NewRequest(method, "http://"+h.a.statusAddr+path, strings.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	c := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	var v map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&v)
	return resp.StatusCode, v, resp.Header, nil
}

// save posts a Settings form as the page does, with the settings password.
func (h *harness) save(form string) (int, map[string]interface{}) {
	h.t.Helper()
	code, v, _ := h.call("POST", "/api/settings", form, map[string]string{"Content-Type": "application/json", "X-Forge-Password": testPassword})
	return code, v
}

// form is a Settings form: node and mining are the bodies of its two sections.
func form(node, mining string) string { return `{"node":{` + node + `},"mining":{` + mining + `}}` }

// fileNow is the config file as it is now.
func (h *harness) fileNow() string {
	b, err := os.ReadFile(h.path)
	if err != nil {
		return "the file cannot be read: " + err.Error()
	}
	return string(b)
}

// fileConfig is the config file's node and mining sections as written.
func (h *harness) fileConfig() (nodeSettings, miningSettings) {
	h.t.Helper()
	var c struct {
		Node   nodeSettings   `json:"node"`
		Mining miningSettings `json:"mining"`
	}
	if err := json.Unmarshal([]byte(h.fileNow()), &c); err != nil {
		h.t.Fatal(err)
	}
	return c.Node, c.Mining
}

// GET answers what the file says, and never a password.
func TestSettingsGet(t *testing.T) {
	n := newFakeNode(t, "gwuser", "node-secret-1")
	h := startGatewayPW(t, gwConfig(t, `"rpc_url":"`+n.url()+`","rpc_user":"gwuser","rpc_password":"node-secret-1"`,
		`"payout_address":"`+testPayout+`","coinbase_tag":"GET test","pool_only":true`), testPassword)
	resp, err := http.Get("http://" + h.a.statusAddr + "/api/settings")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GW-SET-GET: HTTP %d %s", resp.StatusCode, raw)
	}
	if strings.Contains(string(raw), "node-secret-1") || strings.Contains(string(raw), testPassword) {
		t.Fatal("GW-SET-GET-SECRET: a password is in the settings the page reads")
	}
	var v settingsView
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs(h.path)
	if !v.Editable || !v.PasswordRequired || v.PasswordLength != 64 || v.ConfigPath != abs || !v.Configured || v.Problem != "" {
		t.Errorf("GW-SET-GET: %+v", v)
	}
	if v.Node.RPCURL != n.url() || v.Node.RPCUser != "gwuser" || !v.Node.RPCPasswordSet || v.Node.RPCCookieFile != "" {
		t.Errorf("GW-SET-GET-NODE: %+v", v.Node)
	}
	if v.Mining != (miningSettings{PayoutAddress: testPayout, CoinbaseTag: "GET test", PoolOnly: true}) {
		t.Errorf("GW-SET-GET-MINING: %+v", v.Mining)
	}
	// A hand edit shows at the next read.
	if err := writeFile(h.path, strings.Replace(h.fileNow(), "GET test", "edited by hand", 1)); err != nil {
		t.Fatal(err)
	}
	_, got, _ := h.call("GET", "/api/settings", "", nil)
	if m, _ := got["mining"].(map[string]interface{}); m["coinbase_tag"] != "edited by hand" {
		t.Errorf("GW-SET-GET-EDIT: %v", got["mining"])
	}
}

// Settings answer only this computer, at a name of its own.
func TestSettingsAnswerOnlyThisComputer(t *testing.T) {
	h := startGatewayPW(t, freshTestConfig(t), testPassword)
	hd := h.a.statusHandler()
	ask := func(method, host, remote string) int {
		req := httptest.NewRequest(method, "/api/settings", strings.NewReader("{}"))
		req.Host, req.RemoteAddr = host, remote
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		hd.ServeHTTP(rec, req)
		return rec.Code
	}
	before := h.fileNow()
	if c := ask("GET", "rebind.example:3090", "127.0.0.1:5555"); c != http.StatusMisdirectedRequest {
		t.Errorf("GW-SET-LOCAL: Host rebind.example: HTTP %d", c)
	}
	if c := ask("POST", "rebind.example:3090", "127.0.0.1:5555"); c != http.StatusMisdirectedRequest {
		t.Errorf("GW-SET-LOCAL: a save to Host rebind.example: HTTP %d", c)
	}
	if c := ask("GET", "127.0.0.1:3090", "192.168.1.5:5555"); c != http.StatusForbidden {
		t.Errorf("GW-SET-LOCAL: from 192.168.1.5: HTTP %d", c)
	}
	for _, host := range []string{"127.0.0.1:3090", "localhost:3090", "[::1]:3090"} {
		if c := ask("GET", host, "127.0.0.1:5555"); c != http.StatusOK {
			t.Errorf("GW-SET-LOCAL: Host %s: HTTP %d", host, c)
		}
	}
	if c := ask("GET", "127.0.0.1:3090", "[::1]:5555"); c != http.StatusOK {
		t.Errorf("GW-SET-LOCAL: from [::1]: HTTP %d", c)
	}
	if h.fileNow() != before {
		t.Error("GW-SET-LOCAL: the config file changed")
	}
}

// A page on another site cannot save: not across sites, not as a form or plain text.
func TestSettingsRefuseCrossSiteWrites(t *testing.T) {
	n := newFakeNode(t, "u", "p")
	h := startGatewayPW(t, nodeConfig(t, n), testPassword)
	good := form(`"rpc_url":"`+n.url()+`","rpc_user":"u","rpc_password":"p"`, `"payout_address":"`+testPayout+`"`)
	for _, site := range []string{"cross-site", "same-site"} {
		code, v, _ := h.call("POST", "/api/settings", good, map[string]string{"Content-Type": "application/json", "X-Forge-Password": testPassword, "Sec-Fetch-Site": site})
		if code != 403 || v["error"] != "Refused: a page on another site cannot change Forge Gateway's settings." {
			t.Errorf("GW-SET-CSRF: Sec-Fetch-Site %s: HTTP %d %v", site, code, v)
		}
	}
	for _, ct := range []string{"application/x-www-form-urlencoded", "multipart/form-data; boundary=x", "text/plain"} {
		code, v, _ := h.call("POST", "/api/settings", good, map[string]string{"Content-Type": ct, "X-Forge-Password": testPassword})
		if code != 415 || v["error"] != "Refused: send JSON (Content-Type: application/json)." {
			t.Errorf("GW-SET-CSRF: %s: HTTP %d %v", ct, code, v)
		}
	}
	code, _, _ := h.call("POST", "/api/settings", good, map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Forge-Password": testPassword, "Sec-Fetch-Site": "same-origin"})
	if code != 200 {
		t.Errorf("GW-SET-CSRF: a same-origin save: HTTP %d", code)
	}
	if code, _ := h.save(good); code != 200 {
		t.Errorf("GW-SET-CSRF: a save without Sec-Fetch-Site: HTTP %d", code)
	}
}

// Without a settings password (the console, the service) Settings can only be read.
func TestSettingsWithoutAPasswordAreReadOnly(t *testing.T) {
	n := newFakeNode(t, "u", "p")
	h := startGateway(t, nodeConfig(t, n))
	before := h.fileNow()
	code, v, _ := h.call("POST", "/api/settings", form(`"rpc_user":"u","rpc_password":"p"`, `"payout_address":"`+testPayout+`"`),
		map[string]string{"Content-Type": "application/json", "X-Forge-Password": testPassword})
	abs, _ := filepath.Abs(h.path)
	want := "This Forge Gateway was started without a settings password, so its settings are changed in " + abs + ": edit that file, then restart Forge Gateway."
	if code != 403 || v["error"] != want {
		t.Fatalf("GW-SET-READONLY: HTTP %d %v", code, v)
	}
	if h.fileNow() != before {
		t.Fatal("GW-SET-READONLY: the file changed")
	}
	_, got, _ := h.call("GET", "/api/settings", "", nil)
	if got["editable"] != false || got["password_required"] != false || got["password_length"] != 0.0 {
		t.Fatalf("GW-SET-READONLY-GET: %v", got)
	}
}

// A save needs the settings password: missing and wrong are refused, and said apart.
func TestSettingsNeedThePassword(t *testing.T) {
	n := newFakeNode(t, "u", "p")
	h := startGatewayPW(t, freshTestConfig(t), testPassword)
	good := form(`"rpc_url":"`+n.url()+`","rpc_user":"u","rpc_password":"p"`, `"payout_address":"`+testPayout+`"`)
	before := h.fileNow()
	code, v, _ := h.call("POST", "/api/settings", good, map[string]string{"Content-Type": "application/json"})
	if code != 401 || v["password_required"] != true || v["password_wrong"] != nil ||
		v["error"] != "Enter Forge Gateway's settings password to save. Nothing was saved." {
		t.Errorf("GW-SET-PW: missing: HTTP %d %v", code, v)
	}
	code, v, _ = h.call("POST", "/api/settings", good, map[string]string{"Content-Type": "application/json", "X-Forge-Password": strings.Repeat("0", 64)})
	if code != 401 || v["password_required"] != true || v["password_wrong"] != true ||
		v["error"] != "That is not Forge Gateway's settings password. Nothing was saved." {
		t.Errorf("GW-SET-PW: wrong: HTTP %d %v", code, v)
	}
	if h.logs.FilterMessageSnippet("settings change refused: wrong password (from 127.0.0.1:").Len() != 1 {
		t.Error("GW-SET-PW: a wrong password was not logged")
	}
	if h.fileNow() != before {
		t.Fatal("GW-SET-PW: a refused save changed the file")
	}
	code, v, _ = h.call("POST", "/api/settings", good, map[string]string{"Content-Type": "application/json", "X-Forge-Password": " " + testPassword + " "})
	if code != 200 || v["success"] != true {
		t.Fatalf("GW-SET-PW: right: HTTP %d %v", code, v)
	}
}

// A save's body is read up to 64 KiB, no further: the form is a few hundred bytes, and a program
// with the password must not make the gateway read without end. The same form, padded with
// spaces past the limit, is refused and changes nothing; padded up to just under it, it is saved.
func TestSettingsBodyIsBounded(t *testing.T) {
	n := newFakeNode(t, "u", "p")
	h := startGatewayPW(t, freshTestConfig(t), testPassword)
	padded := func(pad int) string {
		return `{"node":` + strings.Repeat(" ", pad) + `{"rpc_url":"` + n.url() + `","rpc_user":"u","rpc_password":"p"},"mining":{"payout_address":"` + testPayout + `"}}`
	}
	before := h.fileNow()
	code, v := h.save(padded(64 << 10))
	if code != 400 || !strings.Contains(fmt.Sprint(v["error"]), "too large") || h.fileNow() != before {
		t.Fatalf("GW-SET-BODY-LIMIT: a body past 64 KiB: HTTP %d %v", code, v)
	}
	if code, v := h.save(padded(64<<10 - 1024)); code != 200 {
		t.Fatalf("GW-SET-BODY-UNDER: a body under 64 KiB: HTTP %d %v", code, v)
	}
}

// Each mistake in the form is refused with its field and a text that says what to enter, and
// nothing is written.
func TestSettingsValidation(t *testing.T) {
	h := startGatewayPW(t, freshTestConfig(t), testPassword)
	dir := t.TempDir()
	notCookie := filepath.Join(dir, "settings.conf")
	if err := writeFile(notCookie, "server=1\nrpcuser=u\n"); err != nil {
		t.Fatal(err)
	}
	user := `"rpc_user":"u","rpc_password":"p"`
	pay := `"payout_address":"` + testPayout + `"`
	cases := []struct{ code, field, msg, form string }{
		{"URL", "node.rpc_url", msgRPCURL, form(`"rpc_url":"ftp://127.0.0.1:8342",`+user, pay)},
		{"URL", "node.rpc_url", msgRPCURL, form(`"rpc_url":"http://127.0.0.1",`+user, pay)},
		{"URL", "node.rpc_url", msgRPCURL, form(`"rpc_url":"127.0.0.1:8342",`+user, pay)},
		{"URL", "node.rpc_url", msgRPCURL, form(`"rpc_url":"http://127.0.0.1:99999",`+user, pay)},
		{"URL", "node.rpc_url", msgRPCURL, form(`"rpc_url":"http://127.0.0.1:8342/\u0007",`+user, pay)},
		{"URL", "node.rpc_url", msgRPCURL, form(`"rpc_url":"http://`+strings.Repeat("a", 2048)+`:8342",`+user, pay)},
		{"NEITHER", "node.rpc_user", msgNoLogin, form(`"rpc_url":""`, pay)},
		{"BOTH", "node.rpc_user", msgBothLogins, form(user+`,"rpc_cookie_file":"`+filepath.ToSlash(filepath.Join(dir, ".cookie"))+`"`, pay)},
		{"COLON", "node.rpc_user", msgUserColon, form(`"rpc_user":"u:x","rpc_password":"p"`, pay)},
		{"CTRL", "node.rpc_user", msgLoginControl, form(`"rpc_user":"u\nx","rpc_password":"p"`, pay)},
		{"CTRL", "node.rpc_user", msgLoginControl, form(`"rpc_user":"`+strings.Repeat("u", 1025)+`","rpc_password":"p"`, pay)},
		{"CTRL", "node.rpc_password", msgLoginControl, form(`"rpc_user":"u","rpc_password":"p\u0001"`, pay)},
		{"CTRL", "node.rpc_password", msgLoginControl, form(`"rpc_user":"u","rpc_password":"`+strings.Repeat("p", 1025)+`"`, pay)},
		{"NOPW", "node.rpc_password", msgNoPassword, form(`"rpc_user":"u","rpc_password":""`, pay)},
		{"COOKIE-REL", "node.rpc_cookie_file", msgCookieRel, form(`"rpc_cookie_file":"node/.cookie"`, pay)},
		{"COOKIE-DIR", "node.rpc_cookie_file", filepath.ToSlash(dir) + " is a folder: enter the full path of the .cookie file in it.",
			form(`"rpc_cookie_file":"`+filepath.ToSlash(dir)+`"`, pay)},
		{"COOKIE-FORM", "node.rpc_cookie_file", filepath.ToSlash(notCookie) + " is not a node's cookie file: it holds one line, user:password.",
			form(`"rpc_cookie_file":"`+filepath.ToSlash(notCookie)+`"`, pay)},
		{"PAYOUT-EMPTY", "mining.payout_address", msgNoPayout, form(user, `"payout_address":"  "`)},
		{"PAYOUT-BAD", "mining.payout_address", msgBadPayout, form(user, `"payout_address":"bitcoincashii:qqqqqq"`)},
		{"PAYOUT-BAD", "mining.payout_address", msgBadPayout, form(user, `"payout_address":"bitcoincash:qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzs23v9ccrn"`)},
		{"PAYOUT-P2SH", "mining.payout_address", msgP2SHPayout, form(user, `"payout_address":"`+testP2SH+`"`)},
		{"TAG", "mining.coinbase_tag", msgTag, form(user, pay+`,"coinbase_tag":"`+strings.Repeat("t", 25)+`"`)},
		{"TAG", "mining.coinbase_tag", msgTag, form(user, pay+`,"coinbase_tag":"café"`)},
	}
	before := h.fileNow()
	for _, k := range cases {
		code, v := h.save(k.form)
		if code != 400 || v["field"] != k.field || v["error"] != k.msg || v["success"] != false {
			t.Errorf("GW-SET-VALID-%s: HTTP %d %v\nwant field %s, %q", k.code, code, v, k.field, k.msg)
		}
	}
	if h.fileNow() != before {
		t.Fatal("GW-SET-VALID: a refused save changed the file")
	}
	// Not the form's JSON at all.
	for _, body := range []string{`not json`, `{"node":{"rpc_usr":"u"}}`, `{"node":{}} {"more":1}`} {
		code, v := h.save(body)
		if code != 400 || !strings.HasPrefix(v["error"].(string), "Refused: the request is not the Settings form's JSON (") {
			t.Errorf("GW-SET-VALID-JSON %q: HTTP %d %v", body, code, v)
		}
	}
	if code, _ := h.save(form(user, pay+`,"coinbase_tag":"`+strings.Repeat("x", 70<<10)+`"`)); code != 400 {
		t.Errorf("GW-SET-VALID-SIZE: a 70 KiB form: HTTP %d", code)
	}
}

// A node deletes its cookie when it stops: a cookie file that is not there is saved, shown, and
// read by itself once the node writes it.
func TestSettingsSaveACookieFileThatIsNotThereYet(t *testing.T) {
	setVar(t, &nodeCheckEvery, 50*time.Millisecond)
	n := newFakeNode(t, "__cookie__", "c2")
	h := startGatewayPW(t, freshTestConfig(t), testPassword)
	cookie := filepath.ToSlash(filepath.Join(t.TempDir(), ".cookie"))
	code, v := h.save(form(`"rpc_url":"`+n.url()+`","rpc_cookie_file":"`+cookie+`"`, `"payout_address":"`+testPayout+`"`))
	if code != 200 || v["success"] != true {
		t.Fatalf("GW-SET-COOKIE-ABSENT: HTTP %d %v", code, v)
	}
	if node, _ := h.fileConfig(); node.RPCCookieFile != cookie || node.RPCUser != "" || node.RPCPassword != "" {
		t.Fatalf("GW-SET-COOKIE-ABSENT: the file has %+v", node)
	}
	var s statusView
	want := "Forge Gateway cannot read the node's cookie file " + cookie + ": it does not exist."
	if !eventually(5*time.Second, func() bool { s = h.a.view(time.Now()); return s.State == stateNodeLogin }) || !strings.HasPrefix(s.StateReason, want) {
		t.Fatalf("GW-SET-COOKIE-ABSENT: %s %q", s.State, s.StateReason)
	}
	if err := writeFile(cookie, "__cookie__:c2\n"); err != nil {
		t.Fatal(err)
	}
	if !eventually(5*time.Second, func() bool { s = h.a.view(time.Now()); return s.State != stateNodeLogin && s.State != stateStarting }) {
		t.Fatalf("GW-SET-COOKIE-ABSENT: the node wrote its cookie, and the state is %s %q", s.State, s.StateReason)
	}
}

// A pasted value often brings a space: the addresses are saved without it, the node's password as
// it was typed.
func TestSettingsTrimWhatAPasteBrings(t *testing.T) {
	n := newFakeNode(t, "u", "p ")
	h := startGatewayPW(t, freshTestConfig(t), testPassword)
	code, v := h.save(form(`"rpc_url":" `+n.url()+` ","rpc_user":" u ","rpc_password":"p "`, `"payout_address":" `+strings.ToUpper(testPayout)+` ","coinbase_tag":" spaced "`))
	if code != 200 {
		t.Fatalf("GW-SET-TRIM: HTTP %d %v", code, v)
	}
	node, mining := h.fileConfig()
	if node.RPCURL != n.url() || node.RPCUser != "u" || node.RPCPassword != "p " || mining.PayoutAddress != testPayout || mining.CoinbaseTag != "spaced" {
		t.Fatalf("GW-SET-TRIM: %+v %+v", node, mining)
	}
}

// The node's password is never sent to the page, so an empty one keeps the saved one; a cookie
// login leaves no user or password in the file.
func TestSettingsKeepTheSavedNodePassword(t *testing.T) {
	n := newFakeNode(t, "u", "kept-secret")
	h := startGatewayPW(t, freshTestConfig(t), testPassword)
	if code, v := h.save(form(`"rpc_url":"`+n.url()+`","rpc_user":"u","rpc_password":"kept-secret"`, `"payout_address":"`+testPayout+`"`)); code != 200 {
		t.Fatalf("GW-SET-KEEP-PW: HTTP %d %v", code, v)
	}
	if code, v := h.save(form(`"rpc_url":"`+n.url()+`","rpc_user":"u","rpc_password":""`, `"payout_address":"`+testPayout+`","coinbase_tag":"second"`)); code != 200 {
		t.Fatalf("GW-SET-KEEP-PW: HTTP %d %v", code, v)
	}
	if node, mining := h.fileConfig(); node.RPCPassword != "kept-secret" || mining.CoinbaseTag != "second" {
		t.Fatalf("GW-SET-KEEP-PW: after a save with no password the file has %q (tag %q)", node.RPCPassword, mining.CoinbaseTag)
	}
	cookie := filepath.ToSlash(filepath.Join(t.TempDir(), ".cookie"))
	if code, v := h.save(form(`"rpc_url":"`+n.url()+`","rpc_cookie_file":"`+cookie+`"`, `"payout_address":"`+testPayout+`"`)); code != 200 {
		t.Fatalf("GW-SET-KEEP-PW: HTTP %d %v", code, v)
	}
	if node, _ := h.fileConfig(); node.RPCUser != "" || node.RPCPassword != "" || node.RPCCookieFile != cookie {
		t.Fatalf("GW-SET-KEEP-PW: switched to the cookie, the file has %+v", node)
	}
	// A cookie login saved, a user without a password has none to keep.
	if code, v := h.save(form(`"rpc_url":"`+n.url()+`","rpc_user":"u","rpc_password":""`, `"payout_address":"`+testPayout+`"`)); code != 400 || v["field"] != "node.rpc_password" {
		t.Fatalf("GW-SET-KEEP-PW: HTTP %d %v", code, v)
	}
}

// Settings write the node and mining sections and keep everything else as it was, in one layout.
func TestWriteSettingsLayout(t *testing.T) {
	p := writeConfig(t, `{"log_level":"debug","status":{"listen":"127.0.0.1:4090"},"stratum":{"listen":"0.0.0.0:4444","max_difficulty":1000000000000},
	"mining":{"payout_address":"x"},"log_file":"gw.log","pool":{"url":"https://pool.bch2.org","key_file":"k.key"},"node":{"rpc_user":"old"}}`)
	err := writeSettings(p, nodeSettings{RPCURL: "http://127.0.0.1:8342", RPCUser: "u", RPCPassword: "a<b>&c"},
		miningSettings{PayoutAddress: testPayout, CoinbaseTag: "", PoolOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	want := `{
  "node": {
    "rpc_url": "http://127.0.0.1:8342",
    "rpc_user": "u",
    "rpc_password": "a<b>&c",
    "rpc_cookie_file": ""
  },
  "mining": {
    "payout_address": "` + testPayout + `",
    "coinbase_tag": "",
    "pool_only": true
  },
  "stratum": {
    "listen": "0.0.0.0:4444",
    "max_difficulty": 1000000000000
  },
  "pool": {
    "url": "https://pool.bch2.org",
    "key_file": "k.key"
  },
  "status": {
    "listen": "127.0.0.1:4090"
  },
  "log_file": "gw.log",
  "log_level": "debug"
}
`
	if string(got) != want {
		t.Fatalf("GW-SET-WRITE: the file is\n%s\nwant\n%s", got, want)
	}
	if _, err := os.Stat(p + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("GW-SET-WRITE: a .tmp is left")
	}
	c, err := loadConfig(p)
	if err != nil || c.Node.RPCPassword != "a<b>&c" || c.Mining.CoinbaseTag != defaultCoinbaseTag || !c.Mining.PoolOnly || c.LogLevel != "debug" {
		t.Fatalf("GW-SET-WRITE-LOAD: %+v %v", c, err)
	}
	// The fresh config is in this layout already: a save changes only the two sections.
	fresh := writeConfig(t, freshConfig(t))
	if err := writeSettings(fresh, nodeSettings{RPCURL: "http://127.0.0.1:8342"}, miningSettings{CoinbaseTag: "Forge Gateway"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(fresh); string(got) != freshConfig(t) {
		t.Fatalf("GW-SET-WRITE-FRESH: the fresh config with its own values written back is\n%s", got)
	}
	// Never a file the gateway would refuse.
	bad := writeConfig(t, `{"node":{},"log_level":"loud"}`)
	before, _ := os.ReadFile(bad)
	if err := writeSettings(bad, nodeSettings{RPCURL: "http://127.0.0.1:8342"}, miningSettings{}); err == nil {
		t.Fatal("GW-SET-WRITE-CHECK: a config the gateway refuses was written")
	}
	if after, _ := os.ReadFile(bad); string(after) != string(before) {
		t.Fatal("GW-SET-WRITE-CHECK: the file changed")
	}
}

// A save through the page leaves the file the gateway loads, with what was posted.
func TestSettingsSaveLoads(t *testing.T) {
	n := newFakeNode(t, "u", "p")
	h := startGatewayPW(t, freshTestConfig(t), testPassword)
	if code, v := h.save(form(`"rpc_url":"`+n.url()+`","rpc_user":"u","rpc_password":"p"`, `"payout_address":"`+testPayout+`","coinbase_tag":"saved","pool_only":true`)); code != 200 || v["configured"] != true {
		t.Fatalf("GW-SET-WRITE: HTTP %d %v", code, v)
	}
	c, err := loadConfig(h.path)
	if err != nil || c.Node.RPCURL != n.url() || c.Node.RPCUser != "u" || c.Node.RPCPassword != "p" || c.Mining.PayoutAddress != testPayout ||
		c.Mining.CoinbaseTag != "saved" || !c.Mining.PoolOnly || c.Stratum.Listen != h.a.start.Stratum.Listen || c.Pool.URL != unreachablePool {
		t.Fatalf("GW-SET-WRITE: %+v %v", c, err)
	}
	if _, err := os.Stat(h.path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("GW-SET-WRITE: a .tmp is left")
	}
}

// The file is replaced by a rename, tried again while Windows refuses it: a save that cannot be
// made leaves the file as it was, and no .tmp beside it.
func TestSettingsWriteIsAtomic(t *testing.T) {
	setVar(t, &renameWait, 10*time.Millisecond)
	n := newFakeNode(t, "u", "p")
	h := startGatewayPW(t, freshTestConfig(t), testPassword)
	var calls, failFirst atomic.Int64
	setVar(t, &renameFile, func(from, to string) error {
		if calls.Add(1) <= failFirst.Load() {
			return errors.New("Access is denied.")
		}
		return os.Rename(from, to)
	})
	good := form(`"rpc_url":"`+n.url()+`","rpc_user":"u","rpc_password":"p"`, `"payout_address":"`+testPayout+`"`)
	before := h.fileNow()
	failFirst.Store(5)
	code, v := h.save(good)
	abs, _ := filepath.Abs(h.path)
	if code != 500 || v["error"] != "Forge Gateway could not save its settings to "+abs+": Access is denied. Nothing was changed." {
		t.Errorf("GW-SET-ATOMIC: renames refused 5 times: HTTP %d %v", code, v)
	}
	if calls.Load() != 5 {
		t.Errorf("GW-SET-ATOMIC: the rename was tried %d times, want 5", calls.Load())
	}
	if h.fileNow() != before {
		t.Error("GW-SET-ATOMIC: the file changed")
	}
	if _, err := os.Stat(h.path + ".tmp"); !os.IsNotExist(err) {
		t.Error("GW-SET-ATOMIC: a .tmp is left")
	}
	calls.Store(0)
	failFirst.Store(4)
	if code, v := h.save(good); code != 200 {
		t.Errorf("GW-SET-ATOMIC: renames refused 4 times, then taken: HTTP %d %v", code, v)
	}
	if node, _ := h.fileConfig(); node.RPCUser != "u" {
		t.Errorf("GW-SET-ATOMIC: the save is not in the file: %+v", node)
	}
}

// A config file that is not JSON is not rewritten: it holds what else the user put in it.
func TestSettingsLeaveABrokenFileAlone(t *testing.T) {
	h := startGatewayPW(t, freshTestConfig(t), testPassword)
	if err := writeFile(h.path, "{ this is not json"); err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs(h.path)
	code, v, _ := h.call("GET", "/api/settings", "", nil)
	if code != 503 || !strings.HasPrefix(v["error"].(string), "Forge Gateway cannot read its settings from "+abs+": ") || !strings.HasSuffix(v["error"].(string), ". Nothing was changed.") {
		t.Errorf("GW-SET-BROKEN-FILE: GET: HTTP %d %v", code, v)
	}
	code, v = h.save(form(`"rpc_user":"u","rpc_password":"p"`, `"payout_address":"`+testPayout+`"`))
	if code != 503 || !strings.HasPrefix(v["error"].(string), "Forge Gateway cannot read its settings from ") {
		t.Errorf("GW-SET-BROKEN-FILE: POST: HTTP %d %v", code, v)
	}
	// Nor would the writer itself replace it.
	var ue *unreadableError
	if err := writeSettings(h.path, nodeSettings{RPCURL: defaultRPCURL, RPCUser: "u", RPCPassword: "p"}, miningSettings{PayoutAddress: testPayout}); !errors.As(err, &ue) {
		t.Errorf("GW-SET-BROKEN-FILE: the writer said %v", err)
	}
	if h.fileNow() != "{ this is not json" {
		t.Error("GW-SET-BROKEN-FILE: the file was rewritten")
	}
}

// Every answer of the status server carries the headers that keep other sites from framing it or
// guessing its content.
func TestStatusServerHeaders(t *testing.T) {
	h := startGatewayPW(t, freshTestConfig(t), testPassword)
	for _, r := range []struct{ method, path string }{{"GET", "/"}, {"GET", "/api/status"}, {"GET", "/api/settings"},
		{"POST", "/api/settings"}, {"POST", "/notify"}, {"GET", "/nothing-here"}, {"PUT", "/api/settings"}} {
		_, _, hd := h.call(r.method, r.path, "{}", map[string]string{"Content-Type": "application/json"})
		for k, want := range map[string]string{"X-Frame-Options": "DENY", "Content-Security-Policy": "frame-ancestors 'none'",
			"X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer"} {
			if hd.Get(k) != want {
				t.Errorf("GW-HEADERS: %s %s: %s is %q", r.method, r.path, k, hd.Get(k))
			}
		}
	}
	if code, _, hd := h.call("PUT", "/api/settings", "{}", nil); code != 405 || hd.Get("Allow") != "GET, HEAD, POST" {
		t.Errorf("GW-HEADERS: PUT /api/settings: HTTP %d, Allow %q", code, hd.Get("Allow"))
	}
}

// coinbaseHas reports whether a job's coinbase pays addr's script.
func paysTo(addr string) func(notify) bool {
	a, _ := cashaddr.Decode(addr, cashaddr.MainnetPrefix)
	script := hex.EncodeToString(a.Script())
	return func(n notify) bool { return strings.Contains(n.coinb2, script) }
}

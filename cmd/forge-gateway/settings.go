package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/BitcoincashII/forge-solo/internal/tidesgw"
	"go.uber.org/zap"
)

// The status page's Settings: GET /api/settings reads the node and mining settings from the config
// file, POST /api/settings checks and saves them there and applies them at once. Saving needs the
// gateway's settings password (SETTINGS_PASSWORD), which the Windows tray app makes and copies:
// other programs and accounts on the computer can reach the status page too.

// settingsPasswordHeader carries the settings password on a save, as Forge Solo's does.
const settingsPasswordHeader = "X-Forge-Password"

// maxSettingsBody bounds a save's body.
const maxSettingsBody = 64 << 10

// maxSettingsTag is the most of a coinbase tag the job manager keeps (mining.sanitizeCoinbaseTag).
const maxSettingsTag = 24

// renameFile replaces the config with the file just written; a variable so the tests can make it fail.
var renameFile = os.Rename

// renameTries and renameWait: Windows refuses a rename while another program has the file open a
// moment (an antivirus scan, an editor), so it is tried again.
var (
	renameTries = 5
	renameWait  = 100 * time.Millisecond
)

// nodeSettings and miningSettings are the config's node and mining sections as Settings writes
// them, keys in this order.
type nodeSettings struct {
	RPCURL        string `json:"rpc_url"`
	RPCUser       string `json:"rpc_user"`
	RPCPassword   string `json:"rpc_password"`
	RPCCookieFile string `json:"rpc_cookie_file"`
}

type miningSettings struct {
	PayoutAddress string `json:"payout_address"`
	CoinbaseTag   string `json:"coinbase_tag"`
	PoolOnly      bool   `json:"pool_only"`
}

// settingsForm is what the page posts.
type settingsForm struct {
	Node   nodeSettings   `json:"node"`
	Mining miningSettings `json:"mining"`
}

// settingsView is GET /api/settings. The node's password is never in it, in any form.
type settingsView struct {
	Editable         bool   `json:"editable"`
	PasswordRequired bool   `json:"password_required"`
	PasswordLength   int    `json:"password_length"`
	ConfigPath       string `json:"config_path"`
	Configured       bool   `json:"configured"`
	Problem          string `json:"problem"`
	Node             struct {
		RPCURL         string `json:"rpc_url"`
		RPCUser        string `json:"rpc_user"`
		RPCPasswordSet bool   `json:"rpc_password_set"`
		RPCCookieFile  string `json:"rpc_cookie_file"`
	} `json:"node"`
	Mining miningSettings `json:"mining"`
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func refuse(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]interface{}{"success": false, "error": msg})
}

// securityHeaders sets on every answer what Forge Solo's dashboard sends: no page may frame the
// status page (a framed, see-through Settings form can be clicked through by someone who never
// sees it), no content type is guessed, no referrer is sent on.
func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr := w.Header()
		hdr.Set("X-Frame-Options", "DENY")
		hdr.Set("Content-Security-Policy", "frame-ancestors 'none'")
		hdr.Set("X-Content-Type-Options", "nosniff")
		hdr.Set("Referrer-Policy", "no-referrer")
		h.ServeHTTP(w, r)
	})
}

// localOnly lets through only a request from this computer, addressed to it by a name of its own.
// A page on any site could rebind its own name to 127.0.0.1 and send same-origin requests here;
// the browser still sends that site's name as Host, so anything but this computer's own name is
// refused.
func localOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLocalHost(r.Host) {
			refuse(w, http.StatusMisdirectedRequest, "Forge Gateway answers Settings only at 127.0.0.1 or localhost.")
			return
		}
		if !isLoopbackAddr(r.RemoteAddr) {
			refuse(w, http.StatusForbidden, "Settings can be read and changed only on the gateway's own computer.")
			return
		}
		h.ServeHTTP(w, r)
	})
}

// isLocalHost reports whether a Host header names this computer: localhost or a loopback address.
func isLocalHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isLoopbackAddr(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// settingsHandler is /api/settings.
func (a *app) settingsHandler() http.Handler {
	return localOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			a.getSettings(w)
		case http.MethodPost:
			a.postSettings(w, r)
		default:
			w.Header().Set("Allow", "GET, HEAD, POST")
			refuse(w, http.StatusMethodNotAllowed, "Refused: Settings are read with GET and saved with POST.")
		}
	}))
}

// configPath is the config file's full path, as the page shows it.
func (a *app) configPath() string {
	if abs, err := filepath.Abs(a.cfgPath); err == nil {
		return abs
	}
	return a.cfgPath
}

// cannotRead is the answer when the config file cannot be read or decoded: rewriting it would lose
// what else is in it, so nothing is changed.
func (a *app) cannotRead(err error) string {
	return "Forge Gateway cannot read its settings from " + a.configPath() + ": " + a.withoutPath(err) + ". Nothing was changed."
}

// withoutPath is err without the config file's path, which the answer names already, and without
// a full stop of its own (Windows ends its texts with one), for the answer's to follow.
func (a *app) withoutPath(err error) string {
	var pe *fs.PathError
	var le *os.LinkError
	s := err.Error()
	switch {
	case errors.As(err, &le):
		s = le.Err.Error()
	case errors.As(err, &pe) && (pe.Path == a.cfgPath || pe.Path == a.cfgPath+".tmp"):
		s = pe.Err.Error()
	}
	for _, p := range []string{a.cfgPath + ": ", a.configPath() + ": "} {
		s = strings.TrimPrefix(s, p)
	}
	return strings.TrimRight(s, ". ")
}

func (a *app) getSettings(w http.ResponseWriter) {
	cfg, err := readConfig(a.cfgPath)
	if err != nil {
		refuse(w, http.StatusServiceUnavailable, a.cannotRead(err))
		return
	}
	problem := cfg.setupProblem()
	v := settingsView{Editable: a.password != "", PasswordRequired: a.password != "", PasswordLength: len(a.password),
		ConfigPath: a.configPath(), Configured: problem == "", Problem: problem}
	v.Node.RPCURL = cfg.Node.RPCURL
	v.Node.RPCUser = cfg.Node.RPCUser
	v.Node.RPCPasswordSet = cfg.Node.RPCPassword != ""
	v.Node.RPCCookieFile = cfg.Node.RPCCookieFile
	v.Mining = miningSettings{PayoutAddress: cfg.Mining.PayoutAddress, CoinbaseTag: cfg.Mining.CoinbaseTag, PoolOnly: cfg.Mining.PoolOnly}
	writeJSON(w, http.StatusOK, v)
}

func (a *app) postSettings(w http.ResponseWriter, r *http.Request) {
	// A page on another site can send a form or a "simple" POST without asking; it cannot send
	// JSON or a custom header without a CORS preflight, which is never granted.
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		refuse(w, http.StatusForbidden, "Refused: a page on another site cannot change Forge Gateway's settings.")
		return
	}
	if ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); !strings.EqualFold(strings.TrimSpace(ct), "application/json") {
		refuse(w, http.StatusUnsupportedMediaType, "Refused: send JSON (Content-Type: application/json).")
		return
	}
	if a.password == "" {
		refuse(w, http.StatusForbidden, "This Forge Gateway was started without a settings password, so its settings are changed in "+
			a.configPath()+": edit that file, then restart Forge Gateway.")
		return
	}
	// Answered, not counted: the password is 64 hex characters, beyond guessing, and a lockout
	// would let another program keep the owner out of their own settings.
	got := strings.TrimSpace(r.Header.Get(settingsPasswordHeader))
	if got == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]interface{}{"success": false, "password_required": true,
			"error": "Enter Forge Gateway's settings password to save. Nothing was saved."})
		return
	}
	// Compared as hashes, so the time taken says nothing about the password's length.
	gotSum, wantSum := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(a.password))
	if subtle.ConstantTimeCompare(gotSum[:], wantSum[:]) != 1 {
		a.log.Warn("settings change refused: wrong password (from " + r.RemoteAddr + ")")
		writeJSON(w, http.StatusUnauthorized, map[string]interface{}{"success": false, "password_required": true, "password_wrong": true,
			"error": "That is not Forge Gateway's settings password. Nothing was saved."})
		return
	}

	a.saveMu.Lock()
	defer a.saveMu.Unlock()
	var form settingsForm
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSettingsBody))
	dec.DisallowUnknownFields()
	err := dec.Decode(&form)
	if err == nil && dec.Decode(&struct{}{}) != io.EOF {
		err = errors.New("more follows the form")
	}
	if err != nil {
		refuse(w, http.StatusBadRequest, "Refused: the request is not the Settings form's JSON ("+err.Error()+").")
		return
	}
	cur, err := readConfig(a.cfgPath)
	if err != nil {
		refuse(w, http.StatusServiceUnavailable, a.cannotRead(err))
		return
	}
	node, mining, field, msg := checkSettingsForm(form, cur)
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "field": field, "error": msg})
		return
	}
	if err := writeSettings(a.cfgPath, node, mining); err != nil {
		var ue *unreadableError
		if errors.As(err, &ue) {
			refuse(w, http.StatusServiceUnavailable, a.cannotRead(ue.err))
			return
		}
		a.log.Error("settings could not be saved", zap.Error(err))
		refuse(w, http.StatusInternalServerError, "Forge Gateway could not save its settings to "+a.configPath()+": "+a.withoutPath(err)+". Nothing was changed.")
		return
	}
	cfg, err := readConfig(a.cfgPath)
	if err != nil {
		a.log.Error("the settings just saved cannot be read", zap.Error(err))
		refuse(w, http.StatusInternalServerError, "Forge Gateway saved its settings to "+a.configPath()+" but cannot read them again: "+a.withoutPath(err)+".")
		return
	}
	problem := cfg.setupProblem()
	a.reload(cfg, problem)
	login := "user " + node.RPCUser
	if node.RPCUser == "" {
		login = "cookie file " + node.RPCCookieFile
	}
	a.log.Info("settings saved from the status page", zap.String("node", node.RPCURL), zap.String("login", login),
		zap.String("payout_address", mining.PayoutAddress), zap.String("coinbase_tag", mining.CoinbaseTag), zap.Bool("pool_only", mining.PoolOnly))
	message := "Saved. Forge Gateway now works with these settings: your miners get new work from them within seconds."
	if before, err := tidesgw.CanonicalAddress(cur.Mining.PayoutAddress); err == nil && before != mining.PayoutAddress {
		message = "Saved. Forge Gateway now works with these settings. The payout address changed, so your miners reconnect once: those that log in with a worker name are then credited to the new address."
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "configured": problem == "", "message": message})
}

// The texts of a save refused for a field: in the order they are checked.
const (
	msgRPCURL       = "The node's RPC address must look like http://127.0.0.1:8342: http:// or https://, the address, a colon and the port."
	msgNoLogin      = "Enter the node's RPC user and password, or the full path of its .cookie file."
	msgBothLogins   = "Enter the node's RPC user and password, or its cookie file, not both."
	msgUserColon    = "The node's RPC user cannot contain a colon (:)."
	msgLoginControl = "The node's RPC user and password cannot contain line breaks or other control characters."
	msgNoPassword   = "Enter the node's RPC password."
	msgCookieRel    = "Enter the full path of the node's .cookie file: it is in the node's data folder."
	msgCookieDir    = "%s is a folder: enter the full path of the .cookie file in it."
	msgNotCookie    = "%s is not a node's cookie file: it holds one line, user:password."
	msgNoPayout     = "Enter your BCH2 payout address."
	msgBadPayout    = "That is not a BCH2 address. A payout address starts bitcoincashii:q; check it for a typo."
	msgP2SHPayout   = "That is a bitcoincashii:p... address. Forge Gateway pays only a bitcoincashii:q... address: use one of those."
	msgTag          = "The coinbase tag can be at most 24 characters: letters, digits, spaces and plain punctuation."
)

// maxLogin bounds the node's RPC user and password.
const maxLogin = 1024

// checkSettingsForm checks a save against the config it replaces, cur, and gives the sections to
// write; or the field that is wrong and why. The addresses, the user and the tag are taken without
// the spaces a paste often brings; the password as it is.
func checkSettingsForm(f settingsForm, cur *Config) (node nodeSettings, mining miningSettings, field, msg string) {
	rpcURL := strings.TrimSpace(f.Node.RPCURL)
	user := strings.TrimSpace(f.Node.RPCUser)
	pass := f.Node.RPCPassword
	cookie := strings.TrimSpace(f.Node.RPCCookieFile)
	payout := strings.TrimSpace(f.Mining.PayoutAddress)
	tag := strings.TrimSpace(f.Mining.CoinbaseTag)

	if rpcURL == "" {
		rpcURL = defaultRPCURL
	} else if !goodRPCURL(rpcURL) {
		return node, mining, "node.rpc_url", msgRPCURL
	}
	switch {
	case user == "" && cookie == "":
		return node, mining, "node.rpc_user", msgNoLogin
	case user != "" && cookie != "":
		return node, mining, "node.rpc_user", msgBothLogins
	}
	if user != "" {
		switch {
		case strings.Contains(user, ":"):
			return node, mining, "node.rpc_user", msgUserColon
		case hasControl(user) || len(user) > maxLogin:
			return node, mining, "node.rpc_user", msgLoginControl
		case hasControl(pass) || len(pass) > maxLogin:
			return node, mining, "node.rpc_password", msgLoginControl
		}
		if pass == "" {
			// Empty keeps the saved password, which the page never sees.
			if cur.Node.RPCUser == "" || cur.Node.RPCPassword == "" {
				return node, mining, "node.rpc_password", msgNoPassword
			}
			pass = cur.Node.RPCPassword
		}
		node = nodeSettings{RPCURL: rpcURL, RPCUser: user, RPCPassword: pass}
	} else {
		if !filepath.IsAbs(cookie) {
			return node, mining, "node.rpc_cookie_file", msgCookieRel
		}
		if st, err := os.Stat(cookie); err == nil && st.IsDir() {
			return node, mining, "node.rpc_cookie_file", fmt.Sprintf(msgCookieDir, cookie)
		}
		// A cookie file that is not there, or cannot be read, is saved: a node deletes its cookie
		// when it stops, and a node that is down must not keep a right path from being saved.
		if content, ok := readSmall(cookie); ok && !isCookie(content) {
			return node, mining, "node.rpc_cookie_file", fmt.Sprintf(msgNotCookie, cookie)
		}
		node = nodeSettings{RPCURL: rpcURL, RPCCookieFile: cookie}
	}
	if payout == "" {
		return node, mining, "mining.payout_address", msgNoPayout
	}
	canon, err := tidesgw.CanonicalAddress(payout)
	if err != nil {
		return node, mining, "mining.payout_address", msgBadPayout
	}
	if isP2SH(canon) {
		return node, mining, "mining.payout_address", msgP2SHPayout
	}
	if len(tag) > maxSettingsTag || !printableASCII(tag) {
		return node, mining, "mining.coinbase_tag", msgTag
	}
	return node, miningSettings{PayoutAddress: canon, CoinbaseTag: tag, PoolOnly: f.Mining.PoolOnly}, "", ""
}

// goodRPCURL: http or https, a host and a port, nothing that is not printable.
func goodRPCURL(s string) bool {
	if len(s) > 2048 || hasControl(s) {
		return false
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return false
	}
	port, err := strconv.Atoi(u.Port())
	return err == nil && port >= 1 && port <= 65535
}

func hasControl(s string) bool {
	return strings.IndexFunc(s, unicode.IsControl) >= 0
}

func printableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// readSmall reads the start of a file that can be read; ok is false when it cannot.
func readSmall(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// isCookie reports whether content is a node's cookie: one line, user:password.
func isCookie(content string) bool {
	s := strings.TrimSpace(content)
	if len(s) > 4096 || strings.ContainsAny(s, "\r\n") {
		return false
	}
	user, pass, ok := strings.Cut(s, ":")
	return ok && user != "" && pass != ""
}

// unreadableError is a config file that cannot be read or decoded, so it is not rewritten.
type unreadableError struct{ err error }

func (e *unreadableError) Error() string { return e.err.Error() }
func (e *unreadableError) Unwrap() error { return e.err }

// configKeys is the order Settings writes the config's sections in; any other key follows.
var configKeys = []string{"node", "mining", "stratum", "pool", "status", "log_file", "log_level"}

// writeSettings puts node and mining in the config file at path and keeps everything else in it as
// it was. The file is replaced whole, by a rename, so it is the old one or the new one, never a
// part; and never one the gateway would refuse.
func writeSettings(path string, node nodeSettings, mining miningSettings) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return &unreadableError{err}
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return &unreadableError{fmt.Errorf("%s: %w", path, err)}
	}
	if top == nil {
		return &unreadableError{fmt.Errorf("%s: it is not a JSON object", path)}
	}
	if top["node"], err = plainJSON(node); err != nil {
		return err
	}
	if top["mining"], err = plainJSON(mining); err != nil {
		return err
	}
	var flat bytes.Buffer
	flat.WriteByte('{')
	put := func(k string) {
		if flat.Len() > 1 {
			flat.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		flat.Write(kb)
		flat.WriteByte(':')
		flat.Write(top[k])
		delete(top, k)
	}
	for _, k := range configKeys {
		if _, ok := top[k]; ok {
			put(k)
		}
	}
	rest := make([]string, 0, len(top))
	for k := range top {
		rest = append(rest, k)
	}
	sort.Strings(rest)
	for _, k := range rest {
		put(k)
	}
	flat.WriteByte('}')
	var out bytes.Buffer
	if err := json.Indent(&out, flat.Bytes(), "", "  "); err != nil {
		return err
	}
	out.WriteByte('\n')
	// The same check readConfig makes, on these bytes, as the file at path would be read.
	if _, err := parseConfig(out.Bytes(), path); err != nil {
		return err
	}
	return replaceFile(path, out.Bytes())
}

// plainJSON is v as JSON, with < > & as they are: a password may hold them.
func plainJSON(v interface{}) (json.RawMessage, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// replaceFile writes content to path.tmp, flushes it to the disk, and renames it over path.
func replaceFile(path string, content []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(content); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		for i := 1; ; i++ {
			if err = renameFile(tmp, path); err == nil || i >= renameTries {
				break
			}
			time.Sleep(renameWait)
		}
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

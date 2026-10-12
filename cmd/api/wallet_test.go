package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/cashaddr"
	"github.com/BitcoincashII/forge-solo/internal/stats"
	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/gofiber/fiber/v2"
)

// The address the regtest proof paid (twenty bytes of 0xa1), and the script it pays to.
const (
	walletTestAddress = "bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2"
	walletTestScript  = "76a914a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a188ac"
)

// regtestUnspents is what a regtest BCH2 node v27.0.2 answered at tip 114 for the test address: a
// coinbase at height 1, two at 112 and 113, and a 12.5 BCH2 payment mined at 114.
const regtestUnspents = `[
	{"txid":"0fb1edc80e3749ec3e13817647950b462d1c54a258335ad9cacc054d2ecc67c2","vout":0,"scriptPubKey":"` + walletTestScript + `","amount":50.00000000,"coinbase":true,"height":1},
	{"txid":"4a7b7b0abf91484946c9c31cbe03e8fad4519d40978481c0730568ac5fe464b5","vout":0,"scriptPubKey":"` + walletTestScript + `","amount":50.00000000,"coinbase":true,"height":112},
	{"txid":"7f7890ee1f8d16ceda6889cfa19cac88ea1a57a0d730ae4c4928c7eed30d1ad2","vout":0,"scriptPubKey":"` + walletTestScript + `","amount":50.00000000,"coinbase":true,"height":113},
	{"txid":"c28373dbc76d067ab3cdad6396d3c0317aa2a6bb49e947b187701cb556b013f9","vout":0,"scriptPubKey":"` + walletTestScript + `","amount":12.50000000,"coinbase":false,"height":114}]`

func regtestCoins(t *testing.T) []scanUnspent {
	t.Helper()
	var u []scanUnspent
	if err := json.Unmarshal([]byte(regtestUnspents), &u); err != nil {
		t.Fatal(err)
	}
	return u
}

// The card splits the coins as the balance card splits a block's reward: a coinbase output is
// spendable once 100 blocks are on top of the block that paid it. A payment is spendable at once.
func TestWalletSplitsCoinsAsTheBalanceCardDoes(t *testing.T) {
	coins := regtestCoins(t)
	f, err := splitCoins(114, coins)
	if err != nil {
		t.Fatal(err)
	}
	if f.SpendableSats != 6250000000 || f.MaturingSats != 10000000000 || f.Outputs != 4 || f.Height != 114 ||
		fmt.Sprint(f.Maturing) != "[{112 5000000000 98} {113 5000000000 99}]" {
		t.Errorf("WALLET-SPLIT: at tip 114 %+v", *f)
	}
	// 100 blocks on top of block 112 at tip 212: it is spendable, and 113 has one block to go.
	if f, _ = splitCoins(212, coins); f.SpendableSats != 11250000000 || f.MaturingSats != 5000000000 ||
		fmt.Sprint(f.Maturing) != "[{113 5000000000 1}]" {
		t.Errorf("WALLET-SPLIT-BOUNDARY: at tip 212 %+v", *f)
	}
	if f, _ = splitCoins(211, coins); f.SpendableSats != 6250000000 || fmt.Sprint(f.Maturing) != "[{112 5000000000 1} {113 5000000000 2}]" {
		t.Errorf("WALLET-SPLIT-BOUNDARY: at tip 211 %+v", *f)
	}
	if f, _ = splitCoins(5000, coins); f.MaturingSats != 0 || f.Maturing == nil || len(f.Maturing) != 0 {
		t.Errorf("WALLET-SPLIT-NONE: nothing maturing is an empty list, not null: %+v", *f)
	}
}

// Amounts are read from the node's decimal text into satoshis exactly, never through a float: the
// sum below, in float64, is not 20999999.99999999.
func TestWalletAmountsAreExact(t *testing.T) {
	for in, want := range map[string]int64{"12.34567891": 1234567891, "0.1": 10000000, "50.00000000": 5000000000,
		"20999999.99999999": 2099999999999999, "21000000": 2100000000000000, "0": 0, "0.00000001": 1} {
		if got, err := satsFromAmount(json.Number(in)); err != nil || got != want {
			t.Errorf("WALLET-EXACT-SATS: %s read as %d (%v), want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"0.000000001", "-1", "1e-8", "1E8", "1.2.3", "", ".5", "+1", "21000000.00000001", "123456789", "0x10", "1,5"} {
		if got, err := satsFromAmount(json.Number(in)); err == nil {
			t.Errorf("WALLET-EXACT-SATS-REFUSED: %q read as %d", in, got)
		}
	}
	var coins []scanUnspent
	for _, a := range []string{"12.34567891", "0.1", "0.1", "0.1", "20999987.35432108"} {
		coins = append(coins, scanUnspent{Amount: json.Number(a), Height: 1})
	}
	if f, err := splitCoins(500, coins); err != nil || f.SpendableSats != 2099999999999999 {
		t.Errorf("WALLET-EXACT-SUM: %v (%v), want 2099999999999999", f, err)
	}
	if _, err := splitCoins(500, append(coins, scanUnspent{Amount: "0.00000002", Height: 1})); err == nil {
		t.Error("WALLET-EXACT-SUM: coins of more than 21 million were added up")
	}
}

// fakeNode is a node's RPC as the Wallet card asks it: getblockchaininfo, and scantxoutset start and
// abort, every call counted and each scan's params kept.
type fakeNode struct {
	t   *testing.T
	srv *httptest.Server

	mu        sync.Mutex
	blocks    int64
	headers   int64
	ibd       bool
	unspents  string        // the scan's unspents, JSON
	scanError string        // the error a scan answers with instead, JSON; "" for none
	stopped   bool          // the scan answers success false
	scanTakes time.Duration // a scan takes this long, or until the caller gives up
	padding   int           // bytes of padding in the scan's answer
	warmup    bool          // every call answers -28
	calls     map[string]int
	scans     []string // each scan start's params
	aborts    int
}

func newFakeNode(t *testing.T) *fakeNode {
	f := &fakeNode{t: t, blocks: 114, headers: 114, unspents: regtestUnspents, calls: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeNode) serve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	body, _ := io.ReadAll(r.Body)
	if json.Unmarshal(body, &req) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.calls[req.Method]++
	blocks, headers, ibd, warmup := f.blocks, f.headers, f.ibd, f.warmup
	unspents, scanError, stopped, takes, padding := f.unspents, f.scanError, f.stopped, f.scanTakes, f.padding
	isAbort := req.Method == "scantxoutset" && len(req.Params) > 0 && string(req.Params[0]) == `"abort"`
	if isAbort {
		f.aborts++
	} else if req.Method == "scantxoutset" {
		p, _ := json.Marshal(req.Params)
		f.scans = append(f.scans, string(p))
	}
	f.mu.Unlock()
	answer := func(result, rpcErr string) {
		if rpcErr != "" {
			w.WriteHeader(http.StatusInternalServerError)
		}
		if result == "" {
			result = "null"
		}
		if rpcErr == "" {
			rpcErr = "null"
		}
		fmt.Fprintf(w, `{"result":%s,"error":%s,"id":"api"}`, result, rpcErr)
	}
	switch {
	case warmup:
		answer("", `{"code":-28,"message":"Loading block index…"}`)
	case req.Method == "getblockchaininfo":
		answer(fmt.Sprintf(`{"chain":"regtest","blocks":%d,"headers":%d,"bestblockhash":"%064x","initialblockdownload":%v,"verificationprogress":1}`,
			blocks, headers, blocks, ibd), "")
	case isAbort:
		answer("true", "")
	case req.Method == "scantxoutset":
		select {
		case <-time.After(takes):
		case <-r.Context().Done():
			return
		}
		if scanError != "" {
			answer("", scanError)
			return
		}
		answer(fmt.Sprintf(`{"success":%v,"txouts":115,"height":%d,"bestblock":"%064x","unspents":%s,"total_amount":162.50000000,"pad":"%s"}`,
			!stopped, blocks, blocks, unspents, strings.Repeat("x", padding)), "")
	default:
		answer("", `{"code":-32601,"message":"Method not found"}`)
	}
}

func (f *fakeNode) set(change func(f *fakeNode)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f)
}

func (f *fakeNode) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[method]
}

func (f *fakeNode) scanParams() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.scans...)
}

// walletClock is the time the Wallet card reads, moved on by the test.
type walletClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *walletClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *walletClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// walletSetup is a database holding addr (and addr1175) in mode, the BCH2 node at node (nil: one
// that does not answer), the Wallet card as the api starts it, and its clock.
func walletSetup(t *testing.T, node *fakeNode, addr, addr1175, mode string) *walletClock {
	t.Helper()
	stats.CloseDB()
	if err := stats.InitDB(filepath.Join(t.TempDir(), "api.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stats.CloseDB)
	t.Setenv("POOL_ADDRESS", "")
	t.Setenv("PAYOUT_ADDRESS_1175", "")
	t.Setenv("MERGE_MINING_AVAILABLE", "")
	t.Setenv("AUX1175_URL", "")
	if err := stats.SavePoolSettings(addr, addr1175, "", mode); err != nil {
		t.Fatal(err)
	}
	if node != nil {
		useNode(t, node.srv.URL)
	} else {
		closed := httptest.NewServer(http.NotFoundHandler())
		closed.Close()
		useNode(t, closed.URL)
	}
	return freshWallet(t, time.Date(2026, 10, 12, 22, 54, 28, 0, time.UTC))
}

// freshWallet is the Wallet card as the api starts it, on a clock that reads now until the test
// moves it on.
func freshWallet(t *testing.T, now time.Time) *walletClock {
	t.Helper()
	clock := &walletClock{now: now}
	walletNow = clock.Now
	reset := func() {
		for _, n := range []*walletNode{bch2Wallet, aux1175Wallet} {
			n.mu.Lock()
			n.chain, n.chainAt, n.script, n.figures, n.bestblock = blockchainInfo{}, time.Time{}, nil, nil, ""
			n.scannedAt, n.retryAt = time.Time{}, time.Time{}
			n.mu.Unlock()
		}
	}
	reset()
	t.Cleanup(func() {
		walletNow = time.Now
		reset()
	})
	return clock
}

// useAux1175 points the 1175 login at node.
func useAux1175(t *testing.T, node *fakeNode) {
	t.Helper()
	t.Setenv("AUX1175_URL", node.srv.URL)
	t.Setenv("AUX1175_USER", "u")
	t.Setenv("AUX1175_PASSWORD", "p")
}

type walletAnswer struct {
	BCH2    *walletCoin `json:"bch2"`
	Aux1175 *walletCoin `json:"aux1175"`
}

func walletApp() *fiber.App {
	app := fiber.New()
	app.Get("/api/v1/wallet", getWallet)
	return app
}

func askWallet(t *testing.T, app *fiber.App, path string) (int, walletAnswer, string) {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest("GET", path, nil), 20000)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	var w walletAnswer
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(b, &w); err != nil {
			t.Fatalf("%s: %v (%s)", path, err, b)
		}
	}
	return resp.StatusCode, w, string(b)
}

func bch2Coin(t *testing.T, app *fiber.App) walletCoin {
	t.Helper()
	code, w, body := askWallet(t, app, "/api/v1/wallet")
	if code != http.StatusOK || w.BCH2 == nil {
		t.Fatalf("the answer has no bch2: %d %s", code, body)
	}
	return *w.BCH2
}

// The node is sent the script the stored address pays to, as raw(), and nothing else: the
// figures are the regtest proof's, in satoshis, with the blocks each maturing coin has to go.
func TestWalletAsksTheNodeForTheStoredAddressOnly(t *testing.T) {
	node := newFakeNode(t)
	walletSetup(t, node, walletTestAddress, "", stats.PayoutModeSolo)
	app := walletApp()
	c := bch2Coin(t, app)
	if c.State != walletOK || c.Message != "" || c.Address != walletTestAddress || c.Figures == nil {
		t.Fatalf("WALLET-OK: %+v", c)
	}
	f := c.Figures
	if f.Height != 114 || f.SpendableSats != 6250000000 || f.MaturingSats != 10000000000 || f.Outputs != 4 ||
		fmt.Sprint(f.Maturing) != "[{112 5000000000 98} {113 5000000000 99}]" {
		t.Errorf("WALLET-FIGURES: %+v", *f)
	}
	if !f.CheckedAt.Equal(time.Date(2026, 10, 12, 22, 54, 28, 0, time.UTC)) || f.CheckedAt.Location() != time.UTC {
		t.Errorf("WALLET-CHECKED-AT: %v, want the time of the scan in UTC", f.CheckedAt)
	}
	want := `["start",["raw(` + walletTestScript + `)"]]`
	if s := node.scanParams(); len(s) != 1 || s[0] != want {
		t.Fatalf("WALLET-PARAMS: the node was asked %v, want %s", s, want)
	}

	// The request takes nothing from the caller: an address in it is not looked up.
	other := testBCH2Address(9)
	if code, w, body := askWallet(t, app, "/api/v1/wallet?address="+other); code != http.StatusOK || w.BCH2 == nil || w.BCH2.Address != walletTestAddress {
		t.Errorf("WALLET-NO-INPUT: ?address= answered %d %s", code, body)
	}
	if code, _, _ := askWallet(t, app, "/api/v1/wallet/"+other); code != http.StatusNotFound {
		t.Errorf("WALLET-NO-INPUT: /wallet/<address> answered %d", code)
	}
	for _, p := range node.scanParams() {
		if p != want {
			t.Errorf("WALLET-NO-INPUT: the node was asked %s", p)
		}
	}
}

// What is stored is decoded here, and only the script it pays to reaches the node: a stored value
// that is not a BCH2 P2PKH address, in any spelling the decoder refuses, asks the node nothing.
func TestWalletLooksUpOnlyAnAddressItCanDecode(t *testing.T) {
	var h [20]byte
	for i := range h {
		h[i] = 0xa1
	}
	body := strings.TrimPrefix(walletTestAddress, "bitcoincashii:")
	for name, stored := range map[string]string{
		"a descriptor":     "addr(x)",
		"a descriptor end": walletTestAddress + ")#",
		"quotes":           `"` + walletTestAddress + `"`,
		"mixed case":       "bitcoincashii:" + strings.ToUpper(body[:10]) + body[10:],
		"P2SH":             cashaddr.Encode(cashaddr.MainnetPrefix, cashaddr.P2SH, h),
		"bad checksum":     walletTestAddress[:len(walletTestAddress)-1] + "3",
		"another prefix":   "bitcoincash:" + body,
	} {
		node := newFakeNode(t)
		walletSetup(t, node, stored, "", stats.PayoutModeSolo)
		c := bch2Coin(t, walletApp())
		if c.State != walletUnavailable || !strings.Contains(c.Message, "is not one Forge Solo can look up") || c.Figures != nil {
			t.Errorf("WALLET-DECODE %s (%q): %+v", name, stored, c)
		}
		if n := node.count("scantxoutset") + node.count("getblockchaininfo"); n != 0 {
			t.Errorf("WALLET-DECODE %s: the node was asked %d times", name, n)
		}
	}

	// POOL_ADDRESS is checked before it is used at all: an invalid one is no payout address.
	node := newFakeNode(t)
	walletSetup(t, node, "", "", stats.PayoutModeSolo)
	t.Setenv("POOL_ADDRESS", "addr("+walletTestAddress+")")
	if code, w, b := askWallet(t, walletApp(), "/api/v1/wallet"); code != http.StatusOK || w.BCH2 != nil || strings.TrimSpace(b) != "{}" {
		t.Errorf("WALLET-DECODE-ENV: %d %s", code, b)
	}
	if n := node.count("scantxoutset"); n != 0 {
		t.Errorf("WALLET-DECODE-ENV: the node was asked to scan %d times", n)
	}
}

// segwitAddress is a segwit address of hrp: version and program, with a bech32 or a bech32m checksum.
func segwitAddress(t *testing.T, hrp string, version byte, program []byte, bech32m bool) string {
	t.Helper()
	data, err := bech32.ConvertBits(program, 8, 5, true)
	if err != nil {
		t.Fatal(err)
	}
	data = append([]byte{version}, data...)
	encode := bech32.Encode
	if bech32m {
		encode = bech32.EncodeM
	}
	s, err := encode(hrp, data)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A 1175 address is decoded by BIP 173 and BIP 350: the witness version, the checksum it takes, and
// the program's length.
func TestWalletDecodes1175Addresses(t *testing.T) {
	p20, p32 := make([]byte, 20), make([]byte, 32)
	for i := range p32 {
		p32[i] = byte(i + 1)
	}
	copy(p20, p32)
	for _, c := range []struct {
		name, addr, want string
	}{
		{"v0 20 bytes", segwitAddress(t, "esf", 0, p20, false), "0014" + hex.EncodeToString(p20)},
		{"v0 32 bytes", segwitAddress(t, "esf", 0, p32, false), "0020" + hex.EncodeToString(p32)},
		{"v1 32 bytes", segwitAddress(t, "esf", 1, p32, true), "5120" + hex.EncodeToString(p32)},
		{"v16 2 bytes", segwitAddress(t, "esf", 16, p20[:2], true), "6002" + hex.EncodeToString(p20[:2])},
		{"upper case", strings.ToUpper(segwitAddress(t, "esf", 0, p20, false)), "0014" + hex.EncodeToString(p20)},
		{"v0 with bech32m", segwitAddress(t, "esf", 0, p20, true), ""},
		{"v1 with bech32", segwitAddress(t, "esf", 1, p32, false), ""},
		{"v0 of 21 bytes", segwitAddress(t, "esf", 0, append(p20, 1), false), ""},
		{"v1 of 41 bytes", segwitAddress(t, "esf", 1, make([]byte, 41), true), ""},
		{"v17", segwitAddress(t, "esf", 17, p32, true), ""},
		{"regtest", segwitAddress(t, "resf", 0, p20, false), ""},
		{"Bitcoin", segwitAddress(t, "bc", 0, p20, false), ""},
		{"a descriptor", "addr(" + segwitAddress(t, "esf", 0, p20, false) + ")", ""},
	} {
		got, err := segwitScript(c.addr, "esf")
		if c.want == "" && err == nil {
			t.Errorf("WALLET-DECODE-1175 %s: %s read as %x", c.name, c.addr, got)
		}
		if c.want != "" && (err != nil || hex.EncodeToString(got) != c.want) {
			t.Errorf("WALLET-DECODE-1175 %s: %s read as %x (%v), want %s", c.name, c.addr, got, err, c.want)
		}
	}
}

// A node that is syncing is not asked for the coins: the card says it syncs.
func TestWalletWaitsForTheNodeToSync(t *testing.T) {
	for name, change := range map[string]func(f *fakeNode){
		"initial block download": func(f *fakeNode) { f.ibd = true },
		"two blocks behind":      func(f *fakeNode) { f.blocks = f.headers - 2 },
		"no headers yet":         func(f *fakeNode) { f.blocks, f.headers = 0, 0 },
	} {
		node := newFakeNode(t)
		node.set(change)
		walletSetup(t, node, walletTestAddress, "", stats.PayoutModeSolo)
		c := bch2Coin(t, walletApp())
		if c.State != walletSyncing || c.Message != "The BCH2 node is still syncing: the coins at your payout address show here once it has caught up." {
			t.Errorf("WALLET-SYNCING %s: %+v", name, c)
		}
		if n := node.count("scantxoutset"); n != 0 {
			t.Errorf("WALLET-SYNCING %s: %d scans", name, n)
		}
	}
	// One block behind its headers is synced, as on the dashboard's banner.
	node := newFakeNode(t)
	node.set(func(f *fakeNode) { f.headers = f.blocks + 1 })
	walletSetup(t, node, walletTestAddress, "", stats.PayoutModeSolo)
	if c := bch2Coin(t, walletApp()); c.State != walletOK {
		t.Errorf("WALLET-SYNCING-SLACK: %+v", c)
	}
}

// The node is scanned once per block, never twice within half a minute, and at once for a new
// address, whose answer replaces the old one's.
func TestWalletScansOncePerBlock(t *testing.T) {
	node := newFakeNode(t)
	clock := walletSetup(t, node, walletTestAddress, "", stats.PayoutModeSolo)
	app := walletApp()
	first := bch2Coin(t, app)
	for i := 0; i < 19; i++ {
		if c := bch2Coin(t, app); c.State != walletOK || !c.Figures.CheckedAt.Equal(first.Figures.CheckedAt) {
			t.Fatalf("WALLET-CACHE: request %d: %+v", i+2, c)
		}
	}
	if n, m := node.count("scantxoutset"), node.count("getblockchaininfo"); n != 1 || m != 1 {
		t.Fatalf("WALLET-CACHE: 20 requests at one tip made %d scans and %d chain reads, want 1 and 1", n, m)
	}

	// A block later, within half a minute: the figures of the block before.
	node.set(func(f *fakeNode) { f.blocks, f.headers = 115, 115 })
	clock.advance(6 * time.Second)
	if c := bch2Coin(t, app); c.State != walletOK || c.Figures.Height != 114 || node.count("scantxoutset") != 1 {
		t.Fatalf("WALLET-CACHE-FLOOR: %+v, %d scans", c, node.count("scantxoutset"))
	}
	if n := node.count("getblockchaininfo"); n != 2 {
		t.Errorf("WALLET-CACHE-CHAIN: %d chain reads after 6 s, want 2", n)
	}
	clock.advance(25 * time.Second)
	if c := bch2Coin(t, app); c.State != walletOK || c.Figures.Height != 115 || node.count("scantxoutset") != 2 {
		t.Fatalf("WALLET-CACHE-NEXT: %+v, %d scans", c, node.count("scantxoutset"))
	}

	// Another address is scanned at once, and the old one's figures are not shown for it.
	other := testBCH2Address(9)
	if err := stats.SavePoolSettings(other, "", "", stats.PayoutModeSolo); err != nil {
		t.Fatal(err)
	}
	node.set(func(f *fakeNode) { f.scanError = `{"code":-1,"message":"something went wrong"}` })
	c := bch2Coin(t, app)
	if c.Address != other || c.State != walletUnavailable || c.Figures != nil || node.count("scantxoutset") != 3 {
		t.Fatalf("WALLET-CACHE-NEW-ADDRESS: %+v, %d scans", c, node.count("scantxoutset"))
	}
	if c.Message != "Forge Solo cannot ask the BCH2 node right now: something went wrong. It asks again in a minute." {
		t.Errorf("WALLET-NODE-ERROR: %q", c.Message)
	}
	script, _ := bch2Script(other)
	if s := node.scanParams(); s[len(s)-1] != `["start",["raw(`+hex.EncodeToString(script)+`)"]]` {
		t.Errorf("WALLET-CACHE-NEW-ADDRESS: the node was asked %s", s[len(s)-1])
	}
}

// Requests that come while the node is being asked wait for its answer: one scan for all of them.
func TestWalletAsksOneNodeOneQuestionAtATime(t *testing.T) {
	node := newFakeNode(t)
	node.set(func(f *fakeNode) { f.scanTakes = 300 * time.Millisecond })
	walletSetup(t, node, walletTestAddress, "", stats.PayoutModeSolo)
	app := walletApp()
	var wg sync.WaitGroup
	got := make([]walletCoin, 20)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, err := app.Test(httptest.NewRequest("GET", "/api/v1/wallet", nil), 20000)
			if err != nil {
				return
			}
			var w walletAnswer
			if json.NewDecoder(resp.Body).Decode(&w) == nil && w.BCH2 != nil {
				got[i] = *w.BCH2
			}
		}(i)
	}
	wg.Wait()
	for i, c := range got {
		if c.State != walletOK || c.Figures == nil || c.Figures.SpendableSats != 6250000000 || !c.Figures.CheckedAt.Equal(got[0].Figures.CheckedAt) {
			t.Errorf("WALLET-SINGLE-FLIGHT: request %d: %+v", i, c)
		}
	}
	if n := node.count("scantxoutset"); n != 1 {
		t.Errorf("WALLET-SINGLE-FLIGHT: 20 requests at once made %d scans", n)
	}
}

// A scan the node refuses, or one stopped part way, is said, and the last figures stay on the card.
func TestWalletKeepsItsFiguresWhenAScanFails(t *testing.T) {
	node := newFakeNode(t)
	clock := walletSetup(t, node, walletTestAddress, "", stats.PayoutModeSolo)
	app := walletApp()
	bch2Coin(t, app)
	for _, c := range []struct {
		code, why string
		change    func(f *fakeNode)
	}{
		{"WALLET-BUSY", "another scan of the node's coins is running", func(f *fakeNode) {
			f.scanError = `{"code":-8,"message":"Scan already in progress, use action \"abort\" or \"status\""}`
		}},
		{"WALLET-STOPPED", "the scan was stopped before it finished", func(f *fakeNode) { f.scanError, f.stopped = "", true }},
	} {
		node.set(func(f *fakeNode) { f.blocks, f.headers = f.blocks+1, f.headers+1 })
		node.set(c.change)
		clock.advance(31 * time.Second)
		got := bch2Coin(t, app)
		want := "Forge Solo cannot ask the BCH2 node right now: " + c.why + ". It asks again in a minute."
		if got.State != walletUnavailable || got.Message != want || got.Figures == nil || got.Figures.Height != 114 {
			t.Errorf("%s: %+v", c.code, got)
		}
	}
}

// A scan that takes longer than the card waits is stopped, and the node is not asked again for two
// minutes.
func TestWalletStopsAScanThatTakesTooLong(t *testing.T) {
	saved := walletScanTimeout
	walletScanTimeout = 50 * time.Millisecond
	t.Cleanup(func() { walletScanTimeout = saved })
	node := newFakeNode(t)
	node.set(func(f *fakeNode) { f.scanTakes = 10 * time.Second })
	clock := walletSetup(t, node, walletTestAddress, "", stats.PayoutModeSolo)
	app := walletApp()
	want := "Forge Solo cannot ask the BCH2 node right now: the node did not finish in a minute. It asks again in a minute."
	if c := bch2Coin(t, app); c.State != walletUnavailable || c.Message != want {
		t.Fatalf("WALLET-TIMEOUT: %+v", c)
	}
	var aborts int
	node.set(func(f *fakeNode) { aborts = f.aborts })
	if aborts != 1 {
		t.Errorf("WALLET-TIMEOUT-ABORT: %d aborts sent, want 1", aborts)
	}
	node.set(func(f *fakeNode) { f.scanTakes = 0 })
	clock.advance(time.Minute)
	if c := bch2Coin(t, app); c.State != walletUnavailable || c.Message != want || node.count("scantxoutset") != 2 {
		t.Errorf("WALLET-TIMEOUT-WAIT: %+v, %d scantxoutset calls (the scan and its abort)", c, node.count("scantxoutset"))
	}
	clock.advance(61 * time.Second)
	if c := bch2Coin(t, app); c.State != walletOK || node.count("scantxoutset") != 3 {
		t.Errorf("WALLET-TIMEOUT-AGAIN: %+v, %d scantxoutset calls", c, node.count("scantxoutset"))
	}
}

// A node that does not answer, one that is starting, and an answer too large to read are each said.
func TestWalletSaysWhyTheNodeCannotBeAsked(t *testing.T) {
	walletSetup(t, nil, walletTestAddress, "", stats.PayoutModeSolo)
	if c := bch2Coin(t, walletApp()); c.State != walletUnavailable ||
		c.Message != "Forge Solo cannot ask the BCH2 node right now: the node is not answering. It asks again in a minute." {
		t.Errorf("WALLET-NODE-DOWN: %+v", c)
	}

	node := newFakeNode(t)
	node.set(func(f *fakeNode) { f.warmup = true })
	walletSetup(t, node, walletTestAddress, "", stats.PayoutModeSolo)
	if c := bch2Coin(t, walletApp()); c.State != walletUnavailable ||
		c.Message != "Forge Solo cannot ask the BCH2 node right now: the node is starting (Loading block index…). It asks again in a minute." {
		t.Errorf("WALLET-NODE-STARTING: %+v", c)
	}

	saved := rpcMaxAnswer
	rpcMaxAnswer = 4 << 10
	t.Cleanup(func() { rpcMaxAnswer = saved })
	node = newFakeNode(t)
	node.set(func(f *fakeNode) { f.padding = 8 << 10 })
	walletSetup(t, node, walletTestAddress, "", stats.PayoutModeSolo)
	if c := bch2Coin(t, walletApp()); c.State != walletUnavailable ||
		c.Message != "Forge Solo cannot ask the BCH2 node right now: the node's answer is larger than 32 MB. It asks again in a minute." {
		t.Errorf("WALLET-TOO-BIG: %+v", c)
	}
}

// With no payout address there is nothing to look up, and no node is asked.
func TestWalletWithoutAnAddressAsksNothing(t *testing.T) {
	node := newFakeNode(t)
	walletSetup(t, node, "", "", stats.PayoutModeSolo)
	useAux1175(t, node)
	if code, _, body := askWallet(t, walletApp(), "/api/v1/wallet"); code != http.StatusOK || strings.TrimSpace(body) != "{}" {
		t.Errorf("WALLET-NO-ADDRESS: %d %s", code, body)
	}
	if n := node.count("getblockchaininfo") + node.count("scantxoutset"); n != 0 {
		t.Errorf("WALLET-NO-ADDRESS: the node was asked %d times", n)
	}
}

// The 1175 address's coins come from the 1175 node, where this app runs one (Umbrel, Windows), in
// TIDES mode too: the coins are there whatever the mode. Where it runs none (Linux), there is no
// 1175 row, whatever the database holds.
func TestWalletShowsThe1175AddressWhereThereIsA1175Node(t *testing.T) {
	program := make([]byte, 20)
	for i := range program {
		program[i] = 0xee
	}
	esf := segwitAddress(t, "esf", 0, program, false)
	want := `["start",["raw(0014` + hex.EncodeToString(program) + `)"]]`
	for _, mode := range []string{stats.PayoutModeSolo, stats.PayoutModeTides} {
		node, aux := newFakeNode(t), newFakeNode(t)
		clock := walletSetup(t, node, walletTestAddress, esf, mode)
		useAux1175(t, aux)
		code, w, body := askWallet(t, walletApp(), "/api/v1/wallet")
		if code != http.StatusOK || w.BCH2 == nil || w.Aux1175 == nil || w.Aux1175.Address != esf || w.Aux1175.State != walletOK ||
			w.Aux1175.Figures == nil || w.Aux1175.Figures.SpendableSats != 6250000000 {
			t.Fatalf("WALLET-1175 %s: %d %s", mode, code, body)
		}
		if s := aux.scanParams(); len(s) != 1 || s[0] != want {
			t.Errorf("WALLET-1175-PARAMS %s: the 1175 node was asked %v, want %s", mode, s, want)
		}
		if s := node.scanParams(); len(s) != 1 || s[0] != `["start",["raw(`+walletTestScript+`)"]]` {
			t.Errorf("WALLET-1175-BCH2 %s: the BCH2 node was asked %v", mode, s)
		}

		// The 1175 node syncing says so on its own row.
		aux.set(func(f *fakeNode) { f.ibd = true })
		clock.advance(6 * time.Second)
		if _, w, _ := askWallet(t, walletApp(), "/api/v1/wallet"); w.Aux1175 == nil || w.Aux1175.State != walletSyncing ||
			w.Aux1175.Message != "The 1175 node is still syncing: the coins at your 1175 address show here once it has caught up." ||
			w.BCH2.State != walletOK {
			t.Errorf("WALLET-1175-SYNCING %s: %+v", mode, w.Aux1175)
		}
	}

	// No 1175 node (Forge Solo for Linux): no 1175 row, and the 1175 address is not looked up.
	node, aux := newFakeNode(t), newFakeNode(t)
	walletSetup(t, node, walletTestAddress, esf, stats.PayoutModeSolo)
	useAux1175(t, aux)
	t.Setenv("MERGE_MINING_AVAILABLE", "0")
	if code, w, body := askWallet(t, walletApp(), "/api/v1/wallet"); code != http.StatusOK || w.BCH2 == nil || w.Aux1175 != nil {
		t.Errorf("WALLET-1175-NO-NODE: %d %s", code, body)
	}
	if n := aux.count("getblockchaininfo") + aux.count("scantxoutset"); n != 0 {
		t.Errorf("WALLET-1175-NO-NODE: the 1175 node was asked %d times", n)
	}

	// A 1175 address the decoder refuses is said on its row, and its node is not asked.
	node, aux = newFakeNode(t), newFakeNode(t)
	walletSetup(t, node, walletTestAddress, segwitAddress(t, "esf", 0, append(program, 1), false), stats.PayoutModeSolo)
	useAux1175(t, aux)
	if _, w, body := askWallet(t, walletApp(), "/api/v1/wallet"); w.Aux1175 == nil || w.Aux1175.State != walletUnavailable ||
		w.Aux1175.Message != "The 1175 address in Settings is not one Forge Solo can look up: it must be an esf1… address with a valid witness program." {
		t.Errorf("WALLET-1175-DECODE: %s", body)
	}
	if n := aux.count("getblockchaininfo") + aux.count("scantxoutset"); n != 0 {
		t.Errorf("WALLET-1175-DECODE: the 1175 node was asked %d times", n)
	}
}

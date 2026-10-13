package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/segwit"
	"github.com/BitcoincashII/forge-solo/internal/stats"
	"github.com/btcsuite/btcd/btcutil/bech32"
)

// The keys of the regtest chains below: public keys 1 (secp256k1's generator) and 2, private key 1
// as a regtest WIF, and the extended public key of Bitcoin Core's own scantxoutset test, whose
// 200,000 addresses make a scan that runs for seconds.
const (
	itKey1Pub  = "0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"
	itKey2Pub  = "03c6047f9441ed7d6d3045406e95c07cd85c778e4b8cef3ca7abac09b95c709ee5"
	itKey1WIF  = "cMahea7zqjxrtgAbB7LSGbcQUr1uX1ojuat9jZodMN87JcbXMTcA"
	itLongScan = "tpubD6NzVbkrYhZ4WaWSyoBvQwbpLkojyoTZPRsgXELWz3Popb3qkjcJyJUGLnL4qHHoQvao8ESaAstxYSnhyswJ76uZPStJRJCTKvosUCJZL5B"
)

// itNode is a regtest node the test drives over RPC.
type itNode struct {
	t               *testing.T
	url, user, pass string
}

func (n itNode) call(method string, params ...interface{}) json.RawMessage {
	n.t.Helper()
	if params == nil {
		params = []interface{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	raw, err := rpcCallCtx(ctx, n.url, n.user, n.pass, method, params)
	if err != nil {
		n.t.Fatalf("%s %v: %v", method, params, err)
	}
	return raw
}

func (n itNode) str(method string, params ...interface{}) string {
	n.t.Helper()
	var s string
	if err := json.Unmarshal(n.call(method, params...), &s); err != nil {
		n.t.Fatalf("%s: %v", method, err)
	}
	return s
}

// address is the node's address for descriptor desc.
func (n itNode) address(desc string) string {
	n.t.Helper()
	var info struct {
		Descriptor string `json:"descriptor"`
	}
	if err := json.Unmarshal(n.call("getdescriptorinfo", desc), &info); err != nil {
		n.t.Fatal(err)
	}
	var addrs []string
	if err := json.Unmarshal(n.call("deriveaddresses", info.Descriptor), &addrs); err != nil || len(addrs) != 1 {
		n.t.Fatalf("deriveaddresses %s: %v %v", info.Descriptor, addrs, err)
	}
	return addrs[0]
}

// coinbaseAt is the coinbase output at height that descriptor desc holds.
func (n itNode) coinbaseAt(desc string, height int64) (txid string, vout int, sats int64) {
	n.t.Helper()
	var scan struct {
		Unspents []struct {
			TxID   string      `json:"txid"`
			Vout   int         `json:"vout"`
			Amount json.Number `json:"amount"`
			Height int64       `json:"height"`
		} `json:"unspents"`
	}
	if err := json.Unmarshal(n.call("scantxoutset", "start", []string{desc}), &scan); err != nil {
		n.t.Fatal(err)
	}
	for _, u := range scan.Unspents {
		if u.Height == height {
			s, err := satsFromAmount(u.Amount)
			if err != nil {
				n.t.Fatal(err)
			}
			return u.TxID, u.Vout, s
		}
	}
	n.t.Fatalf("no coin of %s at height %d", desc, height)
	return "", 0, 0
}

// pay sends 12.5 coins to to from the coinbase desc holds at height 2, the rest less a fee to change.
// wif signs it; with none, the coin needs no key (raw(51)).
func (n itNode) pay(desc, to, change, wif string) string {
	n.t.Helper()
	txid, vout, sats := n.coinbaseAt(desc, 2)
	coins := func(s int64) json.Number { return json.Number(fmt.Sprintf("%d.%08d", s/1e8, s%1e8)) }
	tx := n.str("createrawtransaction", []map[string]interface{}{{"txid": txid, "vout": vout}},
		map[string]json.Number{to: coins(1_250_000_000), change: coins(sats - 1_250_000_000 - 10_000)})
	if wif != "" {
		var signed struct {
			Hex      string `json:"hex"`
			Complete bool   `json:"complete"`
		}
		if err := json.Unmarshal(n.call("signrawtransactionwithkey", tx, []string{wif}), &signed); err != nil || !signed.Complete {
			n.t.Fatalf("signrawtransactionwithkey: %+v %v", signed, err)
		}
		tx = signed.Hex
	}
	return n.str("sendrawtransaction", tx)
}

func itEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Fatalf("%s is not set (scripts/it-wallet.sh sets it)", name)
	}
	return v
}

// TestWalletAgainstRegtestNodes_Live asks a BCH2 v27.0.2 node and a 1175 v29.1.0 node, each on a
// regtest chain of its own and with its wallet off, as Forge Solo runs them, through the api's own
// handler. Each chain pays the address under test a coinbase at height 1, which can be spent, two at
// heights 112 and 113, which cannot yet, and a payment of 12.5 coins. On the BCH2 chain the other
// coinbases pay key 1, which signs the payment; on the 1175 chain they pay a script that needs no key
// (raw(51)), as 1175's regtest keys are not written as Bitcoin's.
//
// Gated on WALLET_IT_BCH2_URL. Run it with ./scripts/it-wallet.sh, which starts the two nodes the
// compose pins (or BCH2_BIN_DIR's and ESF_BIN_DIR's) and fails if this test skips.
func TestWalletAgainstRegtestNodes_Live(t *testing.T) {
	if os.Getenv("WALLET_IT_BCH2_URL") == "" {
		t.Skip("WALLET_IT_BCH2_URL not set; skipping the Wallet card against live regtest nodes")
	}
	bch2 := itNode{t, itEnv(t, "WALLET_IT_BCH2_URL"), itEnv(t, "WALLET_IT_BCH2_USER"), itEnv(t, "WALLET_IT_BCH2_PASSWORD")}
	esf := itNode{t, itEnv(t, "WALLET_IT_1175_URL"), itEnv(t, "WALLET_IT_1175_USER"), itEnv(t, "WALLET_IT_1175_PASSWORD")}

	// BCH2: regtest addresses are written as mainnet ones, so the payout address is the test address.
	key1 := bch2.address("pkh(" + itKey1Pub + ")")
	key1Desc := "raw(" + scriptHex(t, bch2Script, key1) + ")"
	bch2.call("generatetoaddress", 1, walletTestAddress)
	bch2.call("generatetoaddress", 110, key1)
	bch2.call("generatetoaddress", 2, walletTestAddress)
	bch2.pay(key1Desc, walletTestAddress, key1, itKey1WIF)

	// 1175: the payout address is key 2's, read as the mainnet esf1 address of the same script.
	key2 := esf.address("wpkh(" + itKey2Pub + ")")
	change := esf.address("wpkh(" + itKey1Pub + ")")
	hrp := key2[:strings.LastIndexByte(key2, '1')]
	script, err := segwit.Script(key2, hrp)
	if err != nil {
		t.Fatalf("the 1175 node's address %s: %v", key2, err)
	}
	data, _ := bech32.ConvertBits(script[2:], 8, 5, true)
	payout1175, err := bech32.Encode("esf", append([]byte{0}, data...))
	if err != nil {
		t.Fatal(err)
	}
	esf.call("generatetoaddress", 1, key2)
	esf.call("generatetodescriptor", 110, "raw(51)")
	esf.call("generatetoaddress", 2, key2)
	esf.pay("raw(51)", key2, change, "")

	if err := stats.InitDB(filepath.Join(t.TempDir(), "api.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stats.CloseDB)
	t.Setenv("POOL_ADDRESS", "")
	t.Setenv("PAYOUT_ADDRESS_1175", "")
	t.Setenv("MERGE_MINING_AVAILABLE", "")
	t.Setenv("AUX1175_URL", esf.url)
	t.Setenv("AUX1175_USER", esf.user)
	t.Setenv("AUX1175_PASSWORD", esf.pass)
	savedURL, savedUser, savedPass := rpcURL, rpcUser, rpcPass
	rpcURL, rpcUser, rpcPass = bch2.url, bch2.user, bch2.pass
	t.Cleanup(func() { rpcURL, rpcUser, rpcPass = savedURL, savedUser, savedPass })
	if err := stats.SavePoolSettings(walletTestAddress, payout1175, "", stats.PayoutModeSolo); err != nil {
		t.Fatal(err)
	}
	clock := freshWallet(t, time.Now())
	app := walletApp()
	ask := func(code string) (walletCoin, walletCoin) {
		t.Helper()
		status, w, body := askWallet(t, app, "/api/v1/wallet")
		if status != 200 || w.BCH2 == nil || w.Aux1175 == nil {
			t.Fatalf("%s: %d %s", code, status, body)
		}
		return *w.BCH2, *w.Aux1175
	}
	check := func(code, coin string, c walletCoin, height, spendable, maturing int64, outputs int, rows string) {
		t.Helper()
		f := c.Figures
		if c.State != walletOK || f == nil || f.Height != height || f.SpendableSats != spendable || f.MaturingSats != maturing ||
			f.Outputs != outputs || fmt.Sprint(f.Maturing) != rows {
			t.Errorf("%s %s: %+v %+v", code, coin, c, f)
		}
	}

	// The payment waits in the mempool: the card shows coins in blocks only.
	b, e := ask("WALLET-IT-MEMPOOL")
	check("WALLET-IT-MEMPOOL", "BCH2", b, 113, 50_0000_0000, 100_0000_0000, 3, "[{112 5000000000 99} {113 5000000000 100}]")
	check("WALLET-IT-MEMPOOL", "1175", e, 113, 50_0000_0000, 100_0000_0000, 3, "[{112 5000000000 99} {113 5000000000 100}]")

	// A block includes it.
	bch2.call("generatetoaddress", 1, key1)
	esf.call("generatetodescriptor", 1, "raw(51)")
	clock.advance(walletScanFloor + time.Second)
	b, e = ask("WALLET-IT-SPLIT")
	check("WALLET-IT-SPLIT", "BCH2", b, 114, 62_5000_0000, 100_0000_0000, 4, "[{112 5000000000 98} {113 5000000000 99}]")
	check("WALLET-IT-SPLIT", "1175", e, 114, 62_5000_0000, 100_0000_0000, 4, "[{112 5000000000 98} {113 5000000000 99}]")

	// At the same tip the scan is not made again, however long after.
	clock.advance(walletScanFloor + time.Second)
	b2, e2 := ask("WALLET-IT-CACHE")
	if !b2.Figures.CheckedAt.Equal(b.Figures.CheckedAt) || !e2.Figures.CheckedAt.Equal(e.Figures.CheckedAt) {
		t.Errorf("WALLET-IT-CACHE: a second request at the same tip scanned again: %v / %v", b2.Figures.CheckedAt, e2.Figures.CheckedAt)
	}

	// While another scan of the node's coins runs, the node refuses one: said, with the last figures.
	bch2.call("generatetoaddress", 1, key1)
	esf.call("generatetodescriptor", 1, "raw(51)")
	var wg sync.WaitGroup
	for _, long := range []struct {
		n    itNode
		desc string
	}{{bch2, "pkh(" + itLongScan + "/*)"}, {esf, "wpkh(" + itLongScan + "/*)"}} {
		wg.Add(1)
		go func(n itNode, desc string) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			_, _ = rpcCallCtx(ctx, n.url, n.user, n.pass, "scantxoutset",
				[]interface{}{"start", []interface{}{map[string]interface{}{"desc": desc, "range": []int{0, 199999}}}})
		}(long.n, long.desc)
	}
	time.Sleep(300 * time.Millisecond)
	clock.advance(walletScanFloor + time.Second)
	b, e = ask("WALLET-IT-BUSY")
	for coin, c := range map[string]walletCoin{"BCH2": b, "1175": e} {
		if c.State != walletUnavailable || !strings.Contains(c.Message, "another scan of the node's coins is running") ||
			c.Figures == nil || c.Figures.Height != 114 {
			t.Errorf("WALLET-IT-BUSY %s: %+v", coin, c)
		}
	}
	wg.Wait()
	clock.advance(walletScanFloor + time.Second)
	b, e = ask("WALLET-IT-AFTER")
	check("WALLET-IT-AFTER", "BCH2", b, 115, 62_5000_0000, 100_0000_0000, 4, "[{112 5000000000 97} {113 5000000000 98}]")
	check("WALLET-IT-AFTER", "1175", e, 115, 62_5000_0000, 100_0000_0000, 4, "[{112 5000000000 97} {113 5000000000 98}]")
}

// scriptHex is the script decode reads addr as, in hex.
func scriptHex(t *testing.T, decode func(string) ([]byte, error), addr string) string {
	t.Helper()
	s, err := decode(addr)
	if err != nil {
		t.Fatalf("%s: %v", addr, err)
	}
	return fmt.Sprintf("%x", s)
}

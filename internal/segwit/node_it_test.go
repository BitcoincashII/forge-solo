package segwit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcutil/bech32"
)

// nodeVector is an address to put to the node, and what it is.
type nodeVector struct{ name, addr string }

// nodeVectors are addresses of every kind for the chain whose human-readable part is hrp: each
// witness version that matters with programs of the lengths around every limit and both checksums,
// then case, typos, spaces, padding and other chains' addresses.
func nodeVectors(t *testing.T, hrp string) []nodeVector {
	t.Helper()
	prog := func(n int) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = byte((i*7 + 3) % 256)
		}
		return b
	}
	encode := func(hrp string, data []byte, bech32m bool) string {
		f := bech32.Encode
		if bech32m {
			f = bech32.EncodeM
		}
		s, err := f(hrp, data)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	addr := func(hrp string, version byte, program []byte, bech32m bool) string {
		data, err := bech32.ConvertBits(program, 8, 5, true)
		if err != nil {
			t.Fatal(err)
		}
		return encode(hrp, append([]byte{version}, data...), bech32m)
	}
	var out []nodeVector
	for _, v := range []byte{0, 1, 2, 3, 15, 16, 17, 31} {
		for _, n := range []int{0, 1, 2, 16, 19, 20, 21, 31, 32, 33, 40, 41, 50} {
			for _, m := range []bool{false, true} {
				out = append(out, nodeVector{fmt.Sprintf("v%d %d bytes bech32m=%v", v, n, m), addr(hrp, v, prog(n), m)})
			}
		}
	}
	good := addr(hrp, 0, prog(20), false)
	tap := addr(hrp, 1, prog(32), true)
	swapped := []byte(good)
	swapped[8], swapped[9] = swapped[9], swapped[8]
	changed := []byte(good)
	if changed[len(changed)-1] == 'q' {
		changed[len(changed)-1] = 'p'
	} else {
		changed[len(changed)-1] = 'q'
	}
	out = append(out,
		nodeVector{"v0 upper case", strings.ToUpper(good)},
		nodeVector{"v1 upper case", strings.ToUpper(tap)},
		nodeVector{"mixed case", strings.ToUpper(good[:1]) + good[1:]},
		nodeVector{"one character changed", string(changed)},
		nodeVector{"two characters swapped", string(swapped)},
		nodeVector{"a space before", " " + good},
		nodeVector{"a space after", good + " "},
		nodeVector{"the human-readable part alone", hrp + "1"},
		nodeVector{"empty", ""},
		nodeVector{"a descriptor", "addr(" + good + ")"},
		nodeVector{"19 zero bytes", addr(hrp, 0, make([]byte, 19), false)},
		nodeVector{"20 zero bytes", addr(hrp, 0, make([]byte, 20), false)},
	)
	// Padding: 19 bytes leave 3 bits over in 31 groups, set one; 20 bytes with a spare zero group.
	d19, _ := bech32.ConvertBits(prog(19), 8, 5, true)
	d19[len(d19)-1] |= 1
	out = append(out, nodeVector{"padding that is not zero", encode(hrp, append([]byte{0}, d19...), false)})
	d20, _ := bech32.ConvertBits(prog(20), 8, 5, true)
	out = append(out, nodeVector{"5 bits of padding", encode(hrp, append(append([]byte{0}, d20...), 0), false)})
	for _, other := range []string{"esf", "resf", "tesf", "bc", "bcrt", "tb"} {
		if other != hrp {
			out = append(out, nodeVector{other + "1 v0", addr(other, 0, prog(20), false)}, nodeVector{other + "1 v1", addr(other, 1, prog(32), true)})
		}
	}
	return out
}

// rpcCall asks the node at url method with params: its result, or its error.
func rpcCall(t *testing.T, url, user, pass, method string, params ...any) (json.RawMessage, *struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "1.0", "id": "segwit", "method": method, "params": params})
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(user, pass)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	defer resp.Body.Close()
	var r struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("%s: HTTP %d, no JSON-RPC answer: %v", method, resp.StatusCode, err)
	}
	return r.Result, r.Error
}

// TestScriptAgreesWithThe1175Node_Live puts every kind of address to a 1175 v29.1.0 regtest node
// past AuxPoW activation (scripts/it-1175.sh starts one): Script takes an address exactly when the
// node's validateaddress calls it valid and its getauxblock gives work paying it, and refuses one
// exactly when getauxblock refuses it as an invalid payout address (-5). The api's Settings, the
// Wallet card and the stratum check a 1175 address with Script, so what they take, the node pays.
// Regtest addresses are resf1…; the mainnet node's validateaddress and getauxblock gave the same
// verdicts for the same vectors as esf1… addresses when this was written.
func TestScriptAgreesWithThe1175Node_Live(t *testing.T) {
	url := os.Getenv("MM_REGTEST_RPC")
	if url == "" {
		t.Skip("MM_REGTEST_RPC not set; skipping the 1175 address check against a live regtest node")
	}
	user, pass := os.Getenv("MM_REGTEST_USER"), os.Getenv("MM_REGTEST_PASS")
	const hrp = "resf"
	taken, refused := 0, 0
	for _, v := range nodeVectors(t, hrp) {
		_, ours := Script(v.addr, hrp)
		raw, rerr := rpcCall(t, url, user, pass, "validateaddress", v.addr)
		var va struct {
			IsValid bool `json:"isvalid"`
		}
		if rerr != nil || json.Unmarshal(raw, &va) != nil {
			t.Fatalf("SEGWIT-NODE-VALIDATE %s: validateaddress %q answered %s %+v", v.name, v.addr, raw, rerr)
		}
		work, gerr := rpcCall(t, url, user, pass, "getauxblock", v.addr)
		var w struct {
			Hash string `json:"hash"`
		}
		gave := gerr == nil && json.Unmarshal(work, &w) == nil && w.Hash != ""
		switch {
		case ours == nil && (!va.IsValid || !gave):
			t.Errorf("SEGWIT-NODE-TAKES %s: Script takes %q; the node's validateaddress says %v, getauxblock %s %+v", v.name, v.addr, va.IsValid, work, gerr)
		case ours != nil && (va.IsValid || gave || gerr == nil || gerr.Code != -5):
			t.Errorf("SEGWIT-NODE-REFUSES %s: Script refuses %q (%v); the node's validateaddress says %v, getauxblock %s %+v", v.name, v.addr, ours, va.IsValid, work, gerr)
		case ours == nil:
			taken++
		default:
			refused++
		}
	}
	t.Logf("%d addresses taken by Script and the node, %d refused by both", taken, refused)
	if taken < 40 || refused < 100 {
		t.Errorf("SEGWIT-NODE-COVER: %d taken and %d refused: the vectors no longer cover both sides", taken, refused)
	}
}

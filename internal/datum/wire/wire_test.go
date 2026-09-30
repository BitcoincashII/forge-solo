package wire

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/mining"
	"github.com/BitcoincashII/forge-solo/internal/tides"
)

const (
	addrA = "bitcoincashii:qqrek3md53495f9u7vjszc9xp8qcm0xwyy2e7gqtxg"
	addrB = "bitcoincashii:pqrek3md53495f9u7vjszc9xp8qcm0xwyyaur88ga4"
)

// The push is the pool's own (mining.HeightScript) and the minimal script number the node requires.
func TestHeightPush(t *testing.T) {
	for _, tc := range []struct {
		h    int64
		want string
	}{{83216, "03104501"}, {128, "028000"}, {127, "017f"}, {32768, "03008000"}, {8388608, "0400008000"}} {
		if got := hex.EncodeToString(HeightPush(tc.h)); got != tc.want {
			t.Errorf("HEIGHT-PUSH: %d -> %s, want %s", tc.h, got, tc.want)
		}
	}
	for _, h := range []int64{1, 16, 17, 255, 256, 65535, 65536, 83216, 16777215, 16777216, 1<<31 - 1} {
		if !bytes.Equal(HeightPush(h), mining.HeightScript(h)) {
			t.Errorf("HEIGHT-PUSH-POOL: %d differs from the pool's coinbase", h)
		}
	}
}

func buildOK(t *testing.T) (string, string, []tides.Output) {
	t.Helper()
	outs := []tides.Output{{Address: addrA, Sats: 1000}, {Address: addrB, Sats: 2000}}
	c1, c2, err := BuildCoinbase(83216, []byte("/Forge DATUM/"), outs)
	if err != nil {
		t.Fatal(err)
	}
	return c1, c2, outs
}

func TestCoinbaseRoundTrip(t *testing.T) {
	c1, c2, outs := buildOK(t)
	cb, err := ParseCoinbase(c1, c2)
	if err != nil {
		t.Fatalf("ROUNDTRIP: %v", err)
	}
	if cb.Height != 83216 || string(cb.Tag) != "/Forge DATUM/" || cb.Value() != 3000 || cb.Size != len(c1)/2+ExtranonceSize+len(c2)/2 {
		t.Fatalf("ROUNDTRIP: %+v", cb)
	}
	if err := cb.PaysExactly(outs); err != nil {
		t.Fatalf("ROUNDTRIP: %v", err)
	}
	if hex.EncodeToString(cb.Outputs[1].Script) != "a914079b476da46a5a24bcf3250160a609c18dbcce2187" {
		t.Fatalf("ROUNDTRIP: P2SH script %x", cb.Outputs[1].Script)
	}
}

// PaysExactly accepts only the split itself: value, order and script all matter.
func TestPaysExactlyRefusesAnyDifference(t *testing.T) {
	c1, c2, outs := buildOK(t)
	cb, _ := ParseCoinbase(c1, c2)
	for _, tc := range []struct {
		name string
		want []tides.Output
	}{
		{"PAYS-FEWER", outs[:1]},
		{"PAYS-VALUE", []tides.Output{{Address: addrA, Sats: 1001}, {Address: addrB, Sats: 1999}}},
		{"PAYS-ORDER", []tides.Output{{Address: addrB, Sats: 2000}, {Address: addrA, Sats: 1000}}},
		{"PAYS-SCRIPT", []tides.Output{{Address: addrA, Sats: 1000}, {Address: addrA, Sats: 2000}}},
		{"PAYS-EXTRA", append(append([]tides.Output{}, outs...), tides.Output{Address: addrA, Sats: 1})},
	} {
		if err := cb.PaysExactly(tc.want); err == nil {
			t.Errorf("%s: accepted a coinbase that does not pay the split", tc.name)
		}
	}
}

func TestParseCoinbaseRefuses(t *testing.T) {
	c1, c2, _ := buildOK(t)
	b1, _ := hex.DecodeString(c1)
	b2, _ := hex.DecodeString(c2)
	mut := func(b []byte, i int, v byte) string {
		c := append([]byte(nil), b...)
		c[i] = v
		return hex.EncodeToString(c)
	}
	for _, tc := range []struct{ name, c1, c2 string }{
		{"PARSE-VERSION", mut(b1, 0, 2), c2},
		{"PARSE-INPUTS", mut(b1, 4, 2), c2},
		{"PARSE-PREVOUT", mut(b1, 10, 1), c2},
		{"PARSE-SCRIPTLEN", mut(b1, 41, b1[41]+1), c2},
		{"PARSE-SEQUENCE", c1, mut(b2, len("/Forge DATUM/"), 0)},
		{"PARSE-LOCKTIME", c1, mut(b2, len(b2)-1, 1)},
		{"PARSE-TRAILING", c1, c2 + "00"},
		{"PARSE-NONMINIMAL", hex.EncodeToString(append(append(b1[:41:41], b1[41]+1, 4), 0x10, 0x45, 0x01, 0x00)), c2},
		{"PARSE-NOT-HEX", "zz", c2},
	} {
		if _, err := ParseCoinbase(tc.c1, tc.c2); err == nil {
			t.Errorf("%s: parsed", tc.name)
		}
	}
	if _, _, err := BuildCoinbase(83216, bytes.Repeat([]byte("x"), MaxTag+1), []tides.Output{{Address: addrA, Sats: 1}}); err == nil {
		t.Error("BUILD-TAG: an over-long tag was built")
	}
	if _, _, err := BuildCoinbase(83216, nil, []tides.Output{{Address: "qtypo", Sats: 1}}); err == nil {
		t.Error("BUILD-ADDRESS: an undecodable address was built")
	}
}

// Every DATUM coinbase pays the TIDES split with no fee, or everything to the finder while the
// window is empty.
func TestPayouts(t *testing.T) {
	snap := &Snapshot{Work: map[string]float64{addrA: 1, addrB: 3}, Carry: map[string]int64{}, Dust: tides.DustSats}
	got, err := Payouts(snap, 5_000_000_000, addrA)
	want, _, _ := tides.Split(5_000_000_000, snap.Work, snap.Carry, 0, "", tides.DustSats)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("PAYOUTS-SPLIT: %v %v, want %v", got, err, want)
	}
	for _, o := range got {
		if o.Fee {
			t.Fatal("PAYOUTS-NOFEE: a fee output in a DATUM split")
		}
	}
	empty := &Snapshot{Work: map[string]float64{}, Dust: tides.DustSats}
	if got, err := Payouts(empty, 5_000_000_000, addrB); err != nil || !reflect.DeepEqual(got, []tides.Output{{Address: addrB, Sats: 5_000_000_000}}) {
		t.Fatalf("PAYOUTS-EMPTY: %v %v", got, err)
	}
	if _, err := Payouts(empty, 5_000_000_000, "qtypo"); err == nil {
		t.Fatal("PAYOUTS-FINDER: an empty window paid an undecodable finder")
	}
}

func TestSignAndVerify(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1_790_000_000, 0)
	body := []byte(`{"shares":[]}`)
	req := func() *httptestReq {
		r := httptest.NewRequest("POST", "/datum/v1/shares", bytes.NewReader(body))
		Sign(r, priv, body, now)
		return &httptestReq{r}
	}
	if key, err := Verify(req().r, body, now.Add(30*time.Second)); err != nil || key != hex.EncodeToString(priv.Public().(ed25519.PublicKey)) {
		t.Fatalf("SIG-OK: %q %v", key, err)
	}
	if _, err := Verify(req().r, []byte(`{"shares":[1]}`), now); err == nil {
		t.Error("SIG-BODY: a changed body verified")
	}
	r := req()
	r.r.URL.Path = "/datum/v1/jobs"
	if _, err := Verify(r.r, body, now); err == nil {
		t.Error("SIG-PATH: the signature verified for another path")
	}
	if _, err := Verify(req().r, body, now.Add(3*time.Minute)); err == nil || !strings.Contains(err.Error(), "off the pool's clock") {
		t.Errorf("SIG-STALE: %v", err)
	}
	r = req()
	_, other, _ := ed25519.GenerateKey(nil)
	r.r.Header.Set(HeaderKey, hex.EncodeToString(other.Public().(ed25519.PublicKey)))
	if _, err := Verify(r.r, body, now); err == nil {
		t.Error("SIG-KEY: a signature verified under another key")
	}
	r = req()
	r.r.Header.Del(HeaderSig)
	if _, err := Verify(r.r, body, now); err == nil {
		t.Error("SIG-MISSING: an unsigned request verified")
	}
}

type httptestReq struct{ r *http.Request }

// A share difficulty commitment is the coinbase tag's last byte, a signed exponent, and survives
// the coinbase round trip; a tag too long to take it is cut, not refused.
func TestShareDifficultyCommitment(t *testing.T) {
	outs := []tides.Output{{Address: addrA, Sats: 3000}}
	for _, e := range []int{20, 11, -35, 0, 64, -64} {
		tag := CommitShareDiff([]byte("/Forge Solo/"), e)
		c1, c2, err := BuildCoinbase(83216, tag, outs)
		if err != nil {
			t.Fatal(err)
		}
		cb, err := ParseCoinbase(c1, c2)
		if err != nil {
			t.Fatal(err)
		}
		exp := e
		got, err := CommittedShareDiff(&JobRequest{ShareDiffExp: &exp}, cb)
		if err != nil || got != math.Ldexp(1, e) || string(cb.Tag[:len(cb.Tag)-1]) != "/Forge Solo/" {
			t.Fatalf("COMMIT-ROUNDTRIP %d: %g %v (tag %q)", e, got, err, cb.Tag)
		}
		other := e + 1
		if _, err := CommittedShareDiff(&JobRequest{ShareDiffExp: &other}, cb); err == nil {
			t.Fatalf("COMMIT-MISMATCH %d: exponent %d accepted for a tag committing %d", e, other, e)
		}
	}
	long := CommitShareDiff([]byte(strings.Repeat("x", 40)), 20)
	if len(long) != MaxTag || long[MaxTag-1] != 20 {
		t.Fatalf("COMMIT-CUT: %d bytes, last %d", len(long), long[len(long)-1])
	}
	if d, err := CommittedShareDiff(&JobRequest{}, &Coinbase{}); d != 0 || err != nil {
		t.Fatalf("COMMIT-NONE: %g %v", d, err)
	}
}

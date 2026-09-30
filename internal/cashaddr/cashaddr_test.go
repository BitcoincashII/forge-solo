package cashaddr

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// The pool's own address, and what the live v27.0.2 node on Dallas answered for it and for
// variants of it (validateaddress, 2026-09-29, height 83,210).
const (
	poolAddr  = "bitcoincashii:qqrek3md53495f9u7vjszc9xp8qcm0xwyy2e7gqtxg"
	poolHash  = "079b476da46a5a24bcf3250160a609c18dbcce21"
	poolSPK   = "76a914079b476da46a5a24bcf3250160a609c18dbcce2188ac"
	p2shAddr  = "bitcoincashii:pqrek3md53495f9u7vjszc9xp8qcm0xwyyaur88ga4"
	p2shSPK   = "a914079b476da46a5a24bcf3250160a609c18dbcce2187"
	mixedAddr = "bitcoincashii:QQREK3md53495f9u7vjszc9xp8qcm0xwyy2e7gqtxg"
)

// Addresses both nodes pay to.
func TestDecodeAcceptsWhatBothNodesPay(t *testing.T) {
	for _, tc := range []struct{ name, addr, spk string }{
		{"PREFIXED", poolAddr, poolSPK},
		{"BARE", "qqrek3md53495f9u7vjszc9xp8qcm0xwyy2e7gqtxg", poolSPK},
		{"UPPER", "BITCOINCASHII:QQREK3MD53495F9U7VJSZC9XP8QCM0XWYY2E7GQTXG", poolSPK},
		{"P2SH", p2shAddr, p2shSPK},
	} {
		a, err := Decode(tc.addr, MainnetPrefix)
		if err != nil {
			t.Errorf("%s: %s refused: %v", tc.name, tc.addr, err)
			continue
		}
		if got := hex.EncodeToString(a.Script()); got != tc.spk {
			t.Errorf("%s: script %s, the node says %s", tc.name, got, tc.spk)
		}
		if hex.EncodeToString(a.Hash[:]) != poolHash {
			t.Errorf("%s: hash %x", tc.name, a.Hash)
		}
	}
}

// Every refusal, each for its own reason. The first group v27.0.2 refuses too; the second it
// accepts but Landnám does not pay, so the pool must refuse them.
func TestDecodeRefuses(t *testing.T) {
	for _, tc := range []struct{ name, addr, why string }{
		{"BCH-PREFIX", "bitcoincash:qqrek3md53495f9u7vjszc9xp8qcm0xwyycs8ev4px", "is not"},
		{"BCH-BARE", "qqrek3md53495f9u7vjszc9xp8qcm0xwyycs8ev4px", "checksum"},
		{"TOKEN-P2PKH", "bitcoincashii:zqrek3md53495f9u7vjszc9xp8qcm0xwyydndkwdem", "type"},
		{"TOKEN-P2SH", "bitcoincashii:rqrek3md53495f9u7vjszc9xp8qcm0xwyy6ksefwzx", "type"},
		{"HASH32", "bitcoincashii:pvrek3md53495f9u7vjszc9xp8qcm0xwyyrek3md53495f9u7vjsz2pd9ztrl", "20 bytes"},
		{"CHECKSUM", "bitcoincashii:qqrek3md53495f9u7vjszc9xp8qcm0xwyy2e7gqtxq", "checksum"},
		{"TOPBIT", "bitcoincashii:sqrek3md53495f9u7vjszc9xp8qcm0xwyymmtwtjga", "reserved version bit"},
		// A BCH2 payload labelled with another chain's prefix: the label alone refuses it.
		{"WRONG-LABEL", "bitcoincash:qqrek3md53495f9u7vjszc9xp8qcm0xwyy2e7gqtxg", "is not"},

		{"MIXED", mixedAddr, "mixed case"},
		{"PADBITS", "bitcoincashii:qqrek3md53495f9u7vjszc9xp8qcm0xwy9e68reg4f", "padding"},
		{"EXTRAGROUP", "bitcoincashii:qqrek3md53495f9u7vjszc9xp8qcm0xwyyqrqyu8cnt", "padding"},
		{"LEGACY-P2PKH", "1hDkKStZXvUpqtfBdLgU5pYAqYnvuKTXp", "mixed case"},
		{"LEGACY-P2SH", "32PEfrwL7SErv1b6Jj1GtiBUKMqWTdXQ6S", "mixed case"},

		{"EMPTY", "", "too short"},
		{"PREFIX-ONLY", "bitcoincashii:", "too short"},
		{"SPACE", " " + poolAddr, "is not"},
		{"TWO-COLONS", "bitcoincashii:" + poolAddr, "character"},
		{"LONG", strings.Repeat("q", 200), "bad length"},
	} {
		a, err := Decode(tc.addr, MainnetPrefix)
		if err == nil {
			t.Errorf("%s: %q accepted as %v", tc.name, tc.addr, a)
		} else if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.why) {
			t.Errorf("%s: refused with %q, want a refusal for %q", tc.name, err, tc.why)
		}
	}
}

// The prefix is the caller's, never the address's: the same string is valid under one chain and
// refused under another.
func TestDecodeUsesTheCallersPrefix(t *testing.T) {
	if _, err := Decode("bitcoincash:qqrek3md53495f9u7vjszc9xp8qcm0xwyycs8ev4px", "bitcoincash"); err != nil {
		t.Fatalf("PREFIX-PARAM: a BCH address under the BCH prefix: %v", err)
	}
	if _, err := Decode("qqrek3md53495f9u7vjszc9xp8qcm0xwyy2e7gqtxg", "bitcoincash"); err == nil {
		t.Fatal("PREFIX-PARAM: a bare BCH2 address was read under the BCH prefix")
	}
}

func TestEncodeRoundTrips(t *testing.T) {
	for _, s := range []string{poolAddr, p2shAddr} {
		a, err := Decode(s, MainnetPrefix)
		if err != nil {
			t.Fatal(err)
		}
		if a.String() != s {
			t.Fatalf("ROUNDTRIP: %s encodes back as %s", s, a.String())
		}
	}
}

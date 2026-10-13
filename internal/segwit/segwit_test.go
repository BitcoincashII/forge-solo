package segwit

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcutil/bech32"
)

// address is a segwit address of hrp: version and program, with a bech32 or a bech32m checksum.
func address(t *testing.T, hrp string, version byte, program []byte, bech32m bool) string {
	t.Helper()
	data, err := bech32.ConvertBits(program, 8, 5, true)
	if err != nil {
		t.Fatal(err)
	}
	encode := bech32.Encode
	if bech32m {
		encode = bech32.EncodeM
	}
	s, err := encode(hrp, append([]byte{version}, data...))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// counting is n bytes 1, 2, 3, …: a program that is obviously nobody's.
func counting(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i + 1)
	}
	return b
}

// BIP 350's test vectors, which btcutil's own tests carry too: the valid addresses with the output
// script each pays, and the invalid ones, each refused for its own reason.
func TestScriptReadsBIP350sVectors(t *testing.T) {
	for _, c := range []struct{ addr, hrp, script string }{
		{"BC1QW508D6QEJXTDG4Y5R3ZARVARY0C5XW7KV8F3T4", "bc", "0014751e76e8199196d454941c45d1b3a323f1433bd6"},
		{"tb1qrp33g0q5c5txsp9arysrx4k6zdkfs4nce4xj0gdcccefvpysxf3q0sl5k7", "tb", "00201863143c14c5166804bd19203356da136c985678cd4d27a1b8c6329604903262"},
		{"bc1pw508d6qejxtdg4y5r3zarvary0c5xw7kw508d6qejxtdg4y5r3zarvary0c5xw7kt5nd6y", "bc", "5128751e76e8199196d454941c45d1b3a323f1433bd6751e76e8199196d454941c45d1b3a323f1433bd6"},
		{"BC1SW50QGDZ25J", "bc", "6002751e"},
		{"bc1zw508d6qejxtdg4y5r3zarvaryvaxxpcs", "bc", "5210751e76e8199196d454941c45d1b3a323"},
		{"tb1qqqqqp399et2xygdj5xreqhjjvcmzhxw4aywxecjdzew6hylgvsesrxh6hy", "tb", "0020000000c4a5cad46221b2a187905e5266362b99d5e91c6ce24d165dab93e86433"},
		{"tb1pqqqqp399et2xygdj5xreqhjjvcmzhxw4aywxecjdzew6hylgvsesf3hn0c", "tb", "5120000000c4a5cad46221b2a187905e5266362b99d5e91c6ce24d165dab93e86433"},
		{"bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0", "bc", "512079be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"},
	} {
		got, err := Script(c.addr, c.hrp)
		if err != nil || hex.EncodeToString(got) != c.script {
			t.Errorf("SEGWIT-BIP350-VALID: %s read as %x (%v), want %s", c.addr, got, err, c.script)
		}
	}
	for _, c := range []struct{ addr, why string }{
		{"tc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vq5zuyut", "another human-readable part"},
		{"bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqh2y7hd", "version 1 with a bech32 checksum"},
		{"tb1z0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqglt7rf", "version 2 with a bech32 checksum"},
		{"BC1S0XLXVLHEMJA6C4DQV22UAPCTQUPFHLXM9H8Z3K2E72Q4K9HCZ7VQ54WELL", "version 16 with a bech32 checksum"},
		{"bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kemeawh", "version 0 with a bech32m checksum"},
		{"tb1q0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vq24jc47", "version 0 with a bech32m checksum"},
		{"bc1p38j9r5y49hruaue7wxjce0updqjuyyx0kh56v8s25huc6995vvpql3jow4", "a character outside the charset"},
		{"BC130XLXVLHEMJA6C4DQV22UAPCTQUPFHLXM9H8Z3K2E72Q4K9HCZ7VQ7ZWS8R", "witness version 17"},
		{"bc1pw5dgrnzv", "a 1-byte program"},
		{"bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7v8n0nx0muaewav253zgeav", "a 41-byte program"},
		{"BC1QR508D6QEJXTDG4Y5R3ZARVARYV98GJ9P", "a 16-byte version 0 program"},
		{"tb1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vq47Zagq", "mixed case"},
		{"bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7v07qwwzcrf", "more than 4 bits of padding"},
		{"tb1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vpggkg4j", "padding bits that are not zero"},
		{"bc1gmk9yu", "no data"},
	} {
		for _, hrp := range []string{"bc", "tb"} {
			if got, err := Script(c.addr, hrp); err == nil || !errors.Is(err, ErrInvalid) {
				t.Errorf("SEGWIT-BIP350-INVALID: %s (%s) read as %x for %s1…, err %v", c.addr, c.why, got, hrp, err)
			}
		}
	}
}

// A 1175 payout address is a mainnet 1175 segwit address: esf1…, version 0 with 20 or 32 bytes and
// bech32, or version 1 to 16 with 2 to 40 bytes and bech32m. 1.0.12 to 1.0.15 took the 19-byte
// address below, and the 1175 node then refused every getauxblock for it.
func TestCheck1175(t *testing.T) {
	p20, p32 := counting(20), counting(32)
	for _, c := range []struct {
		name, addr string
		ok         bool
	}{
		{"v0 20 bytes", address(t, "esf", 0, p20, false), true},
		{"v0 32 bytes", address(t, "esf", 0, p32, false), true},
		{"v1 32 bytes, bech32m", address(t, "esf", 1, p32, true), true},
		{"v1 20 bytes, bech32m", address(t, "esf", 1, p20, true), true},
		{"v16 2 bytes, bech32m", address(t, "esf", 16, p20[:2], true), true},
		{"v2 40 bytes, bech32m", address(t, "esf", 2, counting(40), true), true},
		{"the 20-byte address the tests use", "esf1quhj7te09uhj7te09uhj7te09uhj7te09dnlk6x", true},
		{"upper case", strings.ToUpper(address(t, "esf", 0, p20, false)), true},

		{"a 19-byte program (saved by 1.0.12 to 1.0.15)", "esf1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqnkz876", false},
		{"v0 21 bytes", address(t, "esf", 0, counting(21), false), false},
		{"v0 31 bytes", address(t, "esf", 0, counting(31), false), false},
		{"v0 with a bech32m checksum", address(t, "esf", 0, p20, true), false},
		{"v1 with a bech32 checksum", address(t, "esf", 1, p32, false), false},
		{"v16 with a bech32 checksum", address(t, "esf", 16, p20[:2], false), false},
		{"v1 1 byte", address(t, "esf", 1, p20[:1], true), false},
		{"v1 41 bytes", address(t, "esf", 1, counting(41), true), false},
		{"v17", address(t, "esf", 17, p32, true), false},
		{"regtest", address(t, "resf", 0, p20, false), false},
		{"Bitcoin", address(t, "bc", 0, p20, false), false},
		{"Bitcoin testnet", address(t, "tb", 1, p32, true), false},
		{"mixed case", "Esf1quhj7te09uhj7te09uhj7te09uhj7te09dnlk6x", false},
		{"mixed case in the data", "esf1quhj7te09uhj7te09uhJ7te09uhj7te09dnlk6x", false},
		{"one character changed", "esf1quhj7te09uhj7te09uhj7te09uhj7te09dnlk6y", false},
		{"two characters swapped", "esf1quhj7te09uhj7te09uhj7et09uhj7te09dnlk6x", false},
		{"a space before", " esf1quhj7te09uhj7te09uhj7te09uhj7te09dnlk6x", false},
		{"a descriptor", "addr(esf1quhj7te09uhj7te09uhj7te09uhj7te09dnlk6x)", false},
		{"a BCH2 address", "bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2", false},
		{"the human-readable part alone", "esf1", false},
		{"not bech32", "esf1payout", false},
		{"empty", "", false},
	} {
		err := Check1175(c.addr)
		if (err == nil) != c.ok {
			t.Errorf("SEGWIT-1175 %s: Check1175(%q) = %v, want ok=%v", c.name, c.addr, err, c.ok)
		}
		if err != nil && !errors.Is(err, ErrInvalid) {
			t.Errorf("SEGWIT-1175-ERR %s: %v does not wrap ErrInvalid", c.name, err)
		}
	}
}

// Script gives the output script each kind pays: OP_0 or OP_1 to OP_16, the program's length, the
// program.
func TestScriptOf1175Addresses(t *testing.T) {
	p20, p32 := counting(20), counting(32)
	for _, c := range []struct{ name, addr, want string }{
		{"v0 20 bytes", address(t, "esf", 0, p20, false), "0014" + hex.EncodeToString(p20)},
		{"v0 32 bytes", address(t, "esf", 0, p32, false), "0020" + hex.EncodeToString(p32)},
		{"v1 32 bytes", address(t, "esf", 1, p32, true), "5120" + hex.EncodeToString(p32)},
		{"v16 2 bytes", address(t, "esf", 16, p20[:2], true), "6002" + hex.EncodeToString(p20[:2])},
		{"upper case", strings.ToUpper(address(t, "esf", 0, p20, false)), "0014" + hex.EncodeToString(p20)},
		{"regtest", address(t, "resf", 0, p20, false), ""},
	} {
		got, err := Script(c.addr, HRP1175)
		if c.want == "" {
			if err == nil {
				t.Errorf("SEGWIT-SCRIPT %s: %s read as %x", c.name, c.addr, got)
			}
			continue
		}
		if err != nil || hex.EncodeToString(got) != c.want {
			t.Errorf("SEGWIT-SCRIPT %s: %s read as %x (%v), want %s", c.name, c.addr, got, err, c.want)
		}
	}
}

// What a refusal says names the fault, for the logs.
func TestCheck1175SaysWhy(t *testing.T) {
	for _, c := range []struct{ addr, want string }{
		{"esf1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqnkz876", "a version 0 witness program of 19 bytes, not 20 or 32"},
		{address(t, "resf", 0, counting(20), false), "it is for resf1…, not esf1…"},
		{address(t, "esf", 0, counting(20), true), "witness version 0 takes a bech32 checksum"},
		{address(t, "esf", 1, counting(32), false), "witness version 1 takes a bech32m checksum"},
	} {
		if err := Check1175(c.addr); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("SEGWIT-WHY: Check1175(%q) = %v, want it to say %q", c.addr, err, c.want)
		}
	}
}

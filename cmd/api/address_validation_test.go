package main

import "testing"

// The two payout-address validators had no test of their own, and they are what stands
// between a mistyped address and a block mined to it. A block's whole reward is paid by its
// coinbase to the stored BCH2 address, and the 1175 aux coinbase pays the stored esf address,
// so a validator that accepts one wrong character sends the reward somewhere unrecoverable.
//
// Vectors are either on the allow-list in TestTestAddressesAreObviouslyFake or carry an
// all-zero/counting payload; none is anyone's address. Each expectation below was checked
// against an independent implementation of the CashAddr and bech32 checksums, not read off
// the code under test.
func TestIsValidBCH2Address(t *testing.T) {
	for _, tc := range []struct {
		name string
		addr string
		want bool
	}{
		{"valid P2PKH", "bitcoincashii:qpgf7a4mx6hpsjd3qnl9pr7ee09j7rk4zclycv4c8m", true},
		// One character changed. The checksum is the only thing that catches this, and it is
		// the realistic failure: a hand-copied address with a single wrong symbol.
		{"single-character typo", "bitcoincashii:qpgf7a4mx6hpsjd3qnl9pr7ee09j7rk4zclycv4c8n", false},
		{"valid, counting payload", "bitcoincashii:qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzse6qye33q", true},
		// The SAME payload encoded for Bitcoin Cash. Checksums are bound to their prefix, so a
		// real BCH address must be refused here rather than silently accepted for BCH2.
		{"same payload, bitcoincash prefix", "bitcoincash:qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzstne440kw", false},
		// Type 1 (P2SH) with a 160-bit hash. Only P2PKH is accepted: the coinbase builder
		// emits a P2PKH script, so a P2SH address would not be paid as its owner expects.
		{"P2SH is refused", "bitcoincashii:pqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqvlngm8nz", false},
		{"prefixless cannot be checksummed", "qpgf7a4mx6hpsjd3qnl9pr7ee09j7rk4zclycv4c8m", false},
		{"character outside the charset", "bitcoincashii:bpgf7a4mx6hpsjd3qnl9pr7ee09j7rk4zclycv4c8m", false},
		// Accepted: CashAddr is case-insensitive, and a wallet may present either form.
		{"uppercase", "BITCOINCASHII:QPGF7A4MX6HPSJD3QNL9PR7EE09J7RK4ZCLYCV4C8M", true},
		{"surrounding whitespace", "  bitcoincashii:qpgf7a4mx6hpsjd3qnl9pr7ee09j7rk4zclycv4c8m  ", true},
		{"empty", "", false},
		{"prefix only", "bitcoincashii:", false},
		{"too short to hold a checksum", "bitcoincashii:qqqq", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isValidBCH2Address(tc.addr); got != tc.want {
				t.Errorf("isValidBCH2Address(%q) = %v, want %v", tc.addr, got, tc.want)
			}
		})
	}
}

// A 1175 address is one the 1175 node pays: esf1…, witness version 0 with a 20- or 32-byte program
// and a bech32 checksum, or version 1 to 16 with 2 to 40 bytes and bech32m. Until 1.0.16 this test
// called the 19-byte address below valid: Settings saved it, and the 1175 node refused every
// getauxblock for it ("-5 Invalid payout address"), so merge-mining never worked.
func TestIsValid1175Address(t *testing.T) {
	// Witness version 0, the twenty bytes 0xe5 0xe5 …: a valid address that is obviously nobody's.
	const valid = "esf1quhj7te09uhj7te09uhj7te09uhj7te09dnlk6x"
	p32 := make([]byte, 32)
	for i := range p32 {
		p32[i] = byte(i + 1)
	}
	for _, tc := range []struct {
		name string
		addr string
		want bool
	}{
		{"valid v0, 20 bytes", valid, true},
		{"valid v0, 32 bytes", segwitAddress(t, "esf", 0, p32, false), true},
		{"valid v1, 32 bytes, bech32m", segwitAddress(t, "esf", 1, p32, true), true},
		{"valid v16, 2 bytes, bech32m", segwitAddress(t, "esf", 16, p32[:2], true), true},
		{"a 19-byte program, as 1.0.12 to 1.0.15 saved", "esf1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqnkz876", false},
		{"v0 with a bech32m checksum", segwitAddress(t, "esf", 0, p32[:20], true), false},
		{"v1 with a bech32 checksum", segwitAddress(t, "esf", 1, p32, false), false},
		{"single-character typo", "esf1quhj7te09uhj7te09uhj7te09uhj7te09dnlk6y", false},
		// A perfectly valid bech32 address for another chain. The hrp check is what stops a
		// pasted BTC address from being stored as the 1175 payout target.
		{"valid bech32, wrong hrp", "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4", false},
		{"1175 regtest", segwitAddress(t, "resf", 0, p32[:20], false), false},
		{"uppercase", "ESF1QUHJ7TE09UHJ7TE09UHJ7TE09UHJ7TE09DNLK6X", true},
		// BIP-173 forbids mixed case outright, and btcutil enforces it.
		{"mixed case", "Esf1quhj7te09uhj7te09uhj7te09uhj7te09dnlk6x", false},
		{"surrounding whitespace", "  " + valid + "  ", true},
		{"empty", "", false},
		{"hrp only", "esf1", false},
		{"not bech32 at all", "esf1payout", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isValid1175Address(tc.addr); got != tc.want {
				t.Errorf("isValid1175Address(%q) = %v, want %v", tc.addr, got, tc.want)
			}
		})
	}
}

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

func TestIsValid1175Address(t *testing.T) {
	// bech32 with hrp "esf" and an all-zero payload: a valid address that is obviously nobody's.
	const valid = "esf1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqnkz876"
	for _, tc := range []struct {
		name string
		addr string
		want bool
	}{
		{"valid esf1", valid, true},
		{"single-character typo", "esf1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqnkz87p", false},
		// A perfectly valid bech32 address for another chain. The hrp check is what stops a
		// pasted BTC address from being stored as the 1175 payout target.
		{"valid bech32, wrong hrp", "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4", false},
		{"uppercase", "ESF1QQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQNKZ876", true},
		// BIP-173 forbids mixed case outright, and btcutil enforces it.
		{"mixed case", "Esf1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqnkz876", false},
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

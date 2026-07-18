package mining

import (
	"encoding/hex"
	"testing"
)

func TestParseAddressToPubkeyHash(t *testing.T) {
	// Known-valid P2PKH BCH2 address -> pubkey hash from the node's scriptPubKey
	// 76a914<509f76bb36ae1849b104fe508fd9cbcb2f0ed516>88ac
	got := parseAddressToPubkeyHash("bitcoincashii:qpgf7a4mx6hpsjd3qnl9pr7ee09j7rk4zclycv4c8m")
	want := "509f76bb36ae1849b104fe508fd9cbcb2f0ed516"
	if got == nil || hex.EncodeToString(got) != want {
		t.Fatalf("valid addr: got %x, want %s", got, want)
	}

	// Single-character typo (still in-charset) must be rejected via the checksum.
	if h := parseAddressToPubkeyHash("bitcoincashii:qpgf7a4mx6hpsjd3qnl9pr7ee09j7rk4zclycv4c8n"); h != nil {
		t.Fatalf("typo addr must be nil, got %x", h)
	}
	// A prefixless address cannot be checksum-verified -> nil.
	if h := parseAddressToPubkeyHash("qpgf7a4mx6hpsjd3qnl9pr7ee09j7rk4zclycv4c8m"); h != nil {
		t.Fatalf("prefixless must be nil, got %x", h)
	}
	// Empty -> nil.
	if h := parseAddressToPubkeyHash(""); h != nil {
		t.Fatalf("empty must be nil, got %x", h)
	}
	// Out-of-charset character (contains 'b') -> nil.
	if h := parseAddressToPubkeyHash("bitcoincashii:bpgf7a4mx6hpsjd3qnl9pr7ee09j7rk4zclycv4c8m"); h != nil {
		t.Fatalf("bad charset must be nil, got %x", h)
	}
}

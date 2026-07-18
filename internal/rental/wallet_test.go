package rental

import (
	"strings"
	"testing"
)

func TestGenerateTestXPub(t *testing.T) {
	xpub, err := GenerateTestXPub()
	if err != nil {
		t.Fatalf("Failed to generate test xpub: %v", err)
	}

	if !strings.HasPrefix(xpub, "xpub") {
		t.Errorf("Expected xpub prefix, got: %s", xpub[:10])
	}

	t.Logf("Generated xpub: %s", xpub)
}

func TestHDWalletDeriveAddress(t *testing.T) {
	// Generate a test xpub
	xpub, err := GenerateTestXPub()
	if err != nil {
		t.Fatalf("Failed to generate test xpub: %v", err)
	}

	wallet, err := NewHDWallet(xpub)
	if err != nil {
		t.Fatalf("Failed to create HD wallet: %v", err)
	}

	// Derive first 5 addresses
	addresses := make([]string, 5)
	for i := uint32(0); i < 5; i++ {
		addr, err := wallet.DeriveAddress(i)
		if err != nil {
			t.Fatalf("Failed to derive address at index %d: %v", i, err)
		}
		addresses[i] = addr

		// Verify address format (native SegWit starts with bc1q)
		if !strings.HasPrefix(addr, "bc1q") {
			t.Errorf("Expected bc1q prefix at index %d, got: %s", i, addr)
		}

		t.Logf("Address %d: %s", i, addr)
	}

	// Verify all addresses are unique
	seen := make(map[string]bool)
	for i, addr := range addresses {
		if seen[addr] {
			t.Errorf("Duplicate address found at index %d: %s", i, addr)
		}
		seen[addr] = true
	}

	// Verify same index produces same address (deterministic)
	addr0Again, err := wallet.DeriveAddress(0)
	if err != nil {
		t.Fatalf("Failed to re-derive address 0: %v", err)
	}
	if addr0Again != addresses[0] {
		t.Errorf("Address derivation not deterministic: got %s, expected %s", addr0Again, addresses[0])
	}
}

func TestHDWalletAddressTypes(t *testing.T) {
	xpub, err := GenerateTestXPub()
	if err != nil {
		t.Fatalf("Failed to generate test xpub: %v", err)
	}

	wallet, err := NewHDWallet(xpub)
	if err != nil {
		t.Fatalf("Failed to create HD wallet: %v", err)
	}

	// Test all three address types
	nativeSegwit, err := wallet.DeriveAddress(0)
	if err != nil {
		t.Fatalf("Failed to derive native SegWit: %v", err)
	}
	if !strings.HasPrefix(nativeSegwit, "bc1q") {
		t.Errorf("Native SegWit should start with bc1q, got: %s", nativeSegwit)
	}
	t.Logf("Native SegWit (bc1q): %s", nativeSegwit)

	p2sh, err := wallet.DeriveAddressP2SH(0)
	if err != nil {
		t.Fatalf("Failed to derive P2SH: %v", err)
	}
	if !strings.HasPrefix(p2sh, "3") {
		t.Errorf("P2SH should start with 3, got: %s", p2sh)
	}
	t.Logf("P2SH-P2WPKH (3...): %s", p2sh)

	legacy, err := wallet.DeriveAddressLegacy(0)
	if err != nil {
		t.Fatalf("Failed to derive legacy: %v", err)
	}
	if !strings.HasPrefix(legacy, "1") {
		t.Errorf("Legacy should start with 1, got: %s", legacy)
	}
	t.Logf("Legacy P2PKH (1...): %s", legacy)
}

func TestValidateXPub(t *testing.T) {
	// Valid xpub
	xpub, _ := GenerateTestXPub()
	if err := ValidateXPub(xpub); err != nil {
		t.Errorf("Valid xpub rejected: %v", err)
	}

	// Empty xpub
	if err := ValidateXPub(""); err == nil {
		t.Error("Empty xpub should be rejected")
	}

	// Invalid xpub
	if err := ValidateXPub("notanxpub"); err == nil {
		t.Error("Invalid xpub should be rejected")
	}
}

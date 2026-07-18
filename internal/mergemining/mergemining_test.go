package mergemining

import (
	"encoding/hex"
	"strings"
	"testing"
)

// Reference values captured from the live 1175 regtest run
// (scratchpad/regtest_auxpow_ref.py, iter 0), which the node ACCEPTED via
// submitauxblock. The Go implementation must reproduce these byte-for-byte.
const (
	refChildHashBE = "09275083d9c9bb784b0302da02405412c28f53c682689479b71dc1e6a85c871f"
	refCommitment  = "fabe6d6d1f875ca8e6c11db779946882c6538fc212544002da02034b78bbc9d9835027090100000000000000"
	refCoinbaseTx  = "02000000010000000000000000000000000000000000000000000000000000000000000000ffffffff2cfabe6d6d1f875ca8e6c11db779946882c6538fc212544002da02034b78bbc9d9835027090100000000000000000000000100f2052a01000000015100000000"
	refHashBlock   = "d48383a5bab4cb10ba3450bb27b8eb6b323b7ab5ffdef9b11c00ded3760e4d62"
	refParentHdr   = "01000000" +
		"0000000000000000000000000000000000000000000000000000000000000000" +
		"e915ed3f302b4205d092ebf3ea9ab136c1a282cac8403ce83d41af0996bcf770" +
		"40420f00" + "ffff7f20" + "00000000"
	refAuxPow = "02000000010000000000000000000000000000000000000000000000000000000000000000ffffffff2cfabe6d6d1f875ca8e6c11db779946882c6538fc212544002da02034b78bbc9d9835027090100000000000000000000000100f2052a01000000015100000000d48383a5bab4cb10ba3450bb27b8eb6b323b7ab5ffdef9b11c00ded3760e4d6200000000000000000000010000000000000000000000000000000000000000000000000000000000000000000000e915ed3f302b4205d092ebf3ea9ab136c1a282cac8403ce83d41af0996bcf77040420f00ffff7f2000000000"
)

func TestBuildCommitment(t *testing.T) {
	c, err := BuildCommitment(refChildHashBE)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(c); got != refCommitment {
		t.Fatalf("commitment mismatch\n got: %s\nwant: %s", got, refCommitment)
	}
	if len(c) != 44 {
		t.Fatalf("commitment length = %d, want 44", len(c))
	}
	// The commitment must be embedded verbatim in the reference parent coinbase.
	if !strings.Contains(refCoinbaseTx, refCommitment) {
		t.Fatal("commitment not found in reference coinbase scriptSig")
	}
}

func TestBuildCommitmentBadInput(t *testing.T) {
	if _, err := BuildCommitment("abcd"); err == nil {
		t.Fatal("expected error for short hash")
	}
	if _, err := BuildCommitment("zz"); err == nil {
		t.Fatal("expected error for non-hex")
	}
}

func TestAuxPowSerialize(t *testing.T) {
	var hb [32]byte
	copy(hb[:], mustHex(t, refHashBlock))
	a := &AuxPow{
		CoinbaseTx:        mustHex(t, refCoinbaseTx),
		HashBlock:         hb,
		MerkleBranch:      nil, // single-tx parent
		NIndex:            0,
		ChainMerkleBranch: nil, // single aux chain
		NChainIndex:       0,
		ParentHeader:      mustHex(t, refParentHdr),
	}
	got, err := a.SerializeHex()
	if err != nil {
		t.Fatal(err)
	}
	if got != refAuxPow {
		t.Fatalf("CAuxPow serialization mismatch\n got: %s\nwant: %s", got, refAuxPow)
	}
}

// TestAuxPowSerializeWithBranches exercises the varint framing for non-empty
// branches (the real multi-tx BCH2 parent case). It checks structural framing,
// not a node-accepted vector (that requires a multi-tx regtest, see follow-up).
func TestAuxPowSerializeWithBranches(t *testing.T) {
	var hb, b1, b2 [32]byte
	for i := range b1 {
		b1[i] = byte(i)
		b2[i] = byte(0xff - i)
	}
	a := &AuxPow{
		CoinbaseTx:   []byte{0x01, 0x02},
		HashBlock:    hb,
		MerkleBranch: [][32]byte{b1, b2},
		NIndex:       0,
		ParentHeader: make([]byte, 80),
	}
	raw, err := a.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	// 2 (cb) + 32 (hashBlock) + 1 (varint=2) + 64 (2 branches) + 4 (nIndex)
	// + 1 (varint=0) + 4 (nChainIndex) + 80 (header) = 188
	if len(raw) != 188 {
		t.Fatalf("serialized length = %d, want 188", len(raw))
	}
	if raw[34] != 0x02 {
		t.Fatalf("merkle-branch varint = %d, want 2", raw[34])
	}
}

// TestAssembleAuxPowHexMatchesReference confirms the single assembly entry point
// (used by the stratum share path and the integration test) reproduces the exact
// node-accepted reference, including deriving hashBlock = dblSHA(parentHeader).
func TestAssembleAuxPowHexMatchesReference(t *testing.T) {
	got, err := AssembleAuxPowHex(mustHex(t, refCoinbaseTx), mustHex(t, refParentHdr), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != refAuxPow {
		t.Fatalf("AssembleAuxPowHex mismatch\n got: %s\nwant: %s", got, refAuxPow)
	}
}

func TestParentHeaderLength(t *testing.T) {
	a := &AuxPow{ParentHeader: make([]byte, 79)}
	if _, err := a.Serialize(); err == nil {
		t.Fatal("expected error for non-80-byte parent header")
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

package mining

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// rev returns a byte-reversed copy (big-endian display <-> internal little-endian).
func rev(b []byte) []byte {
	out := make([]byte, len(b))
	for i := range b {
		out[i] = b[len(b)-1-i]
	}
	return out
}

// fullMerkleRoot computes the canonical Bitcoin merkle root over leaves (given
// in internal little-endian order), duplicating the last node on odd levels.
func fullMerkleRoot(leaves [][]byte) []byte {
	level := make([][]byte, len(leaves))
	copy(level, leaves)
	for len(level) > 1 {
		if len(level)%2 == 1 {
			level = append(level, level[len(level)-1])
		}
		var next [][]byte
		for i := 0; i < len(level); i += 2 {
			pair := append(append([]byte{}, level[i]...), level[i+1]...)
			next = append(next, doubleSHA256(pair))
		}
		level = next
	}
	return level[0]
}

// TestBuildMerkleBranchesReconstructsRoot is the correctness gate for merge
// mining with a MULTI-transaction parent block. It proves that folding the
// coinbase through buildMerkleBranches(txids) (exactly what CAuxPow.vMerkleBranch
// does with nIndex=0, and what stratum's calculateMerkleRoot does) reproduces the
// canonical merkle root of the full [coinbase, tx1..txN] tree — for a spread of
// tx counts covering the odd/even duplication edge cases.
func TestBuildMerkleBranchesReconstructsRoot(t *testing.T) {
	// deterministic pseudo-random 32-byte hashes
	seed := byte(1)
	nextHash := func() []byte {
		h := make([]byte, 32)
		for i := range h {
			seed = seed*13 + 7
			h[i] = seed
		}
		return h
	}

	for _, n := range []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 11, 15, 16, 17, 32, 33} {
		cbHash := doubleSHA256(nextHash()) // stand-in coinbase txid (internal LE)
		leaves := [][]byte{cbHash}
		var txids []string // big-endian display hex, exactly as getblocktemplate provides
		for i := 0; i < n; i++ {
			leLeaf := nextHash() // internal LE txid
			leaves = append(leaves, leLeaf)
			txids = append(txids, hex.EncodeToString(rev(leLeaf)))
		}

		want := fullMerkleRoot(leaves)
		branches := buildMerkleBranches(txids)

		// Fold the coinbase through the branch (same operation as stratum's
		// calculateMerkleRoot and as CAuxPow::Check with nIndex == 0).
		got := cbHash
		for _, bh := range branches {
			b, err := hex.DecodeString(bh)
			if err != nil || len(b) != 32 {
				t.Fatalf("n=%d: bad branch hash %q", n, bh)
			}
			got = doubleSHA256(append(append([]byte{}, got...), b...))
		}

		if !bytes.Equal(got, want) {
			t.Fatalf("n=%d: reconstructed root %x != canonical root %x (%d branches)",
				n, got, want, len(branches))
		}
	}
}

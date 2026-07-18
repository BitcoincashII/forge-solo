// Package mergemining implements the aux-chain (Dogecoin-style AuxPoW) side of
// merge mining for Forge Pool: it fetches aux work from the 1175 node, builds
// the merged-mining commitment that goes into the PARENT (BCH2) coinbase, and
// assembles + submits the CAuxPow proof when a parent share solves the aux
// target.
//
// Destined for github.com/bch2/forge-pool/internal/mergemining. Stdlib-only.
//
// Byte formats verified against the live 1175 regtest node
// (scratchpad/regtest_auxpow_ref.py): 8/8 getauxblock->submitauxblock accepted.
package mergemining

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

func doubleSHA256(b []byte) []byte {
	first := sha256.Sum256(b)
	second := sha256.Sum256(first[:])
	return second[:]
}

// MergedMiningMagic ("fabe6d6d") marks the aux commitment in the parent coinbase.
var MergedMiningMagic = []byte{0xfa, 0xbe, 0x6d, 0x6d}

// reverse returns a byte-reversed copy (big-endian display hex <-> internal LE).
func reverse(b []byte) []byte {
	out := make([]byte, len(b))
	for i := range b {
		out[i] = b[len(b)-1-i]
	}
	return out
}

// BuildCommitment returns the 44-byte merged-mining commitment for a single aux
// chain, to be embedded in the PARENT coinbase scriptSig (in the fixed cb2
// region, after the extranonce — so it is identical for the standard and Braiins
// stratum ports regardless of extranonce size).
//
//	fabe6d6d | childHash(32B little-endian) | merkleSize(4B LE)=1 | nonce(4B LE)=0
//
// childHashHexBE is the "hash" field from getauxblock (big-endian display hex);
// it is stored little-endian in the commitment (uint256 internal order).
func BuildCommitment(childHashHexBE string) ([]byte, error) {
	h, err := hex.DecodeString(childHashHexBE)
	if err != nil {
		return nil, fmt.Errorf("child hash hex: %w", err)
	}
	if len(h) != 32 {
		return nil, fmt.Errorf("child hash must be 32 bytes, got %d", len(h))
	}
	out := make([]byte, 0, 44)
	out = append(out, MergedMiningMagic...)
	out = append(out, reverse(h)...)
	out = binary.LittleEndian.AppendUint32(out, 1) // merkle_size (single chain)
	out = binary.LittleEndian.AppendUint32(out, 0) // nonce
	return out, nil
}

// AuxPow is the CAuxPow proof the aux node accepts via submitauxblock.
//
// All 32-byte fields are in INTERNAL (little-endian) byte order — i.e. exactly
// how they appear on the wire — NOT big-endian display order:
//   - CoinbaseTx:   the parent block's fully-serialized coinbase transaction
//     (no witness) whose scriptSig contains BuildCommitment(childHash).
//   - HashBlock:    the parent block header hash (== dblSHA(ParentHeader)).
//   - MerkleBranch: the coinbase's merkle branch within the parent block. This
//     is the SAME set of branch hashes the pool already computes for stratum
//     (job.MerkleBranches), in internal order. Empty when the parent block has
//     only the coinbase.
//   - ChainMerkleBranch: empty for a single aux chain (nChainIndex 0).
//   - ParentHeader: the parent block's 80-byte header.
type AuxPow struct {
	CoinbaseTx        []byte
	HashBlock         [32]byte
	MerkleBranch      [][32]byte
	NIndex            int32
	ChainMerkleBranch [][32]byte
	NChainIndex       int32
	ParentHeader      []byte // 80 bytes
}

func writeVarInt(b *bytes.Buffer, n uint64) {
	switch {
	case n < 0xfd:
		b.WriteByte(byte(n))
	case n <= 0xffff:
		b.WriteByte(0xfd)
		_ = binary.Write(b, binary.LittleEndian, uint16(n))
	case n <= 0xffffffff:
		b.WriteByte(0xfe)
		_ = binary.Write(b, binary.LittleEndian, uint32(n))
	default:
		b.WriteByte(0xff)
		_ = binary.Write(b, binary.LittleEndian, n)
	}
}

// Serialize encodes the CAuxPow in the exact wire order the 1175 node expects:
//
//	coinbaseTx || hashBlock(32) || vMerkleBranch || nIndex(int32 LE) ||
//	vChainMerkleBranch || nChainIndex(int32 LE) || parentHeader(80)
//
// where each vector is a compactSize count followed by 32-byte entries.
func (a *AuxPow) Serialize() ([]byte, error) {
	if len(a.ParentHeader) != 80 {
		return nil, fmt.Errorf("parent header must be 80 bytes, got %d", len(a.ParentHeader))
	}
	var b bytes.Buffer
	b.Write(a.CoinbaseTx)
	b.Write(a.HashBlock[:])
	writeVarInt(&b, uint64(len(a.MerkleBranch)))
	for _, h := range a.MerkleBranch {
		b.Write(h[:])
	}
	_ = binary.Write(&b, binary.LittleEndian, a.NIndex)
	writeVarInt(&b, uint64(len(a.ChainMerkleBranch)))
	for _, h := range a.ChainMerkleBranch {
		b.Write(h[:])
	}
	_ = binary.Write(&b, binary.LittleEndian, a.NChainIndex)
	b.Write(a.ParentHeader)
	return b.Bytes(), nil
}

// SerializeHex returns the hex string for the submitauxblock RPC argument.
func (a *AuxPow) SerializeHex() (string, error) {
	raw, err := a.Serialize()
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// AssembleAuxPowHex builds the submitauxblock argument for a solved parent block.
// This is the single assembly point used both by the stratum share path and by
// the merge-mining integration tests.
//
//   - coinbaseTx: the parent block's serialized coinbase (carrying the commitment)
//   - parentHeader: the parent block's 80-byte header (already solved to the aux target)
//   - coinbaseMerkleBranchHex: the coinbase's merkle branch within the parent block,
//     in stratum order (internal little-endian hex); empty for a single-tx parent.
//
// The coinbase is tx 0, so nIndex and the (single-chain) chain index are both 0.
func AssembleAuxPowHex(coinbaseTx, parentHeader []byte, coinbaseMerkleBranchHex []string) (string, error) {
	if len(parentHeader) != 80 {
		return "", fmt.Errorf("parent header must be 80 bytes, got %d", len(parentHeader))
	}
	branches := make([][32]byte, 0, len(coinbaseMerkleBranchHex))
	for _, h := range coinbaseMerkleBranchHex {
		b, err := hex.DecodeString(h)
		if err != nil || len(b) != 32 {
			return "", fmt.Errorf("bad merkle branch %q", h)
		}
		var e [32]byte
		copy(e[:], b)
		branches = append(branches, e)
	}
	var hb [32]byte
	copy(hb[:], doubleSHA256(parentHeader)) // parent hash, internal little-endian
	aux := &AuxPow{
		CoinbaseTx:   coinbaseTx,
		HashBlock:    hb,
		MerkleBranch: branches,
		NIndex:       0,
		ParentHeader: parentHeader,
	}
	return aux.SerializeHex()
}

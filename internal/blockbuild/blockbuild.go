// Package blockbuild assembles a block from a stratum job and a miner's solution. It is the one
// place the bytes a node is asked to accept are put together, shared by Forge Solo's stratum and
// the gateway program, so the two can never build a found block differently.
package blockbuild

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/BitcoincashII/forge-solo/internal/mining"
	"github.com/BitcoincashII/forge-solo/internal/stratum"
)

// Coinbase joins a job's two coinbase halves around the miner's extranonces.
func Coinbase(cb1, extranonce1, extranonce2, cb2 string) ([]byte, error) {
	cb1Bytes, err := hex.DecodeString(cb1)
	if err != nil {
		return nil, fmt.Errorf("invalid cb1 hex: %w", err)
	}
	en1Bytes, err := hex.DecodeString(extranonce1)
	if err != nil {
		return nil, fmt.Errorf("invalid extranonce1 hex: %w", err)
	}
	en2Bytes, err := hex.DecodeString(extranonce2)
	if err != nil {
		return nil, fmt.Errorf("invalid extranonce2 hex: %w", err)
	}
	cb2Bytes, err := hex.DecodeString(cb2)
	if err != nil {
		return nil, fmt.Errorf("invalid cb2 hex: %w", err)
	}

	var coinbase bytes.Buffer
	coinbase.Write(cb1Bytes)
	coinbase.Write(en1Bytes)
	coinbase.Write(en2Bytes)
	coinbase.Write(cb2Bytes)

	return coinbase.Bytes(), nil
}

// Block is the serialized block (hex) for a solution to job: the header the share was
// validated against, then the coinbase and the job's transactions.
func Block(job *mining.Job, coinbase []byte, ntime, nonce, versionBits string) (string, error) {
	var block bytes.Buffer

	// Version (4 bytes) - stratum sends as hex string like "20000000"
	// For block, we need little-endian, so reverse the bytes.
	//
	// stratum.RollVersion is the SAME function the share validator used to decide this
	// share won. Calling it rather than repeating the merge is what guarantees the
	// submitted header is the header that was validated -- including for malformed
	// versionBits, which this path used to treat as a fatal error and so threw away the
	// block for a share the validator had happily accepted.
	versionBytes := stratum.RollVersion(job.Version, versionBits)
	if len(versionBytes) == 0 {
		return "", fmt.Errorf("invalid version hex: %q", job.Version)
	}
	reverseBytes(versionBytes)
	block.Write(versionBytes)

	// Previous block hash (32 bytes)
	// Stratum prevhash was reversed, reverse it back for block
	prevHashBytes, err := hex.DecodeString(job.OriginalPrevHash)
	if err != nil {
		return "", fmt.Errorf("invalid prevHash hex: %w", err)
	}
	reverseBytes(prevHashBytes)
	block.Write(prevHashBytes)

	// Merkle root calculation
	// Start with coinbase hash, then combine with merkle branches
	merkleRoot := doubleSHA256(coinbase)
	for i, branchHex := range job.MerkleBranches {
		branch, err := hex.DecodeString(branchHex)
		if err != nil {
			return "", fmt.Errorf("invalid merkle branch[%d] hex: %w", i, err)
		}
		combined := make([]byte, 64)
		copy(combined[:32], merkleRoot)
		copy(combined[32:], branch)
		merkleRoot = doubleSHA256(combined)
	}
	block.Write(merkleRoot)

	// Time (4 bytes) - ntime from miner is big-endian hex, need little-endian
	ntimeBytes, err := hex.DecodeString(ntime)
	if err != nil {
		return "", fmt.Errorf("invalid ntime hex: %w", err)
	}
	reverseBytes(ntimeBytes)
	block.Write(ntimeBytes)

	// Bits (4 bytes) - big-endian hex, need little-endian
	bitsBytes, err := hex.DecodeString(job.NBits)
	if err != nil {
		return "", fmt.Errorf("invalid nbits hex: %w", err)
	}
	reverseBytes(bitsBytes)
	block.Write(bitsBytes)

	// Nonce (4 bytes) - from miner, big-endian hex, need little-endian
	nonceBytes, err := hex.DecodeString(nonce)
	if err != nil {
		return "", fmt.Errorf("invalid nonce hex: %w", err)
	}
	reverseBytes(nonceBytes)
	block.Write(nonceBytes)

	// TX count (varint) - 1 coinbase + N transactions
	txCount := 1 + len(job.Transactions)
	writeVarInt(&block, uint64(txCount))

	// Coinbase transaction
	block.Write(coinbase)

	// Additional transactions from block template
	for i, txHex := range job.Transactions {
		txBytes, err := hex.DecodeString(txHex)
		if err != nil {
			return "", fmt.Errorf("invalid transaction[%d] hex: %w", i, err)
		}
		block.Write(txBytes)
	}

	return hex.EncodeToString(block.Bytes()), nil
}

// Hash is the block's hash in RPC byte order, from the header at the front of blockHex.
func Hash(blockHex string) (string, error) {
	if len(blockHex) < 160 {
		return "", fmt.Errorf("block hex too short for a header: %d chars", len(blockHex))
	}
	header, err := hex.DecodeString(blockHex[:160])
	if err != nil {
		return "", fmt.Errorf("block header hex: %w", err)
	}
	h := doubleSHA256(header)
	reverseBytes(h)
	return hex.EncodeToString(h), nil
}

// writeVarInt writes a variable-length integer to the buffer
func writeVarInt(buf *bytes.Buffer, n uint64) {
	if n < 0xfd {
		buf.WriteByte(byte(n))
	} else if n <= 0xffff {
		buf.WriteByte(0xfd)
		buf.WriteByte(byte(n))
		buf.WriteByte(byte(n >> 8))
	} else if n <= 0xffffffff {
		buf.WriteByte(0xfe)
		buf.WriteByte(byte(n))
		buf.WriteByte(byte(n >> 8))
		buf.WriteByte(byte(n >> 16))
		buf.WriteByte(byte(n >> 24))
	} else {
		buf.WriteByte(0xff)
		buf.WriteByte(byte(n))
		buf.WriteByte(byte(n >> 8))
		buf.WriteByte(byte(n >> 16))
		buf.WriteByte(byte(n >> 24))
		buf.WriteByte(byte(n >> 32))
		buf.WriteByte(byte(n >> 40))
		buf.WriteByte(byte(n >> 48))
		buf.WriteByte(byte(n >> 56))
	}
}

func reverseBytes(b []byte) {
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
}

func doubleSHA256(data []byte) []byte {
	first := sha256.Sum256(data)
	second := sha256.Sum256(first[:])
	return second[:]
}

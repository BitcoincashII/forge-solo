package main

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/bch2/forge-pool/internal/mining"
)

// The 80-byte block header is assembled TWICE by two functions in two packages:
// buildBlock here (runs only when a block is actually won) and
// internal/stratum.buildBlockHeader (runs on every share, to decide whether it won).
// If they ever disagree, every share still validates, the app looks perfectly healthy,
// and the ONE block a home miner finds in months is submitted with a different header
// than the one that met the target -- rejected, and gone.
//
// Neither function had a single test. Both are pinned here to the SAME expected header,
// so a change to either that the other does not match fails immediately.
// internal/stratum/blockheader_test.go asserts the identical constant; keep them equal.
const (
	// version-rolling case: job version 20000000, miner submits the full rolled nVersion
	// 2fffe000; only the 1fffe000 mask bits may come from the miner.
	testJobVersion              = "20000000"
	testRolledVersion           = "2fffe000"
	testOriginalPrev            = "000000000000000000000123456789abcdef0123456789abcdef0123456789ab"
	testStratumPrev             = "456789abcdef0123456789abcdef0123456789ab000001230000000000000000"
	testNTime                   = "6712a3b4"
	testNBits                   = "1d00ffff"
	testNonce                   = "deadbeef"
	testCoinbaseHex             = "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0e03a06b01062f466f7267652f00000000010000000000000000232102aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899ac00000000"
	testMerkleRootHex           = "78711b273e76d5b2eb88dda97f65504719275c48e5456d6d963cac0bde267e42"
	testExpectedMaskedVersionLE = "00e0ff3f"
	testExpectedHeader          = "00e0ff2fab8967452301efcdab8967452301efcdab89674523010000000000000000000078711b273e76d5b2eb88dda97f65504719275c48e5456d6d963cac0bde267e42b4a31267ffff001defbeadde"
)

func TestBuildBlockProducesTheCanonicalHeader(t *testing.T) {
	coinbase, err := hex.DecodeString(testCoinbaseHex)
	if err != nil {
		t.Fatalf("test coinbase: %v", err)
	}
	job := &mining.Job{
		Version:          testJobVersion,
		OriginalPrevHash: testOriginalPrev,
		NBits:            testNBits,
		MerkleBranches:   nil,
	}
	blockHex, err := buildBlock(job, coinbase, testNTime, testNonce, testRolledVersion)
	if err != nil {
		t.Fatalf("buildBlock: %v", err)
	}
	if len(blockHex) < 160 {
		t.Fatalf("block is %d hex chars, too short to contain an 80-byte header", len(blockHex))
	}
	if got := strings.ToLower(blockHex[:160]); got != testExpectedHeader {
		t.Errorf("submitted header does not match the validated header\n got: %s\nwant: %s", got, testExpectedHeader)
	}
}

// The mask must be honoured in BOTH directions: bits outside 1fffe000 come from the job,
// bits inside it come from the miner. A miner that submits a version with the top bits
// cleared must not be able to change the job's non-rollable bits.
func TestBuildBlockVersionRollingIgnoresNonRollableBitsFromMiner(t *testing.T) {
	coinbase, _ := hex.DecodeString(testCoinbaseHex)
	job := &mining.Job{
		Version:          testJobVersion,
		OriginalPrevHash: testOriginalPrev,
		NBits:            testNBits,
	}
	// 0fffe000: same rollable bits as the vector, but the miner also tries to clear the
	// job's 0x20000000 bit. The header must keep the job's bit.
	blockHex, err := buildBlock(job, coinbase, testNTime, testNonce, "0fffe000")
	if err != nil {
		t.Fatalf("buildBlock: %v", err)
	}
	if got := strings.ToLower(blockHex[:8]); got != "00e0ff2f" {
		t.Errorf("version field = %s, want 00e0ff2f (job's non-rollable bits preserved)", got)
	}
}

// Pins EVERY bit of the version-rolling mask. A miner submitting all-ones must end up
// with exactly the mask bits set and every non-rollable bit taken from the job, so any
// widening or narrowing of the mask in either copy changes this value.
// (The earlier cases cannot see a mask change at bit 12 -- job and miner agree there.)
func TestBuildBlockVersionMaskIsExact(t *testing.T) {
	coinbase, _ := hex.DecodeString(testCoinbaseHex)
	job := &mining.Job{
		Version:          testJobVersion,
		OriginalPrevHash: testOriginalPrev,
		NBits:            testNBits,
	}
	blockHex, err := buildBlock(job, coinbase, testNTime, testNonce, "ffffffff")
	if err != nil {
		t.Fatalf("buildBlock: %v", err)
	}
	// 0x20000000 (job, non-rollable) | 0x1fffe000 (mask, from miner) = 0x3fffe000
	if got := strings.ToLower(blockHex[:8]); got != testExpectedMaskedVersionLE {
		t.Errorf("version field = %s, want %s (job bits | full mask)", got, testExpectedMaskedVersionLE)
	}
}

// Malformed versionBits must produce the SAME header the validator produced, not an error.
//
// A miner that is not version-rolling may still put junk in the optional 6th submit
// parameter. The validator tolerates that and validates against the job version; this path
// used to return an error and abandon the submission, so that miner's shares all validated
// and the one block it found was silently discarded. Both sides now call the same
// RollVersion, and this test is what keeps the tolerant behaviour on the submit side.
func TestBuildBlockToleratesMalformedVersionBits(t *testing.T) {
	coinbase, _ := hex.DecodeString(testCoinbaseHex)
	job := &mining.Job{
		Version:          testJobVersion,
		OriginalPrevHash: testOriginalPrev,
		NBits:            testNBits,
	}
	for _, bad := range []string{"zzzzzzzz", "2fffe00", "not-hex", "0x2fffe000"} {
		blockHex, err := buildBlock(job, coinbase, testNTime, testNonce, bad)
		if err != nil {
			t.Fatalf("buildBlock(versionBits=%q) returned %v; the validator accepted this share, "+
				"so refusing to build its block loses the block", bad, err)
		}
		if got := strings.ToLower(blockHex[:8]); got != "00000020" {
			t.Errorf("versionBits=%q gave version field %s, want the job version 00000020", bad, got)
		}
	}
}

// An empty rolled version means the miner is not version-rolling: the job's version must
// go into the header untouched.
func TestBuildBlockWithoutVersionRollingUsesJobVersion(t *testing.T) {
	coinbase, _ := hex.DecodeString(testCoinbaseHex)
	job := &mining.Job{
		Version:          testJobVersion,
		OriginalPrevHash: testOriginalPrev,
		NBits:            testNBits,
	}
	blockHex, err := buildBlock(job, coinbase, testNTime, testNonce, "")
	if err != nil {
		t.Fatalf("buildBlock: %v", err)
	}
	if got := strings.ToLower(blockHex[:8]); got != "00000020" {
		t.Errorf("version field = %s, want 00000020 (job version 20000000, little-endian)", got)
	}
}

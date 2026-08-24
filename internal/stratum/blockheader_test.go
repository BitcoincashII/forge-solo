package stratum

import (
	"encoding/hex"
	"strings"
	"testing"
)

// Companion to cmd/stratum/blockheader_test.go. buildBlockHeader (used to decide whether
// a share won) and buildBlock (used to submit the block) are separate implementations of
// the same 80 bytes, in different packages. Both are pinned to this identical constant so
// they cannot drift apart silently -- the failure mode of a drift is a won block that is
// submitted with the wrong header and rejected.
const (
	hdrJobVersion              = "20000000"
	hdrRolledVersion           = "2fffe000"
	hdrStratumPrev             = "456789abcdef0123456789abcdef0123456789ab000001230000000000000000"
	hdrMerkleRootHex           = "78711b273e76d5b2eb88dda97f65504719275c48e5456d6d963cac0bde267e42"
	hdrNTime                   = "6712a3b4"
	hdrNBits                   = "1d00ffff"
	hdrNonce                   = "deadbeef"
	hdrExpectedMaskedVersionLE = "00e0ff3f"
	hdrExpectedHeader          = "00e0ff2fab8967452301efcdab8967452301efcdab89674523010000000000000000000078711b273e76d5b2eb88dda97f65504719275c48e5456d6d963cac0bde267e42b4a31267ffff001defbeadde"
)

func TestBuildBlockHeaderProducesTheCanonicalHeader(t *testing.T) {
	merkleRoot, err := hex.DecodeString(hdrMerkleRootHex)
	if err != nil {
		t.Fatalf("test merkle root: %v", err)
	}
	job := &Job{
		Version:       hdrJobVersion,
		PrevBlockHash: hdrStratumPrev,
		NBits:         hdrNBits,
	}
	header := buildBlockHeader(job, merkleRoot, hdrNTime, hdrNonce, hdrRolledVersion)
	if len(header) != 80 {
		t.Fatalf("header is %d bytes, want 80", len(header))
	}
	if got := strings.ToLower(hex.EncodeToString(header)); got != hdrExpectedHeader {
		t.Errorf("validated header does not match the submitted header\n got: %s\nwant: %s", got, hdrExpectedHeader)
	}
}

// Companion to TestBuildBlockVersionMaskIsExact: same all-ones probe, same expected
// value, so the two copies of the mask are locked to each other bit for bit.
func TestBuildBlockHeaderVersionMaskIsExact(t *testing.T) {
	merkleRoot, _ := hex.DecodeString(hdrMerkleRootHex)
	job := &Job{
		Version:       hdrJobVersion,
		PrevBlockHash: hdrStratumPrev,
		NBits:         hdrNBits,
	}
	header := buildBlockHeader(job, merkleRoot, hdrNTime, hdrNonce, "ffffffff")
	if got := strings.ToLower(hex.EncodeToString(header[:4])); got != hdrExpectedMaskedVersionLE {
		t.Errorf("version field = %s, want %s (job bits | full mask)", got, hdrExpectedMaskedVersionLE)
	}
}

// The validator's half of TestBuildBlockToleratesMalformedVersionBits: junk in the rolled
// version means "no rolling", and both paths must agree on that.
func TestBuildBlockHeaderToleratesMalformedVersionBits(t *testing.T) {
	merkleRoot, _ := hex.DecodeString(hdrMerkleRootHex)
	job := &Job{
		Version:       hdrJobVersion,
		PrevBlockHash: hdrStratumPrev,
		NBits:         hdrNBits,
	}
	for _, bad := range []string{"zzzzzzzz", "2fffe00", "not-hex", "0x2fffe000"} {
		header := buildBlockHeader(job, merkleRoot, hdrNTime, hdrNonce, bad)
		if len(header) != 80 {
			t.Fatalf("versionBits=%q produced a %d-byte header", bad, len(header))
		}
		if got := strings.ToLower(hex.EncodeToString(header[:4])); got != "00000020" {
			t.Errorf("versionBits=%q gave version field %s, want the job version 00000020", bad, got)
		}
	}
}

func TestBuildBlockHeaderWithoutVersionRollingUsesJobVersion(t *testing.T) {
	merkleRoot, _ := hex.DecodeString(hdrMerkleRootHex)
	job := &Job{
		Version:       hdrJobVersion,
		PrevBlockHash: hdrStratumPrev,
		NBits:         hdrNBits,
	}
	header := buildBlockHeader(job, merkleRoot, hdrNTime, hdrNonce, "")
	if got := strings.ToLower(hex.EncodeToString(header[:4])); got != "00000020" {
		t.Errorf("version field = %s, want 00000020", got)
	}
}

package blockbuild

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/mining"
)

// The same vector cmd/stratum/blockheader_test.go and internal/stratum/blockheader_test.go pin:
// the header a found block is submitted with must be the header its share was validated as.
// Keep the three equal.
const (
	testJobVersion     = "20000000"
	testRolledVersion  = "2fffe000"
	testOriginalPrev   = "000000000000000000000123456789abcdef0123456789abcdef0123456789ab"
	testNTime          = "6712a3b4"
	testNBits          = "1d00ffff"
	testNonce          = "deadbeef"
	testCoinbaseHex    = "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0e03a06b01062f466f7267652f00000000010000000000000000232102aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899ac00000000"
	testExpectedHeader = "00e0ff2fab8967452301efcdab8967452301efcdab89674523010000000000000000000078711b273e76d5b2eb88dda97f65504719275c48e5456d6d963cac0bde267e42b4a31267ffff001defbeadde"
)

func TestBlockProducesTheCanonicalHeader(t *testing.T) {
	coinbase, _ := hex.DecodeString(testCoinbaseHex)
	job := &mining.Job{Version: testJobVersion, OriginalPrevHash: testOriginalPrev, NBits: testNBits,
		Transactions: []string{"aabb", "ccdd"}}
	blockHex, err := Block(job, coinbase, testNTime, testNonce, testRolledVersion)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.ToLower(blockHex[:160]); got != testExpectedHeader {
		t.Fatalf("BLOCK-HEADER: submitted header does not match the validated header\n got: %s\nwant: %s", got, testExpectedHeader)
	}
	// After the header: the transaction count (coinbase + 2), the coinbase, then the job's transactions.
	if rest := blockHex[160:]; rest != "03"+testCoinbaseHex+"aabbccdd" {
		t.Fatalf("BLOCK-BODY: %s", rest)
	}
	h, err := Hash(blockHex)
	if err != nil || len(h) != 64 {
		t.Fatalf("BLOCK-HASH: %q %v", h, err)
	}
}

// The coinbase is the two halves around the two extranonces, byte for byte.
func TestCoinbaseJoinsTheHalvesAroundTheExtranonces(t *testing.T) {
	cb, err := Coinbase("0102", "a1a2a3a4", "b1b2b3b4b5b6b7b8", "0304")
	if err != nil || hex.EncodeToString(cb) != "0102a1a2a3a4b1b2b3b4b5b6b7b80304" {
		t.Fatalf("COINBASE: %x %v", cb, err)
	}
	if _, err := Coinbase("0102", "zz", "00", "03"); err == nil {
		t.Fatal("COINBASE-HEX: a malformed extranonce was accepted")
	}
}

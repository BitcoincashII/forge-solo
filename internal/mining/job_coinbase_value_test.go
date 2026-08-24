package mining

import (
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"
)

// A Job must carry the coinbase value that is actually baked into ITS coinbase.
// Without this, the block-found path has nothing block-specific to record and
// falls back to a process-global "most recent template" value, mis-stating the
// fee total of every block solved across a template refresh.
func TestCreateJob_CarriesItsOwnCoinbaseValue(t *testing.T) {
	jm := &JobManager{pubkeyHash: make([]byte, 20)}

	const sats = int64(50_00012345) // 50 BCH2 subsidy + 0.00012345 in fees
	template := &BlockTemplate{
		Version:           0x20000000,
		PreviousBlockHash: strings.Repeat("00", 31) + "ff",
		CoinbaseValue:     sats,
		Bits:              "207fffff",
		Height:            250,
		CurTime:           1000000,
		Target:            strings.Repeat("ff", 32),
	}

	job := jm.CreateJob(template)
	if job == nil {
		t.Fatal("CreateJob returned nil")
	}
	if job.CoinbaseValue != sats {
		t.Fatalf("job.CoinbaseValue = %d, want %d — the block-found path has no "+
			"block-specific reward to record and will fall back to the global",
			job.CoinbaseValue, sats)
	}

	// The field must agree with the bytes the miner actually hashes, or we would
	// simply be recording a second, independent number that happens to look right.
	cb := job.CoinBase1 + job.CoinBase2
	raw, err := hex.DecodeString(cb)
	if err != nil {
		t.Fatalf("coinbase is not hex: %v", err)
	}
	var le [8]byte
	binary.LittleEndian.PutUint64(le[:], uint64(sats))
	if !strings.Contains(cb, hex.EncodeToString(le[:])) {
		t.Fatalf("the %d-satoshi output value is not present in the coinbase the "+
			"miner hashes (%d bytes); job.CoinbaseValue describes a different block",
			sats, len(raw))
	}
}

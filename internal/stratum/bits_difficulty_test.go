package stratum

import (
	"math"
	"testing"
)

// Vectors from real chains. The regtest one is the reason this test exists: the exponent
// was compared as an unsigned value, so any target EASIER than difficulty 1 (exp > 29)
// wrapped 29-exp to a huge positive number and produced +Inf. A pool that believes the
// network difficulty is infinite never recognises a block: `share.ActualDiff >= networkDiff`
// cannot be true. Unit tests all passed; running the binary against a regtest node found it.
func TestBitsToDifficulty(t *testing.T) {
	tests := []struct {
		name string
		bits string
		want float64
	}{
		// getblockchaininfo on a fresh regtest node reports exactly this difficulty.
		{"regtest powLimit (exp 32 > 29)", "207fffff", 4.656542373906925e-10},
		{"difficulty 1 (exp 29)", "1d00ffff", 1},
		{"easier than difficulty 1 (exp 30)", "1e0ffff0", 0.000244140625},
		{"mainnet-scale target (exp 24)", "1806b99f", 163491654908.95926},
		{"BCH2-scale target (exp 26)", "1a05db8b", 2864140.5078109736},
		{"zero bits", "00000000", 0},
		{"zero mantissa", "1d000000", 0},
		{"not hex", "zzzz", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := BitsToDifficulty(tc.bits)
			if math.IsInf(got, 0) || math.IsNaN(got) {
				t.Fatalf("BitsToDifficulty(%s) = %v — a non-finite difficulty silently disables block detection", tc.bits, got)
			}
			if tc.want == 0 {
				if got != 0 {
					t.Fatalf("BitsToDifficulty(%s) = %v, want 0", tc.bits, got)
				}
				return
			}
			// Relative tolerance: these are floating-point reconstructions of a compact target.
			if diff := math.Abs(got-tc.want) / tc.want; diff > 1e-4 {
				t.Errorf("BitsToDifficulty(%s) = %v, want ~%v (relative error %v)", tc.bits, got, tc.want, diff)
			}
		})
	}
}

// Every exponent a compact target can carry must produce a finite, positive difficulty.
func TestBitsToDifficultyIsFiniteAcrossAllExponents(t *testing.T) {
	for exp := 1; exp <= 0x22; exp++ {
		bits := uint32(exp)<<24 | 0x00ffff
		hex := ""
		const digits = "0123456789abcdef"
		for i := 7; i >= 0; i-- {
			hex += string(digits[(bits>>(uint(i)*4))&0xf])
		}
		got := BitsToDifficulty(hex)
		if math.IsInf(got, 0) || math.IsNaN(got) || got < 0 {
			t.Errorf("BitsToDifficulty(%s) (exponent %d) = %v", hex, exp, got)
		}
	}
}

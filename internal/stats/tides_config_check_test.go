package stats

import (
	"testing"
)

// checkTidesConfig exercises the TIDES settings against whichever backend InitDB opened. Shared
// by the SQLite and Postgres entry points so both backends answer the same questions.
func checkTidesConfig(t *testing.T) {
	t.Helper()

	// Nothing chosen yet: solo, the mode every install ran before TIDES existed.
	if mode, err := GetPayoutMode(); err != nil || mode != PayoutModeSolo {
		t.Fatalf("TIDES-CFG-DEFAULT: fresh payout mode = %q, %v; want solo", mode, err)
	}

	// The mode is a column of the settings row, so saving it must not blank the other settings
	// -- and saving those must not blank it.
	const addr = "bitcoincashii:qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzse6qye33q"
	if err := SavePoolConfig(addr, "", "MyTag"); err != nil {
		t.Fatalf("SavePoolConfig: %v", err)
	}
	if err := SavePayoutMode(PayoutModeTides); err != nil {
		t.Fatalf("SavePayoutMode: %v", err)
	}
	if mode, err := GetPayoutMode(); err != nil || mode != PayoutModeTides {
		t.Fatalf("TIDES-CFG-ROUNDTRIP: payout mode = %q, %v; want tides", mode, err)
	}
	if pool, _, tag, err := GetPoolConfig(); err != nil || pool != addr || tag != "MyTag" {
		t.Fatalf("TIDES-CFG-KEEPS-OTHERS: saving the mode changed the settings row: %q %q %v", pool, tag, err)
	}
	if err := SavePoolConfig(addr, "", "Other"); err != nil {
		t.Fatalf("SavePoolConfig: %v", err)
	}
	if mode, _ := GetPayoutMode(); mode != PayoutModeTides {
		t.Fatalf("TIDES-CFG-SURVIVES-SAVE: saving the other settings reset the mode to %q", mode)
	}

	// Only the two known modes are stored.
	if err := SavePayoutMode("pplns"); err == nil {
		t.Fatal("TIDES-CFG-VALIDATES: an unknown payout mode was stored")
	}
	if mode, _ := GetPayoutMode(); mode != PayoutModeTides {
		t.Fatalf("TIDES-CFG-VALIDATES: a refused save still changed the mode to %q", mode)
	}

	// The gateway identity: made once, then the same on every later ask, whatever fresh seed
	// the caller offers.
	first, err := GatewaySeed("aa11")
	if err != nil || first != "aa11" {
		t.Fatalf("TIDES-CFG-SEED: first seed = %q, %v; want the one offered", first, err)
	}
	again, err := GatewaySeed("bb22")
	if err != nil || again != "aa11" {
		t.Fatalf("TIDES-CFG-SEED-STABLE: second ask = %q, %v; the stored seed must win", again, err)
	}
}

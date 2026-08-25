//go:build sqlite

package stats

import (
	"path/filepath"
	"testing"
)

// The sqlite backend must round-trip a per-miner 1175 payout address, exactly as postgres
// does.
//
// Its schema declares miners.address_1175, but the INSERT never wrote it and neither the
// single-miner read nor the bulk read selected it. On the Windows build the API accepted a
// 1175 address, reported it back from the in-memory copy, and then the next settings
// reload (every 10s) blanked it — losing the address with no error anywhere.
func TestSQLiteRoundTripsPerMinerAddress1175(t *testing.T) {
	if err := InitDB(filepath.Join(t.TempDir(), "s1175.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	defer CloseDB()

	const (
		miner = "bitcoincashii:qzeh9rcyyy8jlyalgh84e8fst6xh649hly2tfwgvwc"
		esf   = "resf1qdxqhqrmk5wrl9gmjhal26rv9sjhu9zh5sa0epz"
	)
	if err := SaveMinerSettings(&MinerSettings{
		Address:     miner,
		SoloMining:  true,
		ManualDiff:  0,
		MinPayout:   0,
		Address1175: esf,
	}); err != nil {
		t.Fatalf("SaveMinerSettings: %v", err)
	}

	got, err := GetMinerSettingsDB(miner)
	if err != nil {
		t.Fatalf("GetMinerSettingsDB: %v", err)
	}
	if got.Address1175 != esf {
		t.Errorf("single-miner read returned Address1175=%q, want %q — the address was "+
			"never persisted, or is not selected back", got.Address1175, esf)
	}

	// The bulk read is what the API's 10-second reload uses; if it omits the column the
	// in-memory copy is blanked on the next tick even when the row is correct on disk.
	all := LoadAllMinerSettings()
	s, ok := all[miner]
	if !ok {
		t.Fatalf("LoadAllMinerSettings lost the miner entirely")
	}
	if s.Address1175 != esf {
		t.Errorf("bulk read returned Address1175=%q, want %q — the 10s settings reload "+
			"blanks the address in memory", s.Address1175, esf)
	}
	if !s.SoloMining {
		t.Error("bulk read lost SoloMining; the added column shifted the scan")
	}

	// Updating must not silently drop it either.
	if err := SaveMinerSettings(&MinerSettings{Address: miner, SoloMining: true, Address1175: ""}); err != nil {
		t.Fatalf("second SaveMinerSettings: %v", err)
	}
	if got, _ := GetMinerSettingsDB(miner); got.Address1175 != "" {
		t.Errorf("clearing the address left %q behind", got.Address1175)
	}
}

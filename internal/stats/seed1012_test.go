//go:build seed1012

package stats

import (
	"os"
	"strings"
	"testing"
)

// The 1.0.12 half of the seed scripts/it-pg-to-sqlite.sh moves: copied into a worktree of v1.0.12
// and run there, with -tags seed1012, against IT_PG, it makes 1.0.12's database with 1.0.12's own
// code, its InitDB and the saves its api and stratum make for the settings: the payout addresses
// and tag, TIDES mode, the TIDES key and a miner's settings. testdata/migrate/seed-1012.sql then
// adds the rest, and its settings rows only where these did not write them, so the values here are
// the file's. The tag is its own because this is 1.0.12's test, not this tree's: 1.0.12's InitDB
// takes a PostgreSQL address, this tree's a file.
func TestITSeed1012(t *testing.T) {
	dsn := os.Getenv("IT_PG")
	if dsn == "" {
		t.Fatal("IT-SEED-SETUP: IT_PG is not set (scripts/it-pg-to-sqlite.sh sets it)")
	}
	if err := InitDB(dsn); err != nil {
		t.Fatalf("IT-SEED-INIT: %v", err)
	}
	defer CloseDB()
	const (
		minerA = "bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2"
		esf    = "esf1quhj7te09uhj7te09uhj7te09uhj7te09dnlk6x"
	)
	if err := SavePoolConfig(minerA, esf, "/forge ü 1.0.12/"); err != nil {
		t.Fatalf("IT-SEED-POOL: %v", err)
	}
	if err := SavePayoutMode(PayoutModeTides); err != nil {
		t.Fatalf("IT-SEED-MODE: %v", err)
	}
	key := strings.Repeat("c3", 32)
	if got, err := GatewaySeed(key); err != nil || got != key {
		t.Fatalf("IT-SEED-KEY: %q, %v", got, err)
	}
	if err := SaveMinerSettings(&MinerSettings{Address: minerA, SoloMining: true, Address1175: esf}); err != nil {
		t.Fatalf("IT-SEED-MINER: %v", err)
	}
}

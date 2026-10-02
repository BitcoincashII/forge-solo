//go:build !sqlite

package stats

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The two halves of scripts/it-postgres-upgrade.sh: a database made by the previous release's image
// is filled the way an install fills it (seed), then opened by this tree's image and checked
// (check). TIDES_PG_DB is the database; PG_UPGRADE_PHASE says which half runs.

const (
	upgradeMiner  = "bitcoincashii:qqupgrade000000000000000000000000000000000000"
	upgradeTag    = "/upgrade-test/"
	upgradeBlocks = 5
)

var upgradeKey = strings.Repeat("a5", 32)

// upgradeHours is how many hourly chunks of stored shares the install has: 1,100 by default, as
// many as the install DATA-1 was found on, more than a small board's lock table takes at once.
func upgradeHours() int {
	if n, err := strconv.Atoi(os.Getenv("UPGRADE_HOURS")); err == nil && n > 0 {
		return n
	}
	return 1100
}

func upgradePhase(t *testing.T, phase string) {
	t.Helper()
	connStr := os.Getenv("TIDES_PG_DB")
	if connStr == "" || os.Getenv("PG_UPGRADE_PHASE") != phase {
		t.Skipf("run by scripts/it-postgres-upgrade.sh (PG_UPGRADE_PHASE=%s)", phase)
	}
	if err := InitDB(connStr); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(CloseDB)
}

func TestPostgresUpgradeSeed(t *testing.T) {
	upgradePhase(t, "seed")
	if err := SavePoolConfig(upgradeMiner, "", upgradeTag); err != nil {
		t.Fatal(err)
	}
	if err := SavePayoutMode(PayoutModeTides); err != nil {
		t.Fatal(err)
	}
	if got, err := GatewaySeed(upgradeKey); err != nil || got != upgradeKey {
		t.Fatalf("PGUP-SEED-KEY: %q, %v", got, err)
	}
	for i := 0; i < upgradeBlocks; i++ {
		h := int64(90000 + i)
		if err := SaveSoloBlockCoinbaseDirect(upgradeMiner, h, 3.125, fmt.Sprintf("%064x", h)); err != nil {
			t.Fatal(err)
		}
	}
	hours := upgradeHours()
	for from := 1; from <= hours; from += 100 {
		if _, err := db.Exec(`INSERT INTO shares (time, miner_address, worker_name, difficulty, is_solo)
			SELECT now() - make_interval(hours => g), $3, 'rig1', 1024, true
			FROM generate_series($1::int, $2::int) g`, from, min(from+99, hours), upgradeMiner); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPostgresUpgradeCheck(t *testing.T) {
	upgradePhase(t, "check")
	text := func(q string) string {
		var s string
		if err := db.QueryRow(q).Scan(&s); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return s
	}
	chunks := func() int {
		n, _ := strconv.Atoi(text(`SELECT COUNT(*)::text FROM timescaledb_information.chunks WHERE hypertable_name = 'shares'`))
		return n
	}
	if v, ext := text(`SHOW server_version`), text(`SELECT extversion FROM pg_extension WHERE extname = 'timescaledb'`); !strings.HasPrefix(v, "16.15") || ext != "2.17.2" {
		t.Fatalf("PGUP-VERSIONS: PostgreSQL %s with TimescaleDB %s; want 16.15 with 2.17.2 still", v, ext)
	}
	if addr, _, tag, err := GetPoolConfig(); err != nil || addr != upgradeMiner || tag != upgradeTag {
		t.Fatalf("PGUP-SETTINGS: the saved settings read back as %q %q, %v", addr, tag, err)
	}
	if mode, err := GetPayoutMode(); err != nil || mode != PayoutModeTides {
		t.Fatalf("PGUP-MODE: the payout mode reads back as %q, %v", mode, err)
	}
	if got, err := GatewaySeed(strings.Repeat("b6", 32)); err != nil || got != upgradeKey {
		t.Fatalf("PGUP-GATEWAY-KEY: the install's TIDES identity changed: %v", err)
	}
	if _, found, earned, err := SoloBlocksSummary(upgradeMiner); err != nil || found != upgradeBlocks || math.Abs(earned-upgradeBlocks*3.125) > 1e-9 {
		t.Fatalf("PGUP-BLOCKS: %d blocks, %.8f, %v", found, earned, err)
	}
	if n := chunks(); n < upgradeHours() {
		t.Fatalf("PGUP-CHUNKS-SETUP: %d chunks, want at least %d", n, upgradeHours())
	}
	if _, err := ClearSoloShares(); err != nil {
		t.Fatalf("PGUP-CLEAR: the old install's stored shares could not be cleared: %v", err)
	}
	if n := chunks(); n != 0 {
		t.Fatalf("PGUP-CLEAR: %d chunks left", n)
	}
	if _, err := db.Exec(`INSERT INTO shares (time, miner_address, worker_name, difficulty, is_solo) VALUES (now(), $1, 'rig1', 1024, false)`, upgradeMiner); err != nil || chunks() != 1 {
		t.Fatalf("PGUP-NEW-CHUNK: the new server made no chunk: %v", err)
	}
}

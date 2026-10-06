//go:build sqlite

package stats

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// storedTime is the one form the SQLite build stores a time in: SQLiteTime's.
var storedTime = regexp.MustCompile(`^\d{4}-\d\d-\d\d \d\d:\d\d:\d\d$`)

// Every time the SQLite build writes is UTC, to the second, in SQLite's own form, whatever the
// computer's zone: this package's tests run five hours behind UTC (TestMain). A time.Time bound as
// it is was stored in Go's form and in the local zone, "2026-10-03 20:01:25.6793959 -0500 CDT
// m=+0.03", and the dashboard, which reads the first 19 characters as UTC, listed it five hours off.
func TestEveryStoredTimeIsUTCSeconds(t *testing.T) {
	if _, offset := time.Now().Zone(); offset == 0 {
		t.Fatal("TIME-SETUP: the local zone is UTC, so a time stored in it cannot show (see TestMain)")
	}
	if err := InitDB(filepath.Join(t.TempDir(), "times.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(CloseDB)
	const miner = "bitcoincashii:qtimesolominer000000000000000000000000000"
	const other = "bitcoincashii:qtimepoolminer000000000000000000000000000"
	hash := func(n int) string { return fmt.Sprintf("%064x", n) }
	do := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("TIME-SETUP: %s: %v", what, err)
		}
	}

	// Solo blocks: recorded, replaced at their height while an unpaid row is there, confirmed,
	// orphaned.
	do("record", SaveSoloBlockCoinbaseDirect(miner, 100, 3.125, hash(1)))
	do("unpaid row", SavePayout(other, 100, 1))
	do("replace", SaveSoloBlockCoinbaseDirectAt(miner, 100, 3.125, hash(2), time.Now()))
	do("record", SaveSoloBlockCoinbaseDirect(miner, 101, 3.125, hash(3)))
	do("confirm", ConfirmSoloBlock(101))
	do("record", SaveSoloBlockCoinbaseDirect(miner, 102, 3.125, hash(4)))
	_, err := OrphanSoloBlock(102)
	do("orphan", err)

	// The pool-style ledger.
	do("block", SaveBlockDBWithSolo(other, 103, hash(5), 3.125, false))
	do("block and payout", SavePayoutAtomicWithSolo(other, 104, 1, hash(6), false))
	do("payout", SavePayout(other, 105, 1))
	_, _, err = VoidOrphanedPayouts(105)
	do("void", err)
	do("payout", SavePayout(other, 106, 1))
	_, rows, _, err := ReserveMaturePayouts(other, 1000)
	do("reserve", err)
	var ids []int64
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	do("finalize", FinalizePayoutRows(ids, hash(7)))

	// Settings, the PIN, a share and the TIDES key.
	do("settings", SavePoolSettings(miner, "", "", PayoutModeSolo))
	do("pool config", SavePoolConfig(miner, "", ""))
	do("payout mode", SavePayoutMode(PayoutModeTides))
	do("miner settings", SaveMinerSettings(&MinerSettings{Address: miner, SoloMining: true}))
	do("pin", SetSettingsPinHash(other, "pinhash"))
	do("share", SaveShare(other, "rig", 1, false))
	_, err = GatewaySeed(strings.Repeat("ab", 32))
	do("gateway key", err)
	// A worker's best share, as the stratum keeps it.
	bests := &StatsManager{workers: make(map[string]*WorkerStats)}
	_, err = bests.LoadBestShares()
	do("best shares read", err)
	bests.UpdateWorker(miner, "rig", true, 1, 5e9)
	do("best share", bests.WriteBestShares())

	// The 1175 ledger: replaced at its height, orphaned and restored, confirmed and settled.
	do("1175 record", Record1175Block(200, hash(8), 25, miner, true))
	do("1175 distribute", Distribute1175Block(200, 0))
	do("1175 replace", Record1175BlockAt(200, hash(9), 25, miner, true, time.Now()))
	do("1175 record", Record1175Block(201, hash(10), 25, miner, true))
	do("1175 distribute", Distribute1175Block(201, 0))
	do("1175 orphan", Orphan1175Block(201))
	_, err = Restore1175Block(201, hash(10))
	do("1175 restore", err)
	do("1175 record", Record1175Block(202, hash(11), 25, miner, true))
	do("1175 distribute", Distribute1175Block(202, 0))
	do("1175 confirm", Confirm1175Block(202))
	_, err = Settle1175ByCoinbase(miner)
	do("1175 settle", err)

	// Every time column of every table.
	now := time.Now()
	tables, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var n string
		tables.Scan(&n)
		names = append(names, n)
	}
	tables.Close()
	columns := 0
	for _, table := range names {
		cols, err := db.Query(`SELECT name, type FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatal(err)
		}
		var timeCols []string
		for cols.Next() {
			var name, typ string
			cols.Scan(&name, &typ)
			if u := strings.ToUpper(typ); strings.Contains(u, "DATE") || strings.Contains(u, "TIME") {
				timeCols = append(timeCols, name)
			}
		}
		cols.Close()
		for _, col := range timeCols {
			columns++
			vals, err := db.Query(fmt.Sprintf(`SELECT typeof(%[1]s), CAST(%[1]s AS TEXT) FROM %[2]s WHERE %[1]s IS NOT NULL`, col, table))
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			for vals.Next() {
				n++
				var typ, v string
				vals.Scan(&typ, &v)
				switch {
				case typ != "text":
					t.Errorf("TIME-TYPE: %s.%s holds a %s, %q", table, col, typ, v)
				case !storedTime.MatchString(v):
					t.Errorf("TIME-FORM: %s.%s holds %q, not UTC to the second", table, col, v)
				default:
					at, _ := time.ParseInLocation("2006-01-02 15:04:05", v, time.UTC)
					if d := at.Sub(now); d < -5*time.Second || d > 5*time.Second {
						t.Errorf("TIME-ZONE: %s.%s holds %s, %v from the UTC time it was written at", table, col, v, d.Round(time.Second))
					}
				}
			}
			vals.Close()
			if n == 0 {
				t.Errorf("TIME-UNWRITTEN: nothing above writes %s.%s, so its form is not checked", table, col)
			}
		}
	}
	if columns < 12 {
		t.Fatalf("TIME-SETUP: only %d time columns found", columns)
	}
}

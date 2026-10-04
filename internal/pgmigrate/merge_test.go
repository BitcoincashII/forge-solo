//go:build sqlite

package pgmigrate

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/dblock"
	"github.com/BitcoincashII/forge-solo/internal/stats"
)

func init() {
	childParts["hold-db"] = holdDBChild
	childParts["raw-hold"] = rawHoldChild
	childParts["lock-only"] = lockOnlyChild
}

// holdDBChild plays the api: it opens the database as every Forge Solo program does (InitDB, which
// holds the in-use lock) and says "open"; for "write <address>" it saves that miner's settings and
// says "wrote"; when its stdin closes it closes the database and says "closed".
func holdDBChild(path string) int {
	log.SetOutput(io.Discard)
	if err := stats.InitDB(path); err != nil {
		fmt.Println("error:", err)
		return 1
	}
	fmt.Println("open")
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		addr, ok := strings.CutPrefix(sc.Text(), "write ")
		if !ok {
			continue
		}
		if err := stats.SaveMinerSettings(&stats.MinerSettings{Address: addr, SoloMining: true}); err != nil {
			fmt.Println("error:", err)
			return 1
		}
		fmt.Println("wrote")
	}
	stats.CloseDB()
	fmt.Println("closed")
	return 0
}

// rawHoldChild has the database open without the in-use lock, as a program that does not take it
// would: it reads once, says "open", and keeps the database open until its stdin closes.
func rawHoldChild(path string) int {
	db, err := sql.Open("sqlite", stats.SQLiteDSN(path))
	if err != nil {
		fmt.Println("error:", err)
		return 1
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM blocks`).Scan(&n); err != nil {
		fmt.Println("error:", err)
		return 1
	}
	fmt.Println("open")
	waitForStdin()
	db.Close()
	fmt.Println("closed")
	return 0
}

// lockOnlyChild holds the in-use lock and nothing else: a program between taking the lock and
// opening the database.
func lockOnlyChild(path string) int {
	l, err := dblock.Shared(dblock.Path(path), 0)
	if err != nil {
		fmt.Println("error:", err)
		return 1
	}
	fmt.Println("open")
	waitForStdin()
	l.Release()
	fmt.Println("closed")
	return 0
}

// emptyPG is a 1.0.12 database with its tables and no rows.
func emptyPG() *MemSource {
	m := NewMemSource(Info{ServerVersion: "16.6", SystemIdentifier: "7692687789219831848"})
	for name, types := range types1012 {
		m.AddTable(name, types)
	}
	return m
}

// pgBlock records a solo block found at h and its coinbase-direct payout, as 1.0.12 did.
func pgBlock(m *MemSource, h int64, hash, miner, status string, created time.Time) {
	m.Insert("blocks", map[string]any{"id": h, "height": h, "hash": hash, "miner_address": miner, "reward": "3.12500000",
		"difficulty": "0", "status": status, "confirmations": int64(0), "is_solo": true, "created_at": created, "confirmed_at": nil})
	m.Insert("payouts", map[string]any{"id": h, "miner_address": miner, "block_height": h, "amount": "3.12500000",
		"confirmed": true, "txid": "coinbase-direct", "status": "paid", "created_at": created, "paid_at": created})
}

func pg1175(m *MemSource, h int64, distributed bool, status string) {
	m.Insert("blocks_1175", map[string]any{"height": h, "hash": hashOf(h, "9"), "gross_reward": 0.78125, "is_solo": true,
		"finder": addrA, "distributed": distributed, "status": status, "created_at": at(10, 0, 0, 0, 0)})
}

// moved is forgesolo.db as a first move from src made it: prepared, then put in place.
func moved(t *testing.T, dir string, src *MemSource) string {
	t.Helper()
	db := filepath.Join(dir, "forgesolo.db")
	if _, err := prepare(t, src, db); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(MigratingPath(db), db); err != nil {
		t.Fatal(err)
	}
	os.Remove(MigratingPath(db) + ".inuse")
	return db
}

// in runs fn with the database at path open, as the api or the stratum has it.
func in(t *testing.T, path string, fn func()) {
	t.Helper()
	if err := stats.InitDB(path); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()
	fn()
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func mergeInto(t *testing.T, src Source, db string) (*Prepared, error) {
	t.Helper()
	return Prepare(context.Background(), src, PrepareOptions{DB: db, Merge: true, Version: "1.0.13-test", Logf: t.Logf})
}

// query is the rows of q, one text column, on the database at path. The database is closed again
// before it returns.
func query(t *testing.T, path, q string, args ...any) []string {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=query_only(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// Going back to 1.0.12 and forward again keeps both periods: the blocks forgesolo.db recorded under
// 1.0.13, and those 1.0.12 recorded after going back, with their payouts.
func TestMergeUnion(t *testing.T) {
	dir := t.TempDir()
	first := emptyPG()
	for h := int64(1); h <= 150; h++ {
		status := "confirmed"
		if h == 150 {
			status = "pending"
		}
		pgBlock(first, h, hashOf(h, "a"), addrA, status, at(1, int(h/60), int(h%60), 0, 0))
	}
	db := moved(t, dir, first)
	in(t, db, func() {
		for h := int64(200); h <= 205; h++ {
			must(t, stats.SaveSoloBlockCoinbaseDirect(addrA, h, 3.125, hashOf(h, "a")))
		}
	})
	// 1.0.12, back on the old database, confirmed 150 and found 151 to 160.
	pg := emptyPG()
	for h := int64(1); h <= 160; h++ {
		pgBlock(pg, h, hashOf(h, "a"), addrA, "confirmed", at(1, int(h/60), int(h%60), 0, 0))
	}
	p, err := mergeInto(t, pg, db)
	if err != nil {
		t.Fatalf("MIG-MERGE-UNION: %v", err)
	}
	tmp := MigratingPath(db)
	heights := query(t, tmp, `SELECT group_concat(height) FROM (SELECT height FROM blocks ORDER BY height)`)
	var want []string
	for h := 1; h <= 160; h++ {
		want = append(want, fmt.Sprint(h))
	}
	for h := 200; h <= 205; h++ {
		want = append(want, fmt.Sprint(h))
	}
	if heights[0] != strings.Join(want, ",") {
		t.Fatalf("MIG-MERGE-UNION: the merged blocks are %s, want 1 to 160 and 200 to 205", heights[0])
	}
	if got := query(t, tmp, `SELECT CAST(COUNT(*) AS TEXT) FROM payouts p JOIN blocks b ON b.height = p.block_height`); got[0] != "166" {
		t.Fatalf("MIG-MERGE-UNION: %s blocks have their payout, want 166", got[0])
	}
	if got := query(t, tmp, `SELECT status FROM blocks WHERE height = 150`); got[0] != "confirmed" {
		t.Fatalf("MIG-MERGE-ROLLBACK: block 150, which 1.0.12 confirmed after going back, is %s", got[0])
	}
	if fmt.Sprint(p.Merge.Added["blocks"]) != "[200 201 202 203 204 205]" || len(p.Merge.Replaced["blocks"]) != 0 {
		t.Fatalf("MIG-MERGE-REPORT: added %v, replaced %v", p.Merge.Added, p.Merge.Replaced)
	}
	meta, _ := ReadMeta(context.Background(), tmp)
	if meta["mode"] != ModeMerge || !strings.Contains(meta["merge"], `"blocks":[200,201,202,203,204,205]`) {
		t.Fatalf("MIG-MERGE-REPORT: migration_meta says mode %q, merge %s", meta["mode"], meta["merge"])
	}
	var id identity
	if err := json.Unmarshal([]byte(meta["existing"]), &id); err != nil {
		t.Fatal(err)
	}
	if now, _ := fileIdentity(db); now != id {
		t.Fatalf("MIG-MERGE-IDENTITY: forgesolo.db is %+v after the prepare, migration_meta recorded %+v", now, id)
	}
}

// The critique's loop: PostgreSQL has a 1175 block undistributed and pending; forgesolo.db had
// distributed it and settled its credit. The merge takes forgesolo.db's block and credit together,
// so the stratum's sweep has nothing to redistribute and never refuses.
func TestMerge1175RefuseLoop(t *testing.T) {
	for _, c := range []struct {
		name        string
		distributed bool // what 1.0.12 had done with the block in PostgreSQL
		code        string
	}{
		{"undistributed in PostgreSQL", false, "MIG-MERGE-1175"},
		{"distributed in both, settled only in forgesolo.db", true, "MIG-MERGE-1175-PAID"},
	} {
		t.Run(c.name, func(t *testing.T) {
			first := emptyPG()
			pg1175(first, 7000, false, "pending")
			db := moved(t, t.TempDir(), first)
			in(t, db, func() {
				must(t, stats.Distribute1175Block(7000, 1000))
				must(t, stats.Confirm1175Block(7000))
				if n, err := stats.Settle1175ByCoinbase(addrA); err != nil || n != 1 {
					t.Fatalf("settle: %d %v", n, err)
				}
			})
			pg := emptyPG()
			pg1175(pg, 7000, c.distributed, "pending")
			if c.distributed {
				pg.Insert("payouts_1175", map[string]any{"id": int64(1), "miner_address": addrA, "block_height": int64(7000), "amount": 0.78125,
					"txid": nil, "status": "pending", "batch": nil, "paid_at": nil, "created_at": at(10, 0, 0, 0, 0)})
			}
			if _, err := mergeInto(t, pg, db); err != nil {
				t.Fatalf("%s: %v", c.code, err)
			}
			in(t, MigratingPath(db), func() {
				hs, err := stats.UndistributedBlocks1175()
				must(t, err)
				for _, h := range hs {
					if err := stats.Distribute1175Block(h, 1000); err != nil {
						t.Errorf("%s: the sweep after the merge failed at %d: %v", c.code, h, err)
					}
				}
			})
			if got := query(t, MigratingPath(db), `SELECT status || '/' || COALESCE(txid, '') FROM payouts_1175 WHERE block_height = 7000`); len(got) != 1 || got[0] != "paid/coinbase-direct" {
				t.Fatalf("%s: the credit at 7000 is %v, want the settled one", c.code, got)
			}
		})
	}
}

// The same height, two blocks: forgesolo.db recorded B, which superseded A; PostgreSQL still has A
// pending. The merge keeps B and its payouts.
func TestMergeSameHeightReorg(t *testing.T) {
	dir := t.TempDir()
	const h = 100500
	first := emptyPG()
	pgBlock(first, h, hashOf(h, "a"), addrA, "pending", at(10, 0, 0, 0, 0))
	db := moved(t, dir, first)
	in(t, db, func() {
		must(t, stats.SaveSoloBlockCoinbaseDirectAt(addrB, h, 3.125, hashOf(h, "b"), at(10, 0, 5, 0, 0)))
	})
	before := query(t, db, `SELECT miner_address || '/' || txid FROM payouts WHERE block_height = ? ORDER BY miner_address`, h)
	pg := emptyPG()
	pgBlock(pg, h, hashOf(h, "a"), addrA, "pending", at(10, 0, 0, 0, 0))
	if _, err := mergeInto(t, pg, db); err != nil {
		t.Fatalf("MIG-MERGE-REORG: %v", err)
	}
	if got := query(t, MigratingPath(db), `SELECT hash FROM blocks WHERE height = ?`, h); got[0] != hashOf(h, "b") {
		t.Fatalf("MIG-MERGE-REORG: the merge kept the reorged-out block %s, want the one that superseded it", got[0])
	}
	if got := query(t, MigratingPath(db), `SELECT miner_address || '/' || txid FROM payouts WHERE block_height = ? ORDER BY miner_address`, h); strings.Join(got, " ") != strings.Join(before, " ") {
		t.Fatalf("MIG-MERGE-REORG: the payouts at %d are %v, want forgesolo.db's %v", h, got, before)
	}
}

// The check after the merge catches a unit that is not the chosen side's, a height that went
// missing, and a miner forgesolo.db knew that went missing.
func TestVerifyMergeCatchesWrongUnits(t *testing.T) {
	ctx := context.Background()
	const h = 100500
	for _, c := range []struct{ code, damage string }{
		{"MIG-MERGE-VERIFY-UNIT", `DELETE FROM payouts WHERE block_height = 100500 AND miner_address = '` + addrB + `'`},
		{"MIG-MERGE-VERIFY-HEIGHT", `DELETE FROM blocks WHERE height = 100501; DELETE FROM payouts WHERE block_height = 100501`},
		{"MIG-MERGE-VERIFY-MINER", `DELETE FROM miners WHERE address = '` + addrB + `'`},
	} {
		t.Run(c.code, func(t *testing.T) {
			first := emptyPG()
			pgBlock(first, h, hashOf(h, "a"), addrA, "pending", at(10, 0, 0, 0, 0))
			s := moved(t, t.TempDir(), first)
			in(t, s, func() {
				must(t, stats.SaveSoloBlockCoinbaseDirectAt(addrB, h, 3.125, hashOf(h, "b"), at(10, 0, 5, 0, 0)))
				must(t, stats.SaveSoloBlockCoinbaseDirect(addrB, h+1, 3.125, hashOf(h+1, "b")))
				must(t, stats.SaveMinerSettings(&stats.MinerSettings{Address: addrB, SoloMining: true}))
			})
			tPath := filepath.Join(t.TempDir(), "forgesolo.db")
			if _, err := prepare(t, first, tPath); err != nil {
				t.Fatal(err)
			}
			tdb, err := sql.Open("sqlite", stats.SQLiteDSN(MigratingPath(tPath)))
			must(t, err)
			defer tdb.Close()
			tdb.SetMaxOpenConns(1)
			sdb, sconn, err := exclusive(ctx, s)
			must(t, err)
			defer sdb.Close()
			defer sconn.Close()
			mp, err := merge(ctx, sconn, tdb)
			must(t, err)
			if err := verifyMerge(ctx, tdb, mp); err != nil {
				t.Fatalf("%s-SETUP: a good merge failed its check: %v", c.code, err)
			}
			for _, q := range strings.Split(c.damage, ";") {
				if _, err := tdb.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			if err := verifyMerge(ctx, tdb, mp); CodeOf(err) != CodeVerify {
				t.Fatalf("%s: the check passed a merge damaged by %q (%v)", c.code, c.damage, err)
			}
		})
	}
}

// The same block on both sides: the side that got further.
func TestMergeSameHash(t *testing.T) {
	const h = 100600
	for _, c := range []struct {
		name, pgStatus string
		inS            func()
		want           string
	}{
		{"pending in PostgreSQL, confirmed in forgesolo.db", "pending", func() { stats.ConfirmSoloBlock(h) }, "confirmed"},
		{"confirmed in PostgreSQL, pending in forgesolo.db", "confirmed", func() {}, "confirmed"},
		{"confirmed in PostgreSQL, orphaned in forgesolo.db", "confirmed", func() { stats.OrphanSoloBlock(h) }, "confirmed"},
		{"orphaned in PostgreSQL, pending in forgesolo.db", "orphaned", func() {}, "orphaned"},
	} {
		t.Run(c.name, func(t *testing.T) {
			first := emptyPG()
			pgBlock(first, h, hashOf(h, "a"), addrA, "pending", at(10, 0, 0, 0, 0))
			db := moved(t, t.TempDir(), first)
			in(t, db, c.inS)
			pg := emptyPG()
			pgBlock(pg, h, hashOf(h, "a"), addrA, c.pgStatus, at(10, 0, 0, 0, 0))
			if _, err := mergeInto(t, pg, db); err != nil {
				t.Fatal(err)
			}
			if got := query(t, MigratingPath(db), `SELECT status FROM blocks WHERE height = ?`, h); got[0] != c.want {
				t.Fatalf("MIG-MERGE-SAMEHASH: %s gave %s, want %s", c.name, got[0], c.want)
			}
		})
	}
}

// Settings follow whichever side changed them last; the TIDES key is PostgreSQL's when it has one.
func TestMergeSettings(t *testing.T) {
	future := time.Date(2099, 1, 2, 3, 4, 5, 0, time.UTC) // after anything forgesolo.db wrote
	first := emptyPG()
	for i, a := range []string{addrA, addrC} {
		first.Insert("miners", map[string]any{"id": int64(i + 1), "address": a, "solo_mining": true, "manual_diff": "0",
			"address_1175": nil, "settings_pin_hash": nil, "created_at": at(1, 0, 0, 0, 0), "updated_at": at(1, 0, 0, 0, 0)})
	}
	first.Insert("pool_config", map[string]any{"id": int64(1), "pool_address": addrA, "payout_address_1175": "", "coinbase_tag": "",
		"payout_mode": "solo", "updated_at": at(1, 0, 0, 0, 0)})

	for _, c := range []struct {
		name          string
		pgPoolLater   bool
		pgKey, sKey   string
		wantPool      string
		wantKey       string
		wantPoolSide  string
		wantDatumSide string
	}{
		{"pool settings changed only in forgesolo.db, key only there", false, "", strings.Repeat("aa", 32), addrB, strings.Repeat("aa", 32), sideExisting, sideExisting},
		{"pool settings changed in both, PostgreSQL later, keys on both", true, strings.Repeat("bb", 32), strings.Repeat("aa", 32), addrC, strings.Repeat("bb", 32), sidePostgres, sidePostgres},
	} {
		t.Run(c.name, func(t *testing.T) {
			db := moved(t, t.TempDir(), first)
			in(t, db, func() {
				must(t, stats.SaveMinerSettings(&stats.MinerSettings{Address: addrA, SoloMining: true, Address1175: addr1175}))
				must(t, stats.SetSettingsPinHash(addrA, pinHash))
				must(t, stats.SaveMinerSettings(&stats.MinerSettings{Address: addrC, SoloMining: true, ManualDiff: 99}))
				must(t, stats.SaveMinerSettings(&stats.MinerSettings{Address: addrB, SoloMining: true}))
				must(t, stats.SavePoolSettings(addrB, addr1175, "/s/", "tides"))
				if _, err := stats.GatewaySeed(c.sKey); err != nil {
					t.Fatal(err)
				}
			})
			pg := emptyPG()
			pg.Insert("miners", map[string]any{"id": int64(1), "address": addrA, "solo_mining": true, "manual_diff": "0",
				"address_1175": nil, "settings_pin_hash": nil, "created_at": at(1, 0, 0, 0, 0), "updated_at": at(1, 0, 0, 0, 0)})
			pg.Insert("miners", map[string]any{"id": int64(2), "address": addrC, "solo_mining": false, "manual_diff": "7.50000000",
				"address_1175": nil, "settings_pin_hash": nil, "created_at": at(1, 0, 0, 0, 0), "updated_at": future})
			poolAt, poolAddr := at(1, 0, 0, 0, 0), addrA
			if c.pgPoolLater {
				poolAt, poolAddr = future, addrC
			}
			pg.Insert("pool_config", map[string]any{"id": int64(1), "pool_address": poolAddr, "payout_address_1175": "", "coinbase_tag": "",
				"payout_mode": "solo", "updated_at": poolAt})
			if c.pgKey != "" {
				pg.Insert("datum_identity", map[string]any{"id": int64(1), "key_seed": c.pgKey, "created_at": at(1, 0, 0, 0, 0)})
			}
			p, err := mergeInto(t, pg, db)
			if err != nil {
				t.Fatalf("MIG-MERGE-SETTINGS-RUN: %v", err)
			}
			tmp := MigratingPath(db)
			if got := query(t, tmp, `SELECT COALESCE(address_1175, '') || '/' || COALESCE(settings_pin_hash, '') FROM miners WHERE address = ?`, addrA); got[0] != addr1175+"/"+pinHash {
				t.Errorf("MIG-MERGE-SETTINGS: a miner changed only in forgesolo.db (its 1175 address and PIN) became %s", got[0])
			}
			if got := query(t, tmp, `SELECT CAST(solo_mining AS TEXT) || '/' || CAST(manual_diff AS TEXT) FROM miners WHERE address = ?`, addrC); got[0] != "0/7.5" {
				t.Errorf("MIG-MERGE-SETTINGS: a miner PostgreSQL changed last became %s, want PostgreSQL's 0/7.5", got[0])
			}
			if got := query(t, tmp, `SELECT CAST(created_at AS TEXT) FROM miners WHERE address = ?`, addrA); got[0] != "2026-09-01 05:00:00" {
				t.Errorf("MIG-MERGE-SETTINGS: the miner's first-seen time became %s", got[0])
			}
			if got := query(t, tmp, `SELECT CAST(COUNT(*) AS TEXT) FROM miners WHERE address = ?`, addrB); got[0] != "1" {
				t.Errorf("MIG-MERGE-SETTINGS: a miner only forgesolo.db knew is missing")
			}
			if got := query(t, tmp, `SELECT pool_address FROM pool_config`); got[0] != c.wantPool || p.Merge.PoolConfig != c.wantPoolSide {
				t.Errorf("MIG-MERGE-POOL: the payout address is %s (from %s), want %s (from %s)", got[0], p.Merge.PoolConfig, c.wantPool, c.wantPoolSide)
			}
			if got := query(t, tmp, `SELECT key_seed FROM datum_identity`); got[0] != c.wantKey || p.Merge.DatumIdentity != c.wantDatumSide {
				t.Errorf("MIG-MERGE-KEY: the TIDES key is %s (from %s), want %s", got[0], p.Merge.DatumIdentity, c.wantKey)
			}
		})
	}
}

// forgesolo.db's shares come along as they are; PostgreSQL's are never read.
func TestMergeCarriesShares(t *testing.T) {
	first := source1012()
	db := moved(t, t.TempDir(), first)
	in(t, db, func() {
		must(t, stats.SaveShare(addrA, "rig1", 1000, false))
		must(t, stats.SaveShare(addrC, "rig2", 3000, false))
	})
	want := query(t, db, `SELECT id || '|' || miner_address || '|' || worker_name || '|' || difficulty || '|' || is_solo || '|' || created_at FROM shares ORDER BY id`)
	if _, err := mergeInto(t, source1012(), db); err != nil {
		t.Fatal(err)
	}
	got := query(t, MigratingPath(db), `SELECT id || '|' || miner_address || '|' || worker_name || '|' || difficulty || '|' || is_solo || '|' || created_at FROM shares ORDER BY id`)
	if len(want) != 2 || strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("MIG-MERGE-SHARES: the shares after the merge are %v, want forgesolo.db's %v", got, want)
	}
}

// A database a newer Forge Solo wrote is refused: nothing is written and forgesolo.db is unchanged.
func TestMergeRefusesNewer(t *testing.T) {
	for _, ddl := range []string{`ALTER TABLE blocks ADD COLUMN found_at DATETIME`, `CREATE TABLE tides_rounds (id INTEGER PRIMARY KEY)`} {
		t.Run(ddl, func(t *testing.T) {
			dir := t.TempDir()
			db := moved(t, dir, source1012())
			raw, err := sql.Open("sqlite", stats.SQLiteDSN(db))
			must(t, err)
			_, err = raw.Exec(ddl)
			must(t, err)
			raw.Close()
			before, _ := fileIdentity(db)
			_, err = mergeInto(t, source1012(), db)
			if CodeOf(err) != CodeRefused || !strings.Contains(err.Error(), "newer Forge Solo") {
				t.Fatalf("MIG-MERGE-NEWER: a database with %q gave %v, want a refusal (30)", ddl, err)
			}
			if after, _ := fileIdentity(db); after.SHA256 != before.SHA256 {
				t.Fatalf("MIG-MERGE-NEWER: the refused merge changed forgesolo.db")
			}
			for _, f := range dirFiles(t, dir) {
				if strings.HasPrefix(f, "forgesolo.db.migrating") {
					t.Fatalf("MIG-MERGE-NEWER: the refused merge left %s", f)
				}
			}
		})
	}
}

// While a program has forgesolo.db open, the merge does not read it: deferred (31), the database
// unchanged, and what the program writes afterwards stays.
func TestMergeDeferredWhileInUse(t *testing.T) {
	for _, c := range []struct{ part, code string }{
		{"hold-db", "MIG-MERGE-DEFERRED"},        // the api: the in-use lock and the database
		{"raw-hold", "MIG-MERGE-DEFERRED-RAW"},   // the database without the lock: SQLite's lock
		{"lock-only", "MIG-MERGE-DEFERRED-LOCK"}, // the lock before the database is opened
	} {
		t.Run(c.part, func(t *testing.T) {
			dir := t.TempDir()
			db := moved(t, dir, source1012())
			holder := startChild(t, c.part, db)
			holder.expect(t, "open", 30*time.Second, c.code+"-SETUP")
			before, _ := fileIdentity(db)
			_, err := mergeInto(t, source1012(), db)
			if CodeOf(err) != CodeDeferred {
				t.Fatalf("%s: a merge while %s had the database gave %v, want deferred (31)", c.code, c.part, err)
			}
			if after, _ := fileIdentity(db); after.SHA256 != before.SHA256 {
				t.Fatalf("%s: the deferred merge changed forgesolo.db", c.code)
			}
			for _, f := range dirFiles(t, dir) {
				if strings.HasPrefix(f, "forgesolo.db.migrating") {
					t.Fatalf("%s: the deferred merge left %s", c.code, f)
				}
			}
			if c.part == "hold-db" {
				const late = "bitcoincashii:qlatewrite00000000000000000000000000000000"
				holder.say(t, "write "+late)
				holder.expect(t, "wrote", 30*time.Second, c.code)
				holder.release()
				holder.expect(t, "closed", 30*time.Second, c.code)
				if got := query(t, db, `SELECT CAST(COUNT(*) AS TEXT) FROM miners WHERE address = ?`, late); got[0] != "1" {
					t.Fatalf("%s: what the api wrote after the deferred merge is lost", c.code)
				}
			}
		})
	}
}

// Rows forgesolo.db holds only in its WAL, written by a program that ended without folding it in,
// are merged.
func TestMergeReadsTheWAL(t *testing.T) {
	src := t.TempDir()
	db := moved(t, src, source1012())
	raw, err := sql.Open("sqlite", stats.SQLiteDSN(db)+"&_pragma=wal_autocheckpoint(0)")
	must(t, err)
	raw.SetMaxOpenConns(1)
	_, err = raw.Exec(`INSERT INTO blocks (height, hash, miner_address, reward, status, is_solo, created_at) VALUES (100900, ?, ?, 3.125, 'pending', 1, '2026-10-01 00:00:00')`,
		hashOf(100900, "w"), addrA)
	must(t, err)
	// A copy of the files as a power cut leaves them: the row is in the WAL only.
	dir := t.TempDir()
	cp := filepath.Join(dir, "forgesolo.db")
	for _, sfx := range []string{"", "-wal"} {
		b, err := os.ReadFile(db + sfx)
		must(t, err)
		must(t, os.WriteFile(cp+sfx, b, 0o600))
	}
	raw.Close()
	mainOnly := filepath.Join(t.TempDir(), "forgesolo.db")
	b, _ := os.ReadFile(cp)
	must(t, os.WriteFile(mainOnly, b, 0o600))
	if got := query(t, mainOnly, `SELECT CAST(COUNT(*) AS TEXT) FROM blocks WHERE height = 100900`); got[0] != "0" {
		t.Fatal("MIG-MERGE-WAL-SETUP: the row is not in the WAL only")
	}
	if _, err := mergeInto(t, source1012(), cp); err != nil {
		t.Fatal(err)
	}
	if got := query(t, MigratingPath(cp), `SELECT CAST(COUNT(*) AS TEXT) FROM blocks WHERE height = 100900`); got[0] != "1" {
		t.Fatalf("MIG-MERGE-WAL: a block forgesolo.db held in its WAL is missing after the merge")
	}
	// What commit compares is the file as the merge read it, its WAL folded in.
	meta, _ := ReadMeta(context.Background(), MigratingPath(cp))
	var id identity
	if err := json.Unmarshal([]byte(meta["existing"]), &id); err != nil {
		t.Fatal(err)
	}
	if now, _ := fileIdentity(cp); now != id {
		t.Fatalf("MIG-MERGE-IDENTITY-WAL: forgesolo.db is %+v after the prepare, migration_meta recorded %+v", now, id)
	}
}

//go:build sqlite

package pgmigrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/stats"
)

func init() {
	childParts["prepare-stall"] = prepareStallChild
}

// prepareStallChild is a prepare that stops once the copy is written and before it is checked,
// says "halfway", and waits there to be killed.
func prepareStallChild(db string) int {
	testStage = func(name string) error {
		if name == "copied" {
			fmt.Println("halfway")
			select {}
		}
		return nil
	}
	_, err := Prepare(context.Background(), source1012(), PrepareOptions{DB: db, Version: "test"})
	fmt.Println("error:", err)
	return 1
}

func prepare(t *testing.T, src Source, db string) (*Prepared, error) {
	t.Helper()
	return Prepare(context.Background(), src, PrepareOptions{DB: db, Version: "1.0.13-test", Logf: t.Logf})
}

func openRO(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)&_pragma=query_only(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// dirFiles is the names in dir.
func dirFiles(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// Every table, every row, every value, as the SQLite build stores it, in <db>.migrating; the
// database itself is not made.
func TestPrepareCopiesEverything(t *testing.T) {
	db := filepath.Join(t.TempDir(), "forgesolo.db")
	p, err := prepare(t, source1012(), db)
	if err != nil {
		t.Fatalf("MIG-PREP-COPY: %v", err)
	}
	if _, err := os.Stat(db); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("MIG-PREP-INPLACE: prepare made the database itself (%v)", err)
	}
	want := map[string]int64{"pool_config": 1, "datum_identity": 1, "miners": 2, "blocks": 14, "payouts": 18,
		"blocks_1175": 4, "payouts_1175": 4, "shares": 0}
	for k, n := range want {
		if p.Counts[k] != n {
			t.Errorf("MIG-PREP-COPY: %s has %d rows, want %d", k, p.Counts[k], n)
		}
	}
	tmp := MigratingPath(db)
	for _, c := range []struct{ table, want string }{
		{"pool_config", "1|'" + addrA + "'|'" + addr1175 + "'|'/forge ü 1.0.12/'|'tides'|'2026-09-20 15:00:00'"},
		{"datum_identity", "1|'" + strings.Repeat("c3", 32) + "'|'2026-09-20 15:00:02'"},
	} {
		if got := rowsOf(t, tmp, c.table); len(got) != 1 || got[0] != c.want {
			t.Errorf("MIG-PREP-COPY: %s is %q, want [%s]", c.table, got, c.want)
		}
	}
	blocks := rowsOf(t, tmp, "blocks")
	wantFirst := "10|100000|'" + hashOf(100000, "a") + "'|'" + addrA + "'|3.125|'confirmed'|1|'2026-09-03 09:05:07'|'2026-09-04 09:05:06'"
	if blocks[0] != wantFirst {
		t.Errorf("MIG-PREP-COPY: the first block is %s, want %s", blocks[0], wantFirst)
	}
	payouts := rowsOf(t, tmp, "payouts")
	for _, w := range []struct{ row, has string }{
		{"'" + addrB + "'|99000|", "|0|'pending'|''|"},       // '' kept
		{"'" + addrA + "'|99000|", "|0|'pending'|NULL|"},     // NULL kept
		{"'" + addrC + "'|99100|", "|NULL|NULL|NULL|"},       // a NULL boolean and status kept
		{"'" + addrC + "'|99001|", "|'2026-09-01 08:00:00'"}, // a microsecond rounds away
	} {
		found := false
		for _, r := range payouts {
			if strings.Contains(r, w.row) {
				found = true
				if !strings.Contains(r, w.has) {
					t.Errorf("MIG-PREP-COPY: payouts row %s lacks %s", r, w.has)
				}
			}
		}
		if !found {
			t.Errorf("MIG-PREP-COPY: no payouts row %s", w.row)
		}
	}
	meta, err := ReadMeta(context.Background(), tmp)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"state": "prepared", "mode": "move", "source": "postgres", "server_version": "16.6",
		"system_identifier": "7692687789219831848", "migrator_version": "1.0.13-test"} {
		if meta[k] != v {
			t.Errorf("MIG-PREP-META: migration_meta %s is %q, want %q", k, meta[k], v)
		}
	}
	if !strings.Contains(meta["skipped"], "pool_config: 1 rows") {
		t.Errorf("MIG-PREP-META: the pool_config row with id 2 is not reported: %q", meta["skipped"])
	}
	if meta["satoshis"] == "" || meta["counts"] == "" || meta["prepared_at"] == "" {
		t.Errorf("MIG-PREP-META: migration_meta lacks counts, sums or the time: %v", meta)
	}
	// One file whose header says WAL, nothing left in a WAL beside it.
	if fi, err := os.Stat(tmp + "-wal"); err == nil && fi.Size() > 0 {
		t.Errorf("MIG-PREP-WAL: %d bytes are left in the copy's WAL", fi.Size())
	}
	b, _ := os.ReadFile(tmp)
	if len(b) < 20 || b[18] != 2 || b[19] != 2 {
		t.Errorf("MIG-PREP-WAL: the copy's header does not say WAL")
	}
	// A block recorded after the move gets a new id.
	if err := stats.InitDB(tmp); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()
	if err := stats.SaveSoloBlockCoinbaseDirect(addrA, 100050, 3.125, hashOf(100050, "f")); err != nil {
		t.Fatalf("MIG-PREP-SEQ: a block recorded after the move failed: %v", err)
	}
}

// Each fault in the copy fails the prepare with code 20, and leaves no copy.
func TestPrepareChecksCatchFaults(t *testing.T) {
	for _, c := range []struct {
		name, code, sql, says string
	}{
		{"a skipped row", "MIG-PREP-FAULT-ROW", `DELETE FROM blocks WHERE height = 100003`, "blocks: 14 rows in PostgreSQL"},
		{"an amount times 1.0000001", "MIG-PREP-FAULT-AMOUNT", `UPDATE payouts SET amount = amount * 1.0000001 WHERE block_height = 100001`, "amount"},
		{"a swapped hash", "MIG-PREP-FAULT-HASH",
			`UPDATE blocks SET hash = CASE height WHEN 100000 THEN (SELECT hash FROM blocks WHERE height = 100001) ELSE (SELECT hash FROM blocks WHERE height = 100000) END WHERE height IN (100000, 100001)`, "hash"},
		{"a bool stored as 't'", "MIG-PREP-FAULT-BOOL", `UPDATE blocks SET is_solo = 't' WHERE height = 100002`, "blocks.is_solo: a value is stored as text"},
		{"sqlite_sequence not advanced", "MIG-PREP-FAULT-SEQ", `UPDATE sqlite_sequence SET seq = 1 WHERE name = 'payouts'`, "payouts: the next new row"},
	} {
		t.Run(c.name, func(t *testing.T) {
			testAfterCopy = func(ctx context.Context, tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, c.sql)
				return err
			}
			defer func() { testAfterCopy = nil }()
			dir := t.TempDir()
			_, err := prepare(t, source1012(), filepath.Join(dir, "forgesolo.db"))
			if CodeOf(err) != CodeVerify || !strings.Contains(err.Error(), c.says) {
				t.Fatalf("%s: prepare gave %v (code %d), want code 20 saying %q", c.code, err, CodeOf(err), c.says)
			}
			if got := dirFiles(t, dir); len(got) != 0 {
				t.Fatalf("%s: a failed prepare left %v", c.code, got)
			}
		})
	}
}

// preparedPlans is a finished copy of source1012, opened again, with the plans and a snapshot of
// its source, for the checks to run on one at a time.
func preparedPlans(t *testing.T) (*sql.DB, Snapshot, []tablePlan) {
	t.Helper()
	db := filepath.Join(t.TempDir(), "forgesolo.db")
	src := source1012()
	if _, err := prepare(t, src, db); err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open("sqlite", stats.SQLiteDSN(MigratingPath(db)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	snap, err := src.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cols, _ := snap.Columns(context.Background())
	plans, err := (&copier{meta: map[string]string{}}).plan(cols)
	if err != nil {
		t.Fatal(err)
	}
	return conn, snap, plans
}

func planOf(plans []tablePlan, name string) tablePlan {
	for _, p := range plans {
		if p.Name == name {
			return p
		}
	}
	panic(name)
}

// Each check catches its fault on its own, without the read-back's help.
func TestEachCheckCatchesItsFault(t *testing.T) {
	ctx := context.Background()
	t.Run("keys", func(t *testing.T) {
		db, snap, plans := preparedPlans(t)
		if _, err := db.Exec(`UPDATE blocks SET hash = 'f' || substr(hash, 2) WHERE height = 100004`); err != nil {
			t.Fatal(err)
		}
		if err := verifyKeys(ctx, snap, db, planOf(plans, "blocks")); CodeOf(err) != CodeVerify {
			t.Fatalf("MIG-CHECK-KEYS: a changed block hash passed the key check (%v)", err)
		}
	})
	t.Run("satoshis", func(t *testing.T) {
		db, snap, plans := preparedPlans(t)
		if _, err := db.Exec(`UPDATE payouts SET amount = amount + 0.00000001 WHERE block_height = 100001`); err != nil {
			t.Fatal(err)
		}
		if err := verifySatoshis(ctx, snap, db, planOf(plans, "payouts"), map[string]int64{}); CodeOf(err) != CodeVerify {
			t.Fatalf("MIG-CHECK-SUMS: one satoshi more passed the sums (%v)", err)
		}
	})
	t.Run("satoshis per miner", func(t *testing.T) {
		db, snap, plans := preparedPlans(t)
		// The same total, moved from one miner to another.
		if _, err := db.Exec(`UPDATE payouts SET miner_address = ? WHERE block_height = 100001`, addrB); err != nil {
			t.Fatal(err)
		}
		if err := verifySatoshis(ctx, snap, db, planOf(plans, "payouts"), map[string]int64{}); CodeOf(err) != CodeVerify {
			t.Fatalf("MIG-CHECK-SUMS-MINER: a credit moved to another miner passed the sums (%v)", err)
		}
	})
	t.Run("settings", func(t *testing.T) {
		db, snap, plans := preparedPlans(t)
		if _, err := db.Exec(`UPDATE pool_config SET coinbase_tag = '/other/'`); err != nil {
			t.Fatal(err)
		}
		if err := verifySettings(ctx, snap, db, plans); CodeOf(err) != CodeVerify || !strings.Contains(err.Error(), "coinbase_tag") {
			t.Fatalf("MIG-CHECK-SETTINGS: a changed coinbase tag passed the settings check (%v)", err)
		}
	})
	t.Run("gateway key", func(t *testing.T) {
		db, snap, plans := preparedPlans(t)
		if _, err := db.Exec(`UPDATE datum_identity SET key_seed = 'other'`); err != nil {
			t.Fatal(err)
		}
		if err := verifySettings(ctx, snap, db, plans); CodeOf(err) != CodeVerify {
			t.Fatalf("MIG-CHECK-KEYSEED: a changed TIDES key passed the settings check (%v)", err)
		}
	})
	t.Run("stored type", func(t *testing.T) {
		db, _, plans := preparedPlans(t)
		var want [][]any
		rows, _ := db.Query(`SELECT height, hash, CAST(gross_reward AS REAL), is_solo, finder, distributed, status, CAST(created_at AS TEXT) FROM blocks_1175 ORDER BY height`)
		for rows.Next() {
			r := make([]any, 8)
			p := make([]any, 8)
			for i := range r {
				p[i] = &r[i]
			}
			rows.Scan(p...)
			want = append(want, r)
		}
		rows.Close()
		if err := verifyStored(ctx, db, planOf(plans, "blocks_1175").Table, want); err != nil {
			t.Fatalf("MIG-CHECK-TYPE-SETUP: %v", err)
		}
		if _, err := db.Exec(`UPDATE blocks_1175 SET gross_reward = '0.78125 ESF' WHERE height = 5000`); err != nil {
			t.Fatal(err)
		}
		if err := verifyStored(ctx, db, planOf(plans, "blocks_1175").Table, want); CodeOf(err) != CodeVerify {
			t.Fatalf("MIG-CHECK-TYPE: an amount stored as text passed (%v)", err)
		}
	})
}

// An old cluster with no Forge Solo database, or with none of its tables, gives a valid empty
// database: a fresh install's.
func TestPrepareEmptySource(t *testing.T) {
	missing := NewMemSource(Info{})
	missing.SetMissing()
	for name, src := range map[string]*MemSource{"no database": missing, "no tables": NewMemSource(Info{})} {
		t.Run(name, func(t *testing.T) {
			db := filepath.Join(t.TempDir(), "forgesolo.db")
			p, err := prepare(t, src, db)
			if err != nil {
				t.Fatalf("MIG-PREP-EMPTY: %v", err)
			}
			if p.Source != "none" {
				t.Errorf("MIG-PREP-EMPTY: source is %q, want none", p.Source)
			}
			for k, n := range p.Counts {
				if n != 0 {
					t.Errorf("MIG-PREP-EMPTY: %s has %d rows", k, n)
				}
			}
			schema, err := ReadSchema(context.Background(), openRO(t, MigratingPath(db)))
			if err != nil {
				t.Fatal(err)
			}
			for _, tb := range Tables {
				if schema[tb.Name] == nil {
					t.Errorf("MIG-PREP-EMPTY: the empty database has no %s table", tb.Name)
				}
			}
			if meta, _ := ReadMeta(context.Background(), MigratingPath(db)); meta["source"] != "none" || meta["state"] != "prepared" {
				t.Errorf("MIG-PREP-EMPTY: migration_meta says %v", meta)
			}
		})
	}
}

// A 1.0.0 database moves with 1.0.12's defaults for what it lacks.
func TestPrepare100Shape(t *testing.T) {
	db := filepath.Join(t.TempDir(), "forgesolo.db")
	p, err := prepare(t, source100(), db)
	if err != nil {
		t.Fatalf("MIG-PREP-100: %v", err)
	}
	if p.Counts["blocks"] != 5 || p.Counts["payouts"] != 5 || p.Counts["miners"] != 1 || p.Counts["pool_config"] != 0 {
		t.Fatalf("MIG-PREP-100: counts %v", p.Counts)
	}
	for _, r := range rowsOf(t, MigratingPath(db), "payouts") {
		if !strings.Contains(r, "|'pending'|'coinbase-direct'|") {
			t.Errorf("MIG-PREP-100: a payout without a status column did not get 'pending': %s", r)
		}
	}
	meta, _ := ReadMeta(context.Background(), MigratingPath(db))
	if !strings.Contains(meta["defaulted"], "payouts.status") || !strings.Contains(meta["defaulted"], "miners.address_1175") {
		t.Errorf("MIG-PREP-100: the defaulted columns are not recorded: %q", meta["defaulted"])
	}
}

// Every table is read in one snapshot: a block recorded while the copy runs is either in every
// table or in none, and the counts the checks compare are of the same moment.
func TestPrepareReadsOneSnapshot(t *testing.T) {
	src := source1012()
	inserted := false
	src.BeforeRead = func(ctx context.Context, table string) error {
		if table == "blocks" && !inserted {
			inserted = true
			src.Insert("blocks", map[string]any{"id": int64(500), "height": int64(100100), "hash": hashOf(100100, "z"), "miner_address": addrA,
				"reward": "3.12500000", "difficulty": "0", "status": "pending", "confirmations": int64(0), "is_solo": true,
				"created_at": at(25, 0, 0, 0, 0), "confirmed_at": nil})
			src.Insert("payouts", map[string]any{"id": int64(500), "miner_address": addrA, "block_height": int64(100100), "amount": "3.12500000",
				"confirmed": true, "txid": "coinbase-direct", "status": "paid", "created_at": at(25, 0, 0, 0, 0), "paid_at": at(25, 0, 0, 0, 0)})
		}
		return nil
	}
	db := filepath.Join(t.TempDir(), "forgesolo.db")
	p, err := prepare(t, src, db)
	if err != nil {
		t.Fatalf("MIG-PREP-SNAPSHOT: a block recorded during the copy failed it: %v", err)
	}
	if n := src.Snapshots(); n != 1 {
		t.Fatalf("MIG-PREP-SNAPSHOT: the copy read the old database in %d snapshots, want 1", n)
	}
	if p.Counts["blocks"] != 14 || p.Counts["payouts"] != 18 {
		t.Fatalf("MIG-PREP-SNAPSHOT: the copy holds %d blocks and %d payouts, want the snapshot's 14 and 18", p.Counts["blocks"], p.Counts["payouts"])
	}
}

// A prepare killed half way leaves only its own files, never the database, and the next one makes
// the same database from scratch.
func TestPrepareKilledHalfWay(t *testing.T) {
	ref := filepath.Join(t.TempDir(), "forgesolo.db")
	if _, err := prepare(t, source1012(), ref); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	db := filepath.Join(dir, "forgesolo.db")
	c := startChild(t, "prepare-stall", db)
	c.expect(t, "halfway", 60*time.Second, "MIG-PREP-KILL-SETUP")
	if err := c.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	c.cmd.Wait()
	for _, f := range dirFiles(t, dir) {
		if !strings.HasPrefix(f, "forgesolo.db.migrating") {
			t.Errorf("MIG-PREP-KILL: a prepare killed half way left %s", f)
		}
	}
	if _, err := prepare(t, source1012(), db); err != nil {
		t.Fatalf("MIG-PREP-KILL-RERUN: the next prepare failed: %v", err)
	}
	if a, b := dump(t, MigratingPath(ref)), dump(t, MigratingPath(db)); a != b {
		t.Fatalf("MIG-PREP-KILL-RERUN: the rerun made another database:\n%s\nwant:\n%s", b, a)
	}
}

// A move never replaces a database that is there: that takes a merge.
func TestPrepareRefusesAnExistingDatabase(t *testing.T) {
	db := filepath.Join(t.TempDir(), "forgesolo.db")
	if err := os.WriteFile(db, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepare(t, source1012(), db); CodeOf(err) != CodeRefused {
		t.Fatalf("MIG-PREP-EXISTS: a move over an existing database gave %v, want a refusal (30)", err)
	}
}

// A time without a time zone in the old database is refused, and nothing is left behind.
func TestPrepareRefusesTimeWithoutZone(t *testing.T) {
	src := source1012()
	types := map[string]string{}
	for k, v := range types1012["blocks"] {
		types[k] = v
	}
	types["created_at"] = "timestamp without time zone"
	src.AddTable("blocks", types)
	dir := t.TempDir()
	if _, err := prepare(t, src, filepath.Join(dir, "forgesolo.db")); CodeOf(err) != CodeRefused {
		t.Fatalf("MIG-PREP-TZ: gave %v, want a refusal (30)", err)
	}
	if got := dirFiles(t, dir); len(got) != 0 {
		t.Fatalf("MIG-PREP-TZ: a refused prepare left %v", got)
	}
}

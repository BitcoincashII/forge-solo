//go:build sqlite

package pgmigrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/migstatus"
	"github.com/BitcoincashII/forge-solo/internal/stats"
)

// oldData is an old-data folder as a cleanly stopped PostgreSQL 16 leaves it.
func oldData(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "pgdata")
	if err := os.MkdirAll(filepath.Join(dir, "global"), 0o700); err != nil {
		t.Fatal(err)
	}
	must(t, os.WriteFile(filepath.Join(dir, "PG_VERSION"), []byte("16\n"), 0o600))
	setControl(t, dir, "pg_control-shutdown")
	return dir
}

// setControl puts a captured pg_control in the old-data folder: what PostgreSQL leaves there when
// it stops or runs.
func setControl(t *testing.T, pgdata, fixture string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", fixture))
	must(t, err)
	must(t, os.WriteFile(filepath.Join(pgdata, "global", "pg_control"), b, 0o600))
}

func commit(t *testing.T, db, pgdata string) (*Committed, error) {
	t.Helper()
	return Commit(context.Background(), CommitOptions{DB: db, PGData: pgdata, Version: "1.0.13-test", Logf: t.Logf})
}

func controlHash(t *testing.T, pgdata string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(pgdata, "global", "pg_control"))
	must(t, err)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// moveAndCommit is a first move of src, prepared and committed.
func moveAndCommit(t *testing.T, src *MemSource, db, pgdata string) {
	t.Helper()
	if _, err := prepare(t, src, db); err != nil {
		t.Fatal(err)
	}
	if _, err := commit(t, db, pgdata); err != nil {
		t.Fatal(err)
	}
}

// A move: the copy is in place, its WAL and lock files gone, the marker holds pg_control's hash, the
// status says done, and the next start does nothing.
func TestCommitMove(t *testing.T) {
	dir := t.TempDir()
	db, pgdata := filepath.Join(dir, "forgesolo.db"), oldData(t)
	if _, err := prepare(t, source1012(), db); err != nil {
		t.Fatal(err)
	}
	want := dump(t, MigratingPath(db))
	c, err := commit(t, db, pgdata)
	if err != nil {
		t.Fatalf("MIG-COMMIT-MOVE: %v", err)
	}
	if got := dump(t, db); got != want {
		t.Fatalf("MIG-COMMIT-MOVE: the database in place is not the prepared copy")
	}
	for _, f := range dirFiles(t, dir) {
		if strings.HasPrefix(f, "forgesolo.db.migrating") {
			t.Errorf("MIG-COMMIT-MOVE: the commit left %s", f)
		}
	}
	m, err := readMarker(db)
	if err != nil || m.PGControlSHA256 != controlHash(t, pgdata) || m.Mode != ModeMove || m.Counts["blocks"] != 14 || m.Version != "1.0.13-test" {
		t.Fatalf("MIG-COMMIT-MARKER: %+v %v", m, err)
	}
	if c.Marker.PGControlSHA256 != m.PGControlSHA256 {
		t.Fatalf("MIG-COMMIT-MARKER: the commit reports another marker than it wrote")
	}
	st, ok, err := migstatus.Read(db)
	if err != nil || !ok || st.State != migstatus.Done {
		t.Fatalf("MIG-COMMIT-STATUS: %+v %v %v", st, ok, err)
	}
	if d := Plan(db, pgdata); d.Action != ActionNone {
		t.Fatalf("MIG-COMMIT-NEXTPLAN: after the move the next start says %s, want none", d)
	}
	// 1.0.12 started on the old data again: the next start merges.
	setControl(t, pgdata, "pg_control-shutdown-2")
	if d := Plan(db, pgdata); d.Action != ActionMerge {
		t.Fatalf("MIG-COMMIT-NEXTPLAN: after 1.0.12 ran on the old data the next start says %s, want merge", d)
	}
}

// A WAL left beside the database by a program that ended without folding it in would turn the new
// file back into the old database: the commit deletes it first.
func TestCommitRemovesStaleWAL(t *testing.T) {
	old := filepath.Join(t.TempDir(), "forgesolo.db")
	if err := stats.InitDB(old); err != nil {
		t.Fatal(err)
	}
	stats.CloseDB()
	raw, err := sql.Open("sqlite", stats.SQLiteDSN(old)+"&_pragma=wal_autocheckpoint(0)")
	must(t, err)
	raw.SetMaxOpenConns(1)
	_, err = raw.Exec(`INSERT INTO pool_config (id, pool_address) VALUES (1, 'OLD-ADDRESS')`)
	must(t, err)
	wal, err := os.ReadFile(old + "-wal")
	must(t, err)
	raw.Close()

	dir := t.TempDir()
	db, pgdata := filepath.Join(dir, "forgesolo.db"), oldData(t)
	if _, err := prepare(t, source1012(), db); err != nil {
		t.Fatal(err)
	}
	must(t, os.WriteFile(db+"-wal", wal, 0o600))
	if _, err := commit(t, db, pgdata); err != nil {
		t.Fatalf("MIG-COMMIT-WAL: %v", err)
	}
	after, err := sql.Open("sqlite", db+"?_pragma=busy_timeout(10000)&_pragma=query_only(1)")
	must(t, err)
	defer after.Close()
	var got string
	if err := after.QueryRow(`SELECT pool_address FROM pool_config`).Scan(&got); err != nil || got != addrA {
		t.Fatalf("MIG-COMMIT-WAL: after the commit the payout address reads %q (%v), want the moved %s", got, err, addrA)
	}
}

// mergeSetup is a first move, a block recorded under 1.0.13, then 1.0.12 run on the old data again
// with more blocks: the state a merge starts from. It returns the second old database.
func mergeSetup(t *testing.T, db, pgdata string) *MemSource {
	t.Helper()
	first := emptyPG()
	for h := int64(1); h <= 5; h++ {
		pgBlock(first, h, hashOf(h, "a"), addrA, "confirmed", at(1, 0, int(h), 0, 0))
	}
	moveAndCommit(t, first, db, pgdata)
	in(t, db, func() { must(t, stats.SaveSoloBlockCoinbaseDirect(addrA, 300, 3.125, hashOf(300, "a"))) })
	second := emptyPG()
	for h := int64(1); h <= 8; h++ {
		pgBlock(second, h, hashOf(h, "a"), addrA, "confirmed", at(1, 0, int(h), 0, 0))
	}
	setControl(t, pgdata, "pg_control-shutdown-2")
	return second
}

func prepareMerge(t *testing.T, src Source, db string) {
	t.Helper()
	if _, err := mergeInto(t, src, db); err != nil {
		t.Fatal(err)
	}
}

// A commit cut short leaves a state the next start finishes correctly: before the rename, the old
// state; after it, a database without a marker, which is merged again to the same result.
func TestCommitCrash(t *testing.T) {
	refDir := t.TempDir()
	refDB, refPG := filepath.Join(refDir, "forgesolo.db"), oldData(t)
	src := mergeSetup(t, refDB, refPG)
	prepareMerge(t, src, refDB)
	if _, err := commit(t, refDB, refPG); err != nil {
		t.Fatal(err)
	}
	want := dump(t, refDB)

	crash := func(at string) {
		testStage = func(name string) error {
			if name == at {
				return errors.New("power cut")
			}
			return nil
		}
	}
	defer func() { testStage = nil }()

	t.Run("before the rename", func(t *testing.T) {
		db, pgdata := filepath.Join(t.TempDir(), "forgesolo.db"), oldData(t)
		src := mergeSetup(t, db, pgdata)
		before := dump(t, db)
		prepareMerge(t, src, db)
		crash("renaming")
		_, err := commit(t, db, pgdata)
		testStage = nil
		if err == nil {
			t.Fatal("the crash did not happen")
		}
		if d := Plan(db, pgdata); d.Action != ActionMerge {
			t.Fatalf("MIG-COMMIT-CRASH-BEFORE: after a crash before the rename the next start says %s, want merge", d)
		}
		if dump(t, db) != before {
			t.Fatalf("MIG-COMMIT-CRASH-BEFORE: a crash before the rename changed forgesolo.db")
		}
	})
	t.Run("after the rename", func(t *testing.T) {
		db, pgdata := filepath.Join(t.TempDir(), "forgesolo.db"), oldData(t)
		src := mergeSetup(t, db, pgdata)
		prepareMerge(t, src, db)
		crash("renamed")
		_, err := commit(t, db, pgdata)
		testStage = nil
		if err == nil {
			t.Fatal("the crash did not happen")
		}
		if d := Plan(db, pgdata); d.Action != ActionMerge {
			t.Fatalf("MIG-COMMIT-CRASH-AFTER: after a crash between the rename and the marker the next start says %s, want merge", d)
		}
		prepareMerge(t, src, db)
		if _, err := commit(t, db, pgdata); err != nil {
			t.Fatalf("MIG-COMMIT-CRASH-AFTER: the merge after the crash failed: %v", err)
		}
		if got := dump(t, db); got != want {
			t.Fatalf("MIG-COMMIT-CRASH-AFTER: the merge after the crash made another database:\n%s\nwant:\n%s", got, want)
		}
	})
}

// What a commit refuses, leaving everything as it was.
func TestCommitRefuses(t *testing.T) {
	for _, c := range []struct {
		code   string
		merge  bool
		damage func(t *testing.T, db, pgdata string)
	}{
		{"MIG-COMMIT-PID", false, func(t *testing.T, db, pgdata string) {
			must(t, os.WriteFile(filepath.Join(pgdata, "postmaster.pid"), []byte("1\n"), 0o600))
		}},
		{"MIG-COMMIT-NOTSHUTDOWN", false, func(t *testing.T, db, pgdata string) { setControl(t, pgdata, "pg_control-inproduction") }},
		{"MIG-COMMIT-CONTROL", false, func(t *testing.T, db, pgdata string) {
			must(t, os.WriteFile(filepath.Join(pgdata, "global", "pg_control"), []byte("garbage"), 0o600))
		}},
		{"MIG-COMMIT-UNPREPARED", false, func(t *testing.T, db, pgdata string) { must(t, removeMigrating(db)) }},
		{"MIG-COMMIT-NOTPREPARED", false, func(t *testing.T, db, pgdata string) {
			raw, err := sql.Open("sqlite", MigratingPath(db))
			must(t, err)
			_, err = raw.Exec(`UPDATE migration_meta SET value = 'copying' WHERE key = 'state'`)
			must(t, err)
			raw.Close()
		}},
		{"MIG-COMMIT-APPEARED", false, func(t *testing.T, db, pgdata string) {
			if err := stats.InitDB(db); err != nil {
				t.Fatal(err)
			}
			stats.CloseDB()
		}},
		{"MIG-COMMIT-CHANGED", true, func(t *testing.T, db, pgdata string) {
			in(t, db, func() { must(t, stats.SaveMinerSettings(&stats.MinerSettings{Address: addrB, SoloMining: true})) })
		}},
	} {
		t.Run(c.code, func(t *testing.T) {
			dir := t.TempDir()
			db, pgdata := filepath.Join(dir, "forgesolo.db"), oldData(t)
			if c.merge {
				src := mergeSetup(t, db, pgdata)
				prepareMerge(t, src, db)
			} else if _, err := prepare(t, source1012(), db); err != nil {
				t.Fatal(err)
			}
			c.damage(t, db, pgdata)
			var before string
			if _, err := os.Stat(db); err == nil {
				id, _ := fileIdentity(db)
				before = id.SHA256
			}
			_, err := commit(t, db, pgdata)
			if CodeOf(err) != CodeRefused {
				t.Fatalf("%s: the commit gave %v (code %d), want a refusal (30)", c.code, err, CodeOf(err))
			}
			after := ""
			if _, err := os.Stat(db); err == nil {
				id, _ := fileIdentity(db)
				after = id.SHA256
			}
			if after != before {
				t.Fatalf("%s: the refused commit changed forgesolo.db", c.code)
			}
		})
	}
}

// Every merge keeps a copy of what it replaced, and only the newest stays.
func TestCommitKeepsNewestBeforeMerge(t *testing.T) {
	dir := t.TempDir()
	db, pgdata := filepath.Join(dir, "forgesolo.db"), oldData(t)
	src := mergeSetup(t, db, pgdata)
	defer func() { now = time.Now }()
	for i, ts := range []string{"2026-10-05T01:02:03Z", "2026-10-06T01:02:03Z", "2026-10-07T01:02:03Z"} {
		when, _ := time.Parse(time.RFC3339, ts)
		now = func() time.Time { return when }
		prepareMerge(t, src, db)
		before := dump(t, db)
		c, err := commit(t, db, pgdata)
		if err != nil {
			t.Fatalf("merge %d: %v", i+1, err)
		}
		if dump(t, c.BeforeMerge) != before {
			t.Fatalf("MIG-COMMIT-BEFOREMERGE: the copy kept is not the database the merge replaced")
		}
	}
	var kept []string
	for _, f := range dirFiles(t, dir) {
		if strings.Contains(f, ".before-merge-") {
			kept = append(kept, f)
		}
	}
	sort.Strings(kept)
	if strings.Join(kept, ",") != "forgesolo.db.before-merge-20261007T010203Z" {
		t.Fatalf("MIG-COMMIT-RETAIN: after three merges the folder keeps %v, want only the newest copy", kept)
	}
}

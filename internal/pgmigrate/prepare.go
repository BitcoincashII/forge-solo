//go:build sqlite

package pgmigrate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/stats"
)

// The two ways a prepare builds the new database.
const (
	ModeMove  = "move"  // there is no forgesolo.db: the old data alone
	ModeMerge = "merge" // forgesolo.db is there too: the old data and it, merged
)

// MigratingPath is where prepare builds the new database: beside the database, never in its place,
// so a crash or a failed check leaves the database as it was.
func MigratingPath(db string) string { return db + ".migrating" }

// PrepareOptions says what to prepare.
type PrepareOptions struct {
	DB      string // the database the copy is for: forgesolo.db
	Version string // this migrator's version, for the record
	Logf    func(format string, args ...any)
}

func (o PrepareOptions) logf(format string, args ...any) {
	if o.Logf != nil {
		o.Logf(format, args...)
	}
}

// Prepared is what a prepare made.
type Prepared struct {
	Mode     string
	Source   string           // postgres, or none when there was no old database
	Counts   map[string]int64 // rows per table in the new database
	Satoshis map[string]int64 // the old database's sums, per money column
}

// testAfterCopy, when a test sets it, runs inside the copy's transaction, after every row is in:
// a test damages the copy there to show the checks catch it.
var testAfterCopy func(ctx context.Context, tx *sql.Tx) error

// testStage, when a test sets it, runs at the named points of a prepare and a commit: a test stops
// there, or fails the step as a crash would.
var testStage func(name string) error

func stage(name string) error {
	if testStage != nil {
		return testStage(name)
	}
	return nil
}

// Prepare copies the old database src into <db>.migrating and proves the copy complete:
//  1. it removes what an earlier try left of <db>.migrating;
//  2. it makes <db>.migrating with this build's stats.InitDB, the schema of a fresh install;
//  3. it reads PostgreSQL in one read-only snapshot (no DDL, no write; shares and pool_stats are
//     never read);
//  4. it writes every table in one SQLite transaction, keeping the ids;
//  5. it runs every check of the copy (verify.go) and records the move in migration_meta;
//  6. it checks the file's integrity, folds the WAL in, closes it and syncs it to disk.
//
// The database itself is not touched: commit puts the copy in its place. On a failure the copy is
// removed and the error carries the exit code.
func Prepare(ctx context.Context, src Source, o PrepareOptions) (p *Prepared, err error) {
	if _, serr := os.Lstat(o.DB); serr == nil {
		return nil, refused("forgesolo.db is already there, so the old data must be merged into it", nil)
	}
	tmp := MigratingPath(o.DB)
	if err := removeMigrating(o.DB); err != nil {
		return nil, newErr(CodeWrite, "what an earlier try left could not be removed", err)
	}
	defer func() {
		if err != nil {
			removeMigrating(o.DB)
		}
	}()
	if err := stats.InitDB(tmp); err != nil {
		return nil, newErr(CodeWrite, "the new database could not be made", err)
	}
	stats.CloseDB()
	tdb, err := sql.Open("sqlite", stats.SQLiteDSN(tmp))
	if err != nil {
		return nil, newErr(CodeWrite, "the new database could not be opened", err)
	}
	defer tdb.Close()
	tdb.SetMaxOpenConns(1)

	c := &copier{o: o, t: tdb, meta: map[string]string{}}
	if err := c.copy(ctx, src); err != nil {
		return nil, err
	}
	p = &Prepared{Mode: ModeMove, Source: c.source, Satoshis: c.satoshis}
	if p.Counts, err = countRows(ctx, tdb); err != nil {
		return nil, newErr(CodeWrite, "the new database could not be read back", err)
	}
	c.meta["mode"] = p.Mode
	c.meta["counts"] = jsonText(p.Counts)
	if err := writeMeta(ctx, tdb, c.meta, o.Version); err != nil {
		return nil, err
	}
	if err := finish(ctx, tdb, tmp); err != nil {
		return nil, err
	}
	return p, nil
}

// copier is one copy of the old database into the new one.
type copier struct {
	o        PrepareOptions
	t        *sql.DB
	source   string
	satoshis map[string]int64
	meta     map[string]string
}

// tablePlan is one table as the old database has it.
type tablePlan struct {
	Table
	have []string // the spec's columns PostgreSQL has, in spec order
}

func (c *copier) copy(ctx context.Context, src Source) error {
	snap, err := src.Snapshot(ctx)
	if errors.Is(err, ErrNoDatabase) {
		c.o.logf("the old cluster has no forgesolo database: nothing to move")
		c.source, c.meta["source"] = "none", "none"
		return nil
	}
	if err != nil {
		return err
	}
	defer snap.Close()
	info, err := snap.Info(ctx)
	if err != nil {
		return err
	}
	cols, err := snap.Columns(ctx)
	if err != nil {
		return err
	}
	plans, err := c.plan(cols)
	if err != nil {
		return err
	}
	if len(plans) == 0 {
		c.o.logf("the old database has none of Forge Solo's tables: nothing to move")
		c.source, c.meta["source"] = "none", "none"
		return nil
	}
	c.source, c.meta["source"] = "postgres", "postgres"
	c.meta["server_version"], c.meta["system_identifier"] = info.ServerVersion, info.SystemIdentifier

	written := map[string][][]any{}
	var skipped []string
	for _, p := range plans {
		f := AllRows
		if p.Singleton {
			f = OnlyID1
			n, err := snap.Count(ctx, p.Name, NotID1)
			if err != nil {
				return err
			}
			if n > 0 {
				skipped = append(skipped, fmt.Sprintf("%s: %d rows with an id other than 1", p.Name, n))
				c.o.logf("%s: %d rows with an id other than 1 are left behind; Forge Solo only ever reads id 1", p.Name, n)
			}
		}
		vals, err := snap.Read(ctx, p.Name, p.pgNames(), f, p.Key)
		if err != nil {
			return err
		}
		rows := make([][]any, len(vals))
		for i, v := range vals {
			if rows[i], err = convertRow(p.Table, p.pgNames(), v); err != nil {
				if CodeOf(err) == CodeOther {
					err = newErr(CodeOther, "a value of the old database could not be converted", err)
				}
				return err
			}
		}
		written[p.Name] = rows
	}
	if len(skipped) > 0 {
		c.meta["skipped"] = jsonText(skipped)
	}

	inserted, err := c.write(ctx, plans, written)
	if err != nil {
		return err
	}
	if err := stage("copied"); err != nil {
		return err
	}
	for _, p := range plans {
		c.o.logf("%s: %d rows", p.Name, inserted[p.Name])
	}
	c.satoshis = map[string]int64{}
	if err := verifyCopy(ctx, snap, c.t, plans, written, inserted, c.satoshis); err != nil {
		return err
	}
	c.meta["satoshis"] = jsonText(c.satoshis)
	return nil
}

// plan matches the old database's tables and columns against the spec. A column of a type the move
// does not carry is refused; a column or table the spec does not know is logged and left.
func (c *copier) plan(cols map[string]map[string]string) ([]tablePlan, error) {
	var plans []tablePlan
	var missing, unknown []string
	for _, t := range Tables {
		types, ok := cols[t.Name]
		if !ok {
			continue
		}
		p := tablePlan{Table: t}
		known := map[string]bool{}
		for _, col := range t.Columns {
			known[col.PG] = true
			typ, ok := types[col.PG]
			if !ok {
				missing = append(missing, t.Name+"."+col.PG)
				continue
			}
			if err := checkType(t.Name, col, typ); err != nil {
				return nil, err
			}
			p.have = append(p.have, col.PG)
		}
		for name := range types {
			if !known[name] && t.Dropped[name] == "" {
				unknown = append(unknown, t.Name+"."+name)
			}
		}
		plans = append(plans, p)
	}
	for name := range cols {
		if _, ok := table(name); !ok && NotCopied[name] == "" {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	if len(missing) > 0 {
		c.meta["defaulted"] = jsonText(missing)
		c.o.logf("columns an older version did not have, given their defaults: %s", strings.Join(missing, ", "))
	}
	if len(unknown) > 0 {
		c.meta["unknown"] = jsonText(unknown)
		c.o.logf("left behind, unknown to this version: %s", strings.Join(unknown, ", "))
	}
	return plans, nil
}

func (p tablePlan) pgNames() []string { return p.have }

// write inserts every row in one transaction, keeping the ids.
func (c *copier) write(ctx context.Context, plans []tablePlan, rows map[string][][]any) (map[string]int64, error) {
	tx, err := c.t.BeginTx(ctx, nil)
	if err != nil {
		return nil, newErr(CodeWrite, "the new database could not be written", err)
	}
	defer tx.Rollback()
	inserted := map[string]int64{}
	for _, p := range plans {
		names := p.sqliteNames()
		q := `INSERT INTO ` + p.Name + ` (` + strings.Join(names, ", ") + `) VALUES (` + strings.TrimSuffix(strings.Repeat("?, ", len(names)), ", ") + `)`
		stmt, err := tx.PrepareContext(ctx, q)
		if err != nil {
			return nil, newErr(CodeWrite, "the new database could not be written", err)
		}
		for _, r := range rows[p.Name] {
			res, err := stmt.ExecContext(ctx, r...)
			if err != nil {
				stmt.Close()
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				return nil, newErr(CodeWrite, "the new database could not be written", fmt.Errorf("%s: %w", p.Name, err))
			}
			n, _ := res.RowsAffected()
			inserted[p.Name] += n
		}
		stmt.Close()
	}
	if testAfterCopy != nil {
		if err := testAfterCopy(ctx, tx); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, newErr(CodeWrite, "the new database could not be written", err)
	}
	return inserted, nil
}

// sqliteNames are the table's SQLite column names, in order.
func (t Table) sqliteNames() []string {
	out := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		out[i] = c.SQLite
	}
	return out
}

// countRows is the rows in each copied table, and in shares.
func countRows(ctx context.Context, q Queryer) (map[string]int64, error) {
	out := map[string]int64{}
	names := []string{"shares"}
	for _, t := range Tables {
		names = append(names, t.Name)
	}
	for _, n := range names {
		var c int64
		if err := scanOne(ctx, q, &c, `SELECT COUNT(*) FROM `+n); err != nil {
			return nil, err
		}
		out[n] = c
	}
	return out, nil
}

func scanOne(ctx context.Context, q Queryer, dest any, query string, args ...any) error {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	if err := rows.Scan(dest); err != nil {
		return err
	}
	return rows.Close()
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// writeMeta records the move in the new database: where its data came from, what it holds, and
// that it is prepared, which commit requires.
func writeMeta(ctx context.Context, t *sql.DB, meta map[string]string, version string) error {
	meta["migrator_version"] = version
	meta["prepared_at"] = time.Now().UTC().Format(time.RFC3339)
	meta["state"] = "prepared"
	tx, err := t.BeginTx(ctx, nil)
	if err != nil {
		return newErr(CodeWrite, "the new database could not be written", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE `+metaTable+` (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		return newErr(CodeWrite, "the new database could not be written", err)
	}
	for k, v := range meta {
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+metaTable+` (key, value) VALUES (?, ?)`, k, v); err != nil {
			return newErr(CodeWrite, "the new database could not be written", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return newErr(CodeWrite, "the new database could not be written", err)
	}
	return nil
}

// ReadMeta is the record of the move in the database at path.
func ReadMeta(ctx context.Context, path string) (map[string]string, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=query_only(1)")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT key, value FROM `+metaTable)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// finish checks the new database's integrity, folds its WAL into the file, closes it and syncs it
// to disk. What is left is one file whose header says WAL, so a program that opens it next does
// not have to switch it and race the other program to do so.
func finish(ctx context.Context, t *sql.DB, path string) error {
	var res string
	if err := scanOne(ctx, t, &res, `PRAGMA integrity_check`); err != nil || res != "ok" {
		return verifyErr("the integrity check of the new database said %q (%v)", res, err)
	}
	var busy, logged, moved int
	rows, err := t.QueryContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	if err == nil {
		if rows.Next() {
			err = rows.Scan(&busy, &logged, &moved)
		}
		rows.Close()
	}
	if err != nil || busy != 0 {
		return newErr(CodeWrite, "the new database could not be finished", fmt.Errorf("checkpoint: busy %d, %v", busy, err))
	}
	if err := t.Close(); err != nil {
		return newErr(CodeWrite, "the new database could not be closed", err)
	}
	if fi, err := os.Stat(path + "-wal"); err == nil && fi.Size() > 0 {
		return verifyErr("the new database still has %d bytes in its WAL after it was closed", fi.Size())
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return newErr(CodeWrite, "the new database could not be synced", err)
	}
	defer f.Close()
	head := make([]byte, 20)
	if _, err := io.ReadFull(f, head); err != nil {
		return verifyErr("the new database's header could not be read: %v", err)
	}
	if head[18] != 2 || head[19] != 2 {
		return verifyErr("the new database's header does not say WAL (%d, %d)", head[18], head[19])
	}
	if err := f.Sync(); err != nil {
		return newErr(CodeWrite, "the new database could not be synced", err)
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		return newErr(CodeWrite, "the new database could not be synced", err)
	}
	return nil
}

// syncDir makes a new file or a rename in dir durable. Windows has no such call for a folder, and
// its renames are durable once they return.
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}

// migratingFiles are the files of a prepare's copy.
func migratingFiles(db string) []string {
	tmp := MigratingPath(db)
	return []string{tmp, tmp + "-wal", tmp + "-shm", tmp + "-journal", tmp + ".inuse"}
}

// removeMigrating removes what a prepare made, or what an interrupted one left.
func removeMigrating(db string) error {
	for _, f := range migratingFiles(db) {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

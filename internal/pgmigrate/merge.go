//go:build sqlite

package pgmigrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/BitcoincashII/forge-solo/internal/dblock"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// A merge: forgesolo.db (S) exists, and the old PostgreSQL database holds data S does not, because
// 1.0.12 ran on it again after the move (a return to 1.0.12 and back), or a move was cut short after
// its rename. The new database T is a fresh copy of PostgreSQL, and S's data is merged into it.
//
// A block and its payouts are one unit, and so are a 1175 block and its credits: a unit comes whole
// from one side and is never mixed, so no state is made that neither side had.

// MergeReport is what a merge took from forgesolo.db.
type MergeReport struct {
	Added         map[string][]int64 `json:"added"`    // heights only forgesolo.db had, per ledger
	Replaced      map[string][]int64 `json:"replaced"` // heights where its unit had got further
	MinersAdded   int                `json:"miners_added"`
	MinersUpdated int                `json:"miners_updated"`
	PoolConfig    string             `json:"pool_config"`    // the side the pool settings came from
	DatumIdentity string             `json:"datum_identity"` // the side the TIDES key came from
	Shares        int64              `json:"shares"`
	BestShares    int64              `json:"best_shares"` // the workers' all-time best shares
}

// The two sides, as the report names them.
const (
	sidePostgres = "postgres"
	sideExisting = "forgesolo.db"
)

func isBusy(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code()&0xff == sqlite3.SQLITE_BUSY
}

// exclusive opens the database at path so that no other connection can have it: SQLite's
// exclusive locking mode, no wait, and a write transaction to take the lock. A program that has
// the database open, even one that does not take the in-use lock, makes it fail with SQLITE_BUSY:
// that is deferred (31). The connection keeps the lock until it is closed.
func exclusive(ctx context.Context, path string) (*sql.DB, *sql.Conn, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(0)&_pragma=locking_mode(EXCLUSIVE)")
	if err != nil {
		return nil, nil, newErr(CodeOther, "the database could not be opened", err)
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err == nil {
		if _, err = conn.ExecContext(ctx, `BEGIN EXCLUSIVE`); err == nil {
			_, err = conn.ExecContext(ctx, `COMMIT`)
		}
	}
	if err != nil {
		if conn != nil {
			conn.Close()
		}
		db.Close()
		if isBusy(err) {
			return nil, nil, deferred(fmt.Errorf("another program has %s open", path))
		}
		return nil, nil, newErr(CodeOther, "the database could not be opened", err)
	}
	return db, conn, nil
}

// identity is a database file's size, time and content, to tell whether it changed.
type identity struct {
	Size   int64  `json:"size"`
	MTime  int64  `json:"mtime_ns"`
	SHA256 string `json:"sha256"`
}

func fileIdentity(path string) (identity, error) {
	f, err := os.Open(path)
	if err != nil {
		return identity{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return identity{}, err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return identity{}, err
	}
	return identity{Size: fi.Size(), MTime: fi.ModTime().UnixNano(), SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

// existing is forgesolo.db while a merge reads it.
type existing struct {
	db   *sql.DB
	conn *sql.Conn
	lock *dblock.Lock // the in-use lock, when prepare took it itself
	id   identity
}

// openExisting takes forgesolo.db for a merge. Nobody may have it open: the in-use lock (unless the
// caller holds it already) and SQLite's own exclusive lock, both without waiting; either busy is
// deferred (31) with nothing changed. Its WAL is folded in on the one connection the merge reads it
// through, which keeps the lock until prepare ends, so it cannot change while it is read.
func openExisting(ctx context.Context, path string, held *dblock.Lock) (_ *existing, err error) {
	if _, err := os.Stat(path); err != nil {
		return nil, refused("there is no forgesolo.db to merge with", err)
	}
	e := &existing{}
	defer func() {
		if err != nil {
			e.close()
		}
	}()
	if held == nil {
		l, err := dblock.TryExclusive(dblock.Path(path))
		if errors.Is(err, dblock.ErrBusy) {
			return nil, deferred(fmt.Errorf("a program holds %s", dblock.Path(path)))
		}
		if err != nil {
			return nil, newErr(CodeOther, "the database's in-use lock could not be taken", err)
		}
		e.lock = l
	}
	if e.db, e.conn, err = exclusive(ctx, path); err != nil {
		return nil, err
	}
	var busy, logged, moved int
	if err := e.conn.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logged, &moved); err != nil || busy != 0 {
		return nil, newErr(CodeOther, "the database's WAL could not be folded in", fmt.Errorf("busy %d: %v", busy, err))
	}
	if e.id, err = fileIdentity(path); err != nil {
		return nil, newErr(CodeOther, "the database could not be read", err)
	}
	return e, nil
}

func (e *existing) close() {
	if e.conn != nil {
		e.conn.Close()
	}
	if e.db != nil {
		e.db.Close()
	}
	e.lock.Release()
}

// sideRows are one table's rows on one side, each a value per column of the table's spec, read as
// they are stored. A column the side lacks reads as the column's default.
func sideRows(ctx context.Context, q Queryer, tb Table) ([][]any, error) {
	have, err := tableColumns(ctx, q, tb.Name)
	if err != nil {
		return nil, err
	}
	if len(have) == 0 {
		return nil, nil // the table is not there: an empty file the services made
	}
	present := map[string]bool{}
	for _, c := range have {
		present[c.name] = true
	}
	sel := make([]string, len(tb.Columns))
	args := []any{}
	for i, c := range tb.Columns {
		if present[c.SQLite] {
			sel[i] = selectExpr(c)
		} else {
			sel[i] = "?"
			args = append(args, c.Default)
		}
	}
	rows, err := q.QueryContext(ctx, `SELECT `+strings.Join(sel, ", ")+` FROM `+tb.Name+` ORDER BY `+strings.Join(tb.Key, ", "), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]any
	for rows.Next() {
		r := make([]any, len(tb.Columns))
		p := make([]any, len(r))
		for i := range r {
			p[i] = &r[i]
		}
		if err := rows.Scan(p...); err != nil {
			return nil, err
		}
		for i, v := range r {
			if b, ok := v.([]byte); ok {
				r[i] = string(b)
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// at is the index of the column name in tb.
func (t Table) at(name string) int {
	for i, c := range t.Columns {
		if c.SQLite == name {
			return i
		}
	}
	panic(t.Name + "." + name)
}

// unit is one height of a ledger on one side: its block row (nil when there is none) and the
// credits of that height.
type unit struct {
	block   []any
	credits [][]any
}

func units(blocks, credits Table, bRows, cRows [][]any) map[int64]*unit {
	out := map[int64]*unit{}
	get := func(h int64) *unit {
		if out[h] == nil {
			out[h] = &unit{}
		}
		return out[h]
	}
	for _, r := range bRows {
		get(r[blocks.at("height")].(int64)).block = r
	}
	for _, r := range cRows {
		u := get(r[credits.at("block_height")].(int64))
		u.credits = append(u.credits, r)
	}
	return out
}

func asText(v any) string { s, _ := v.(string); return s }

// later reports whether time a is after time b, both as SQLite holds them; no time is before any.
// Only the first 19 characters are compared, as every query reads a time.
func later(a, b any) bool {
	sa, aok := a.(string)
	sb, bok := b.(string)
	switch {
	case !aok:
		return false
	case !bok:
		return true
	}
	return first19(sa) > first19(sb)
}

func first19(s string) string {
	if len(s) > 19 {
		return s[:19]
	}
	return s
}

// ledger is how one ledger's units are compared.
type ledger struct {
	blocks, credits Table
	rank            func(Table, Table, *unit) int // how far a unit got
}

var bch2Ledger = ledger{blocks: mustTable("blocks"), credits: mustTable("payouts"), rank: bch2Rank}
var ledger1175 = ledger{blocks: mustTable("blocks_1175"), credits: mustTable("payouts_1175"), rank: rank1175}

func mustTable(name string) Table {
	t, ok := table(name)
	if !ok {
		panic(name)
	}
	return t
}

// bch2Rank: a block confirmed or orphaned has been judged; a pending one not yet.
func bch2Rank(b, _ Table, u *unit) int {
	switch asText(u.block[b.at("status")]) {
	case "confirmed", "orphaned":
		return 1
	}
	return 0
}

// rank1175: 3 when a credit is paid or being sent, 2 when the block was distributed, 1 when it was
// judged (confirmed or orphaned), 0 when it is pending.
func rank1175(b, c Table, u *unit) int {
	for _, r := range u.credits {
		if s := asText(r[c.at("status")]); s == "paid" || s == "sending" {
			return 3
		}
	}
	if d, _ := u.block[b.at("distributed")].(int64); d == 1 {
		return 2
	}
	switch asText(u.block[b.at("status")]) {
	case "confirmed", "orphaned":
		return 1
	}
	return 0
}

// existingWins decides one height: whether forgesolo.db's unit s replaces PostgreSQL's t. PostgreSQL
// is the copy T is made of, and it ran last, so it wins every tie.
//   - Only one side has the height: that side.
//   - A unit with its block row beats one with credits alone.
//   - The same hash: the unit that got further.
//   - Different hashes: the block that is not orphaned; if both or neither are, the later found
//     time, which a block that superseded another at its height carries.
func (l ledger) existingWins(t, s *unit) bool {
	switch {
	case s == nil:
		return false
	case t == nil:
		return true
	case (t.block == nil) != (s.block == nil):
		return s.block != nil
	case t.block == nil:
		return false
	}
	hash := l.blocks.at("hash")
	if t.block[hash] == s.block[hash] {
		return l.rank(l.blocks, l.credits, s) > l.rank(l.blocks, l.credits, t)
	}
	status := l.blocks.at("status")
	tOrphan, sOrphan := asText(t.block[status]) == "orphaned", asText(s.block[status]) == "orphaned"
	if tOrphan != sOrphan {
		return tOrphan
	}
	created := l.blocks.at("created_at")
	return later(s.block[created], t.block[created])
}

// withoutID is a row without its id column, for an insert that numbers it anew.
func withoutID(tb Table, r []any) ([]string, []any) {
	var cols []string
	var vals []any
	for i, c := range tb.Columns {
		if c.SQLite == "id" {
			continue
		}
		cols = append(cols, c.SQLite)
		vals = append(vals, r[i])
	}
	return cols, vals
}

func insertRow(ctx context.Context, tx *sql.Tx, table string, cols []string, vals []any) error {
	q := `INSERT INTO ` + table + ` (` + strings.Join(cols, ", ") + `) VALUES (` + strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ") + `)`
	_, err := tx.ExecContext(ctx, q, vals...)
	return err
}

// mergePlan is what a merge takes from forgesolo.db, decided before anything is written.
type mergePlan struct {
	report MergeReport
	// for the check after the merge: each ledger's chosen unit per height, rows without ids
	chosen  map[string]map[int64]*unit
	sMiners []string
}

// merge merges forgesolo.db, read through s, into the copy of PostgreSQL in t, in one transaction.
func merge(ctx context.Context, s Queryer, t *sql.DB) (*mergePlan, error) {
	mp := &mergePlan{
		report: MergeReport{Added: map[string][]int64{}, Replaced: map[string][]int64{}},
		chosen: map[string]map[int64]*unit{},
	}
	read := func(q Queryer, tb Table) ([][]any, error) {
		rows, err := sideRows(ctx, q, tb)
		if err != nil {
			return nil, newErr(CodeOther, "forgesolo.db could not be read", fmt.Errorf("%s: %w", tb.Name, err))
		}
		return rows, nil
	}
	tx, err := t.BeginTx(ctx, nil)
	if err != nil {
		return nil, newErr(CodeWrite, "the new database could not be written", err)
	}
	defer tx.Rollback()
	wErr := func(err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return newErr(CodeWrite, "the new database could not be written", err)
	}

	for _, l := range []ledger{bch2Ledger, ledger1175} {
		var side [2]map[int64]*unit
		for i, q := range []Queryer{tx, s} {
			b, err := read(q, l.blocks)
			if err != nil {
				return nil, err
			}
			c, err := read(q, l.credits)
			if err != nil {
				return nil, err
			}
			side[i] = units(l.blocks, l.credits, b, c)
		}
		tU, sU := side[0], side[1]
		heights := map[int64]bool{}
		for h := range tU {
			heights[h] = true
		}
		for h := range sU {
			heights[h] = true
		}
		sorted := make([]int64, 0, len(heights))
		for h := range heights {
			sorted = append(sorted, h)
		}
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		chosen := map[int64]*unit{}
		for _, h := range sorted {
			tu, su := tU[h], sU[h]
			if !l.existingWins(tu, su) {
				chosen[h] = tu
				continue
			}
			chosen[h] = su
			if tu != nil {
				for _, q := range []string{
					`DELETE FROM ` + l.blocks.Name + ` WHERE height = ?`,
					`DELETE FROM ` + l.credits.Name + ` WHERE block_height = ?`,
				} {
					if _, err := tx.ExecContext(ctx, q, h); err != nil {
						return nil, wErr(err)
					}
				}
				mp.report.Replaced[l.blocks.Name] = append(mp.report.Replaced[l.blocks.Name], h)
			} else {
				mp.report.Added[l.blocks.Name] = append(mp.report.Added[l.blocks.Name], h)
			}
			if su.block != nil {
				cols, vals := withoutID(l.blocks, su.block)
				if err := insertRow(ctx, tx, l.blocks.Name, cols, vals); err != nil {
					return nil, wErr(err)
				}
			}
			for _, r := range su.credits {
				cols, vals := withoutID(l.credits, r)
				if err := insertRow(ctx, tx, l.credits.Name, cols, vals); err != nil {
					return nil, wErr(err)
				}
			}
		}
		mp.chosen[l.blocks.Name] = chosen
	}

	// miners, by address: the settings of the side that changed them last, and the first time seen.
	miners := mustTable("miners")
	tm, err := read(tx, miners)
	if err != nil {
		return nil, err
	}
	sm, err := read(s, miners)
	if err != nil {
		return nil, err
	}
	addr, upd, created := miners.at("address"), miners.at("updated_at"), miners.at("created_at")
	byAddr := map[string][]any{}
	for _, r := range tm {
		byAddr[asText(r[addr])] = r
	}
	for _, r := range sm {
		a := asText(r[addr])
		mp.sMiners = append(mp.sMiners, a)
		tr := byAddr[a]
		if tr == nil {
			cols, vals := withoutID(miners, r)
			if err := insertRow(ctx, tx, "miners", cols, vals); err != nil {
				return nil, wErr(err)
			}
			mp.report.MinersAdded++
			continue
		}
		if later(r[upd], tr[upd]) {
			var sets []string
			var vals []any
			for _, c := range []string{"solo_mining", "manual_diff", "address_1175", "settings_pin_hash", "updated_at"} {
				sets = append(sets, c+" = ?")
				vals = append(vals, r[miners.at(c)])
			}
			if _, err := tx.ExecContext(ctx, `UPDATE miners SET `+strings.Join(sets, ", ")+` WHERE address = ?`, append(vals, a)...); err != nil {
				return nil, wErr(err)
			}
			mp.report.MinersUpdated++
		}
		if later(tr[created], r[created]) || (tr[created] == nil && r[created] != nil) {
			if _, err := tx.ExecContext(ctx, `UPDATE miners SET created_at = ? WHERE address = ?`, r[created], a); err != nil {
				return nil, wErr(err)
			}
		}
	}

	// pool_config: the settings saved last; PostgreSQL's on a tie, forgesolo.db's when PostgreSQL has none.
	pc := mustTable("pool_config")
	tp, err := read(tx, pc)
	if err != nil {
		return nil, err
	}
	sp, err := read(s, pc)
	if err != nil {
		return nil, err
	}
	mp.report.PoolConfig = sidePostgres
	if len(sp) > 0 && (len(tp) == 0 || later(sp[0][pc.at("updated_at")], tp[0][pc.at("updated_at")])) {
		if _, err := tx.ExecContext(ctx, `DELETE FROM pool_config`); err != nil {
			return nil, wErr(err)
		}
		if err := insertRow(ctx, tx, "pool_config", pc.sqliteNames(), sp[0]); err != nil {
			return nil, wErr(err)
		}
		mp.report.PoolConfig = sideExisting
	}

	// datum_identity: PostgreSQL's, which ran last, when it has one; the other key stays in the copy
	// commit keeps of forgesolo.db.
	di := mustTable("datum_identity")
	td, err := read(tx, di)
	if err != nil {
		return nil, err
	}
	sd, err := read(s, di)
	if err != nil {
		return nil, err
	}
	mp.report.DatumIdentity = sidePostgres
	if len(td) == 0 && len(sd) > 0 {
		if err := insertRow(ctx, tx, "datum_identity", di.sqliteNames(), sd[0]); err != nil {
			return nil, wErr(err)
		}
		mp.report.DatumIdentity = sideExisting
	}

	// shares: forgesolo.db's, as they are. PostgreSQL's are never copied.
	if mp.report.Shares, err = carryRows(ctx, s, tx, "shares", "id"); err != nil {
		return nil, err
	}
	// best_shares: forgesolo.db's, as they are. 1.0.12 kept none.
	if mp.report.BestShares, err = carryRows(ctx, s, tx, "best_shares", "miner_address, worker_name"); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, wErr(err)
	}
	return mp, nil
}

// carryRows copies forgesolo.db's rows of table into the new database verbatim, ids and all, in
// the order given. A time is copied as the text it is stored as.
func carryRows(ctx context.Context, s Queryer, tx *sql.Tx, table, order string) (int64, error) {
	theirs, err := tableColumns(ctx, s, table)
	if err != nil {
		return 0, newErr(CodeOther, "forgesolo.db could not be read", err)
	}
	ours, err := tableColumns(ctx, tx, table)
	if err != nil {
		return 0, newErr(CodeWrite, "the new database could not be read", err)
	}
	has := map[string]bool{}
	for _, c := range theirs {
		has[c.name] = true
	}
	var cols []string
	for _, c := range ours {
		if has[c.name] {
			cols = append(cols, c.name)
		}
	}
	if len(cols) == 0 {
		return 0, nil
	}
	sel := make([]string, len(cols))
	for i, c := range cols {
		sel[i] = c
		if strings.HasSuffix(c, "_at") {
			sel[i] = "CAST(" + c + " AS TEXT)"
		}
	}
	rows, err := s.QueryContext(ctx, `SELECT `+strings.Join(sel, ", ")+` FROM `+table+` ORDER BY `+order)
	if err != nil {
		return 0, newErr(CodeOther, "forgesolo.db could not be read", err)
	}
	var all [][]any
	for rows.Next() {
		r := make([]any, len(cols))
		p := make([]any, len(r))
		for i := range r {
			p[i] = &r[i]
		}
		if err := rows.Scan(p...); err != nil {
			rows.Close()
			return 0, newErr(CodeOther, "forgesolo.db could not be read", err)
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, newErr(CodeOther, "forgesolo.db could not be read", err)
	}
	for _, r := range all {
		if err := insertRow(ctx, tx, table, cols, r); err != nil {
			return 0, newErr(CodeWrite, "the new database could not be written", err)
		}
	}
	return int64(len(all)), nil
}

// verifyMerge checks the merged database: every height of either side is there, each unit is the
// chosen side's row for row, and every miner forgesolo.db knew is there.
func verifyMerge(ctx context.Context, t Queryer, mp *mergePlan) error {
	for _, l := range []ledger{bch2Ledger, ledger1175} {
		b, err := sideRows(ctx, t, l.blocks)
		if err != nil {
			return verifyErr("%s: %v", l.blocks.Name, err)
		}
		c, err := sideRows(ctx, t, l.credits)
		if err != nil {
			return verifyErr("%s: %v", l.credits.Name, err)
		}
		got := units(l.blocks, l.credits, b, c)
		want := mp.chosen[l.blocks.Name]
		if len(got) != len(want) {
			return verifyErr("%s: %d heights after the merge, want %d (both sides together)", l.blocks.Name, len(got), len(want))
		}
		for h, w := range want {
			g := got[h]
			if g == nil {
				return verifyErr("%s: height %d is missing after the merge", l.blocks.Name, h)
			}
			if unitKey(l, g) != unitKey(l, w) {
				return verifyErr("%s: height %d is not the chosen side's unit after the merge", l.blocks.Name, h)
			}
		}
	}
	rows, err := t.QueryContext(ctx, `SELECT address FROM miners`)
	if err != nil {
		return verifyErr("miners: %v", err)
	}
	have := map[string]bool{}
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			rows.Close()
			return verifyErr("miners: %v", err)
		}
		have[a] = true
	}
	rows.Close()
	for _, a := range mp.sMiners {
		if !have[a] {
			return verifyErr("miners: %s, which forgesolo.db knew, is missing after the merge", a)
		}
	}
	return nil
}

// unitKey is a unit's rows without their ids, in a fixed order.
func unitKey(l ledger, u *unit) string {
	var parts []string
	if u.block != nil {
		_, v := withoutID(l.blocks, u.block)
		parts = append(parts, "B"+groupKey(v))
	}
	var cs []string
	for _, r := range u.credits {
		_, v := withoutID(l.credits, r)
		cs = append(cs, "C"+groupKey(v))
	}
	sort.Strings(cs)
	return strings.Join(append(parts, cs...), "\n")
}

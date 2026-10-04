//go:build sqlite

package pgmigrate

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/BitcoincashII/forge-solo/internal/stats"
)

// The checks that prove a copy complete. Each compares the new database with the snapshot the copy
// was read from, never with the copy's own idea of what it wrote alone: a bug in the copy must not
// be able to agree with itself. Any failure is exit code 20, and nothing is committed.

func verifyErr(format string, args ...any) *Error {
	return newErr(CodeVerify, "the copy of the old data did not check out", fmt.Errorf(format, args...))
}

// verifyCopy runs every check of the copy of plans into t. written holds the rows as converted,
// inserted what each table's inserts reported. sums receives the old database's satoshi sums.
func verifyCopy(ctx context.Context, snap Snapshot, t Queryer, plans []tablePlan, written map[string][][]any,
	inserted map[string]int64, sums map[string]int64) error {
	present := map[string]bool{}
	for _, p := range plans {
		present[p.Name] = true
		if err := verifyCount(ctx, snap, t, p, len(written[p.Name]), inserted[p.Name]); err != nil {
			return err
		}
		if err := verifyStored(ctx, t, p.Table, written[p.Name]); err != nil {
			return err
		}
		if err := verifyKeys(ctx, snap, t, p); err != nil {
			return err
		}
		if err := verifySatoshis(ctx, snap, t, p, sums); err != nil {
			return err
		}
	}
	for _, tb := range Tables {
		if present[tb.Name] {
			continue
		}
		var n int64
		if err := scanOne(ctx, t, &n, `SELECT COUNT(*) FROM `+tb.Name); err != nil || n != 0 {
			return verifyErr("%s, which the old database does not have, holds %d rows (%v)", tb.Name, n, err)
		}
	}
	if err := verifySettings(ctx, snap, t, plans); err != nil {
		return err
	}
	return verifySequences(ctx, t)
}

func (p tablePlan) filter() Filter {
	if p.Singleton {
		return OnlyID1
	}
	return AllRows
}

// verifyCount: PostgreSQL's count = the rows read = the rows written = the rows in the new table.
func verifyCount(ctx context.Context, snap Snapshot, t Queryer, p tablePlan, read int, inserted int64) error {
	pg, err := snap.Count(ctx, p.Name, p.filter())
	if err != nil {
		return err
	}
	var lite int64
	if err := scanOne(ctx, t, &lite, `SELECT COUNT(*) FROM `+p.Name); err != nil {
		return verifyErr("%s: %v", p.Name, err)
	}
	if pg != int64(read) || int64(read) != inserted || inserted != lite {
		return verifyErr("%s: %d rows in PostgreSQL, %d read, %d written, %d in the new database", p.Name, pg, read, inserted, lite)
	}
	return nil
}

// selectExpr reads a column back as it is stored. A time is read through a cast: the driver turns
// the text of a DATETIME column into a time.Time, which would hide how it is stored.
func selectExpr(c Column) string {
	if c.Kind == KindTime || c.Kind == KindText {
		return "CAST(" + c.SQLite + " AS TEXT)"
	}
	return c.SQLite
}

// storedTypes are the SQLite storage classes a column of kind k may hold.
func storedTypes(k Kind) []string {
	switch k {
	case KindInt, KindBool:
		return []string{"integer"}
	case KindMoney, KindFloat:
		return []string{"real", "integer"}
	}
	return []string{"text"}
}

// verifyStored reads every row back, in key order, and checks each value is stored as its kind
// says (no number held as text, no 't' for a boolean, NULL only where the column allows it) and
// equals what was written: text and integers exactly, floats bit for bit, times as exact strings.
func verifyStored(ctx context.Context, t Queryer, tb Table, want [][]any) error {
	info, err := tableColumns(ctx, t, tb.Name)
	if err != nil {
		return verifyErr("%s: %v", tb.Name, err)
	}
	nullable := map[string]bool{}
	for _, c := range info {
		nullable[c.name] = !c.notNull && !c.pk
	}
	sel := make([]string, 0, 2*len(tb.Columns))
	for _, c := range tb.Columns {
		sel = append(sel, "typeof("+c.SQLite+")", selectExpr(c))
	}
	rows, err := t.QueryContext(ctx, `SELECT `+strings.Join(sel, ", ")+` FROM `+tb.Name+` ORDER BY `+strings.Join(tb.Key, ", "))
	if err != nil {
		return verifyErr("%s: %v", tb.Name, err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		vals := make([]any, 2*len(tb.Columns))
		ptrs := make([]any, len(vals))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return verifyErr("%s: %v", tb.Name, err)
		}
		if n >= len(want) {
			return verifyErr("%s: the new database holds more rows than were written", tb.Name)
		}
		for j, c := range tb.Columns {
			typ, _ := vals[2*j].(string)
			ok := typ == "null" && nullable[c.SQLite]
			for _, allowed := range storedTypes(c.Kind) {
				ok = ok || typ == allowed
			}
			if !ok {
				return verifyErr("%s.%s: a value is stored as %s, want %s", tb.Name, c.SQLite, typ, strings.Join(storedTypes(c.Kind), " or "))
			}
			if got := vals[2*j+1]; !sameValue(want[n][j], got) {
				return verifyErr("%s row %d: %s was written as %#v and reads back as %#v", tb.Name, n+1, c.SQLite, want[n][j], got)
			}
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return verifyErr("%s: %v", tb.Name, err)
	}
	if n != len(want) {
		return verifyErr("%s: %d rows read back of %d written", tb.Name, n, len(want))
	}
	return nil
}

// sameValue compares a written value with the one read back: same type, same value, floats bit for
// bit.
func sameValue(want, got any) bool {
	if b, ok := got.([]byte); ok {
		got = string(b)
	}
	switch x := want.(type) {
	case nil:
		return got == nil
	case int64:
		y, ok := got.(int64)
		return ok && x == y
	case float64:
		y, ok := got.(float64)
		return ok && math.Float64bits(x) == math.Float64bits(y)
	case string:
		y, ok := got.(string)
		return ok && x == y
	}
	return false
}

// verifyKeys: the rows that name each row on both sides are the same set, read from PostgreSQL
// again and from the new database: no block, payout, credit or miner missing or changed.
func verifyKeys(ctx context.Context, snap Snapshot, t Queryer, p tablePlan) error {
	var cols []Column
	for _, name := range p.Natural {
		for _, c := range p.Columns {
			if c.SQLite == name {
				cols = append(cols, c)
			}
		}
	}
	pgNames := make([]string, len(cols))
	sel := make([]string, len(cols))
	for i, c := range cols {
		pgNames[i], sel[i] = c.PG, selectExpr(c)
	}
	pgRows, err := snap.Read(ctx, p.Name, pgNames, p.filter(), p.Key)
	if err != nil {
		return err
	}
	pgSet := map[string]int{}
	for _, r := range pgRows {
		vals := make([]any, len(cols))
		for i, c := range cols {
			if vals[i], err = convert(c.Kind, r[i]); err != nil {
				return err
			}
		}
		pgSet[groupKey(vals)]++
	}
	rows, err := t.QueryContext(ctx, `SELECT `+strings.Join(sel, ", ")+` FROM `+p.Name)
	if err != nil {
		return verifyErr("%s: %v", p.Name, err)
	}
	defer rows.Close()
	liteSet := map[string]int{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return verifyErr("%s: %v", p.Name, err)
		}
		liteSet[groupKey(vals)]++
	}
	if err := rows.Err(); err != nil {
		return verifyErr("%s: %v", p.Name, err)
	}
	if missing, extra := setDiff(pgSet, liteSet), setDiff(liteSet, pgSet); len(missing)+len(extra) > 0 {
		return verifyErr("%s (%s): %d keys of PostgreSQL are not in the new database, %d there are not in PostgreSQL (%s)",
			p.Name, strings.Join(p.Natural, ", "), len(missing), len(extra), strings.Join(append(missing, extra...), "; "))
	}
	return nil
}

func setDiff(a, b map[string]int) []string {
	var out []string
	for k, n := range a {
		if b[k] != n {
			out = append(out, strings.ReplaceAll(strings.ReplaceAll(k, "\x1f", ","), "\x00", "NULL"))
		}
	}
	sort.Strings(out)
	return out
}

// moneyGroups are the columns a table's satoshi sums are split by, besides the whole table: who was
// credited and in what state.
func (p tablePlan) moneyGroups() []string {
	has := map[string]bool{}
	for _, name := range p.have {
		has[name] = true
	}
	var out []string
	for _, g := range []string{"miner_address", "status"} {
		if has[g] {
			out = append(out, g)
		}
	}
	return out
}

// verifySatoshis: every money column sums to the same satoshis in PostgreSQL's exact decimals as in
// the new database's REALs, for the whole table and for each (miner_address, status).
func verifySatoshis(ctx context.Context, snap Snapshot, t Queryer, p tablePlan, sums map[string]int64) error {
	for _, c := range p.Columns {
		if c.Kind != KindMoney {
			continue
		}
		for _, group := range [][]string{nil, p.moneyGroups()} {
			if group != nil && len(group) == 0 {
				continue
			}
			pg, err := snap.Satoshis(ctx, p.Name, c.PG, group)
			if err != nil {
				return err
			}
			lite, err := sqliteSatoshis(ctx, t, p.Name, c.SQLite, group)
			if err != nil {
				return verifyErr("%s.%s: %v", p.Name, c.SQLite, err)
			}
			keys := map[string]bool{}
			for k := range pg {
				keys[k] = true
			}
			for k := range lite {
				keys[k] = true
			}
			for k := range keys {
				if pg[k] != lite[k] {
					return verifyErr("%s.%s by %v (%s): %d satoshis in PostgreSQL, %d in the new database",
						p.Name, c.SQLite, group, strings.ReplaceAll(k, "\x1f", ","), pg[k], lite[k])
				}
			}
			if group == nil {
				sums[p.Name+"."+c.SQLite] = pg[""]
			}
		}
	}
	return nil
}

func sqliteSatoshis(ctx context.Context, t Queryer, table, col string, group []string) (map[string]int64, error) {
	sel := make([]string, 0, len(group)+1)
	for _, g := range group {
		sel = append(sel, "CAST("+g+" AS TEXT)")
	}
	sel = append(sel, `COALESCE(SUM(CAST(ROUND(`+col+` * 100000000) AS INTEGER)), 0)`)
	q := `SELECT ` + strings.Join(sel, ", ") + ` FROM ` + table
	if len(group) > 0 {
		q += ` GROUP BY ` + strings.Join(group, ", ")
	}
	rows, err := t.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		vals := make([]any, len(group))
		dest := make([]any, 0, len(group)+1)
		for i := range vals {
			dest = append(dest, &vals[i])
		}
		var sum int64
		dest = append(dest, &sum)
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		out[groupKey(vals)] = sum
	}
	return out, rows.Err()
}

// verifySettings: the pool's settings are PostgreSQL's field by field, the TIDES gateway key is the
// same, and the payout mode Forge Solo resolves from them is the same.
func verifySettings(ctx context.Context, snap Snapshot, t Queryer, plans []tablePlan) error {
	pgMode := stats.PayoutModeSolo
	for _, p := range plans {
		if p.Name != "pool_config" && p.Name != "datum_identity" {
			continue
		}
		rows, err := snap.Read(ctx, p.Name, p.have, OnlyID1, p.Key)
		if err != nil {
			return err
		}
		sel := make([]string, len(p.Columns))
		for i, c := range p.Columns {
			sel[i] = selectExpr(c)
		}
		lite, err := t.QueryContext(ctx, `SELECT `+strings.Join(sel, ", ")+` FROM `+p.Name+` WHERE id = 1`)
		if err != nil {
			return verifyErr("%s: %v", p.Name, err)
		}
		var got []any
		for lite.Next() {
			got = make([]any, len(p.Columns))
			ptrs := make([]any, len(got))
			for i := range got {
				ptrs[i] = &got[i]
			}
			if err := lite.Scan(ptrs...); err != nil {
				lite.Close()
				return verifyErr("%s: %v", p.Name, err)
			}
		}
		lite.Close()
		if len(rows) == 0 {
			if got != nil {
				return verifyErr("%s: PostgreSQL has no row, the new database has one", p.Name)
			}
			continue
		}
		want, err := convertRow(p.Table, p.have, rows[0])
		if err != nil {
			return err
		}
		if got == nil {
			return verifyErr("%s: the row is missing from the new database", p.Name)
		}
		for i, c := range p.Columns {
			if !sameValue(want[i], got[i]) {
				return verifyErr("%s.%s: %#v in PostgreSQL, %#v in the new database", p.Name, c.SQLite, want[i], got[i])
			}
			if p.Name == "pool_config" && c.SQLite == "payout_mode" {
				if m, _ := want[i].(string); stats.ValidPayoutMode(m) {
					pgMode = m
				}
			}
		}
	}
	liteMode := stats.PayoutModeSolo
	var m any
	if err := scanOne(ctx, t, &m, `SELECT payout_mode FROM pool_config WHERE id = 1`); err == nil {
		if s, _ := m.(string); stats.ValidPayoutMode(s) {
			liteMode = s
		}
	}
	if pgMode != liteMode {
		return verifyErr("the payout mode resolves to %s from PostgreSQL and to %s from the new database", pgMode, liteMode)
	}
	return nil
}

// verifySequences: every table that numbers its rows itself goes on past the ids the copy kept, so
// a block recorded after the move does not collide with a moved one.
func verifySequences(ctx context.Context, t Queryer) error {
	rows, err := t.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND sql LIKE '%AUTOINCREMENT%'`)
	if err != nil {
		return verifyErr("sqlite_sequence: %v", err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return verifyErr("sqlite_sequence: %v", err)
		}
		names = append(names, n)
	}
	rows.Close()
	for _, n := range names {
		var maxID, seq int64
		if err := scanOne(ctx, t, &maxID, `SELECT COALESCE(MAX(id), 0) FROM `+n); err != nil {
			return verifyErr("%s: %v", n, err)
		}
		if err := scanOne(ctx, t, &seq, `SELECT COALESCE(MAX(seq), 0) FROM sqlite_sequence WHERE name = ?`, n); err != nil {
			return verifyErr("sqlite_sequence: %v", err)
		}
		if seq < maxID {
			return verifyErr("%s: the next new row would take id %d, but ids up to %d are in use", n, seq+1, maxID)
		}
	}
	return nil
}

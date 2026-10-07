package pgmigrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"
)

// ErrNoDatabase means the cluster has no Forge Solo database: there is nothing to move.
var ErrNoDatabase = errors.New("the old cluster has no forgesolo database")

// Info names the cluster a snapshot reads.
type Info struct {
	ServerVersion    string
	SystemIdentifier string
}

// Filter limits a read to some of a table's rows.
type Filter int

const (
	AllRows Filter = iota
	OnlyID1        // the one row a single-row table keeps
	NotID1         // the rows SQLite's single-row tables cannot hold
)

func (f Filter) where() string {
	switch f {
	case OnlyID1:
		return " WHERE id = 1"
	case NotID1:
		return " WHERE id <> 1"
	}
	return ""
}

func (f Filter) keeps(row map[string]any) bool {
	switch f {
	case OnlyID1:
		return row["id"] == int64(1)
	case NotID1:
		return row["id"] != int64(1)
	}
	return true
}

// Source is an earlier version's PostgreSQL database.
type Source interface {
	// Snapshot begins the one read-only, repeatable-read transaction every read of a prepare goes
	// through, so every table is read as of the same moment. ErrNoDatabase: there is no database.
	Snapshot(ctx context.Context) (Snapshot, error)
	Close() error
}

// Snapshot reads one consistent view of the old database. Nothing it does writes.
type Snapshot interface {
	Info(ctx context.Context) (Info, error)
	// Columns is each table of the public schema with its columns and their types, as
	// information_schema names them.
	Columns(ctx context.Context) (map[string]map[string]string, error)
	Count(ctx context.Context, table string, f Filter) (int64, error)
	// Read returns the rows of table that f keeps, the columns cols in that order, sorted by order.
	// A numeric value comes as its exact text, timestamptz as time.Time, boolean as bool, an integer
	// as int64, a float as float64, text as string and NULL as nil.
	Read(ctx context.Context, table string, cols []string, f Filter, order []string) ([][]any, error)
	// Satoshis is SUM(ROUND(col * 1e8)) over table, exact, for each group of the values of the
	// group columns (one group, "", when there are none).
	Satoshis(ctx context.Context, table, col string, group []string) (map[string]int64, error)
	Close() error
}

// groupKey names a group of satoshi sums by its values, NULL distinct from ”.
func groupKey(vals []any) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		switch x := v.(type) {
		case nil:
			parts[i] = "\x00"
		case string:
			parts[i] = "=" + x
		default:
			parts[i] = fmt.Sprintf("=%v", x)
		}
	}
	return strings.Join(parts, "\x1f")
}

// OpenPostgres is the database at dsn, through lib/pq. It connects at the first Snapshot.
func OpenPostgres(dsn string) (Source, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, newErr(CodeSource, "the old database could not be reached", err)
	}
	db.SetMaxOpenConns(1)
	return &pgSource{db: db}, nil
}

type pgSource struct{ db *sql.DB }

func (p *pgSource) Close() error { return p.db.Close() }

func (p *pgSource) Snapshot(ctx context.Context) (Snapshot, error) {
	tx, err := p.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		var pe *pq.Error
		switch {
		case errors.As(err, &pe) && pe.Code == "3D000":
			return nil, ErrNoDatabase
		case ctx.Err() != nil:
			return nil, ctx.Err()
		}
		return nil, newErr(CodeSource, "the old database could not be reached", err)
	}
	return &pgSnapshot{tx: tx}, nil
}

type pgSnapshot struct {
	tx    *sql.Tx
	types map[string]map[string]string
}

func readErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return newErr(CodeSource, "the old database could not be read", err)
}

func (s *pgSnapshot) Close() error { return s.tx.Rollback() }

func (s *pgSnapshot) Info(ctx context.Context) (Info, error) {
	var in Info
	if err := s.tx.QueryRowContext(ctx, `SHOW server_version`).Scan(&in.ServerVersion); err != nil {
		return in, readErr(ctx, err)
	}
	// Only a name for the record: an error here must not end the snapshot.
	if _, err := s.tx.ExecContext(ctx, `SAVEPOINT info`); err != nil {
		return in, readErr(ctx, err)
	}
	if err := s.tx.QueryRowContext(ctx, `SELECT system_identifier::text FROM pg_control_system()`).Scan(&in.SystemIdentifier); err != nil {
		if _, err := s.tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT info`); err != nil {
			return in, readErr(ctx, err)
		}
	}
	return in, nil
}

func (s *pgSnapshot) Columns(ctx context.Context) (map[string]map[string]string, error) {
	if s.types != nil {
		return s.types, nil
	}
	rows, err := s.tx.QueryContext(ctx, `
		SELECT c.table_name, c.column_name, c.data_type
		FROM information_schema.columns c
		JOIN information_schema.tables t ON t.table_schema = c.table_schema AND t.table_name = c.table_name
		WHERE c.table_schema = 'public' AND t.table_type = 'BASE TABLE'`)
	if err != nil {
		return nil, readErr(ctx, err)
	}
	defer rows.Close()
	out := map[string]map[string]string{}
	for rows.Next() {
		var t, c, d string
		if err := rows.Scan(&t, &c, &d); err != nil {
			return nil, readErr(ctx, err)
		}
		if out[t] == nil {
			out[t] = map[string]string{}
		}
		out[t][c] = d
	}
	if err := rows.Err(); err != nil {
		return nil, readErr(ctx, err)
	}
	s.types = out
	return out, nil
}

func (s *pgSnapshot) Count(ctx context.Context, table string, f Filter) (int64, error) {
	var n int64
	if err := s.tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+pq.QuoteIdentifier(table)+f.where()).Scan(&n); err != nil {
		return 0, readErr(ctx, err)
	}
	return n, nil
}

func (s *pgSnapshot) Read(ctx context.Context, table string, cols []string, f Filter, order []string) ([][]any, error) {
	types, err := s.Columns(ctx)
	if err != nil {
		return nil, err
	}
	sel := make([]string, len(cols))
	for i, c := range cols {
		sel[i] = pq.QuoteIdentifier(c)
		if types[table][c] == pgNumeric {
			sel[i] += "::text"
		}
	}
	ord := make([]string, len(order))
	for i, c := range order {
		ord[i] = pq.QuoteIdentifier(c)
	}
	q := `SELECT ` + strings.Join(sel, ", ") + ` FROM ` + pq.QuoteIdentifier(table) + f.where()
	if len(ord) > 0 {
		q += ` ORDER BY ` + strings.Join(ord, ", ")
	}
	rows, err := s.tx.QueryContext(ctx, q)
	if err != nil {
		return nil, readErr(ctx, err)
	}
	defer rows.Close()
	var out [][]any
	for rows.Next() {
		holders := make([]any, len(cols))
		for i, c := range cols {
			switch types[table][c] {
			case pgTimestampTZ, pgTimestamp:
				holders[i] = new(sql.NullTime)
			case pgBool:
				holders[i] = new(sql.NullBool)
			case pgInt8, pgInt4, pgInt2:
				holders[i] = new(sql.NullInt64)
			case pgFloat8, pgFloat4:
				holders[i] = new(sql.NullFloat64)
			default:
				holders[i] = new(sql.NullString)
			}
		}
		if err := rows.Scan(holders...); err != nil {
			return nil, readErr(ctx, err)
		}
		row := make([]any, len(cols))
		for i, h := range holders {
			switch v := h.(type) {
			case *sql.NullTime:
				if v.Valid {
					row[i] = v.Time
				}
			case *sql.NullBool:
				if v.Valid {
					row[i] = v.Bool
				}
			case *sql.NullInt64:
				if v.Valid {
					row[i] = v.Int64
				}
			case *sql.NullFloat64:
				if v.Valid {
					row[i] = v.Float64
				}
			case *sql.NullString:
				if v.Valid {
					row[i] = v.String
				}
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, readErr(ctx, err)
	}
	return out, nil
}

func (s *pgSnapshot) Satoshis(ctx context.Context, table, col string, group []string) (map[string]int64, error) {
	sel := make([]string, 0, len(group)+1)
	for _, g := range group {
		sel = append(sel, pq.QuoteIdentifier(g)+"::text")
	}
	sel = append(sel, `COALESCE(SUM(ROUND(`+pq.QuoteIdentifier(col)+`::numeric * 100000000)), 0)::bigint`)
	q := `SELECT ` + strings.Join(sel, ", ") + ` FROM ` + pq.QuoteIdentifier(table)
	if len(group) > 0 {
		by := make([]string, len(group))
		for i := range group {
			by[i] = strconv.Itoa(i + 1)
		}
		q += ` GROUP BY ` + strings.Join(by, ", ")
	}
	rows, err := s.tx.QueryContext(ctx, q)
	if err != nil {
		return nil, readErr(ctx, err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		gs := make([]sql.NullString, len(group))
		dest := make([]any, 0, len(group)+1)
		for i := range gs {
			dest = append(dest, &gs[i])
		}
		var sum int64
		dest = append(dest, &sum)
		if err := rows.Scan(dest...); err != nil {
			return nil, readErr(ctx, err)
		}
		vals := make([]any, len(group))
		for i, g := range gs {
			if g.Valid {
				vals[i] = g.String
			}
		}
		out[groupKey(vals)] = sum
	}
	if err := rows.Err(); err != nil {
		return nil, readErr(ctx, err)
	}
	return out, nil
}

// MemSource is a PostgreSQL database held in memory, with the reads and the value types of the
// lib/pq Source: for tests, and to show what a Source must do.
type MemSource struct {
	mu        sync.Mutex
	tables    map[string]*MemTable
	info      Info
	missing   bool
	snapshots int
	// BeforeRead, when set, runs before each Read: a test changes the live data there, stalls, or
	// fails the read.
	BeforeRead func(ctx context.Context, table string) error
}

// MemTable is one table of a MemSource: its columns' PostgreSQL types and its rows.
type MemTable struct {
	Types map[string]string
	Rows  []map[string]any
}

// NewMemSource is an empty in-memory database.
func NewMemSource(info Info) *MemSource {
	return &MemSource{tables: map[string]*MemTable{}, info: info}
}

// AddTable creates a table with the given column types.
func (m *MemSource) AddTable(name string, types map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tables[name] = &MemTable{Types: types}
}

// Insert adds a row to the live data. A snapshot already begun does not see it.
func (m *MemSource) Insert(table string, row map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tables[table].Rows = append(m.tables[table].Rows, row)
}

// SetMissing makes the database not exist, as PostgreSQL's 3D000.
func (m *MemSource) SetMissing() { m.mu.Lock(); m.missing = true; m.mu.Unlock() }

// Snapshots is how many snapshots were begun.
func (m *MemSource) Snapshots() int { m.mu.Lock(); defer m.mu.Unlock(); return m.snapshots }

func (m *MemSource) Close() error { return nil }

func (m *MemSource) Snapshot(ctx context.Context) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.missing {
		return nil, ErrNoDatabase
	}
	m.snapshots++
	snap := &memSnapshot{src: m, tables: map[string]*MemTable{}, info: m.info}
	for name, t := range m.tables {
		c := &MemTable{Types: t.Types}
		for _, r := range t.Rows {
			cr := make(map[string]any, len(r))
			for k, v := range r {
				cr[k] = v
			}
			c.Rows = append(c.Rows, cr)
		}
		snap.tables[name] = c
	}
	return snap, nil
}

type memSnapshot struct {
	src    *MemSource
	tables map[string]*MemTable
	info   Info
}

func (s *memSnapshot) Close() error                       { return nil }
func (s *memSnapshot) Info(context.Context) (Info, error) { return s.info, nil }

func (s *memSnapshot) Columns(context.Context) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	for name, t := range s.tables {
		out[name] = t.Types
	}
	return out, nil
}

func (s *memSnapshot) table(name string) (*MemTable, error) {
	t, ok := s.tables[name]
	if !ok {
		return nil, newErr(CodeSource, "the old database could not be read", fmt.Errorf("relation %q does not exist", name))
	}
	return t, nil
}

func (s *memSnapshot) Count(ctx context.Context, table string, f Filter) (int64, error) {
	t, err := s.table(table)
	if err != nil {
		return 0, err
	}
	var n int64
	for _, r := range t.Rows {
		if f.keeps(r) {
			n++
		}
	}
	return n, nil
}

func (s *memSnapshot) Read(ctx context.Context, table string, cols []string, f Filter, order []string) ([][]any, error) {
	if s.src.BeforeRead != nil {
		if err := s.src.BeforeRead(ctx, table); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t, err := s.table(table)
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	for _, r := range t.Rows {
		if f.keeps(r) {
			rows = append(rows, r)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		for _, c := range order {
			if d := compareValues(rows[i][c], rows[j][c]); d != 0 {
				return d < 0
			}
		}
		return false
	})
	out := make([][]any, len(rows))
	for i, r := range rows {
		out[i] = make([]any, len(cols))
		for j, c := range cols {
			if _, ok := t.Types[c]; !ok {
				return nil, newErr(CodeSource, "the old database could not be read", fmt.Errorf("column %s.%s does not exist", table, c))
			}
			out[i][j] = r[c]
		}
	}
	return out, nil
}

func (s *memSnapshot) Satoshis(ctx context.Context, table, col string, group []string) (map[string]int64, error) {
	t, err := s.table(table)
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	if len(group) == 0 {
		out[""] = 0
	}
	for _, r := range t.Rows {
		vals := make([]any, len(group))
		for i, g := range group {
			vals[i] = r[g]
		}
		k := groupKey(vals)
		sat, err := satoshis(r[col])
		if err != nil {
			return nil, err
		}
		out[k] += sat
	}
	return out, nil
}

// satoshis is ROUND(v * 1e8) as PostgreSQL computes it on v::numeric: a numeric exactly, a float
// through the 15 significant digits PostgreSQL turns a double precision into, halves away from 0.
func satoshis(v any) (int64, error) {
	var text string
	switch x := v.(type) {
	case nil:
		return 0, nil
	case string:
		text = x
	case float64:
		text = strconv.FormatFloat(x, 'g', 15, 64)
	case int64:
		text = strconv.FormatInt(x, 10)
	default:
		return 0, fmt.Errorf("an amount read as %T", v)
	}
	r, ok := new(big.Rat).SetString(text)
	if !ok {
		return 0, fmt.Errorf("%q is not a number", text)
	}
	r.Mul(r, big.NewRat(100000000, 1))
	n, d := new(big.Int).Set(r.Num()), r.Denom()
	neg := n.Sign() < 0
	n.Abs(n)
	q, rem := new(big.Int).QuoRem(n, d, new(big.Int))
	if rem.Mul(rem, big.NewInt(2)).Cmp(d) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if neg {
		q.Neg(q)
	}
	return q.Int64(), nil
}

// compareValues orders two values of one column as PostgreSQL does for the types used as keys.
func compareValues(a, b any) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1 // NULLS LAST, as PostgreSQL sorts ascending
	case b == nil:
		return -1
	}
	switch x := a.(type) {
	case int64:
		y := b.(int64)
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	case string:
		return strings.Compare(x, b.(string))
	case time.Time:
		return x.Compare(b.(time.Time))
	}
	return strings.Compare(fmt.Sprint(a), fmt.Sprint(b))
}

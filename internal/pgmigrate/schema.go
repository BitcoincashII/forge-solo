//go:build sqlite

package pgmigrate

import (
	"context"
	"database/sql"
	"sort"
	"strings"
)

// Queryer is a SQLite database, connection or transaction.
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Schema is a SQLite database's tables, each with its column names.
type Schema map[string]map[string]bool

// ReadSchema returns q's tables and their columns, SQLite's own tables (sqlite_sequence) included.
func ReadSchema(ctx context.Context, q Queryer) (Schema, error) {
	rows, err := q.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		return nil, err
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		names = append(names, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s := Schema{}
	for _, n := range names {
		cols, err := tableColumns(ctx, q, n)
		if err != nil {
			return nil, err
		}
		s[n] = map[string]bool{}
		for _, c := range cols {
			s[n][c.name] = true
		}
	}
	return s, nil
}

// sqliteColumn is one row of PRAGMA table_info.
type sqliteColumn struct {
	name    string
	notNull bool
	pk      bool
}

func tableColumns(ctx context.Context, q Queryer, table string) ([]sqliteColumn, error) {
	rows, err := q.QueryContext(ctx, `SELECT name, "notnull", pk FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sqliteColumn
	for rows.Next() {
		var c sqliteColumn
		var nn, pk int
		if err := rows.Scan(&c.name, &nn, &pk); err != nil {
			return nil, err
		}
		c.notNull, c.pk = nn != 0, pk != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

// SchemaNewerThanMine reports every table and column in s that mine, a database this build's
// stats.InitDB has just made, does not have: data a newer Forge Solo wrote. A merge refuses such a
// database, so an older migrator never drops what a newer release keeps. SQLite's own tables and
// the migrator's own record are not the app's data and are not compared.
func SchemaNewerThanMine(ctx context.Context, s, mine Queryer) ([]string, error) {
	theirs, err := ReadSchema(ctx, s)
	if err != nil {
		return nil, err
	}
	ours, err := ReadSchema(ctx, mine)
	if err != nil {
		return nil, err
	}
	var extra []string
	for t, cols := range theirs {
		if strings.HasPrefix(t, "sqlite_") || t == metaTable {
			continue
		}
		have, ok := ours[t]
		if !ok {
			extra = append(extra, "table "+t)
			continue
		}
		for c := range cols {
			if !have[c] {
				extra = append(extra, "column "+t+"."+c)
			}
		}
	}
	sort.Strings(extra)
	return extra, nil
}

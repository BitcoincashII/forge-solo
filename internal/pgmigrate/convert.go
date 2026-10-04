//go:build sqlite

package pgmigrate

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/stats"
)

// PostgreSQL column types, as information_schema.columns.data_type names them.
const (
	pgTimestampTZ = "timestamp with time zone"
	pgTimestamp   = "timestamp without time zone"
	pgBool        = "boolean"
	pgNumeric     = "numeric"
	pgFloat8      = "double precision"
	pgFloat4      = "real"
	pgInt8        = "bigint"
	pgInt4        = "integer"
	pgInt2        = "smallint"
	pgText        = "text"
	pgVarchar     = "character varying"
	pgChar        = "character"
)

// ConvertTime is a PostgreSQL timestamptz as the SQLite build stores a time: UTC, rounded to the
// nearest second, "YYYY-MM-DD HH:MM:SS". Rounded, not cut: the PostgreSQL build showed a time as
// EXTRACT(EPOCH ...)::bigint, which rounds. The instant does not depend on the zone lib/pq read it
// in, nor on the server's TimeZone.
func ConvertTime(t time.Time) string {
	return stats.SQLiteTime(t.UTC().Round(time.Second))
}

// ConvertBool is a PostgreSQL boolean as SQLite stores one.
func ConvertBool(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// ConvertNumeric is a PostgreSQL numeric, read as its text, as the REAL SQLite stores. It is the
// conversion the PostgreSQL build's Scan into a float64 made, so the dashboard sees the same number.
// NaN and infinities are refused: no amount is either, and SQLite would store NULL.
func ConvertNumeric(s string) (float64, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", s)
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("%q is not an amount", s)
	}
	return f, nil
}

// typeFits reports whether a PostgreSQL column of type pgType can be carried as kind k.
func typeFits(k Kind, pgType string) bool {
	switch k {
	case KindInt:
		return pgType == pgInt8 || pgType == pgInt4 || pgType == pgInt2
	case KindText:
		return pgType == pgText || pgType == pgVarchar || pgType == pgChar
	case KindBool:
		return pgType == pgBool
	case KindMoney, KindFloat:
		return pgType == pgNumeric || pgType == pgFloat8 || pgType == pgFloat4
	case KindTime:
		return pgType == pgTimestampTZ
	}
	return false
}

// checkType refuses a column the move cannot carry as its spec says. A timestamp without a time
// zone is refused, not guessed at: every Forge Solo schema since 1.0.0 uses timestamptz, so one
// would mean a database this migrator does not know.
func checkType(table string, c Column, pgType string) error {
	if typeFits(c.Kind, pgType) {
		return nil
	}
	if pgType == pgTimestamp {
		return refused("the old database holds a time without a time zone, which Forge Solo never wrote",
			fmt.Errorf("%s.%s is %s", table, c.PG, pgType))
	}
	return refused("the old database has a column of a type Forge Solo never wrote",
		fmt.Errorf("%s.%s is %s, want %s", table, c.PG, pgType, c.Kind))
}

// convertRow is one PostgreSQL row of t as the SQLite row the copy writes, a value for each of t's
// columns in order. vals holds the columns PostgreSQL has, in the order of have; a column it lacks
// gets the column's default.
func convertRow(t Table, have []string, vals []any) ([]any, error) {
	at := make(map[string]int, len(have))
	for i, name := range have {
		at[name] = i
	}
	row := make([]any, len(t.Columns))
	for i, c := range t.Columns {
		j, ok := at[c.PG]
		if !ok {
			row[i] = c.Default
			continue
		}
		v, err := convert(c.Kind, vals[j])
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", t.Name, c.PG, err)
		}
		row[i] = v
	}
	return row, nil
}

// convert is v, as a Snapshot reads it from a PostgreSQL column of kind k, as SQLite stores it.
// NULL stays NULL in every kind.
func convert(k Kind, v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	switch k {
	case KindTime:
		if t, ok := v.(time.Time); ok {
			return ConvertTime(t), nil
		}
	case KindBool:
		if b, ok := v.(bool); ok {
			return ConvertBool(b), nil
		}
	case KindMoney, KindFloat:
		switch x := v.(type) {
		case string: // numeric, as its text
			f, err := ConvertNumeric(x)
			if err != nil {
				return nil, refused("the old database holds an amount that is not a number", err)
			}
			return f, nil
		case float64:
			return x, nil
		}
	case KindInt:
		if i, ok := v.(int64); ok {
			return i, nil
		}
	case KindText:
		if s, ok := v.(string); ok {
			return s, nil
		}
	}
	return nil, fmt.Errorf("a %s column read as %T", k, v)
}

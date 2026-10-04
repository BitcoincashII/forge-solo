//go:build sqlite && it

package pgmigrate

// What scripts/it-pg-to-sqlite.sh reads and writes on the databases it moves, built into one test
// binary (go test -c -tags 'sqlite it') that the script runs on the dev machine and in containers,
// one test at a time:
//   - TestITFigures: the 54 figures the dashboard and the stratum depend on, from PostgreSQL before
//     a move and from forgesolo.db after it;
//   - TestITPostgresRows and TestITCompareRows: every row as PostgreSQL's to_json gives it, then the
//     same rows as SQLite's json_object gives them, compared value by value by rules of their own,
//     apart from the migrator's;
//   - TestITSnapshot: forgesolo.db's rows in one canonical form, to compare two moves, or a move on
//     Windows with one here;
//   - TestITWrite: what the app writes in forgesolo.db between the steps (as its user, uid 10001);
//   - TestITControl: pg_control files captured from a real cluster read as the committed ones do.
//
// Each takes its inputs from IT_* variables and fails, never skips, without them.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/stats"
	"github.com/lib/pq"
)

func itEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Fatalf("IT-SETUP: %s is not set (scripts/it-pg-to-sqlite.sh sets it)", name)
	}
	return v
}

// itOpenSQLite opens forgesolo.db read-only for a check.
func itOpenSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("IT-SETUP: %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func itOpenPostgres(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatalf("IT-SETUP: PostgreSQL: %v", err)
	}
	return db
}

// The 1.0.12 columns of the seven tables the move carries, with what a database from before a
// column existed stands for (1.0.12's own defaults). A column missing from an older PostgreSQL
// database reads as its default; a table missing reads as empty.
type itColumn struct{ name, pgType, missing string }

var itTables = []struct {
	name string
	cols []itColumn
}{
	{"pool_config", []itColumn{{"id", "int", ""}, {"pool_address", "text", "''"}, {"payout_address_1175", "text", "''"},
		{"coinbase_tag", "text", "''"}, {"payout_mode", "text", "'solo'"}, {"updated_at", "timestamptz", ""}}},
	{"datum_identity", []itColumn{{"id", "int", ""}, {"key_seed", "text", ""}, {"created_at", "timestamptz", ""}}},
	{"miners", []itColumn{{"id", "bigint", ""}, {"address", "text", ""}, {"solo_mining", "boolean", ""},
		{"manual_diff", "numeric", ""}, {"address_1175", "text", ""}, {"settings_pin_hash", "text", ""},
		{"created_at", "timestamptz", ""}, {"updated_at", "timestamptz", ""}}},
	{"blocks", []itColumn{{"id", "bigint", ""}, {"height", "bigint", ""}, {"hash", "text", ""}, {"miner_address", "text", ""},
		{"reward", "numeric", ""}, {"status", "text", ""}, {"is_solo", "boolean", "false"},
		{"created_at", "timestamptz", ""}, {"confirmed_at", "timestamptz", ""}}},
	{"payouts", []itColumn{{"id", "bigint", ""}, {"miner_address", "text", ""}, {"block_height", "bigint", ""},
		{"amount", "numeric", ""}, {"confirmed", "boolean", ""}, {"txid", "text", ""}, {"status", "text", "'pending'"},
		{"created_at", "timestamptz", ""}, {"paid_at", "timestamptz", ""}}},
	{"blocks_1175", []itColumn{{"height", "bigint", ""}, {"hash", "text", ""}, {"gross_reward", "numeric", ""},
		{"is_solo", "boolean", "false"}, {"finder", "text", ""}, {"distributed", "boolean", "false"}, {"status", "text", ""},
		{"created_at", "timestamptz", ""}}},
	{"payouts_1175", []itColumn{{"id", "bigint", ""}, {"miner_address", "text", ""}, {"block_height", "bigint", ""},
		{"amount", "numeric", ""}, {"txid", "text", ""}, {"status", "text", ""}, {"batch", "text", ""},
		{"paid_at", "timestamptz", ""}, {"created_at", "timestamptz", ""}}},
}

// itRow is one row in a form both databases reduce to: integers (bools as 0/1, money in satoshis,
// times in Unix seconds rounded to the second), text, or nil.
type itRow map[string]any

// itRows reads every table's rows, reduced. On PostgreSQL a column an older database lacks reads as
// its default, and the singletons are read at id 1 only, as the move takes them.
func itRows(t *testing.T, db *sql.DB, postgres bool) map[string][]itRow {
	t.Helper()
	have := map[string]map[string]bool{}
	if postgres {
		rows, err := db.Query(`SELECT table_name, column_name FROM information_schema.columns WHERE table_schema = 'public'`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var tb, c string
			if err := rows.Scan(&tb, &c); err != nil {
				t.Fatal(err)
			}
			if have[tb] == nil {
				have[tb] = map[string]bool{}
			}
			have[tb][c] = true
		}
		rows.Close()
	}
	out := map[string][]itRow{}
	for _, tb := range itTables {
		var sel []string
		for _, c := range tb.cols {
			switch {
			case !postgres || have[tb.name][c.name]:
				sel = append(sel, c.name)
			case c.missing != "":
				sel = append(sel, c.missing+"::"+c.pgType+" AS "+c.name)
			default:
				sel = append(sel, "NULL::"+c.pgType+" AS "+c.name)
			}
		}
		if postgres && have[tb.name] == nil {
			continue // a database from before this table: none of its rows
		}
		q := `SELECT ` + strings.Join(sel, ", ") + ` FROM ` + tb.name
		if tb.name == "pool_config" || tb.name == "datum_identity" {
			q += ` WHERE id = 1`
		}
		rows, err := db.Query(q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		for rows.Next() {
			vals := make([]any, len(tb.cols))
			ptrs := make([]any, len(vals))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			r := itRow{}
			for i, c := range tb.cols {
				r[c.name] = itReduce(t, tb.name+"."+c.name, c.pgType, vals[i])
			}
			out[tb.name] = append(out[tb.name], r)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	return out
}

func itReduce(t *testing.T, where, kind string, v any) any {
	t.Helper()
	if v == nil {
		return nil
	}
	switch kind {
	case "boolean":
		switch x := v.(type) {
		case bool:
			if x {
				return int64(1)
			}
			return int64(0)
		case int64:
			return x
		}
	case "numeric":
		var s string
		switch x := v.(type) {
		case []byte:
			s = string(x)
		case string:
			s = x
		case float64:
			return int64(math.Round(x * 1e8))
		case int64:
			return x * 100_000_000
		}
		r, ok := new(big.Rat).SetString(s)
		if !ok {
			t.Fatalf("IT-REDUCE: %s: %q is not a number", where, s)
		}
		r.Mul(r, big.NewRat(100_000_000, 1))
		f, _ := r.Float64()
		return int64(math.Round(f))
	case "timestamptz":
		switch x := v.(type) {
		case time.Time:
			return x.Round(time.Second).Unix()
		case string:
			tm, err := time.ParseInLocation("2006-01-02 15:04:05", x, time.UTC)
			if err != nil {
				t.Fatalf("IT-REDUCE: %s: %q is not a stored time", where, x)
			}
			return tm.Unix()
		}
	case "int", "bigint":
		switch x := v.(type) {
		case int64:
			return x
		}
	case "text":
		switch x := v.(type) {
		case string:
			return x
		case []byte:
			return string(x)
		}
	}
	t.Fatalf("IT-REDUCE: %s: %T %v is not a %s", where, v, v, kind)
	return nil
}

// The figures, each a name and what it is over the reduced rows.
type itFigure struct {
	name string
	of   func(map[string][]itRow) string
}

func itCount(table string, where func(itRow) bool) func(map[string][]itRow) string {
	return func(d map[string][]itRow) string {
		n := 0
		for _, r := range d[table] {
			if where == nil || where(r) {
				n++
			}
		}
		return strconv.Itoa(n)
	}
}

func itSum(table, col string, where func(itRow) bool) func(map[string][]itRow) string {
	return func(d map[string][]itRow) string {
		var s int64
		for _, r := range d[table] {
			if v, ok := r[col].(int64); ok && (where == nil || where(r)) {
				s += v
			}
		}
		return strconv.FormatInt(s, 10)
	}
}

func itText(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case int64:
		return strconv.FormatInt(x, 10)
	case string:
		return strconv.Quote(x)
	}
	return fmt.Sprint(v)
}

// itGroups is "value:count" for each value of the columns, in order.
func itGroups(table string, cols ...string) func(map[string][]itRow) string {
	return func(d map[string][]itRow) string {
		n := map[string]int{}
		for _, r := range d[table] {
			var k []string
			for _, c := range cols {
				k = append(k, itText(r[c]))
			}
			n[strings.Join(k, "|")]++
		}
		var out []string
		for k, c := range n {
			out = append(out, fmt.Sprintf("%s:%d", k, c))
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
}

// itDigest is a hash of the columns of every row, the rows in order.
func itDigest(table string, cols ...string) func(map[string][]itRow) string {
	return func(d map[string][]itRow) string {
		var lines []string
		for _, r := range d[table] {
			var k []string
			for _, c := range cols {
				k = append(k, itText(r[c]))
			}
			lines = append(lines, strings.Join(k, "|"))
		}
		sort.Strings(lines)
		sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
		return fmt.Sprintf("%d rows %s", len(lines), hex.EncodeToString(sum[:8]))
	}
}

func itField(table, col string) func(map[string][]itRow) string {
	return func(d map[string][]itRow) string {
		if len(d[table]) == 0 {
			return "none"
		}
		return itText(d[table][0][col])
	}
}

func itMax(table, col string) func(map[string][]itRow) string {
	return func(d map[string][]itRow) string {
		var m int64
		for _, r := range d[table] {
			if v, ok := r[col].(int64); ok && v > m {
				m = v
			}
		}
		return strconv.FormatInt(m, 10)
	}
}

func itRange(table, col string) func(map[string][]itRow) string {
	return func(d map[string][]itRow) string {
		if len(d[table]) == 0 {
			return "none"
		}
		lo, hi := int64(math.MaxInt64), int64(math.MinInt64)
		for _, r := range d[table] {
			v, _ := r[col].(int64)
			lo, hi = min(lo, v), max(hi, v)
		}
		return fmt.Sprintf("%d..%d", lo, hi)
	}
}

func itIs(col string, want any) func(itRow) bool {
	return func(r itRow) bool { return r[col] == want }
}

func itNull(col string) func(itRow) bool { return func(r itRow) bool { return r[col] == nil } }

var itFigures = []itFigure{
	{"count.pool_config", itCount("pool_config", nil)},
	{"count.datum_identity", itCount("datum_identity", nil)},
	{"count.miners", itCount("miners", nil)},
	{"count.blocks", itCount("blocks", nil)},
	{"count.payouts", itCount("payouts", nil)},
	{"count.blocks_1175", itCount("blocks_1175", nil)},
	{"count.payouts_1175", itCount("payouts_1175", nil)},
	{"sat.blocks", itSum("blocks", "reward", nil)},
	{"sat.payouts", itSum("payouts", "amount", nil)},
	{"sat.payouts.paid", itSum("payouts", "amount", itIs("status", "paid"))},
	{"sat.payouts.pending", itSum("payouts", "amount", itIs("status", "pending"))},
	{"sat.blocks_1175", itSum("blocks_1175", "gross_reward", nil)},
	{"sat.payouts_1175", itSum("payouts_1175", "amount", nil)},
	{"sat.payouts_1175.paid", itSum("payouts_1175", "amount", itIs("status", "paid"))},
	{"sat.payouts_1175.pending", itSum("payouts_1175", "amount", itIs("status", "pending"))},
	{"pool.address", itField("pool_config", "pool_address")},
	{"pool.address_1175", itField("pool_config", "payout_address_1175")},
	{"pool.tag", itField("pool_config", "coinbase_tag")},
	{"pool.mode", itField("pool_config", "payout_mode")},
	{"pool.updated", itField("pool_config", "updated_at")},
	{"key.seed", itField("datum_identity", "key_seed")},
	{"key.created", itField("datum_identity", "created_at")},
	{"miners.addresses", itDigest("miners", "address")},
	{"miners.solo", itGroups("miners", "solo_mining")},
	{"miners.manual_diff", itSum("miners", "manual_diff", nil)},
	{"miners.address_1175", itDigest("miners", "address", "address_1175")},
	{"miners.pin", itDigest("miners", "address", "settings_pin_hash")},
	{"miners.created", itSum("miners", "created_at", nil)},
	{"miners.updated", itSum("miners", "updated_at", nil)},
	{"miners.max_id", itMax("miners", "id")},
	{"blocks.status", itGroups("blocks", "status")},
	{"blocks.solo", itGroups("blocks", "is_solo")},
	{"blocks.created", itSum("blocks", "created_at", nil)},
	{"blocks.confirmed", itSum("blocks", "confirmed_at", nil)},
	{"blocks.unconfirmed", itCount("blocks", itNull("confirmed_at"))},
	{"blocks.heights", itRange("blocks", "height")},
	{"blocks.hashes", itDigest("blocks", "height", "hash")},
	{"blocks.miners", itGroups("blocks", "miner_address")},
	{"blocks.max_id", itMax("blocks", "id")},
	{"payouts.status", itGroups("payouts", "status")},
	{"payouts.txid", itGroups("payouts", "txid")},
	{"payouts.confirmed", itGroups("payouts", "confirmed")},
	{"payouts.created", itSum("payouts", "created_at", nil)},
	{"payouts.paid", itSum("payouts", "paid_at", nil)},
	{"payouts.rows", itDigest("payouts", "miner_address", "block_height", "amount", "status", "txid", "confirmed")},
	{"payouts.miners", itGroups("payouts", "miner_address")},
	{"payouts.max_id", itMax("payouts", "id")},
	{"blocks_1175.status", itGroups("blocks_1175", "status", "distributed")},
	{"blocks_1175.solo", itGroups("blocks_1175", "is_solo")},
	{"blocks_1175.rows", itDigest("blocks_1175", "height", "hash", "finder")},
	{"blocks_1175.created", itSum("blocks_1175", "created_at", nil)},
	{"payouts_1175.status", itGroups("payouts_1175", "status", "txid", "batch")},
	{"payouts_1175.times", itDigest("payouts_1175", "miner_address", "block_height", "paid_at", "created_at")},
	{"payouts_1175.max_id", itMax("payouts_1175", "id")},
}

// TestITFigures writes the figures, one "name=value" line each in name order, to IT_OUT: from the
// PostgreSQL database at IT_PG, or from the forgesolo.db at IT_DB.
func TestITFigures(t *testing.T) {
	if len(itFigures) != 54 {
		t.Fatalf("IT-FIGURES-54: %d figures", len(itFigures))
	}
	var d map[string][]itRow
	if dsn := os.Getenv("IT_PG"); dsn != "" {
		d = itRows(t, itOpenPostgres(t, dsn), true)
	} else {
		d = itRows(t, itOpenSQLite(t, itEnv(t, "IT_DB")), false)
	}
	var lines []string
	for _, f := range itFigures {
		lines = append(lines, f.name+"="+f.of(d))
	}
	sort.Strings(lines)
	if err := os.WriteFile(itEnv(t, "IT_OUT"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// itKeys says how each table's rows are matched across the two databases.
var itKeys = map[string][]string{
	"pool_config": {"id"}, "datum_identity": {"id"}, "miners": {"address"}, "blocks": {"height"},
	"payouts": {"miner_address", "block_height"}, "blocks_1175": {"height"}, "payouts_1175": {"miner_address", "block_height"},
}

// TestITPostgresRows writes every row of the seven tables at IT_PG, as to_json gives it, to IT_OUT.
func TestITPostgresRows(t *testing.T) {
	db := itOpenPostgres(t, itEnv(t, "IT_PG"))
	out := map[string][]json.RawMessage{}
	for _, tb := range itTables {
		var there bool
		if err := db.QueryRow(`SELECT to_regclass($1) IS NOT NULL`, "public."+tb.name).Scan(&there); err != nil {
			t.Fatal(err)
		}
		if !there {
			continue
		}
		rows, err := db.Query(`SELECT to_json(t)::text FROM ` + pq.QuoteIdentifier(tb.name) + ` t`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out[tb.name] = append(out[tb.name], json.RawMessage(s))
		}
		rows.Close()
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(itEnv(t, "IT_OUT"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// What the comparison expects of a column a PostgreSQL row does not have, and the PostgreSQL
// columns the move leaves behind.
var (
	itDefaults = map[string]any{"payouts.status": "pending", "pool_config.payout_mode": "solo",
		"blocks.is_solo": float64(0), "blocks_1175.is_solo": float64(0), "blocks_1175.distributed": float64(0)}
	itDropped = map[string]bool{"pool_config.min_payout": true, "miners.min_payout": true, "miners.balance": true,
		"miners.total_paid": true, "blocks.difficulty": true, "blocks.confirmations": true}
	itPGTime = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?([+-]\d\d(:\d\d)?|Z)$`)
)

// itExpect is what SQLite should hold for a PostgreSQL value in JSON: a bool as 1 or 0, a time as
// UTC to the nearest second, anything else as it is.
func itExpect(v any) any {
	switch x := v.(type) {
	case bool:
		if x {
			return float64(1)
		}
		return float64(0)
	case string:
		if itPGTime.MatchString(x) {
			for _, layout := range []string{"2006-01-02T15:04:05.999999999Z07:00", "2006-01-02T15:04:05.999999999Z07"} {
				if tm, err := time.Parse(layout, x); err == nil {
					return tm.UTC().Round(time.Second).Format("2006-01-02 15:04:05")
				}
			}
		}
	}
	return v
}

func itKey(table string, r map[string]any) string {
	var k []string
	for _, c := range itKeys[table] {
		k = append(k, fmt.Sprint(r[c]))
	}
	return strings.Join(k, "|")
}

// TestITCompareRows compares the rows TestITPostgresRows wrote (IT_ROWS) with forgesolo.db (IT_DB),
// read with json_object, every row and column. A table PostgreSQL did not have must be empty.
func TestITCompareRows(t *testing.T) {
	b, err := os.ReadFile(itEnv(t, "IT_ROWS"))
	if err != nil {
		t.Fatal(err)
	}
	var pg map[string][]map[string]any
	if err := json.Unmarshal(b, &pg); err != nil {
		t.Fatal(err)
	}
	db := itOpenSQLite(t, itEnv(t, "IT_DB"))
	compared, bad := 0, 0
	for _, tb := range itTables {
		rows, err := db.Query(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, tb.name)
		if err != nil {
			t.Fatal(err)
		}
		var cols, args []string
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				t.Fatal(err)
			}
			cols = append(cols, c)
			args = append(args, "'"+c+"', "+c)
		}
		rows.Close()
		lite := map[string]map[string]any{}
		r2, err := db.Query(`SELECT json_object(` + strings.Join(args, ", ") + `) FROM ` + tb.name)
		if err != nil {
			t.Fatal(err)
		}
		for r2.Next() {
			var s string
			if err := r2.Scan(&s); err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(s), &m); err != nil {
				t.Fatal(err)
			}
			lite[itKey(tb.name, m)] = m
		}
		r2.Close()
		pgRows := pg[tb.name]
		if tb.name == "pool_config" || tb.name == "datum_identity" {
			var only []map[string]any
			for _, r := range pgRows {
				if r["id"] == float64(1) {
					only = append(only, r)
				}
			}
			pgRows = only
		}
		if len(lite) != len(pgRows) {
			t.Errorf("IT-ROWS-COUNT: %s has %d rows in PostgreSQL and %d in forgesolo.db", tb.name, len(pgRows), len(lite))
			bad++
		}
		for _, p := range pgRows {
			k := itKey(tb.name, p)
			l, ok := lite[k]
			if !ok {
				t.Errorf("IT-ROWS-MISSING: %s %s is not in forgesolo.db", tb.name, k)
				bad++
				continue
			}
			for c, pv := range p {
				if itDropped[tb.name+"."+c] {
					continue
				}
				lv, ok := l[c]
				if !ok {
					t.Errorf("IT-ROWS-COLUMN: %s.%s is not in forgesolo.db", tb.name, c)
					bad++
					continue
				}
				if want := itExpect(pv); fmt.Sprintf("%T %v", want, want) != fmt.Sprintf("%T %v", lv, lv) {
					t.Errorf("IT-ROWS-VALUE: %s %s %s: PostgreSQL %v (%T), forgesolo.db %v (%T), want %v", tb.name, k, c, pv, pv, lv, lv, want)
					bad++
				}
			}
			for _, c := range cols {
				if _, ok := p[c]; ok {
					continue
				}
				if want := itDefaults[tb.name+"."+c]; fmt.Sprint(want) != fmt.Sprint(l[c]) {
					t.Errorf("IT-ROWS-DEFAULT: %s %s %s: %v, want %v for a column PostgreSQL did not have", tb.name, k, c, l[c], want)
					bad++
				}
			}
			compared++
		}
		if bad > 50 {
			t.Fatal("IT-ROWS: too many differences")
		}
	}
	if compared == 0 {
		t.Fatal("IT-ROWS-NONE: no row was compared")
	}
	t.Logf("compared %d rows", compared)
}

// TestITSnapshot is every row of the seven tables in forgesolo.db (IT_DB), one canonical line each,
// in key order. With IT_WANT it must equal that file; with IT_OUT it is written there.
func TestITSnapshot(t *testing.T) {
	db := itOpenSQLite(t, itEnv(t, "IT_DB"))
	var b strings.Builder
	for _, tb := range itTables {
		rows, err := db.Query(`SELECT * FROM ` + tb.name)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		var lines []string
		keys := map[string]string{}
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			m := map[string]any{}
			for i, c := range cols {
				if f, ok := vals[i].(float64); ok {
					m[c] = json.Number(strconv.FormatFloat(f, 'g', -1, 64))
				} else {
					m[c] = vals[i]
				}
			}
			line, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			k := itKey(tb.name, m)
			lines = append(lines, string(line))
			keys[string(line)] = k
		}
		rows.Close()
		sort.Slice(lines, func(i, j int) bool { return keys[lines[i]] < keys[lines[j]] })
		fmt.Fprintf(&b, "%s %d\n", tb.name, len(lines))
		for _, l := range lines {
			b.WriteString(l + "\n")
		}
	}
	got := b.String()
	if want := os.Getenv("IT_WANT"); want != "" {
		w, err := os.ReadFile(want)
		if err != nil {
			t.Fatal(err)
		}
		if string(w) != got {
			gl, wl := strings.Split(got, "\n"), strings.Split(string(w), "\n")
			for i := 0; i < max(len(gl), len(wl)); i++ {
				var g, x string
				if i < len(gl) {
					g = gl[i]
				}
				if i < len(wl) {
					x = wl[i]
				}
				if g != x {
					t.Fatalf("IT-SNAPSHOT-DIFFERS: %s differs from %s from line %d:\n got: %s\nwant: %s", itEnv(t, "IT_DB"), want, i+1, g, x)
				}
			}
		}
		return
	}
	if err := os.WriteFile(itEnv(t, "IT_OUT"), []byte(got), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestITQuery prints what IT_SQL answers on forgesolo.db (IT_DB), read-only: one "row: " line a
// row, its values separated by "|", NULL as "null".
func TestITQuery(t *testing.T) {
	db := itOpenSQLite(t, itEnv(t, "IT_DB"))
	rows, err := db.Query(itEnv(t, "IT_SQL"))
	if err != nil {
		t.Fatalf("IT-QUERY: %v", err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, v := range vals {
			switch x := v.(type) {
			case nil:
				out = append(out, "null")
			case []byte:
				out = append(out, string(x))
			default:
				out = append(out, fmt.Sprint(x))
			}
		}
		fmt.Println("row: " + strings.Join(out, "|"))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

// The seed's miner A, who finds the blocks.
const itMinerA = "bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2"

// TestITWrite writes in forgesolo.db (IT_DB) what Forge Solo 1.0.13 writes there, as its user:
//   - IT_ACTION=block: a solo block of miner A at IT_HEIGHT, as the stratum records one;
//   - IT_ACTION=later: the per-height cases of a later merge. 1175 block 5002, still pending and
//     undistributed in the old database, is distributed, confirmed and settled here; and BCH2
//     block 100149, pending there, is replaced at its height by another hash (IT_HASH), as the
//     stratum records a block that superseded another;
//   - IT_ACTION=hold: the database open for IT_HOLD seconds, as the api and the stratum hold it.
func TestITWrite(t *testing.T) {
	db := itEnv(t, "IT_DB")
	if err := stats.InitDB(db); err != nil {
		t.Fatalf("IT-WRITE-OPEN: %v", err)
	}
	defer stats.CloseDB()
	switch a := itEnv(t, "IT_ACTION"); a {
	case "block":
		h, err := strconv.ParseInt(itEnv(t, "IT_HEIGHT"), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		if err := stats.SaveSoloBlockCoinbaseDirect(itMinerA, h, 3.125, fmt.Sprintf("%064x", h)); err != nil {
			t.Fatalf("IT-WRITE-BLOCK: %v", err)
		}
	case "later":
		undistributed, err := stats.UndistributedBlocks1175()
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range undistributed {
			if h == 5002 {
				if err := stats.Distribute1175Block(5002, 0); err != nil {
					t.Fatalf("IT-WRITE-1175: %v", err)
				}
			}
		}
		if err := stats.Confirm1175Block(5002); err != nil {
			t.Fatalf("IT-WRITE-1175: %v", err)
		}
		if n, err := stats.Settle1175ByCoinbase(itMinerA); err != nil || n == 0 {
			t.Fatalf("IT-WRITE-1175: settled %d, %v", n, err)
		}
		if err := stats.SaveSoloBlockCoinbaseDirect(itMinerA, 100149, 3.125, itEnv(t, "IT_HASH")); err != nil {
			t.Fatalf("IT-WRITE-REORG: %v", err)
		}
	case "hold":
		s, err := strconv.Atoi(itEnv(t, "IT_HOLD"))
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Duration(s) * time.Second)
	default:
		t.Fatalf("IT-SETUP: IT_ACTION %q", a)
	}
}

// TestITControl reads pg_control files the script captured from a real cluster (IT_CONTROL_DIR:
// pg_control-shutdown, after a clean stop, and pg_control-inproduction, from a running server) and
// the committed fixtures the unit tests use: both pairs must read as a PostgreSQL 16 control file
// whose checksum holds, in the states their names say.
func TestITControl(t *testing.T) {
	read := func(path string) Control {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(t.TempDir(), "global")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "pg_control"), b, 0o600); err != nil {
			t.Fatal(err)
		}
		c, err := ReadControl(filepath.Dir(dir))
		if err != nil {
			t.Fatalf("IT-CONTROL-READ: %s: %v", path, err)
		}
		return c
	}
	for _, dir := range []string{itEnv(t, "IT_CONTROL_DIR"), "testdata"} {
		if c := read(filepath.Join(dir, "pg_control-shutdown")); c.State != StateShutDown || c.Version != ControlVersion16 {
			t.Errorf("IT-CONTROL-SHUTDOWN: %s/pg_control-shutdown has state %d, version %d", dir, c.State, c.Version)
		}
		if c := read(filepath.Join(dir, "pg_control-inproduction")); c.State != StateInProduction || c.Version != ControlVersion16 {
			t.Errorf("IT-CONTROL-INPRODUCTION: %s/pg_control-inproduction has state %d, version %d", dir, c.State, c.Version)
		}
	}
}

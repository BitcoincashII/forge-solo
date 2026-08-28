package forgesolo

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Every column a query writes must exist in that dialect's schema.
//
// The Postgres miners table never declared balance or total_paid, yet three functions ran
// UPDATE miners SET balance = ..., total_paid = ... against it. Every one of those discarded
// the error, so the write failed silently on every install and nothing ever noticed -- the
// columns existed only in the SQLite schema. Nothing read them either, so the two dialects
// disagreed for as long as both existed.
//
// A dialect may declare a column nobody writes (Postgres has several). The reverse is always
// a bug: the statement either errors at runtime or, when the error is dropped, does nothing.
//
// Scope: INSERT INTO ... (cols), UPDATE <t> SET ..., and the DO UPDATE SET of an upsert.
// Columns written through any other shape are not covered.

var (
	reCreate = regexp.MustCompile(`(?is)CREATE TABLE (?:IF NOT EXISTS )?([a-z_0-9]+)\s*\(`)
	reAdd    = regexp.MustCompile(`(?i)ALTER TABLE ([a-z_0-9]+) ADD COLUMN (?:IF NOT EXISTS )?([a-z_0-9]+)`)
	reInsert = regexp.MustCompile(`(?is)INSERT\s+(?:OR\s+IGNORE\s+)?INTO\s+([a-z_0-9]+)\s*\(([^)]*)\)`)
	reUpdate = regexp.MustCompile(`(?is)UPDATE\s+([a-z_0-9]+)\s+SET\s`)
	reUpsert = regexp.MustCompile(`(?is)DO\s+UPDATE\s+SET\s`)
	reIdent  = regexp.MustCompile(`^[a-z_][a-z_0-9]*$`)
	constrKW = map[string]bool{"primary": true, "unique": true, "foreign": true,
		"constraint": true, "check": true, "exclude": true}
)

// stripGoComments removes // lines so prose cannot be mistaken for SQL.
func stripGoComments(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "//") {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// sqlBlobs returns the backtick-quoted strings, where this tree keeps its SQL.
func sqlBlobs(src string) []string {
	var out []string
	for i := 0; i < len(src); i++ {
		if src[i] != '`' {
			continue
		}
		j := strings.IndexByte(src[i+1:], '`')
		if j < 0 {
			break
		}
		out = append(out, src[i+1:i+1+j])
		i += j + 1
	}
	return out
}

// splitTopLevel splits on commas that are not inside parentheses.
func splitTopLevel(s string) []string {
	var parts []string
	depth, start := 0, 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, s[start:])
}

// assignedColumns reads the LHS identifiers of a SET clause, stopping at a top-level WHERE.
func assignedColumns(after string) []string {
	depth := 0
	end := len(after)
	upper := strings.ToUpper(after)
	for i := 0; i < len(after); i++ {
		switch after[i] {
		case '(':
			depth++
		case ')':
			depth--
		}
		if depth == 0 && strings.HasPrefix(upper[i:], "WHERE") {
			end = i
			break
		}
	}
	var cols []string
	for _, part := range splitTopLevel(after[:end]) {
		eq := strings.IndexByte(part, '=')
		if eq < 0 {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(part[:eq]))
		if reIdent.MatchString(name) {
			cols = append(cols, name)
		}
	}
	return cols
}

// schemaOf collects table -> declared columns from CREATE TABLE and ADD COLUMN.
func schemaOf(t *testing.T, paths []string) map[string]map[string]bool {
	t.Helper()
	schema := map[string]map[string]bool{}
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		src := string(raw)
		if strings.HasSuffix(p, ".go") {
			src = stripGoComments(src)
		}
		for _, m := range reCreate.FindAllStringSubmatchIndex(src, -1) {
			table := strings.ToLower(src[m[2]:m[3]])
			body, depth := "", 1
			for i := m[1]; i < len(src) && depth > 0; i++ {
				switch src[i] {
				case '(':
					depth++
				case ')':
					depth--
				}
				if depth > 0 {
					body += string(src[i])
				}
			}
			if schema[table] == nil {
				schema[table] = map[string]bool{}
			}
			for _, line := range strings.Split(body, "\n") {
				line = strings.TrimSpace(strings.SplitN(line, "--", 2)[0])
				if line == "" {
					continue
				}
				name := strings.ToLower(strings.Fields(line)[0])
				if constrKW[name] || !reIdent.MatchString(name) {
					continue
				}
				schema[table][name] = true
			}
		}
		for _, m := range reAdd.FindAllStringSubmatch(src, -1) {
			table := strings.ToLower(m[1])
			if schema[table] == nil {
				schema[table] = map[string]bool{}
			}
			schema[table][strings.ToLower(m[2])] = true
		}
	}
	return schema
}

func TestEveryWrittenColumnExistsInItsDialectSchema(t *testing.T) {
	dialects := map[string]struct{ schema, queries []string }{
		"postgres": {
			schema:  []string{"internal/stats/db.go", "internal/stats/dialect.go", "init-db.sql"},
			queries: []string{"internal/stats/db.go", "internal/stats/dialect.go", "internal/stats/payout1175.go"},
		},
		"sqlite": {
			schema:  []string{"internal/stats/db_sqlite.go", "internal/stats/dialect_sqlite.go"},
			queries: []string{"internal/stats/db_sqlite.go", "internal/stats/dialect_sqlite.go", "internal/stats/payout1175.go"},
		},
	}

	for name, d := range dialects {
		schema := schemaOf(t, d.schema)
		if len(schema) == 0 {
			t.Fatalf("%s: parsed no tables; the guard would pass vacuously", name)
		}
		checked := 0
		for _, p := range d.queries {
			raw, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("read %s: %v", p, err)
			}
			for _, blob := range sqlBlobs(stripGoComments(string(raw))) {
				report := func(table, col string) {
					cols, known := schema[strings.ToLower(table)]
					if !known {
						return // table defined elsewhere (e.g. a hypertable helper)
					}
					checked++
					if !cols[col] {
						t.Errorf("%s/%s writes %s.%s, which the %s schema does not declare",
							name, p, table, col, name)
					}
				}
				for _, m := range reInsert.FindAllStringSubmatch(blob, -1) {
					for _, c := range strings.Split(m[2], ",") {
						c = strings.ToLower(strings.TrimSpace(c))
						if reIdent.MatchString(c) {
							report(m[1], c)
						}
					}
				}
				for _, loc := range reUpdate.FindAllStringSubmatchIndex(blob, -1) {
					table := blob[loc[2]:loc[3]]
					for _, c := range assignedColumns(blob[loc[1]:]) {
						report(table, c)
					}
				}
				for _, m := range reInsert.FindAllStringSubmatchIndex(blob, -1) {
					rest := blob[m[1]:]
					if u := reUpsert.FindStringIndex(rest); u != nil {
						for _, c := range assignedColumns(rest[u[1]:]) {
							report(blob[m[2]:m[3]], c)
						}
					}
				}
			}
		}
		if checked == 0 {
			t.Errorf("%s: no column writes were checked; the guard is vacuous", name)
		} else {
			t.Logf("%s: %d column writes checked against %d tables", name, checked, len(schema))
		}
	}
}

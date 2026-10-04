//go:build sqlite

package pgmigrate

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The spec must follow both schemas. Without these, a column or table a later change adds is
// silently left empty in the moved database, or dropped by a merge, and nothing says so.

// (a) Every column the app creates in a copied table is carried, or listed with a reason.
func TestEveryAppColumnIsCarried(t *testing.T) {
	db, _ := freshDB(t)
	ctx := context.Background()
	for _, tb := range Tables {
		cols, err := tableColumns(ctx, db, tb.Name)
		if err != nil {
			t.Fatal(err)
		}
		if len(cols) == 0 {
			t.Errorf("MIG-DRIFT-APP: the app no longer creates %s, which the spec copies", tb.Name)
			continue
		}
		have := map[string]bool{}
		for _, c := range cols {
			have[c.name] = true
		}
		mapped := map[string]bool{}
		for _, c := range tb.Columns {
			mapped[c.SQLite] = true
			if !have[c.SQLite] {
				t.Errorf("MIG-DRIFT-APP: the spec writes %s.%s, which the app does not create", tb.Name, c.SQLite)
			}
		}
		for _, c := range cols {
			if !mapped[c.name] && NotFromPostgres[tb.Name+"."+c.name] == "" {
				t.Errorf("MIG-DRIFT-APP: the app creates %s.%s, which the move does not carry: map it in spec.go, or list it in NotFromPostgres with the reason",
					tb.Name, c.name)
			}
		}
	}
}

// (b) Every column of 1.0.12's PostgreSQL schema is carried, or left behind for a reason, and every
// table is copied or left behind for a reason.
func TestEveryOldColumnHasARule(t *testing.T) {
	schema := pgSchemaColumns(t, frozenStatements(t))
	for name, cols := range schema {
		tb, ok := table(name)
		if !ok {
			if NotCopied[name] == "" {
				t.Errorf("MIG-DRIFT-PG: PostgreSQL table %s is neither copied nor left behind with a reason", name)
			}
			continue
		}
		mapped := map[string]bool{}
		for _, c := range tb.Columns {
			mapped[c.PG] = true
		}
		for c := range cols {
			if !mapped[c] && tb.Dropped[c] == "" {
				t.Errorf("MIG-DRIFT-PG: PostgreSQL column %s.%s is neither carried nor left behind with a reason", name, c)
			}
		}
	}
	for _, tb := range Tables {
		if schema[tb.Name] == nil {
			t.Errorf("MIG-DRIFT-PG: the spec copies %s, which 1.0.12's PostgreSQL schema does not have", tb.Name)
		}
	}
	// The columns 1.0.12 dropped are still in databases it never upgraded.
	for _, st := range frozenStatements(t) {
		if m := dropColumn.FindStringSubmatch(st); m != nil {
			if tb, _ := table(m[1]); tb.Dropped[m[2]] == "" {
				t.Errorf("MIG-DRIFT-PG: %s.%s, which older databases still have, is not left behind with a reason", m[1], m[2])
			}
		}
	}
}

// (c) The frozen schema is what the PostgreSQL build makes, statement for statement.
func TestFrozenSchemaIsThePostgresBuilds(t *testing.T) {
	var built []string
	for _, src := range []struct{ file, fn string }{
		{"../stats/db.go", "initPostgresSchema"},
		{"../stats/dialect.go", "init1175Schema"},
	} {
		f, err := parser.ParseFile(token.NewFileSet(), src.file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.GenDecl:
				for _, s := range d.Specs {
					if vs, ok := s.(*ast.ValueSpec); ok && len(vs.Names) == 1 && vs.Names[0].Name == "corePostgresSchema" {
						built = append(built, splitSQL(unquote(t, vs.Values[0]))...)
					}
				}
			case *ast.FuncDecl:
				if d.Name.Name != src.fn {
					continue
				}
				ast.Inspect(d.Body, func(n ast.Node) bool {
					if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if s := unquote(t, lit); isDDL(s) {
							built = append(built, normalizeSQL(s))
						}
					}
					return true
				})
			}
		}
	}
	frozen := frozenStatements(t)
	if strings.Join(built, "\n") != strings.Join(frozen, "\n") {
		t.Errorf("MIG-DRIFT-FROZEN: the PostgreSQL build's schema is no longer testdata/pg-1.0.12-schema.sql. A schema change must "+
			"also change what the move carries. Built:\n%s\n\nFrozen:\n%s", strings.Join(built, "\n"), strings.Join(frozen, "\n"))
	}
}

// (d) Every table the app creates has a rule: a table added later is not dropped by a merge
// without anyone deciding so.
func TestEveryTableHasARule(t *testing.T) {
	db, _ := freshDB(t)
	schema, err := ReadSchema(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	for name := range schema {
		if TableRules[name] == "" {
			t.Errorf("MIG-DRIFT-TABLE: the app creates table %s, which has no rule in TableRules", name)
		}
	}
	for _, tb := range Tables {
		if TableRules[tb.Name] == "" {
			t.Errorf("MIG-DRIFT-TABLE: copied table %s has no rule in TableRules", tb.Name)
		}
	}
}

// (e) A database a newer Forge Solo wrote is recognised, by an extra table or an extra column.
func TestSchemaNewerThanMine(t *testing.T) {
	ctx := context.Background()
	mine, _ := freshDB(t)
	s, _ := freshDB(t)
	if got, err := SchemaNewerThanMine(ctx, s, mine); err != nil || len(got) != 0 {
		t.Fatalf("MIG-NEWER-NONE: a fresh database reads as newer: %v %v", got, err)
	}
	for _, q := range []string{
		`CREATE TABLE migration_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`ANALYZE`,
	} {
		if _, err := s.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := SchemaNewerThanMine(ctx, s, mine); len(got) != 0 {
		t.Fatalf("MIG-NEWER-OWN: the migrator's own table or SQLite's read as newer: %v", got)
	}
	if _, err := s.Exec(`ALTER TABLE blocks ADD COLUMN found_at DATETIME`); err != nil {
		t.Fatal(err)
	}
	got, _ := SchemaNewerThanMine(ctx, s, mine)
	if strings.Join(got, ",") != "column blocks.found_at" {
		t.Fatalf("MIG-NEWER-COLUMN: an extra column gave %v, want [column blocks.found_at]", got)
	}
	if _, err := s.Exec(`CREATE TABLE tides_rounds (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	got, _ = SchemaNewerThanMine(ctx, s, mine)
	if strings.Join(got, ",") != "column blocks.found_at,table tides_rounds" {
		t.Fatalf("MIG-NEWER-TABLE: an extra table gave %v, want [column blocks.found_at table tides_rounds]", got)
	}
}

var (
	createTable = regexp.MustCompile(`(?is)^CREATE TABLE IF NOT EXISTS (\w+) \((.*)\)$`)
	addColumn   = regexp.MustCompile(`(?i)^ALTER TABLE (\w+) ADD COLUMN IF NOT EXISTS (\w+) `)
	dropColumn  = regexp.MustCompile(`(?i)^ALTER TABLE (\w+) DROP COLUMN IF EXISTS (\w+)$`)
	leadingWord = regexp.MustCompile(`^\w+`)
)

// pgSchemaColumns is each table's columns after the statements have run.
func pgSchemaColumns(t *testing.T, stmts []string) map[string]map[string]bool {
	t.Helper()
	out := map[string]map[string]bool{}
	for _, st := range stmts {
		if m := createTable.FindStringSubmatch(st); m != nil {
			cols := map[string]bool{}
			for _, part := range splitTopLevel(m[2]) {
				name := leadingWord.FindString(strings.TrimSpace(part))
				switch strings.ToUpper(name) {
				case "", "PRIMARY", "UNIQUE", "CHECK", "CONSTRAINT", "FOREIGN":
					continue
				}
				cols[name] = true
			}
			out[m[1]] = cols
		} else if m := addColumn.FindStringSubmatch(st); m != nil {
			out[m[1]][m[2]] = true
		} else if m := dropColumn.FindStringSubmatch(st); m != nil {
			delete(out[m[1]], m[2])
		}
	}
	return out
}

// splitTopLevel splits a column list at the commas outside parentheses.
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

func frozenStatements(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile("testdata/pg-1.0.12-schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	return splitSQL(string(b))
}

// splitSQL splits a script into its statements, normalised. A ';' inside a $$ block or a quoted
// string does not end a statement.
func splitSQL(script string) []string {
	var out []string
	var cur strings.Builder
	inDollar, inQuote := false, false
	lines := strings.Split(script, "\n")
	for _, line := range lines {
		if !inDollar && !inQuote {
			if i := strings.Index(line, "--"); i >= 0 {
				line = line[:i]
			}
		}
		for i := 0; i < len(line); i++ {
			switch {
			case !inQuote && strings.HasPrefix(line[i:], "$$"):
				inDollar = !inDollar
				cur.WriteString("$$")
				i++
				continue
			case !inDollar && line[i] == '\'':
				inQuote = !inQuote
			case !inDollar && !inQuote && line[i] == ';':
				if s := normalizeSQL(cur.String()); s != "" {
					out = append(out, s)
				}
				cur.Reset()
				continue
			}
			cur.WriteByte(line[i])
		}
		cur.WriteByte('\n')
	}
	if s := normalizeSQL(cur.String()); s != "" {
		out = append(out, s)
	}
	return out
}

func normalizeSQL(s string) string {
	return strings.TrimSuffix(strings.Join(strings.Fields(s), " "), ";")
}

func isDDL(s string) bool {
	u := strings.ToUpper(strings.TrimSpace(s))
	for _, p := range []string{"CREATE ", "ALTER ", "DROP ", "DO "} {
		if strings.HasPrefix(u, p) {
			return true
		}
	}
	return false
}

func unquote(t *testing.T, e ast.Expr) string {
	t.Helper()
	lit, ok := e.(*ast.BasicLit)
	if !ok {
		t.Fatalf("not a string literal: %T", e)
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

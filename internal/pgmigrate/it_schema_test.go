//go:build it

package pgmigrate

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The frozen schema, testdata/pg-1.0.12-schema.sql, is what 1.0.12's own InitDB ran, statement for
// statement: corePostgresSchema, the migrations after it, then Init1175Schema's ledger, read from
// the sources of the v1.0.12 tag at IT_V1012 (scripts/it-pg-to-sqlite.sh checks them out). The
// spec follows that file (drift_test.go), so this is what keeps the move 1.0.12's.
func TestITFrozenSchemaIs1012s(t *testing.T) {
	stats := filepath.Join(itEnv(t, "IT_V1012"), "internal", "stats")
	var built []string
	for _, src := range []struct{ file, fn string }{
		{"db.go", "InitDB"},
		{"dialect.go", "Init1175Schema"},
	} {
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(stats, src.file), nil, 0)
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
		t.Errorf("IT-FROZEN-SCHEMA: testdata/pg-1.0.12-schema.sql is not what 1.0.12's InitDB ran. 1.0.12's:\n%s\n\nFrozen:\n%s",
			strings.Join(built, "\n"), strings.Join(frozen, "\n"))
	}
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

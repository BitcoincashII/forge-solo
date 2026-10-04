package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// The dashboard shows the API's messages as they are (the save confirmation after every save,
// the save errors), and its own text uses plain punctuation: no em-dash in any of the API's
// strings either.
func TestAPIStringsHaveNoEmDash(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		ast.Inspect(f, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING && strings.ContainsRune(lit.Value, '\u2014') {
				t.Errorf("EMDASH: %s: %s", fset.Position(lit.Pos()), lit.Value)
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("EMDASH: no source files were checked")
	}
}

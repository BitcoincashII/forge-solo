package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// The tests beside this one build their own app, so they would all still pass if main() stopped
// installing the gate. main() must install the one built from the environment, and before the
// first route: fiber runs handlers in the order they were added, so a route added before the gate
// would never reach it.
func TestMainInstallsSettingsPasswordGateBeforeRoutes(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var gate, firstRoute token.Pos
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "main" {
			return true
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "app" {
				return true
			}
			switch sel.Sel.Name {
			case "Use":
				if len(call.Args) == 1 {
					if inner, ok := call.Args[0].(*ast.CallExpr); ok {
						if id, ok := inner.Fun.(*ast.Ident); ok && id.Name == "settingsPasswordGateFromEnv" && len(inner.Args) == 0 && gate == token.NoPos {
							gate = call.Pos()
						}
					}
				}
			case "Get", "Post", "Put", "Patch", "Delete", "All", "Group", "Static":
				if firstRoute == token.NoPos {
					firstRoute = call.Pos()
				}
			}
			return true
		})
		return false
	})
	if gate == token.NoPos {
		t.Fatal("PW-WIRING-MISSING: main() never calls app.Use(settingsPasswordGateFromEnv()): on Umbrel any app could change the settings")
	}
	if firstRoute != token.NoPos && firstRoute < gate {
		t.Fatalf("PW-WIRING-ORDER: a route is added at %s, before the gate at %s", fset.Position(firstRoute), fset.Position(gate))
	}
}

// The Settings page must send the password in the header the gate reads.
func TestSettingsPageSendsThePasswordHeader(t *testing.T) {
	b, err := os.ReadFile("../../web/dist/settings.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(b)
	if !strings.Contains(page, "headers['"+settingsPasswordHeader+"']=pw") {
		t.Errorf("PW-PAGE-HEADER: settings.html does not send the password as %s", settingsPasswordHeader)
	}
	for _, want := range []string{"d.password_required === true", "else if(d.password_required)", "d.password_wrong"} {
		if !strings.Contains(page, want) {
			t.Errorf("PW-PAGE-FLOW: settings.html no longer has %q", want)
		}
	}
}

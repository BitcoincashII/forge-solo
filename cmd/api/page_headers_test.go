package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// The dashboard's pages, as the API serves them on its own port, may not be framed by another
// page. Its /api/ answers carry none of these headers: the servers in front of it set them, and a
// second X-Frame-Options is one a browser may ignore.
func TestPageSecurityHeaders(t *testing.T) {
	app := fiber.New()
	app.Use(pageSecurityHeaders)
	app.Get("/settings", func(c *fiber.Ctx) error { return c.SendString("<html>") })
	app.Get("/api/v1/stats", func(c *fiber.Ctx) error { return c.JSON(fiber.Map{}) })

	resp, err := app.Test(httptest.NewRequest("GET", "/settings", nil))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"X-Frame-Options": "DENY", "Content-Security-Policy": "frame-ancestors 'none'",
		"X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer"} {
		if got := resp.Header.Values(k); len(got) != 1 || got[0] != v {
			t.Errorf("API-PAGE-NOT-FRAMED: /settings has %s %q, want %q", k, got, v)
		}
	}
	resp, err = app.Test(httptest.NewRequest("GET", "/api/v1/stats", nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Header.Values("X-Frame-Options"); len(got) != 0 {
		t.Errorf("API-JSON-NO-DUP: /api/ answers carry X-Frame-Options %q, which the servers in front add again", got)
	}
}

// main() must use the headers before its first route: fiber runs handlers in the order they were
// added, and the test above builds its own app.
func TestPageSecurityHeadersWired(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var used, firstRoute token.Pos
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "main" {
			continue
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
				if len(call.Args) != 1 {
					break
				}
				if id, ok := call.Args[0].(*ast.Ident); ok && id.Name == "pageSecurityHeaders" && used == token.NoPos {
					used = call.Pos()
				}
			case "Get", "Post", "Put", "Patch", "Delete", "All", "Group", "Static":
				if firstRoute == token.NoPos {
					firstRoute = call.Pos()
				}
			}
			return true
		})
	}
	if used == token.NoPos || (firstRoute != token.NoPos && firstRoute < used) {
		t.Fatal("API-HEADERS-WIRED: main() does not use pageSecurityHeaders before its first route")
	}
}

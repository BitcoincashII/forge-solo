package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// The dashboard's pages, as the API serves them on its own port, may not be framed by another
// page. Its /api/ answers carry none of these headers: the servers in front of it set them, and a
// second X-Frame-Options is one a browser may ignore. No answer grants another origin a cross-origin
// read: the dashboard is served from the API's own origin and needs none, and the old default let
// any page on localhost:3000 read the API.
func TestPageSecurityHeaders(t *testing.T) {
	app := fiber.New()
	app.Use(pageSecurityHeaders)
	app.Get("/settings", func(c *fiber.Ctx) error { return c.SendString("<html>") })
	app.Get("/api/v1/stats", func(c *fiber.Ctx) error { return c.JSON(fiber.Map{}) })

	req := httptest.NewRequest("GET", "/settings", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"X-Frame-Options": "DENY", "Content-Security-Policy": "frame-ancestors 'none'",
		"X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer"} {
		if got := resp.Header.Values(k); len(got) != 1 || got[0] != v {
			t.Errorf("API-PAGE-NOT-FRAMED: /settings has %s %q, want %q", k, got, v)
		}
	}
	if got := resp.Header.Values("Access-Control-Allow-Origin"); len(got) != 0 {
		t.Errorf("API-NO-CORS: /settings grants a cross-origin read: Access-Control-Allow-Origin %q", got)
	}
	req = httptest.NewRequest("GET", "/api/v1/stats", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	resp, err = app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Header.Values("X-Frame-Options"); len(got) != 0 {
		t.Errorf("API-JSON-NO-DUP: /api/ answers carry X-Frame-Options %q, which the servers in front add again", got)
	}
	if got := resp.Header.Values("Access-Control-Allow-Origin"); len(got) != 0 {
		t.Errorf("API-NO-CORS: /api/v1/stats grants a cross-origin read: Access-Control-Allow-Origin %q", got)
	}
}

// main() must use the headers before its first route: fiber runs handlers in the order they were
// added, and the test above builds its own app. And nothing in the api can grant a cross-origin
// read: no file imports a CORS middleware or sets the header itself.
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

	paths, err := filepath.Glob("*.go")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no sources: %v", err)
	}
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), "/middleware/cors") || strings.Contains(string(src), "Access-Control-Allow-Origin") {
			t.Errorf("API-NO-CORS: %s can grant a cross-origin read; the dashboard needs none", p)
		}
	}
}

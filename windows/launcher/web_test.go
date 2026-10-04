package main

import (
	"go/ast"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A dashboard call reaches the API addressed to the API itself, and the API's rate limit is told
// the address the call came from, not an X-Real-IP or X-Forwarded-For the client sent.
func TestAPIProxySetsTheClientAddress(t *testing.T) {
	var host, realIP, xff string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, realIP, xff = r.Host, r.Header.Get("X-Real-IP"), strings.Join(r.Header.Values("X-Forwarded-For"), ",")
		_, _ = io.WriteString(w, "api:"+r.URL.Path)
	}))
	defer api.Close()
	apiAddr := strings.TrimPrefix(api.URL, "http://")

	r := httptest.NewRequest("GET", "/api/v1/stats", nil)
	r.Host = "localhost:3080"
	r.RemoteAddr = "127.0.0.1:51515"
	r.Header.Set("X-Real-IP", "10.9.9.9")
	r.Header.Set("X-Forwarded-For", "10.8.8.8")
	w := httptest.NewRecorder()
	dashboardHandler(t.TempDir(), apiAddr).ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != "api:/api/v1/stats" {
		t.Fatalf("the call got %d %q", w.Code, w.Body.String())
	}
	if host != apiAddr {
		t.Errorf("WEB-API-HOST: the API was addressed as %q, want %q", host, apiAddr)
	}
	if realIP != "127.0.0.1" {
		t.Errorf("WEB-REAL-IP: the API was told the client is %q, want 127.0.0.1 (the client sent 10.9.9.9)", realIP)
	}
	if xff != "127.0.0.1" {
		t.Errorf("WEB-XFF: X-Forwarded-For %q, want 127.0.0.1 alone (the client sent 10.8.8.8)", xff)
	}
}

// Every answer the dashboard gives carries the headers the Umbrel app's nginx sends: pages, files,
// API calls, redirects and refusals alike.
func TestDashboardSecurityHeaders(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "solo.html"), []byte("page"), 0o644); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "{}") }))
	defer api.Close()
	h := dashboardHandler(root, strings.TrimPrefix(api.URL, "http://"))
	for _, tc := range []struct{ path, host string }{
		{"/solo", "127.0.0.1:3080"}, {"/", "127.0.0.1:3080"}, {"/api/v1/stats", "localhost:3080"},
		{"/missing.js", "127.0.0.1:3080"}, {"/solo", "rebind.example:3080"},
	} {
		r := httptest.NewRequest("GET", tc.path, nil)
		r.Host = tc.host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		for k, v := range map[string]string{"X-Frame-Options": "DENY", "Content-Security-Policy": "frame-ancestors 'none'",
			"X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer"} {
			if got := w.Header().Get(k); got != v {
				t.Errorf("HDR-WIN: %s (Host %s, status %d): %s = %q, want %q", tc.path, tc.host, w.Code, k, got, v)
			}
		}
	}
}

// The dashboard routes paths as on Linux and Umbrel: /solo/<address> is the solo page, which reads
// the address from its path, and a folder is never listed.
func TestDashboardRoutesLikeLinux(t *testing.T) {
	root := t.TempDir()
	for f, body := range map[string]string{"solo.html": "solo page", "js/app.js": "js", "css/app.css": "css"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, f), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := dashboardHandler(root, "127.0.0.1:1")
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Host = "127.0.0.1:3080"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/solo/bitcoincashii:qtestaddr", "/solo/bitcoincashii%3Aqtestaddr", "/solo/"} {
		if w := get(path); w.Code != 200 || w.Body.String() != "solo page" || w.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("WEB-SOLO-ADDRESS: %s: status %d, Cache-Control %q, body %.40q; want the solo page, not cached", path, w.Code, w.Header().Get("Cache-Control"), w.Body.String())
		}
	}
	for _, path := range []string{"/js/", "/css/"} {
		if w := get(path); w.Code != http.StatusNotFound {
			t.Errorf("WEB-NO-LISTING: %s: status %d, body %.60q; want 404, no folder listing", path, w.Code, w.Body.String())
		}
	}
	if w := get("/js/app.js"); w.Code != 200 || w.Body.String() != "js" {
		t.Errorf("WEB-FILES: /js/app.js: status %d", w.Code)
	}
}

// Forge Solo opens the dashboard at /solo?v=<version>, an address 1.0.12, which sent its pages with
// no Cache-Control, never served: a browser showed 1.0.12's /solo for hours after the update, with
// no notice of a move that failed. The / redirect keeps the query. That page tells the browser to
// drop what it kept of the dashboard, so the pages its links lead to, and the scripts, come fresh
// too; a page asked for any other way does not.
func TestTheDashboardAsForgeSoloOpensIt(t *testing.T) {
	saved := version
	version = "1.0.13"
	t.Cleanup(func() { version = saved })
	root := t.TempDir()
	for _, f := range []string{"solo.html", "settings.html", "tides.html"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := dashboardHandler(root, "127.0.0.1:1")
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Host = "127.0.0.1:3080"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	u, err := url.Parse(dashboardURL())
	if err != nil || u.Host != "127.0.0.1:"+webPort || u.Path != "/solo" || u.Query().Get("v") != "1.0.13" {
		t.Fatalf("DASH-URL-VERSION: Forge Solo opens the dashboard at %q (%v)", dashboardURL(), err)
	}
	for path, want := range map[string]string{"/?v=1.0.13": "/solo?v=1.0.13", "/index.html?v=1.0.13": "/solo?v=1.0.13", "/": "/solo"} {
		if w := get(path); w.Code != http.StatusFound || w.Header().Get("Location") != want {
			t.Errorf("WEB-REDIRECT-KEEPS-VERSION: %s goes to %q (status %d), want %q", path, w.Header().Get("Location"), w.Code, want)
		}
	}
	for _, path := range []string{u.RequestURI(), "/settings?v=1.0.13", "/tides?v=1.0.13"} {
		w := get(path)
		if w.Code != 200 || w.Header().Get("Clear-Site-Data") != `"cache"` || w.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("WEB-OPENED-FRESH: %s: status %d, Clear-Site-Data %q, Cache-Control %q; want the browser to drop what it kept",
				path, w.Code, w.Header().Get("Clear-Site-Data"), w.Header().Get("Cache-Control"))
		}
	}
	for _, path := range []string{"/solo", "/solo?v=1.0.12", "/solo?v=", "/settings", "/solo/bitcoincashii:qtestaddr"} {
		if w := get(path); w.Header().Get("Clear-Site-Data") != "" {
			t.Errorf("WEB-CLEARS-ONLY-OPENED: %s has the browser drop what it kept", path)
		}
	}
	version = "" // a build without its version
	if w := get("/solo"); w.Header().Get("Clear-Site-Data") != "" {
		t.Error("WEB-CLEARS-NO-VERSION: with no version, every page has the browser drop what it kept")
	}
}

// Every place Forge Solo opens the dashboard (a second start, the tray's Open Dashboard, the open
// after the start and Try Again's) opens it at dashboardURL.
func TestEveryOpenIsByVersion(t *testing.T) {
	fset, files := launcherCode(t)
	sites := 0
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "openBrowser" {
				return true
			}
			sites++
			byVersion := false
			if arg, isCall := call.Args[0].(*ast.CallExpr); isCall {
				id, isID := arg.Fun.(*ast.Ident)
				byVersion = isID && id.Name == "dashboardURL" && len(arg.Args) == 0
			}
			if !byVersion {
				at := fset.Position(call.Pos())
				t.Errorf("DASH-OPEN-SITE: %s:%d opens the browser at another address than dashboardURL()", at.Filename, at.Line)
			}
			return true
		})
	}
	if sites < 3 {
		t.Fatalf("setup: only %d places open the browser", sites)
	}
}

// The pages are checked with the server each time they are shown: a browser keeping an old page
// across an update kept the Settings page from before the password, where no save could succeed.
func TestDashboardPagesAreNotCached(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"solo.html", "settings.html", "tides.html", "other.html", "app.js"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := dashboardHandler(root, "127.0.0.1:1")
	for path, want := range map[string]string{"/solo": "no-cache", "/settings": "no-cache", "/tides": "no-cache", "/other.html": "no-cache", "/app.js": ""} {
		r := httptest.NewRequest("GET", path, nil)
		r.Host = "127.0.0.1:3080"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if got := w.Header().Get("Cache-Control"); w.Code != 200 || got != want {
			t.Errorf("WEB-NO-CACHE: %s: status %d, Cache-Control %q, want %q", path, w.Code, got, want)
		}
	}
}

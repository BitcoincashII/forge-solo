package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func webFixture(t *testing.T) (root string, api *httptest.Server, seen *http.Header) {
	t.Helper()
	root = t.TempDir()
	for _, f := range []string{"solo.html", "settings.html", "tides.html", "js/app.js"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, f), []byte("file:"+f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	seen = &http.Header{}
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = r.Header.Clone()
		_, _ = io.WriteString(w, "api:"+r.URL.Path)
	}))
	t.Cleanup(api.Close)
	return root, api, seen
}

func get(t *testing.T, h http.Handler, path string, auth ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	if len(auth) == 2 {
		r.SetBasicAuth(auth[0], auth[1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestDashboardRoutes(t *testing.T) {
	root, api, _ := webFixture(t)
	h := dashboardHandler(root, strings.TrimPrefix(api.URL, "http://"), "")
	for _, c := range []struct{ path, body, cache string }{
		{"/solo", "file:solo.html", "no-cache"},
		{"/solo/anything", "file:solo.html", "no-cache"},
		{"/settings", "file:settings.html", "no-cache"},
		{"/tides", "file:tides.html", "no-cache"},
		{"/tides.html", "file:tides.html", "no-cache"},
		{"/js/app.js", "file:js/app.js", ""},
		{"/api/v1/pool/config", "api:/api/v1/pool/config", ""},
	} {
		w := get(t, h, c.path)
		if w.Code != 200 || w.Body.String() != c.body || w.Header().Get("Cache-Control") != c.cache {
			t.Errorf("%s: %d %q cache %q, want %q cache %q", c.path, w.Code, w.Body.String(), w.Header().Get("Cache-Control"), c.body, c.cache)
		}
	}
	for _, p := range []string{"/", "/index.html"} {
		if w := get(t, h, p); w.Code != 302 || w.Header().Get("Location") != "/solo" {
			t.Errorf("%s: %d to %q, want 302 to /solo", p, w.Code, w.Header().Get("Location"))
		}
	}
	if w := get(t, h, "/js/"); w.Code != 404 {
		t.Errorf("/js/: %d, want 404 (no listings)", w.Code)
	}
}

func TestDashboardPassword(t *testing.T) {
	root, api, seen := webFixture(t)
	h := dashboardHandler(root, strings.TrimPrefix(api.URL, "http://"), "s3cret")
	for _, p := range []string{"/solo", "/api/v1/pool/config", "/"} {
		if w := get(t, h, p); w.Code != 401 || !strings.Contains(w.Header().Get("WWW-Authenticate"), "Basic") {
			t.Errorf("%s without the password: %d", p, w.Code)
		}
		if w := get(t, h, p, "forge", "wrong"); w.Code != 401 {
			t.Errorf("%s with a wrong password: %d", p, w.Code)
		}
		if w := get(t, h, p, "admin", "s3cret"); w.Code != 401 {
			t.Errorf("%s with a wrong user: %d", p, w.Code)
		}
	}
	if w := get(t, h, "/solo", "forge", "s3cret"); w.Code != 200 {
		t.Errorf("/solo with the password: %d", w.Code)
	}
	if w := get(t, h, "/api/v1/pool/config", "forge", "s3cret"); w.Code != 200 || seen.Get("Authorization") != "" {
		t.Errorf("api with the password: %d, Authorization passed on: %q", w.Code, seen.Get("Authorization"))
	}
}

func TestWebNeedsPassword(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:3080": false, "localhost:3080": false, "[::1]:3080": false, "127.0.0.2:3080": false,
		"0.0.0.0:3080": true, ":3080": true, "192.168.1.5:3080": true, "[::]:3080": true, "box.lan:3080": true, "junk": true,
	} {
		if got := webNeedsPassword(addr); got != want {
			t.Errorf("%s: %v, want %v", addr, got, want)
		}
	}
}

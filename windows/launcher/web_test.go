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

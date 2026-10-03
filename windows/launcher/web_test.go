package main

import (
	"io"
	"net/http"
	"net/http/httptest"
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
	onlyLocalHost(dashboardMux(t.TempDir(), apiAddr)).ServeHTTP(w, r)
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

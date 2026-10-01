package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// With no password the dashboard listens on this machine only and answers only to its own names;
// a DNS-rebinding page sends its own. With a password the password protects it, wherever it listens.
func TestDashboardAnswersOnlyLocalHostsWithoutAPassword(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		password, host string
		want           int
	}{
		{"", "127.0.0.1:3080", http.StatusFound},
		{"", "localhost:3080", http.StatusFound},
		{"", "[::1]:3080", http.StatusFound},
		{"", "rebind.attacker.example:3080", http.StatusMisdirectedRequest},
		{"secret", "192.168.1.5:3080", http.StatusUnauthorized},
	} {
		h := dashboardHandler(root, "127.0.0.1:1", tc.password)
		req := httptest.NewRequest("GET", "/", nil)
		req.Host = tc.host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("password %q, Host %s: status %d, want %d", tc.password, tc.host, rec.Code, tc.want)
		}
	}
}

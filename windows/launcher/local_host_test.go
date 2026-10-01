package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The dashboard answers only to this machine's own names; a DNS-rebinding page sends its own.
func TestDashboardAnswersOnlyLocalHosts(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	for host, want := range map[string]int{"127.0.0.1:3080": 200, "localhost:3080": 200, "[::1]:3080": 200,
		"rebind.attacker.example:3080": 421, "192.168.1.5:3080": 421} {
		req := httptest.NewRequest("GET", "/api/v1/pool/config", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		onlyLocalHost(ok).ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("Host %s: status %d, want %d", host, rec.Code, want)
		}
	}
}

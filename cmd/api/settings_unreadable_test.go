package main

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/stats"
	"github.com/gofiber/fiber/v2"
)

// When the stored settings cannot be read, every endpoint that needs them says so (503) instead of
// answering with defaults. With defaults, a database hiccup made Settings show "not configured",
// blank fields and Solo, and the user's next save wrote those over the real settings: the TIDES
// mode, the 1175 address and the tag were lost.
func TestSettingsUnreadableIsNotReportedAsDefaults(t *testing.T) {
	stats.CloseDB() // no database: every read fails, as during an outage
	t.Setenv("HOME_APP", "1")
	t.Setenv("POOL_ADDRESS", "bitcoincashii:qtestminer00000000000000000000000000000000") // never reached: the read fails first

	app := fiber.New()
	app.Get("/api/v1/pool/config", getPoolConfig)
	app.Post("/api/v1/pool/config", savePoolConfig)
	app.Get("/api/v1/tides/me", getTidesMine)
	for _, r := range []struct{ method, path, body string }{
		{"GET", "/api/v1/pool/config", ""},
		{"POST", "/api/v1/pool/config", `{"pool_address":"","coinbase_tag":"x"}`},
		{"GET", "/api/v1/tides/me", ""},
	} {
		req := httptest.NewRequest(r.method, r.path, strings.NewReader(r.body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != fiber.StatusServiceUnavailable || !strings.Contains(string(b), "cannot read its saved settings") {
			t.Errorf("%s %s: %d %s, want 503 saying the settings cannot be read", r.method, r.path, resp.StatusCode, b)
		}
	}
}

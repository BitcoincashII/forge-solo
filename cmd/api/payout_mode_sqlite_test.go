//go:build sqlite

package main

import (
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/stats"
	"github.com/gofiber/fiber/v2"
)

// The payout mode is saved with the rest of the settings, through the same endpoint the Settings
// page uses, and only in a form the stratum can act on.
func TestPayoutModeSettings(t *testing.T) {
	if err := stats.InitDB(filepath.Join(t.TempDir(), "api.db")); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()
	t.Setenv("HOME_APP", "1")
	t.Setenv("POOL_ADDRESS", "")

	app := fiber.New()
	app.Get("/api/v1/pool/config", getPoolConfig)
	app.Post("/api/v1/pool/config", savePoolConfig)
	post := func(body string) (int, string) {
		req := httptest.NewRequest("POST", "/api/v1/pool/config", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	mode := func() string {
		m, err := stats.GetPayoutMode()
		if err != nil {
			t.Fatal(err)
		}
		return m
	}

	if code, _ := post(`{"payout_mode":"tides"}`); code != 400 || mode() != stats.PayoutModeSolo {
		t.Fatalf("TIDES-API-NEEDS-ADDRESS: TIDES was accepted with no payout address (%d, mode %s)", code, mode())
	}
	const addr = "bitcoincashii:qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzse6qye33q"
	if code, b := post(`{"pool_address":"` + addr + `","payout_mode":"TIDES"}`); code != 200 || mode() != stats.PayoutModeTides {
		t.Fatalf("TIDES-API-SAVE: %d %s, mode %s", code, b, mode())
	}
	if code, _ := post(`{"payout_mode":"pplns"}`); code != 400 || mode() != stats.PayoutModeTides {
		t.Fatalf("TIDES-API-VALIDATE: an unknown mode was accepted or changed the mode (%d, %s)", code, mode())
	}
	// A save that does not mention the mode -- the tag alone, or a page older than TIDES --
	// leaves it alone.
	if code, _ := post(`{"coinbase_tag":"Mine"}`); code != 200 || mode() != stats.PayoutModeTides {
		t.Fatalf("TIDES-API-PARTIAL: a save without payout_mode changed the mode to %s (%d)", mode(), code)
	}

	resp, err := app.Test(httptest.NewRequest("GET", "/api/v1/pool/config", nil))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), `"payout_mode":"tides"`) || !strings.Contains(string(b), addr) {
		t.Fatalf("TIDES-API-READ: the settings read-back does not show the mode: %s", b)
	}
	if code, _ := post(`{"payout_mode":"solo"}`); code != 200 || mode() != stats.PayoutModeSolo {
		t.Fatalf("TIDES-API-BACK-TO-SOLO: %d, mode %s", code, mode())
	}
}

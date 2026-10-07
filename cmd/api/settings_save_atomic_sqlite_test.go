package main

import (
	"database/sql"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/stats"
	"github.com/gofiber/fiber/v2"
)

// A save is all or nothing. When the API answers with an error the page tells the owner nothing
// was saved, so a save that fails part way must not have stored the rest of it.
func TestSettingsSaveIsAllOrNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.db")
	if err := stats.InitDB(path); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()
	t.Setenv("POOL_ADDRESS", "")

	app := fiber.New()
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

	const addr = "bitcoincashii:qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzse6qye33q"
	if code, b := post(`{"pool_address":"` + addr + `","coinbase_tag":"Before"}`); code != 200 {
		t.Fatalf("SAVE-ATOMIC-SETUP: %d %s", code, b)
	}

	// Make the database refuse the payout-mode part of a save, as a failing write would.
	raw, err := sql.Open("sqlite", path+"?_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TRIGGER refuse_tides BEFORE UPDATE ON pool_config
		WHEN NEW.payout_mode = 'tides' BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	code, b := post(`{"pool_address":"` + addr + `","payout_address_1175":"esf1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqnkz876","coinbase_tag":"After","payout_mode":"tides"}`)
	_, payout1175, tag, err := stats.GetPoolConfig()
	if err != nil {
		t.Fatal(err)
	}
	if code != 500 || tag != "Before" || payout1175 != "" {
		t.Fatalf("SAVE-ATOMIC: a save that failed (%d %s) still stored part of itself: tag %q, 1175 address %q", code, b, tag, payout1175)
	}
	if mode, err := stats.GetPayoutMode(); err != nil || mode != stats.PayoutModeSolo {
		t.Fatalf("SAVE-ATOMIC-MODE: the refused mode was stored: %q %v", mode, err)
	}
}

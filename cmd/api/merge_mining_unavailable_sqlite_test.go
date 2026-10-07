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

// Forge Solo for Linux runs no 1175 node (MERGE_MINING_AVAILABLE=0). Its Settings page only hid
// the 1175 box, and the API took a 1175 address all the same: saved, it switched merge-mining on
// against no node. Without a 1175 node the API refuses a 1175 address and keeps the one a database
// brought from Umbrel or Windows holds, and /pool/config shows none.
func TestNo1175NodeNo1175Address(t *testing.T) {
	if err := stats.InitDB(filepath.Join(t.TempDir(), "api.db")); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()
	t.Setenv("POOL_ADDRESS", "")
	t.Setenv("PAYOUT_ADDRESS_1175", "")
	const (
		addr  = "bitcoincashii:qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzse6qye33q"
		moved = "esf1quhj7te09uhj7te09uhj7te09uhj7te09dnlk6x"
		other = "esf1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqnkz876"
	)
	if err := stats.SavePoolSettings(addr, moved, "Before", stats.PayoutModeSolo); err != nil {
		t.Fatal(err)
	}
	app := fiber.New()
	app.Post("/api/v1/pool/config", savePoolConfig)
	post := func(esf, tag string) (int, string) {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/v1/pool/config", strings.NewReader(
			`{"pool_address":"`+addr+`","payout_address_1175":"`+esf+`","coinbase_tag":"`+tag+`","payout_mode":"solo"}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	stored := func() (string, string) {
		t.Helper()
		_, esf, tag, err := stats.GetPoolConfig()
		if err != nil {
			t.Fatal(err)
		}
		return esf, tag
	}

	t.Setenv("MERGE_MINING_AVAILABLE", "0")
	if cfg := getJSON(t, getPoolConfig, "/pool/config"); cfg["payout_address_1175"] != "" || cfg["merge_mining_available"] != false {
		t.Errorf("API-NO-MM-SHOWN: with no 1175 node /pool/config shows 1175 address %v (merge_mining_available %v)", cfg["payout_address_1175"], cfg["merge_mining_available"])
	}
	code, body := post(other, "After")
	if esf, tag := stored(); code != 400 || esf != moved || tag != "Before" || !strings.Contains(body, "no 1175 node") {
		t.Errorf("API-NO-MM-ACCEPTED: with no 1175 node a 1175 address was answered %d %s, and stored: %q, tag %q", code, body, esf, tag)
	}
	code, body = post("", "After")
	if esf, tag := stored(); code != 200 || esf != moved || tag != "After" {
		t.Errorf("API-NO-MM-CLEARED: a save without a 1175 address (%d %s) left 1175 address %q, tag %q; want the stored one kept", code, body, esf, tag)
	}
	code, body = post(moved, "Again")
	if esf, tag := stored(); code != 200 || esf != moved || tag != "Again" {
		t.Errorf("API-NO-MM-OLD-PAGE: a save that sends the stored 1175 address back, as a page from before the update does, was answered %d %s (1175 %q, tag %q)", code, body, esf, tag)
	}

	// With a 1175 node, as on Umbrel and Windows, nothing of this changes.
	t.Setenv("MERGE_MINING_AVAILABLE", "")
	if cfg := getJSON(t, getPoolConfig, "/pool/config"); cfg["payout_address_1175"] != moved {
		t.Errorf("API-MM-SHOWN: with a 1175 node /pool/config shows 1175 address %v, want %s", cfg["payout_address_1175"], moved)
	}
	code, body = post(other, "Node")
	if esf, _ := stored(); code != 200 || esf != other {
		t.Errorf("API-MM-SAVED: with a 1175 node a new 1175 address was answered %d %s and stored as %q", code, body, esf)
	}
	code, body = post("", "Node")
	if esf, _ := stored(); code != 200 || esf != "" {
		t.Errorf("API-MM-CLEARS: with a 1175 node a blank 1175 address (%d %s) left %q; it clears it", code, body, esf)
	}
}

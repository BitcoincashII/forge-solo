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

const (
	// A 1175 address with a 19-byte witness program. 1.0.12 to 1.0.15 saved it, and the 1175 node
	// refused every getauxblock for it ("-5 Invalid payout address").
	bad1175 = "esf1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqnkz876"
	// Valid 1175 addresses: witness version 0, twenty bytes of 0xe5 and of 0xee.
	good1175  = "esf1quhj7te09uhj7te09uhj7te09uhj7te09dnlk6x"
	other1175 = "esf1qamhwamhwamhwamhwamhwamhwamhwamhwnlvzj4"
)

// Settings refuses a 1175 address the 1175 node would refuse, says so, and stores nothing of that
// save; a valid one in capitals is stored in lower case.
func TestSettingsRefusesA1175AddressTheNodeRefuses(t *testing.T) {
	if err := stats.InitDB(filepath.Join(t.TempDir(), "api.db")); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()
	t.Setenv("POOL_ADDRESS", "")
	t.Setenv("PAYOUT_ADDRESS_1175", "")
	t.Setenv("MERGE_MINING_AVAILABLE", "")
	const addr = "bitcoincashii:qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzse6qye33q"
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
	if code, body := post(good1175, "Before"); code != 200 {
		t.Fatalf("SET1175-SETUP: %d %s", code, body)
	}
	for _, c := range []struct{ name, addr string }{
		{"a 19-byte program, as 1.0.12 to 1.0.15 saved", bad1175},
		{"v0 with a bech32m checksum", "esf1quhj7te09uhj7te09uhj7te09uhj7te09c006ly"},
		{"v1 with a bech32 checksum", "esf1puhj7te09uhj7te09uhj7te09uhj7te09uhj7te09uhj7te09uhjs3nz485"},
		{"1175 regtest", "resf1quhj7te09uhj7te09uhj7te09uhj7te09lda9ej"},
		{"mixed case", "Esf1quhj7te09uhj7te09uhj7te09uhj7te09dnlk6x"},
		{"one character changed", "esf1quhj7te09uhj7te09uhj7te09uhj7te09dnlk6y"},
	} {
		code, body := post(c.addr, "After")
		esf, tag := stored()
		if code != 400 || !strings.Contains(body, "not a valid 1175 address") || !strings.Contains(body, "leave the box empty to turn merge-mining off") {
			t.Errorf("SET1175-REFUSED %s: %q was answered %d %s", c.name, c.addr, code, body)
		}
		if esf != good1175 || tag != "Before" {
			t.Errorf("SET1175-NOTHING-SAVED %s: after the refusal the store holds 1175 %q, tag %q", c.name, esf, tag)
		}
	}
	if code, body := post("  "+strings.ToUpper(other1175)+" ", "Upper"); code != 200 {
		t.Errorf("SET1175-UPPER: %q in capitals was answered %d %s", other1175, code, body)
	}
	if esf, tag := stored(); esf != other1175 || tag != "Upper" {
		t.Errorf("SET1175-LOWER: a 1175 address saved in capitals is stored as %q (tag %q), want %q", esf, tag, other1175)
	}
}

// A 1175 address stored by 1.0.12 to 1.0.15 (or moved from 1.0.12's database) that is not a 1175
// address is shown as it is, with payout_address_1175_invalid, so the dashboard and Settings say
// merge-mining is off and ask for a valid one. The stratum takes PAYOUT_ADDRESS_1175 when none is
// stored, so an invalid one there is flagged too. Where this app runs no 1175 node, nothing is.
func TestAStored1175AddressThatIsNotOneIsFlagged(t *testing.T) {
	if err := stats.InitDB(filepath.Join(t.TempDir(), "api.db")); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()
	t.Setenv("POOL_ADDRESS", "")
	t.Setenv("MERGE_MINING_AVAILABLE", "")
	const addr = "bitcoincashii:qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzse6qye33q"
	for _, c := range []struct {
		name, stored, env, mode, mm string
		shown                       string
		flagged                     bool
	}{
		{"stored, solo", bad1175, "", stats.PayoutModeSolo, "", bad1175, true},
		{"stored, TIDES", bad1175, "", stats.PayoutModeTides, "", bad1175, true},
		{"stored valid", good1175, "", stats.PayoutModeSolo, "", good1175, false},
		{"stored valid, env invalid", good1175, bad1175, stats.PayoutModeSolo, "", good1175, false},
		{"none stored, env invalid", "", bad1175, stats.PayoutModeSolo, "", "", true},
		{"none stored, env valid", "", good1175, stats.PayoutModeSolo, "", good1175, false},
		{"none", "", "", stats.PayoutModeSolo, "", "", false},
		{"stored, no 1175 node", bad1175, "", stats.PayoutModeSolo, "0", "", false},
	} {
		t.Setenv("PAYOUT_ADDRESS_1175", c.env)
		t.Setenv("MERGE_MINING_AVAILABLE", c.mm)
		if err := stats.SavePoolSettings(addr, c.stored, "", c.mode); err != nil {
			t.Fatal(err)
		}
		cfg := getJSON(t, getPoolConfig, "/pool/config")
		if cfg["payout_address_1175"] != c.shown {
			t.Errorf("FLAG1175-SHOWN %s: /pool/config shows 1175 address %v, want %q", c.name, cfg["payout_address_1175"], c.shown)
		}
		flag, there := cfg["payout_address_1175_invalid"]
		if c.flagged && flag != true {
			t.Errorf("FLAG1175 %s: payout_address_1175_invalid is %v, want true", c.name, flag)
		}
		if !c.flagged && there {
			t.Errorf("FLAG1175-NONE %s: payout_address_1175_invalid is %v, want it left out", c.name, flag)
		}
	}
}

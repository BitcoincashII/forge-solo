package main

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/stats"
	"github.com/gofiber/fiber/v2"
)

// Through the real handlers, as on Umbrel: a request without the app's password changes nothing,
// whichever endpoint it uses, and the Settings page is told to ask for the password.
func TestSettingsNeedThePassword(t *testing.T) {
	if err := stats.InitDB(filepath.Join(t.TempDir(), "api.db")); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()
	t.Setenv("POOL_ADDRESS", "")
	const pw = "0b4f3c2d1e0f9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a4b"
	old := settingsPassword
	settingsPassword = pw
	t.Cleanup(func() { settingsPassword = old })

	app := fiber.New()
	app.Use(rejectCrossSiteWrites)
	app.Use(settingsPasswordGate(settingsPassword, settingsPasswordRequired("")))
	app.Get("/api/v1/pool/config", getPoolConfig)
	app.Post("/api/v1/pool/config", savePoolConfig)
	post := func(path, body, password string) (int, string) {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if password != "" {
			req.Header.Set(settingsPasswordHeader, password)
		}
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	stored := func() string {
		a, _, _, err := stats.GetPoolConfig()
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	readBody := func() string {
		resp, err := app.Test(httptest.NewRequest("GET", "/api/v1/pool/config", nil))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	readField := func(k string) any {
		var d map[string]any
		if err := json.Unmarshal([]byte(readBody()), &d); err != nil {
			t.Fatal(err)
		}
		return d[k]
	}
	readFlag := func() any { return readField("password_required") }

	if f := readFlag(); f != true {
		t.Fatalf("PW-E2E-FLAG: the settings read says password_required=%v, so the page would not ask for it", f)
	}
	if n := readField("password_length"); n != float64(len(pw)) {
		t.Fatalf("PW-E2E-LENGTH: the settings read says password_length=%v, want %d", n, len(pw))
	}
	if b := readBody(); strings.Contains(b, pw) {
		t.Fatalf("PW-E2E-NOT-SHOWN: the settings read shows the password: %s", b)
	}
	const mine = "bitcoincashii:qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzse6qye33q"
	body := `{"pool_address":"` + mine + `","payout_mode":"solo"}`
	if code, b := post("/api/v1/pool/config", body, ""); code != 401 || stored() != "" {
		t.Fatalf("PW-E2E-NO-PASSWORD: %d %s, stored %q", code, b, stored())
	}
	if code, b := post("/api/v1/pool/config", body, strings.Repeat("a", 64)); code != 401 || stored() != "" {
		t.Fatalf("PW-E2E-WRONG: %d %s, stored %q", code, b, stored())
	}
	if code, b := post("/api/v1/pool/config", body, pw); code != 200 || stored() != mine {
		t.Fatalf("PW-E2E-SAVE: %d %s, stored %q", code, b, stored())
	}

	settingsPassword = ""
	if f := readFlag(); f != false {
		t.Fatalf("PW-E2E-FLAG-OFF: password_required=%v with no password set", f)
	}
}

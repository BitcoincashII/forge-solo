package main

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// Whether a settings change needs the password follows from where the API listens, so an install
// cannot be left open by forgetting to turn the check on.
func TestSettingsPasswordRequired(t *testing.T) {
	for _, tc := range []struct {
		listenHost string
		want       bool
	}{
		{"", true}, // Umbrel: every interface
		{"0.0.0.0", true},
		{"127.0.0.1", false}, // Forge Solo for Windows and Linux
		{"localhost", false},
	} {
		if got := settingsPasswordRequired(tc.listenHost); got != tc.want {
			t.Errorf("PW-RULE: API_LISTEN_HOST=%q: %v, want %v", tc.listenHost, got, tc.want)
		}
	}
}

// Another app on the Umbrel can reach the API directly, so a change must carry the app's password.
func TestSettingsPasswordGate(t *testing.T) {
	const pw = "5d0f2c9e8b7a6f5e4d3c2b1a09f8e7d6c5b4a39281706f5e4d3c2b1a0f9e8d7c"
	type tc struct {
		code, method, password string
		setHeader              bool
		want                   int
	}
	run := func(gate fiber.Handler, cases []tc) {
		t.Helper()
		for _, c := range cases {
			app := fiber.New()
			app.Use(gate)
			reached := false
			app.All("/api/v1/pool/config", func(ctx *fiber.Ctx) error { reached = true; return ctx.SendString("ok") })
			req := httptest.NewRequest(c.method, "/api/v1/pool/config", strings.NewReader(`{"pool_address":"x"}`))
			req.Header.Set("Content-Type", "application/json")
			if c.setHeader {
				req.Header.Set(settingsPasswordHeader, c.password)
			}
			resp, err := app.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != c.want {
				t.Errorf("%s: status %d %s, want %d", c.code, resp.StatusCode, b, c.want)
			}
			if reached != (c.want == 200) {
				t.Errorf("%s: the handler ran=%v with status %d", c.code, reached, resp.StatusCode)
			}
			if c.want == 401 && !strings.Contains(string(b), `"password_required":true`) {
				t.Errorf("%s: a refusal must tell the page to ask for the password: %s", c.code, b)
			}
		}
	}

	run(settingsPasswordGate(pw, true), []tc{
		{"PW-NONE", "POST", "", false, 401},
		{"PW-EMPTY", "POST", "", true, 401},
		{"PW-WRONG", "POST", strings.Repeat("0", 64), true, 401},
		{"PW-PREFIX", "POST", pw[:63], true, 401},
		{"PW-LONGER", "POST", pw + "0", true, 401},
		{"PW-CASE", "POST", strings.ToUpper(pw), true, 401},
		{"PW-RIGHT", "POST", pw, true, 200},
		// HTTP already drops spaces and tabs around a header value; a non-breaking space, which a
		// copy from a web page can carry, reaches the gate.
		{"PW-PASTED-SPACES", "POST", "\u00a0 " + pw + "\u00a0", true, 200},
		{"PW-OTHER-METHOD", "PUT", "", false, 401},
		{"PW-DELETE", "DELETE", "", false, 401},
		{"PW-READ-OPEN", "GET", "", false, 200},
		{"PW-HEAD-OPEN", "HEAD", "", false, 200},
	})
	// Started without its password where other machines can reach it: no change at all.
	run(settingsPasswordGate("", true), []tc{
		{"PW-FAIL-CLOSED", "POST", "", false, 503},
		{"PW-FAIL-CLOSED-ANY", "POST", "anything", true, 503},
		{"PW-FAIL-CLOSED-READ", "GET", "", false, 200},
	})
	// Windows and Linux: the API answers only on this machine.
	run(settingsPasswordGate("", false), []tc{
		{"PW-LOCAL-OPEN", "POST", "", false, 200},
	})
}

// A Settings page from before the password (1.0.12, still open or cached by the browser) has no
// password box and shows the API's error as it is, so that error must say what to do there.
func TestMissingPasswordAnswerTellsAnOldPageToReload(t *testing.T) {
	app := fiber.New()
	app.Use(settingsPasswordGate(strings.Repeat("ab", 32), true))
	app.Post("/api/v1/pool/config", func(c *fiber.Ctx) error { return c.SendString("ok") })
	answer := func(password string) string {
		req := httptest.NewRequest("POST", "/api/v1/pool/config", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		if password != "" {
			req.Header.Set(settingsPasswordHeader, password)
		}
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	none := answer("")
	for _, want := range []string{"Nothing was saved.", "no password box", "reload"} {
		if !strings.Contains(none, want) {
			t.Errorf("PW-OLD-PAGE: the answer to a save without the password lacks %q: %s", want, none)
		}
	}
	if wrong := answer(strings.Repeat("0", 64)); strings.Contains(wrong, "reload") {
		t.Errorf("PW-OLD-PAGE-WRONG: a wrong password came from a page with the box; it is not told to reload: %s", wrong)
	}
}

// What main() installs, from the environment each platform gives the API.
func TestSettingsPasswordGateFromEnv(t *testing.T) {
	const pw = "c3b2a1908f7e6d5c4b3a29180f7e6d5c4b3a29180f7e6d5c4b3a29180f7e6d5c"
	old := settingsPassword
	t.Cleanup(func() { settingsPassword = old })
	for _, tc := range []struct {
		code, listenHost, password, sent string
		want                             int
	}{
		{"PW-ENV-UMBREL-NONE", "", pw, "", 401},
		{"PW-ENV-UMBREL-RIGHT", "", " " + pw + "\n", pw, 200},
		{"PW-ENV-UMBREL-NO-PASSWORD", "", "", "", 503},
		{"PW-ENV-LOCAL", "127.0.0.1", "", "", 200},
		{"PW-ENV-LOCAL-WITH-PASSWORD", "127.0.0.1", pw, "", 401},
	} {
		t.Setenv("API_LISTEN_HOST", tc.listenHost)
		t.Setenv("SETTINGS_PASSWORD", tc.password)
		app := fiber.New()
		app.Use(settingsPasswordGateFromEnv())
		app.Post("/api/v1/pool/config", func(c *fiber.Ctx) error { return c.SendString("ok") })
		req := httptest.NewRequest("POST", "/api/v1/pool/config", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		if tc.sent != "" {
			req.Header.Set(settingsPasswordHeader, tc.sent)
		}
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != tc.want {
			t.Errorf("%s: status %d, want %d", tc.code, resp.StatusCode, tc.want)
		}
		if want := strings.TrimSpace(tc.password); settingsPassword != want {
			t.Errorf("%s: settingsPassword is %q, want %q (the page is told password_required from it)", tc.code, settingsPassword, want)
		}
	}
}

package main

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// A state-changing request a browser makes for another site must be refused before any handler
// runs: the home app's settings save has no login, and a hidden HTML form on any web page could
// otherwise set its own payout address (proven with a form-encoded POST before this guard).
func TestRejectCrossSiteWrites(t *testing.T) {
	app := fiber.New()
	app.Use(rejectCrossSiteWrites)
	reached := 0
	app.All("/api/v1/pool/config", func(c *fiber.Ctx) error { reached++; return c.SendString("ok") })

	for _, tc := range []struct {
		name, method, ctype, site string
		want                      int
	}{
		{"hidden form", "POST", "application/x-www-form-urlencoded", "cross-site", 403},
		{"hidden form, no fetch metadata", "POST", "application/x-www-form-urlencoded", "", 415},
		{"multipart form", "POST", "multipart/form-data; boundary=x", "", 415},
		{"text/plain form", "POST", "text/plain", "", 415},
		{"cross-site JSON", "POST", "application/json", "cross-site", 403},
		{"same-site JSON (another port)", "POST", "application/json", "same-site", 403},
		{"dashboard save", "POST", "application/json", "same-origin", 200},
		{"dashboard save, charset", "POST", "application/json; charset=utf-8", "same-origin", 200},
		{"non-browser client", "POST", "application/json", "", 200},
		{"read from anywhere", "GET", "", "cross-site", 200},
	} {
		body := `{"pool_address":"x"}`
		if strings.HasPrefix(tc.ctype, "multipart/") {
			body = "--x\r\nContent-Disposition: form-data; name=\"PoolAddress\"\r\n\r\nv\r\n--x--\r\n"
		}
		req := httptest.NewRequest(tc.method, "/api/v1/pool/config", strings.NewReader(body))
		if tc.ctype != "" {
			req.Header.Set("Content-Type", tc.ctype)
		}
		if tc.site != "" {
			req.Header.Set("Sec-Fetch-Site", tc.site)
		}
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.name, resp.StatusCode, b, tc.want)
		}
	}
	if reached != 4 {
		t.Fatalf("the handler ran %d times, want 4 (only the allowed requests)", reached)
	}
}

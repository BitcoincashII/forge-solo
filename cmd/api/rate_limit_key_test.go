package main

import (
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// The rate limit counts a request against X-Real-IP, which the proxy in front sets; failing that,
// against the X-Forwarded-For hop a proxy appended, the last, not the first, which the client wrote.
func TestRateLimitKey(t *testing.T) {
	app := fiber.New()
	app.Get("/k", func(c *fiber.Ctx) error { return c.SendString(rateLimitKey(c)) })
	for _, tc := range []struct {
		realIP, xff, want, code string
	}{
		{"192.168.1.20", "10.8.8.8, 10.7.7.7", "192.168.1.20", "RATE-KEY-REAL-IP"},
		{"", "10.8.8.8, 192.168.1.20", "192.168.1.20", "RATE-KEY-LAST-HOP"},
		{"", "192.168.1.20", "192.168.1.20", "RATE-KEY-ONE-HOP"},
		{"", "", "0.0.0.0", "RATE-KEY-DIRECT"}, // fiber's test requests come from 0.0.0.0
	} {
		req := httptest.NewRequest("GET", "/k", nil)
		if tc.realIP != "" {
			req.Header.Set("X-Real-IP", tc.realIP)
		}
		if tc.xff != "" {
			req.Header.Set("X-Forwarded-For", tc.xff)
		}
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(resp.Body)
		if string(got) != tc.want {
			t.Errorf("%s: X-Real-IP %q, X-Forwarded-For %q: counted against %q, want %q", tc.code, tc.realIP, tc.xff, got, tc.want)
		}
	}
}

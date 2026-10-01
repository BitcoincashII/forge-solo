package main

import (
	"bytes"
	"log"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// Polls and the healthcheck are not logged; writes and failures are.
func TestOnlyWritesAndFailuresAreLogged(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	app := fiber.New()
	app.Use(logRequests)
	app.Get("/api/v1/stats", func(c *fiber.Ctx) error { return c.SendString("ok") })
	app.Post("/api/v1/pool/config", func(c *fiber.Ctx) error { return c.SendString("saved") })
	app.Get("/api/v1/broken", func(c *fiber.Ctx) error { return fiber.NewError(fiber.StatusServiceUnavailable, "down") })
	for _, r := range []struct{ method, path string }{
		{"GET", "/api/v1/stats"}, {"HEAD", "/api/v1/stats"}, {"POST", "/api/v1/pool/config"},
		{"GET", "/api/v1/broken"}, {"GET", "/api/v1/missing"},
	} {
		if _, err := app.Test(httptest.NewRequest(r.method, r.path, nil)); err != nil {
			t.Fatal(err)
		}
	}
	got := buf.String()
	for _, want := range []string{"POST /api/v1/pool/config 200", "GET /api/v1/broken 503", "GET /api/v1/missing 404"} {
		if !strings.Contains(got, want) {
			t.Errorf("log lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "/api/v1/stats") {
		t.Errorf("a poll or the healthcheck was logged:\n%s", got)
	}
}

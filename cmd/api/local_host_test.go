package main

import (
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// On a loopback listener the API answers only to this machine's own names, which a DNS-rebinding
// page cannot send.
func TestLoopbackListenerAnswersOnlyLocalHosts(t *testing.T) {
	for host, want := range map[string]bool{"127.0.0.1": true, "localhost": true, "::1": true, "[::1]": true,
		"": false, "0.0.0.0": false, "192.168.1.5": false} {
		if got := listenHostIsLoopback(host); got != want {
			t.Errorf("listenHostIsLoopback(%q) = %v, want %v", host, got, want)
		}
	}
	app := fiber.New()
	app.Use(onlyLocalHost)
	app.Get("/api/v1/pool/config", func(c *fiber.Ctx) error { return c.SendString("ok") })
	for host, want := range map[string]int{"127.0.0.1:31800": 200, "localhost:31800": 200, "[::1]:31800": 200,
		"rebind.attacker.example:31800": 421, "192.168.1.5:31800": 421} {
		req := httptest.NewRequest("GET", "/api/v1/pool/config", nil)
		req.Host = host
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != want {
			t.Errorf("Host %s: status %d, want %d", host, resp.StatusCode, want)
		}
	}
}

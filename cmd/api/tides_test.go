package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// The dashboard's TIDES data comes from Forge Pool through the api: fetched only when asked,
// kept for a few seconds so a page polling it does not become load on the pool, and never passed
// on unless it is JSON from a 200.
func TestTidesProxyCachesAndOnlyPassesGoodAnswers(t *testing.T) {
	var hits int32
	status := int32(200)
	body := atomic.Value{}
	body.Store(`{"available":true,"miners":[]}`)
	pool := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.URL.Path != "/api/v1/tides" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(int(atomic.LoadInt32(&status)))
		io.WriteString(w, body.Load().(string))
	}))
	defer pool.Close()
	t.Setenv("DATUM_POOL_URL", pool.URL+"/")
	tidesCacheMu.Lock()
	tidesCache = map[string]tidesCached{}
	tidesCacheMu.Unlock()

	app := fiber.New()
	app.Get("/api/v1/tides", getTidesPool)
	get := func() (int, string) {
		resp, err := app.Test(httptest.NewRequest("GET", "/api/v1/tides", nil))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	if code, b := get(); code != 200 || !strings.Contains(b, `"available":true`) {
		t.Fatalf("TIDES-API-PROXY: %d %s", code, b)
	}
	get()
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("TIDES-API-CACHE: two dashboard polls within the cache window asked the pool %d times", n)
	}

	// Past the cache, a pool that errors or answers garbage is reported, not relayed.
	tidesCacheMu.Lock()
	tidesCache = map[string]tidesCached{}
	tidesCacheMu.Unlock()
	atomic.StoreInt32(&status, 503)
	if code, b := get(); code != http.StatusBadGateway || !strings.Contains(b, `"available":false`) {
		t.Fatalf("TIDES-API-ERROR: a 503 from the pool came back as %d %s", code, b)
	}
	atomic.StoreInt32(&status, 200)
	body.Store(`<html>not json</html>`)
	if code, b := get(); code != http.StatusBadGateway || strings.Contains(b, "<html>") {
		t.Fatalf("TIDES-API-NOTJSON: a non-JSON answer was relayed: %d %s", code, b)
	}
}

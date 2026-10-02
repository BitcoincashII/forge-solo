package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/stats"
	"github.com/gofiber/fiber/v2"
)

// inMode makes the TIDES endpoints see payout mode m, with no database.
func inMode(t *testing.T, m string) {
	saved := tidesPayoutMode
	tidesPayoutMode = func() (string, error) { return m, nil }
	t.Cleanup(func() { tidesPayoutMode = saved })
	tidesCacheMu.Lock()
	tidesCache = map[string]tidesCached{}
	tidesCacheMu.Unlock()
}

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
	inMode(t, stats.PayoutModeTides)

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

// In solo mode the api asks Forge Pool nothing, whoever asks it: the payout address is sent to the
// pool only by an install that chose TIDES.
func TestTidesAsksThePoolNothingInSoloMode(t *testing.T) {
	var hits int32
	pool := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		io.WriteString(w, `{}`)
	}))
	defer pool.Close()
	t.Setenv("DATUM_POOL_URL", pool.URL)
	t.Setenv("POOL_ADDRESS", "")
	app := fiber.New()
	app.Get("/api/v1/tides", getTidesPool)
	app.Get("/api/v1/tides/me", getTidesMine)
	inMode(t, stats.PayoutModeSolo)
	for _, path := range []string{"/api/v1/tides", "/api/v1/tides/me"} {
		resp, err := app.Test(httptest.NewRequest("GET", path, nil))
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusConflict {
			t.Errorf("TIDES7-SOLO: %s in solo mode answered %d", path, resp.StatusCode)
		}
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("TIDES7-SOLO: in solo mode the pool was asked %d times", n)
	}
	inMode(t, stats.PayoutModeTides)
	if resp, _ := app.Test(httptest.NewRequest("GET", "/api/v1/tides", nil)); resp.StatusCode != 200 || atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("TIDES7-TIDES-ON: in TIDES mode the window was not fetched (%d, %d requests)", resp.StatusCode, atomic.LoadInt32(&hits))
	}
}

// The api holds the pool's address to the stratum's rules: https (plain http only to this
// machine), and no redirect followed.
func TestTidesPoolAddressIsChecked(t *testing.T) {
	app := fiber.New()
	app.Get("/api/v1/tides", getTidesPool)
	inMode(t, stats.PayoutModeTides)
	t.Setenv("DATUM_POOL_URL", "http://pool.example")
	resp, _ := app.Test(httptest.NewRequest("GET", "/api/v1/tides", nil))
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(b), "https") {
		t.Fatalf("TIDES7-HTTPS: a plain-http pool address gave %d %s", resp.StatusCode, b)
	}

	var elsewhere int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&elsewhere, 1)
		io.WriteString(w, `{}`)
	}))
	defer other.Close()
	pool := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusFound)
	}))
	defer pool.Close()
	t.Setenv("DATUM_POOL_URL", pool.URL)
	inMode(t, stats.PayoutModeTides)
	resp, _ = app.Test(httptest.NewRequest("GET", "/api/v1/tides", nil))
	if resp.StatusCode != http.StatusBadGateway || atomic.LoadInt32(&elsewhere) != 0 {
		t.Fatalf("TIDES7-REDIRECT: a redirect was followed (%d, %d requests elsewhere)", resp.StatusCode, atomic.LoadInt32(&elsewhere))
	}
}

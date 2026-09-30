package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/stats"
	"github.com/gofiber/fiber/v2"
)

// TIDES mode on the dashboard: Forge Pool's TIDES window and this install's DATUM payouts. The
// browser cannot ask the pool itself -- the pool sends no CORS headers -- and the payout address
// is already known here, so the api fetches both and keeps them for a few seconds. Nothing is
// fetched unless the dashboard asks, and it asks only in TIDES mode.

// tidesPoolURL is Forge Pool's base URL: DATUM_POOL_URL, else the public pool.
func tidesPoolURL() string {
	if u := strings.TrimSpace(os.Getenv("DATUM_POOL_URL")); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "https://pool.bch2.org"
}

const (
	tidesCacheFor = 10 * time.Second
	tidesMaxBody  = 2 << 20
)

type tidesCached struct {
	body []byte
	at   time.Time
}

var (
	tidesHTTP    = &http.Client{Timeout: 8 * time.Second}
	tidesCacheMu sync.Mutex
	tidesCache   = map[string]tidesCached{}
)

// fetchTides GETs path from the pool, answering from a copy up to tidesCacheFor old.
func fetchTides(path string) ([]byte, error) {
	now := time.Now()
	tidesCacheMu.Lock()
	if c, ok := tidesCache[path]; ok && now.Sub(c.at) < tidesCacheFor {
		tidesCacheMu.Unlock()
		return c.body, nil
	}
	tidesCacheMu.Unlock()

	resp, err := tidesHTTP.Get(tidesPoolURL() + path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, tidesMaxBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the pool answered %s", resp.Status)
	}
	if !json.Valid(body) {
		return nil, errors.New("the pool's answer is not JSON")
	}
	tidesCacheMu.Lock()
	tidesCache[path] = tidesCached{body: body, at: now}
	tidesCacheMu.Unlock()
	return body, nil
}

// payoutAddressInEffect is the BCH2 payout address the dashboard shows: the saved one, else a
// valid POOL_ADDRESS from the app config.
func payoutAddressInEffect() string {
	poolAddr, _, _, err := stats.GetPoolConfig()
	if err != nil {
		poolAddr = ""
	}
	if poolAddr == "" {
		if env := strings.TrimSpace(os.Getenv("POOL_ADDRESS")); env != "" {
			if isValidBCH2Address(env) {
				poolAddr = strings.ToLower(env)
			} else {
				log.Printf("WARNING: POOL_ADDRESS is set but is not a valid mainnet bitcoincashii: P2PKH address (%q); ignoring it", env)
			}
		}
	}
	return poolAddr
}

func sendTides(c *fiber.Ctx, body []byte, err error) error {
	if err != nil {
		return c.Status(http.StatusBadGateway).JSON(fiber.Map{"available": false, "error": "Forge Pool did not answer: " + err.Error()})
	}
	c.Set("Content-Type", "application/json")
	return c.Send(body)
}

// getTidesPool is the pool's TIDES window: who is in it, what the next DATUM block pays each,
// and the recent DATUM blocks.
func getTidesPool(c *fiber.Ctx) error {
	body, err := fetchTides("/api/v1/tides")
	return sendTides(c, body, err)
}

// getTidesMine is this install's row in the window and its payouts from DATUM blocks.
func getTidesMine(c *fiber.Ctx) error {
	addr := payoutAddressInEffect()
	if addr == "" {
		return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "No BCH2 payout address is set yet."})
	}
	body, err := fetchTides("/api/v1/tides/miners/" + url.PathEscape(addr))
	return sendTides(c, body, err)
}

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

	"github.com/BitcoincashII/forge-solo/internal/datum/gateway"
	"github.com/BitcoincashII/forge-solo/internal/stats"
	"github.com/BitcoincashII/forge-solo/internal/tidesgw"
	"github.com/gofiber/fiber/v2"
)

// TIDES mode on the dashboard: Forge Pool's TIDES window and this install's DATUM payouts. The
// browser cannot ask the pool itself -- the pool sends no CORS headers -- and the payout address
// is already known here, so the api fetches both and keeps them for a few seconds. Nothing is
// fetched unless the dashboard asks, and only in TIDES mode (tidesModeOff).

const (
	tidesCacheFor = 10 * time.Second
	tidesMaxBody  = 2 << 20
)

type tidesCached struct {
	body []byte
	at   time.Time
}

var (
	// No redirects: the pool's address is checked to be https (CheckPoolURL), and a redirect
	// could send the request, the payout address in its path, anywhere, plain http too.
	tidesHTTP    = &http.Client{Timeout: 8 * time.Second, CheckRedirect: gateway.NoRedirects}
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

	base := tidesgw.PoolURL()
	if err := tidesgw.CheckPoolURL(base); err != nil {
		return nil, err
	}
	resp, err := tidesHTTP.Get(base + path)
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
//
// An error means the stored settings could not be read: the caller must say so rather than
// report "not configured", which invites the user to re-enter, and so overwrite, their settings.
func payoutAddressInEffect() (string, error) {
	poolAddr, _, _, err := stats.GetPoolConfig()
	if err != nil {
		return "", err
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
	return poolAddr, nil
}

func sendTides(c *fiber.Ctx, body []byte, err error) error {
	if err != nil {
		return c.Status(http.StatusBadGateway).JSON(fiber.Map{"available": false, "error": "Forge Pool did not answer: " + err.Error()})
	}
	c.Set("Content-Type", "application/json")
	return c.Send(body)
}

// tidesPayoutMode reads the payout mode; a variable so a test without a database can choose it.
var tidesPayoutMode = stats.GetPayoutMode

// tidesModeOff answers for a TIDES endpoint, and reports true, unless TIDES mode is chosen: in
// solo mode the api asks Forge Pool nothing. The dashboard asks only in TIDES mode; any other
// caller asking in solo mode had the pool sent this install's payout address for nothing.
func tidesModeOff(c *fiber.Ctx) (bool, error) {
	mode, err := tidesPayoutMode()
	if err != nil {
		return true, settingsUnreadable(c, err)
	}
	if mode != stats.PayoutModeTides {
		return true, c.Status(http.StatusConflict).JSON(fiber.Map{"available": false,
			"error": "TIDES mode is off: in solo mode Forge Solo asks Forge Pool nothing."})
	}
	return false, nil
}

// getTidesPool is the pool's TIDES window: who is in it, what the next DATUM block pays each,
// and the recent DATUM blocks.
func getTidesPool(c *fiber.Ctx) error {
	if off, err := tidesModeOff(c); off {
		return err
	}
	body, err := fetchTides("/api/v1/tides")
	return sendTides(c, body, err)
}

// getTidesMine is this install's row in the window and its payouts from DATUM blocks.
func getTidesMine(c *fiber.Ctx) error {
	if off, err := tidesModeOff(c); off {
		return err
	}
	addr, err := payoutAddressInEffect()
	if err != nil {
		return settingsUnreadable(c, err)
	}
	if addr == "" {
		return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "No BCH2 payout address is set yet."})
	}
	body, err := fetchTides("/api/v1/tides/miners/" + url.PathEscape(addr))
	return sendTides(c, body, err)
}

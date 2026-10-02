package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/cashaddr"
	"github.com/gofiber/fiber/v2"
)

func testBCH2Address(b byte) string {
	var h [20]byte
	for i := range h {
		h[i] = b
	}
	return cashaddr.Encode(cashaddr.MainnetPrefix, cashaddr.P2PKH, h)
}

// The dashboard's blocks and payouts: the totals the stratum computed over every block are passed
// on, and figures that cannot be read are said to be so (503, naming what did not answer) instead
// of answered as no blocks and 0.00.
func TestFiguresThatCannotBeReadAreNotShownAsZero(t *testing.T) {
	var dbDown atomic.Bool
	stratum := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dbDown.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, `{"error":"the database is not answering"}`)
			return
		}
		switch r.URL.Path {
		case "/internal/miner-solo-blocks":
			io.WriteString(w, `{"blocks":[{"height":5,"reward":3.1}],"total":130,"totalReward":401.5}`)
		case "/internal/miner-solo-payouts":
			io.WriteString(w, `{"payouts":[],"total":130,"totalPaid":401.5}`)
		default:
			http.NotFound(w, r)
		}
	}))
	saved := stratumURL
	stratumURL = stratum.URL
	t.Cleanup(func() { stratumURL = saved })

	app := fiber.New()
	app.Get("/api/v1/miners/:address/solo-blocks", getMinerSoloBlocks)
	app.Get("/api/v1/miners/:address/solo-payouts", getMinerSoloPayouts)
	base := "/api/v1/miners/" + testBCH2Address(4)
	get := func(path string) (int, map[string]any) {
		resp, err := app.Test(httptest.NewRequest("GET", base+path, nil))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		json.NewDecoder(resp.Body).Decode(&m)
		return resp.StatusCode, m
	}

	if code, m := get("/solo-blocks"); code != 200 || m["total"] != 130.0 || m["totalReward"] != 401.5 {
		t.Fatalf("DATA6-API-BLOCKS: %d %v", code, m)
	}
	if code, m := get("/solo-payouts"); code != 200 || m["total"] != 130.0 || m["totalPaid"] != 401.5 {
		t.Fatalf("DATA6-API-PAYOUTS: %d %v", code, m)
	}

	dbDown.Store(true)
	for _, p := range []string{"/solo-blocks", "/solo-payouts"} {
		if code, m := get(p); code != http.StatusServiceUnavailable || !strings.Contains(m["error"].(string), "database") {
			t.Errorf("DATA5-API-DB: %s with the database down: %d %v", p, code, m)
		}
	}
	stratum.Close()
	for _, p := range []string{"/solo-blocks", "/solo-payouts"} {
		if code, m := get(p); code != http.StatusServiceUnavailable || !strings.Contains(m["error"].(string), "mining service") {
			t.Errorf("DATA5-API-STRATUM: %s with the mining service down: %d %v", p, code, m)
		}
	}
}

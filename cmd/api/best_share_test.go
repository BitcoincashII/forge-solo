package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// The miner's athDiff is its all-time best share, which the mining service keeps across restarts:
// also when the worker that found it is not listed, after a restart before it is back, or after a
// day without a share. It was the best of the workers listed.
func TestMinerBestShareOutlivesItsWorkers(t *testing.T) {
	addr := testBCH2Address(9)
	other := testBCH2Address(10)
	upper := strings.ToUpper(addr)
	stratum := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/workers" {
			io.WriteString(w, `{"workers":[{"miner_id":"`+addr+`","worker_name":"bitaxe","online":true,"valid_shares":3,"ath_diff":2048,`+
				`"last_share_at":"2026-10-06T12:00:00Z","connected_at":"2026-10-06T11:00:00Z"}],`+
				`"miner_ath_diff":{"`+addr+`":5500000000,"`+other+`":9e12}}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer stratum.Close()
	oldURL, oldToken := stratumURL, internalAPIToken
	stratumURL, internalAPIToken = stratum.URL, "t"
	t.Cleanup(func() { stratumURL, internalAPIToken = oldURL, oldToken })

	app := fiber.New()
	app.Get("/api/v1/miners/:address", getMiner)
	for _, asked := range []string{addr, upper} {
		resp, err := app.Test(httptest.NewRequest("GET", "/api/v1/miners/"+asked, nil), 10000)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		var m struct {
			ATH float64 `json:"athDiff"`
		}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%s: %v: %s", asked, err, b)
		}
		switch m.ATH {
		case 5.5e9:
		case 9e12:
			t.Errorf("ATH-API-OTHER-MINER: asked as %s, the miner's athDiff is another miner's 9e12, want its own 5.5e9", asked)
		default:
			t.Errorf("ATH-API-MINER: asked as %s, the miner's athDiff is %v, want the kept 5.5e9", asked, m.ATH)
		}
	}
	if got := getStratumWorkerList().MinerATH; got[addr] != 5.5e9 || got[other] != 9e12 {
		t.Errorf("ATH-API-DECODE: the miners' best shares read from the mining service are %v", got)
	}
}

package main

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// Current Effort is the mining service's per-share figure (each share over the difficulty of the
// job it was mined on), summed over the address's workers. The API sent only the round's work,
// which the dashboard divided by the tip's difficulty, rescaling the round at every retarget.
func TestMinerRoundEffortIsTheStratumsFigure(t *testing.T) {
	addr, other := testBCH2Address(8), testBCH2Address(9)
	stratum := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/workers" {
			io.WriteString(w, `{"workers":[`+
				`{"miner_id":"`+addr+`","worker_name":"a","online":true,"total_work":3.1e9,"round_effort":2.104},`+
				`{"miner_id":"`+addr+`","worker_name":"b","online":false,"total_work":1e9,"round_effort":0.5},`+
				`{"miner_id":"`+other+`","worker_name":"c","online":true,"total_work":9e9,"round_effort":9}]}`)
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
	resp, err := app.Test(httptest.NewRequest("GET", "/api/v1/miners/"+addr, nil), 10000)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	e, ok := got["roundEffort"].(float64)
	if !ok {
		t.Fatalf("API-ROUND-EFFORT: the miner's answer has no roundEffort: %s", b)
	}
	if math.Abs(e-2.604) > 1e-9 {
		t.Fatalf("API-ROUND-EFFORT-SUM: roundEffort %v, want 2.604 (this address's two workers, not the other's)", e)
	}
}

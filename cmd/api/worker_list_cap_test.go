package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// The dashboard's worker list is the busiest workers first, and no more than maxWorkersListed of
// them, with the true total beside it: hundreds of thousands of names were 35 MB per poll.
func TestMinerWorkerListIsCapped(t *testing.T) {
	const miner = "bitcoincashii:qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzse6qye33q"
	var workers []WorkerStats
	for i := 0; i < 700; i++ {
		workers = append(workers, WorkerStats{MinerID: miner, WorkerName: fmt.Sprintf("w%d", i), Online: i%2 == 0, Hashrate5m: float64(i)})
	}
	stratum := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"workers": workers})
	}))
	defer stratum.Close()
	oldURL, oldToken := stratumURL, internalAPIToken
	stratumURL, internalAPIToken = stratum.URL, "t"
	t.Cleanup(func() { stratumURL, internalAPIToken = oldURL, oldToken })

	app := fiber.New()
	app.Get("/api/v1/miners/:address/workers", getMinerWorkers)
	resp, err := app.Test(httptest.NewRequest("GET", "/api/v1/miners/"+miner+"/workers", nil), 10000)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	var got struct {
		Workers []struct {
			Name       string  `json:"name"`
			Online     bool    `json:"online"`
			Hashrate5m float64 `json:"hashrate5m"`
		} `json:"workers"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Workers) != maxWorkersListed || got.Total != 700 {
		t.Fatalf("WORKERS-CAP: %d workers listed, total %d; want %d listed of 700", len(got.Workers), got.Total, maxWorkersListed)
	}
	// Online before offline, the faster before the slower.
	if !got.Workers[0].Online || got.Workers[0].Name != "w698" || !got.Workers[349].Online || got.Workers[350].Online {
		t.Fatalf("WORKERS-ORDER: first %+v, 350th %+v, 351st %+v", got.Workers[0], got.Workers[349], got.Workers[350])
	}
}

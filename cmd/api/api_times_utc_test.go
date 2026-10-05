package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
)

// The stratum stamps a worker's times in its own zone: -05:00 on a PC in the Americas, the host's
// zone on Linux, UTC on Umbrel. The API passed them on as they came, so the same worker read
// "08:22:25-05:00" on Windows and "13:22:25Z" on Umbrel. The API says them in UTC on every platform.
func TestWorkerTimesAreUTCInTheAPI(t *testing.T) {
	addr := testBCH2Address(7)
	const lastShare, connectedAt = "2026-10-05T08:22:25.261266-05:00", "2026-10-05T07:56:40.1-05:00"
	stratum := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/workers" {
			io.WriteString(w, `{"workers":[{"miner_id":"`+addr+`","worker_name":"rig1","online":true,"valid_shares":1,`+
				`"last_share_at":"`+lastShare+`","connected_at":"`+connectedAt+`"}]}`)
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
	app.Get("/api/v1/miners/:address/workers", getMinerWorkers)
	get := func(path string, out any) {
		t.Helper()
		resp, err := app.Test(httptest.NewRequest("GET", path, nil), 10000)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s: %v: %s", path, err, b)
		}
	}
	// Each time must be the stratum's instant, written in UTC.
	check := func(code, got, sent string) {
		t.Helper()
		want, _ := time.Parse(time.RFC3339Nano, sent)
		if !strings.HasSuffix(got, "Z") {
			t.Errorf("%s: %q is not in UTC (the stratum sent %q)", code, got, sent)
			return
		}
		if at, err := time.Parse(time.RFC3339Nano, got); err != nil || !at.Equal(want) {
			t.Errorf("API-UTC-INSTANT: %q is not the instant the stratum sent, %q (%v)", got, sent, err)
		}
	}

	var miner struct {
		LastShare string `json:"lastShare"`
	}
	get("/api/v1/miners/"+addr, &miner)
	check("API-UTC-MINER-LASTSHARE", miner.LastShare, lastShare)

	var workers struct {
		Workers []struct {
			LastShare   string `json:"lastShare"`
			ConnectedAt string `json:"connectedAt"`
		} `json:"workers"`
	}
	get("/api/v1/miners/"+addr+"/workers", &workers)
	if len(workers.Workers) != 1 {
		t.Fatalf("API-UTC-WORKERS: %d workers listed, want 1", len(workers.Workers))
	}
	check("API-UTC-WORKERS-LASTSHARE", workers.Workers[0].LastShare, lastShare)
	check("API-UTC-WORKERS-CONNECTEDAT", workers.Workers[0].ConnectedAt, connectedAt)
}

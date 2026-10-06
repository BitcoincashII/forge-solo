package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// A worker connected with no share yet is listed with the time it connected and no last share:
// lastShare is null, in the workers list and in the miner's answer while none of its workers has a
// share. Both said "0001-01-01T00:00:00Z", a share in the year 1.
func TestNoShareYetIsNull(t *testing.T) {
	addr := testBCH2Address(8)
	const connectedAt = "2026-10-05T18:14:48.5-05:00"
	stratum := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/workers" {
			io.WriteString(w, `{"workers":[{"miner_id":"`+addr+`","worker_name":"nerdminer","online":true,"valid_shares":0,`+
				`"last_share_at":"0001-01-01T00:00:00Z","connected_at":"`+connectedAt+`"}]}`)
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
	get := func(path string) map[string]json.RawMessage {
		t.Helper()
		resp, err := app.Test(httptest.NewRequest("GET", path, nil), 10000)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%s: %v: %s", path, err, b)
		}
		return m
	}

	var workers []map[string]json.RawMessage
	if err := json.Unmarshal(get("/api/v1/miners/" + addr + "/workers")["workers"], &workers); err != nil || len(workers) != 1 {
		t.Fatalf("API-NO-SHARE-WORKERS: %d workers listed (%v), want 1", len(workers), err)
	}
	if got := string(workers[0]["lastShare"]); got != "null" {
		t.Errorf("API-WORKERS-NO-SHARE-NULL: a worker with no share yet has lastShare %s, want null", got)
	}
	if got := string(workers[0]["connectedAt"]); got != `"2026-10-05T23:14:48.5Z"` {
		t.Errorf("API-WORKERS-CONNECTED-AT: the worker connected at %s is listed with connectedAt %s", connectedAt, got)
	}
	if got := string(get("/api/v1/miners/" + addr)["lastShare"]); got != "null" {
		t.Errorf("API-MINER-NO-SHARE-NULL: a miner with no share yet has lastShare %s, want null", got)
	}
}

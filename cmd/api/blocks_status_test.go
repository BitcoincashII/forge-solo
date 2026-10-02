package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// The public blocks list never calls an orphan confirmed, however deep its height is, and says
// what each block's status is.
func TestBlocksAPIReportsOrphansAsOrphaned(t *testing.T) {
	stratum := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"total": 2, "blocks": []map[string]any{
			{"height": 100, "hash": "aa", "status": "orphaned", "is_solo": true},
			{"height": 101, "hash": "bb", "status": "pending", "is_solo": true},
		}})
	}))
	defer stratum.Close()
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":200,"error":null}`))
	}))
	defer node.Close()
	oldS, oldT, oldR, oldU, oldP := stratumURL, internalAPIToken, rpcURL, rpcUser, rpcPass
	stratumURL, internalAPIToken, rpcURL, rpcUser, rpcPass = stratum.URL, "t", node.URL, "u", "p"
	t.Cleanup(func() { stratumURL, internalAPIToken, rpcURL, rpcUser, rpcPass = oldS, oldT, oldR, oldU, oldP })

	app := fiber.New()
	app.Get("/api/v1/blocks", getBlocksAPI)
	resp, err := app.Test(httptest.NewRequest("GET", "/api/v1/blocks", nil), 10000)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	var got struct {
		Blocks []struct {
			Height    int64  `json:"height"`
			Confirmed bool   `json:"confirmed"`
			Status    string `json:"status"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(b, &got); err != nil || len(got.Blocks) != 2 {
		t.Fatalf("BLOCKS-API: %s (%v)", b, err)
	}
	for _, blk := range got.Blocks {
		switch blk.Height {
		case 100:
			if blk.Confirmed || blk.Status != "orphaned" {
				t.Errorf("BLOCKS-API-ORPHAN: an orphan 100 deep reads confirmed=%v status=%q", blk.Confirmed, blk.Status)
			}
		case 101:
			if !blk.Confirmed {
				t.Errorf("BLOCKS-API-DEEP: a pending block 99 deep reads unconfirmed")
			}
		}
	}
}

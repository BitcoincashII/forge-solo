package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/stats"
	"github.com/gofiber/fiber/v2"
)

// The balance card shows what has matured as of the node's height. With no height from the node
// it said everything was still maturing, and with no answer from the database 0.00: now the last
// height the node gave stands in, and what cannot be known is said to be unknown.
func TestTheBalanceIsNeverAGuess(t *testing.T) {
	if err := stats.InitDB(filepath.Join(t.TempDir(), "api.db")); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()
	addr := testBCH2Address(5)
	if err := stats.SaveSoloBlockCoinbaseDirect(normalizeAddress(addr), 500, 3.125, strings.Repeat("ab", 32)); err != nil {
		t.Fatal(err)
	}

	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"result":1000,"error":null}`)
	}))
	defer node.Close()
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close() // refuses connections, as a node that is down
	stratum := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/workers" {
			io.WriteString(w, `{"workers":[]}`)
			return
		}
		io.WriteString(w, `{"matureBalance":0,"immatureBalance":0}`)
	}))
	defer stratum.Close()
	savedRPC, savedUser, savedPass, savedStratum := rpcURL, rpcUser, rpcPass, stratumURL
	t.Cleanup(func() { rpcURL, rpcUser, rpcPass, stratumURL = savedRPC, savedUser, savedPass, savedStratum })
	rpcUser, rpcPass, stratumURL = "u", "p", stratum.URL
	lastNodeHeight.Store(0)

	app := fiber.New()
	app.Get("/api/v1/miners/:address", getMiner)
	get := func() map[string]any {
		resp, err := app.Test(httptest.NewRequest("GET", "/api/v1/miners/"+addr, nil))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		json.NewDecoder(resp.Body).Decode(&m)
		return m
	}

	rpcURL = gone.URL
	if m := get(); m["balanceKnown"] != false {
		t.Fatalf("DATA8-NO-HEIGHT: the node never answered, yet the balance was given: %v", m)
	}
	rpcURL = node.URL
	if m := get(); m["balanceKnown"] != true || m["matureBalance"] != 3.125 {
		t.Fatalf("DATA8-KNOWN: %v", m)
	}
	rpcURL = gone.URL
	if m := get(); m["balanceKnown"] != true || m["matureBalance"] != 3.125 || m["immatureBalance"] != 0.0 {
		t.Fatalf("DATA8-LAST-HEIGHT: with the node down the block counted as maturing again: %v", m)
	}
	stats.CloseDB()
	rpcURL = node.URL
	if m := get(); m["balanceKnown"] != false {
		t.Fatalf("DATA5-BALANCE: no database, yet the balance was given: %v", m)
	}
}

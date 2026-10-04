package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// /api/v1/miners/:address/payouts answers a list, never null: the stratum's answer for a miner
// with no payouts was null, and so was the api's when that answer could not be read.
func TestMinerPayoutsListIsNeverNull(t *testing.T) {
	var answer atomic.Value
	stratum := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/miner-payouts" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, answer.Load().(string))
	}))
	defer stratum.Close()
	saved := stratumURL
	stratumURL = stratum.URL
	t.Cleanup(func() { stratumURL = saved })

	app := fiber.New()
	app.Get("/api/v1/miners/:address/payouts", getMinerPayouts)
	addr := testBCH2Address(6)
	get := func() string {
		resp, err := app.Test(httptest.NewRequest("GET", "/api/v1/miners/"+addr+"/payouts", nil))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}

	for _, c := range []struct{ code, stratum, want string }{
		{"API-PAYOUTS-NULL", `{"payouts":null,"total":0,"totalPaid":0}`, `"payouts":[]`},
		{"API-PAYOUTS-UNREADABLE", `not json`, `"payouts":[]`},
		{"API-PAYOUTS-LIST", `{"payouts":[{"txid":"coinbase-direct","amount":3.125}],"total":1,"totalPaid":3.125}`,
			`"payouts":[{"amount":3.125,"txid":"coinbase-direct"}]`},
	} {
		answer.Store(c.stratum)
		if got := get(); !strings.Contains(got, c.want) {
			t.Errorf("%s: the stratum answered %s and the api %s; want %s in it", c.code, c.stratum, got, c.want)
		}
	}
}

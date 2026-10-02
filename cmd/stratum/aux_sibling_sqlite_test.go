//go:build sqlite

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/stats"
	"go.uber.org/zap"
)

// Two 1175 blocks at one height: the ledger keeps the one on the aux chain. A recorded block the
// node knows but has left off its chain (confirmations -1) was kept, and the block that won was
// dropped from the dashboard.
func TestThe1175BlockOnTheChainIsTheOneKept(t *testing.T) {
	if err := stats.InitDB(filepath.Join(t.TempDir(), "aux.db")); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()
	var mu sync.Mutex
	confs := map[string]float64{}
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string        `json:"method"`
			Params []interface{} `json:"params"`
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &req)
		mu.Lock()
		c, ok := confs[req.Params[0].(string)]
		mu.Unlock()
		if req.Method != "getblock" || !ok {
			io.WriteString(w, `{"result":null,"error":{"code":-5,"message":"Block not found"}}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"result": map[string]interface{}{"confirmations": c}, "error": nil})
	}))
	defer node.Close()
	savedURL, savedUser, savedPass, savedLogger, savedJM := aux1175NodeURL, aux1175User, aux1175Pass, logger, jobManager
	aux1175NodeURL, aux1175User, aux1175Pass, logger, jobManager = node.URL, "u", "p", zap.NewNop(), nil
	t.Cleanup(func() {
		aux1175NodeURL, aux1175User, aux1175Pass, logger, jobManager = savedURL, savedUser, savedPass, savedLogger, savedJM
	})
	hash := func(c string) string { return strings.Repeat(c, 64) }
	set := func(h string, c float64) { mu.Lock(); confs[h] = c; mu.Unlock() }
	const finder = "esf1qfinder"

	// The recorded block lost: the node left it off its chain. The new one is recorded.
	set(hash("a"), -1)
	set(hash("b"), 1)
	aux1175BlockHandler(700, hash("a"), 25_0000_0000, finder, true)
	aux1175BlockHandler(700, hash("b"), 25_0000_0000, finder, true)
	if got, _ := stats.Get1175BlockHashAtHeight(700); got != hash("b") {
		t.Fatalf("PAY4-STALE-REPLACED: the ledger kept %.8s…, a block off the chain, over the one on it", got)
	}

	// The recorded block is on the chain: a sibling arriving later does not replace it.
	set(hash("c"), 3)
	set(hash("d"), -1)
	aux1175BlockHandler(701, hash("c"), 25_0000_0000, finder, true)
	aux1175BlockHandler(701, hash("d"), 25_0000_0000, finder, true)
	if got, _ := stats.Get1175BlockHashAtHeight(701); got != hash("c") {
		t.Fatalf("PAY4-KEEP-WINNER: a sibling replaced the block on the chain (%.8s…)", got)
	}
}

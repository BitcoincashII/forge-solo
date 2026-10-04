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
	"time"

	"github.com/BitcoincashII/forge-solo/internal/stats"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// auxConfNode is a 1175 node that answers getblock with the confirmations set for a hash, and
// "Block not found" for any other. Its getblockchaininfo says it is caught up at height 1000
// unless chain says otherwise.
type auxConfNode struct {
	mu    sync.Mutex
	confs map[string]float64
	chain map[string]interface{}
	asked map[string]int // getblock calls, by hash
}

func (n *auxConfNode) set(hash string, c float64) {
	n.mu.Lock()
	n.confs[hash] = c
	n.mu.Unlock()
}

func (n *auxConfNode) setChain(blocks, headers float64, ibd bool) {
	n.mu.Lock()
	n.chain = map[string]interface{}{"blocks": blocks, "headers": headers, "initialblockdownload": ibd}
	n.mu.Unlock()
}

func (n *auxConfNode) askedAbout(hash string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.asked[hash]
}

func (n *auxConfNode) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string        `json:"method"`
		Params []interface{} `json:"params"`
	}
	b, _ := io.ReadAll(r.Body)
	json.Unmarshal(b, &req)
	var hash string
	if len(req.Params) > 0 {
		hash, _ = req.Params[0].(string)
	}
	n.mu.Lock()
	c, ok := n.confs[hash]
	chain := n.chain
	if req.Method == "getblock" {
		if n.asked == nil {
			n.asked = map[string]int{}
		}
		n.asked[hash]++
	}
	n.mu.Unlock()
	if req.Method == "getblockchaininfo" {
		if chain == nil {
			chain = map[string]interface{}{"blocks": 1000, "headers": 1000, "initialblockdownload": false}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"result": chain, "error": nil})
		return
	}
	if req.Method != "getblock" || !ok {
		io.WriteString(w, `{"result":null,"error":{"code":-5,"message":"Block not found"}}`)
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"result": map[string]interface{}{"confirmations": c}, "error": nil})
}

// useAuxConfNode points the 1175 globals at a fresh auxConfNode and logs to an observer.
func useAuxConfNode(t *testing.T) (*auxConfNode, *observer.ObservedLogs) {
	t.Helper()
	node := &auxConfNode{confs: map[string]float64{}}
	srv := httptest.NewServer(node)
	t.Cleanup(srv.Close)
	core, logs := observer.New(zapcore.DebugLevel)
	savedURL, savedUser, savedPass, savedLogger, savedJM := aux1175NodeURL, aux1175User, aux1175Pass, logger, jobManager
	aux1175NodeURL, aux1175User, aux1175Pass, logger, jobManager = srv.URL, "u", "p", zap.New(core), nil
	t.Cleanup(func() {
		aux1175NodeURL, aux1175User, aux1175Pass, logger, jobManager = savedURL, savedUser, savedPass, savedLogger, savedJM
	})
	return node, logs
}

// waitForLog waits until a log message containing one of snippets has been written.
func waitForLog(t *testing.T, logs *observer.ObservedLogs, code string, snippets ...string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range snippets {
			if logs.FilterMessageSnippet(s).Len() > 0 {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s: none of %q was logged", code, snippets)
}

// A 1175 block found while the database is down is recorded, and credited, once it is back. It
// was logged as "may be lost" and never recorded: missing from the dashboard for good, though
// the 1175 coinbase paid it.
func TestA1175BlockFoundWhileTheDatabaseIsDownIsRecordedLater(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aux.db")
	if err := stats.InitDB(path); err != nil {
		t.Fatal(err)
	}
	shortRetries(t)
	useAuxConfNode(t)
	const finder = "bitcoincashii:qfinder1175"
	hash := strings.Repeat("a", 64)

	stats.CloseDB() // the database is down when the block is found
	start := time.Now()
	aux1175BlockHandler(900, hash, 25_0000_0000, finder, true)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("DATA2-1175-NOT-BLOCKING: the handler held the submit goroutine for %v", d)
	}
	time.Sleep(50 * time.Millisecond)
	if err := stats.InitDB(path); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if got, _ := stats.Get1175BlockHashAtHeight(900); got == hash {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("DATA2-1175-DB-BACK: a 1175 block found while the database was down was never recorded once it was back")
		}
		time.Sleep(20 * time.Millisecond)
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		if n, paid, _ := stats.Miner1175Totals(finder, true); n == 1 && paid == 25 {
			break
		}
		if time.Now().After(deadline) {
			n, paid, err := stats.Miner1175Totals(finder, true)
			t.Fatalf("DATA2-1175-CREDITED: recorded, but the finder's 1175 totals are %d blocks, %v paid (%v)", n, paid, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The retry decides between siblings again once the database is back: the block on the aux chain
// stays recorded. Retrying the record alone would put the losing sibling over it.
func TestA1175SiblingRetriedAfterTheDatabaseIsBackKeepsTheBlockOnTheChain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aux.db")
	if err := stats.InitDB(path); err != nil {
		t.Fatal(err)
	}
	shortRetries(t)
	node, logs := useAuxConfNode(t)
	const finder = "bitcoincashii:qfinder1175"
	onChain, sibling := strings.Repeat("c", 64), strings.Repeat("d", 64)
	node.set(onChain, 3)
	node.set(sibling, -1)
	if err := stats.Record1175Block(901, onChain, 25, finder, true); err != nil {
		t.Fatal(err)
	}

	stats.CloseDB()
	aux1175BlockHandler(901, sibling, 25_0000_0000, finder, true)
	time.Sleep(50 * time.Millisecond)
	if err := stats.InitDB(path); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()
	waitForLog(t, logs, "DATA2-1175-SIBLING-DONE", "keeping it", "after the database came back")
	if got, _ := stats.Get1175BlockHashAtHeight(901); got != onChain {
		t.Fatalf("DATA2-1175-SIBLING: after the database came back the ledger holds %.8s…, not the block on the chain", got)
	}
}

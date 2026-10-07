package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
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
	_, logs := useAuxConfNode(t)
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
	waitForLog(t, logs, "DATA2-1175-DONE", "1175 block distributed") // the retry's last word, before the test's cleanup
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

// Another program (the api, a VACUUM) holds the SQLite write lock for longer than two writes wait
// for it. SQLite answers "database is locked" (SQLITE_BUSY) to the 1175 block's record and to the
// retry's first try. The record is tried again until the lock is free, as for any other error,
// and the block is then in the ledger and credited.
func TestAuxRecordSurvivesHeldLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aux.db")
	if err := stats.InitDB(path); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()
	shortRetries(t)
	blockRecordRetryFor = time.Minute // shortRetries puts it back
	busy := sqliteBusyWait(t)
	_, logs := useAuxConfNode(t)
	const finder = "bitcoincashii:qheldfinder0000"
	hash := strings.Repeat("e", 64)

	ctx := context.Background()
	other, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	holder, err := other.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if _, err := holder.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	held := time.Now()
	if _, err := holder.ExecContext(ctx, `INSERT INTO shares (miner_address, difficulty) VALUES ('api', 1)`); err != nil {
		t.Fatal(err)
	}

	go aux1175BlockHandler(902, hash, 25_0000_0000, finder, true) // as the submit goroutine does
	failed := waitForLogWithin(t, logs, busy+5*time.Second, "AUX-HELD-SETUP", "Failed to record a 1175 block")
	if e, _ := failed.ContextMap()["error"].(string); !strings.Contains(e, "SQLITE_BUSY") {
		t.Fatalf("AUX-HELD-SETUP: the first record failed with %q, not SQLITE_BUSY", e)
	}
	// Held through two busy waits: the handler's record and the retry's first try both fail, so the
	// block reaches the ledger only through a retry that tries a busy database again.
	time.Sleep(time.Until(held.Add(2*busy + 5*time.Second)))
	if _, err := holder.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatal(err)
	}

	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if got, _ := stats.Get1175BlockHashAtHeight(902); got == hash {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("AUX-HELD-RECORD: the 1175 block was never recorded once the other program let the lock go")
		}
	}
	waitForLogWithin(t, logs, 15*time.Second, "AUX-HELD-DONE", "1175 block distributed") // the retry's last word
	run1175PayoutCycle()
	if left, err := stats.UndistributedBlocks1175(); err != nil || len(left) != 0 {
		t.Fatalf("AUX-HELD-CREDIT: still undistributed after the sweep: %v (%v)", left, err)
	}
	if n, paid, err := stats.Miner1175Totals(finder, true); err != nil || n != 1 || paid != 25 {
		t.Fatalf("AUX-HELD-CREDIT: the finder's 1175 totals are %d blocks, %v paid (%v), want 1 paying 25", n, paid, err)
	}
}

// waitForLogWithin waits up to d for a log message containing snippet, and returns it.
func waitForLogWithin(t *testing.T, logs *observer.ObservedLogs, d time.Duration, code, snippet string) observer.LoggedEntry {
	t.Helper()
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if got := logs.FilterMessageSnippet(snippet).All(); len(got) > 0 {
			return got[0]
		}
	}
	t.Fatalf("%s: %q was not logged within %v", code, snippet, d)
	return observer.LoggedEntry{}
}

// sqliteBusyWait is how long a write waits for another program's write lock before SQLite answers
// SQLITE_BUSY: the busy_timeout in stats.SQLiteDSN.
func sqliteBusyWait(t *testing.T) time.Duration {
	t.Helper()
	dsn := stats.SQLiteDSN("forgesolo.db")
	_, query, _ := strings.Cut(dsn, "?")
	q, err := url.ParseQuery(query)
	if err != nil {
		t.Fatalf("AUX-HELD-SETUP: %q: %v", dsn, err)
	}
	ms, err := strconv.Atoi(q.Get("_busy_timeout"))
	if err != nil || ms <= 0 {
		t.Fatalf("AUX-HELD-SETUP: no busy_timeout in %q", dsn)
	}
	return time.Duration(ms) * time.Millisecond
}

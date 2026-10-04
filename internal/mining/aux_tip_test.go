package mining

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// tipNode is a 1175 node whose tip the test moves. Each getauxblock call makes new work on the
// tip, as 1175 does; the calls are counted.
type tipNode struct {
	mu        sync.Mutex
	tip       string
	failWork  bool // getauxblock answers an error
	workCalls int
	tipCalls  int
}

func (n *tipNode) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string `json:"method"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	n.mu.Lock()
	defer n.mu.Unlock()
	switch {
	case req.Method == "getbestblockhash":
		n.tipCalls++
		json.NewEncoder(w).Encode(map[string]any{"result": n.tip, "error": nil})
	case req.Method == "getauxblock" && !n.failWork:
		n.workCalls++
		json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{
			"hash": fmt.Sprintf("%064x", n.workCalls), "chainid": 1175, "previousblockhash": n.tip, "target": repeatStr("f", 64),
		}, "error": nil})
	default:
		if req.Method == "getauxblock" {
			n.workCalls++
		}
		json.NewEncoder(w).Encode(map[string]any{"result": nil, "error": map[string]any{"code": -1, "message": "no"}})
	}
}

func (n *tipNode) set(tip string, failWork bool) {
	n.mu.Lock()
	n.tip, n.failWork = tip, failWork
	n.mu.Unlock()
}

func (n *tipNode) counts() (work, tips int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.workCalls, n.tipCalls
}

// mergeMining is a job manager merge-mining against a fresh tipNode, asking for its tip every
// poll and for work every fetch when the tip stands still.
func mergeMining(t *testing.T, poll, fetch time.Duration) (*JobManager, *tipNode) {
	t.Helper()
	node := &tipNode{tip: repeatStr("1", 64)}
	srv := httptest.NewServer(node)
	jm := &JobManager{pubkeyHash: make([]byte, 20), auxPollEvery: poll, auxFetchEvery: fetch}
	t.Cleanup(func() {
		jm.DisableMergeMining()
		time.Sleep(2 * poll) // the loop sees it is off and stops
		srv.CloseClientConnections()
		srv.Close()
	})
	jm.EnableMergeMining(srv.URL, "u", "p", "esf1")
	if w, _ := jm.AuxWorkNow(); w == nil || w.PreviousBlockHash != repeatStr("1", 64) {
		t.Fatalf("AUX-TIP-SETUP: %+v", w)
	}
	return jm, node
}

// A 1175 block found by anyone moves the 1175 tip, and the work follows it within a poll: it was
// fetched every 15 s whatever happened. The node is asked for its tip, a cheap call, every poll,
// and for work only once for the new tip, so work items are minted no faster than before.
func TestAuxWorkFollowsANewTip(t *testing.T) {
	jm, node := mergeMining(t, 10*time.Millisecond, time.Hour)
	tip2 := repeatStr("2", 64)
	node.set(tip2, false)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if w, _ := jm.AuxWorkNow(); w != nil && w.PreviousBlockHash == tip2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("AUX-TIP-FOLLOW: the work stayed on the old 1175 tip")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if work, tips := node.counts(); work != 2 || tips < 5 {
		t.Fatalf("AUX-TIP-NO-MINT: %d getauxblock calls (want 2: the start and the new tip), %d tip polls", work, tips)
	}
}

// Work for a new tip that cannot be fetched is tried once, then by age, not on every poll.
func TestAuxWorkForANewTipIsTriedOnce(t *testing.T) {
	_, node := mergeMining(t, 10*time.Millisecond, time.Hour)
	node.set(repeatStr("2", 64), true)
	time.Sleep(300 * time.Millisecond)
	if work, _ := node.counts(); work != 2 {
		t.Fatalf("AUX-TIP-ONCE: %d getauxblock calls in 30 polls, want 2 (the start and one for the new tip)", work)
	}
}

// While the tip stands still the work is still fetched by age, and only by age.
func TestAuxWorkIsRefreshedByAge(t *testing.T) {
	_, node := mergeMining(t, 10*time.Millisecond, 60*time.Millisecond)
	time.Sleep(400 * time.Millisecond)
	if work, _ := node.counts(); work < 4 || work > 10 {
		t.Fatalf("AUX-AGE-REFRESH: %d getauxblock calls in 400 ms at one per 60 ms", work)
	}
}

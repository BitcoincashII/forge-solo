package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/mining"
)

// auxWorkNode is a 1175 node whose work depends on the address it pays, on a tip the test sets.
type auxWorkNode struct {
	mu  sync.Mutex
	tip string
}

func (n *auxWorkNode) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string   `json:"method"`
		Params []string `json:"params"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	n.mu.Lock()
	tip := n.tip
	n.mu.Unlock()
	switch req.Method {
	case "getauxblock":
		h := sha256.Sum256([]byte(req.Params[0] + tip))
		json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{
			"hash": hex.EncodeToString(h[:]), "chainid": 1175, "previousblockhash": tip, "height": 206,
			"target": strings.Repeat("f", 64), "coinbasevalue": 25_0000_0000, "bits": "207fffff",
		}, "error": nil})
	case "getbestblockhash":
		json.NewEncoder(w).Encode(map[string]any{"result": tip, "error": nil})
	default:
		json.NewEncoder(w).Encode(map[string]any{"result": nil, "error": map[string]any{"code": -32601, "message": "Method not found"}})
	}
}

// auxJobManager is a job manager paying testAddr(1) and merge-mining against a fresh auxWorkNode.
func auxJobManager(t *testing.T) (*mining.JobManager, *auxWorkNode, *httptest.Server) {
	t.Helper()
	node := &auxWorkNode{tip: strings.Repeat("11", 32)}
	srv := httptest.NewServer(node)
	jm := mining.NewJobManager("", "", "", "", "")
	if err := jm.SetPoolAddress(testAddr(1)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		jm.DisableMergeMining()
		srv.CloseClientConnections()
		srv.Close()
	})
	return jm, node, srv
}

var auxTestTemplate = &mining.BlockTemplate{Version: 0x20000000, PreviousBlockHash: strings.Repeat("00", 32), Bits: "1902c9b9",
	Height: 83470, CurTime: 1_700_000_000, CoinbaseValue: 50_0000_0000}

// The dashboard re-points or clears the 1175 address: the next job comes at once, carries work
// paying the new address (or none), and makes miners drop the work whose 1175 block pays the old
// one. The job manager has the new work at once, but no job was due until the periodic one, and
// that one went out without clean_jobs.
func TestA1175AddressChangeOnTheDashboardMovesMinersAtOnce(t *testing.T) {
	jm, _, srv := auxJobManager(t)
	due := func(cur *mining.Job) bool {
		_, auxPayTo := jm.AuxWorkNow()
		return jobDue(cur, false, false, false, false, jm.PayoutAddress(), auxPayTo)
	}
	jm.EnableMergeMining(srv.URL, "u", "p", "esf1old")
	job1 := jm.CreateJob(auxTestTemplate)
	if job1 == nil || job1.AuxWork == nil || job1.AuxPayTo != "esf1old" {
		t.Fatalf("AUX-REPOINT-SETUP: the first job %+v", job1)
	}

	jm.EnableMergeMining(srv.URL, "u", "p", "esf1new") // what watchPoolConfig does
	if !due(job1) {
		t.Fatal("AUX-REPOINT-DUE: after the 1175 re-point no job is due until the periodic one")
	}
	job2 := jm.CreateJob(auxTestTemplate)
	if job2.AuxPayTo != "esf1new" || job2.AuxWork == nil || job2.AuxWork.Hash == job1.AuxWork.Hash {
		t.Fatalf("AUX-REPOINT-WORK: the next job says it pays %q with work %+v", job2.AuxPayTo, job2.AuxWork)
	}
	if !mustDropWork(job1, job2, false) {
		t.Fatal("AUX-REPOINT-CLEAN: the next job let miners keep work paying the old 1175 address")
	}
	if due(job2) || mustDropWork(job2, jm.CreateJob(auxTestTemplate), false) {
		t.Fatal("AUX-REPOINT-SETTLED: with nothing changed since, a job is due or miners drop their work")
	}

	// A job built while the 1175 node gives no work still names the 1175 address in effect, so a
	// node gone quiet for a moment does not make miners drop their work.
	quiet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Work queue depth exceeded", http.StatusServiceUnavailable)
	}))
	defer quiet.Close()
	jm.EnableMergeMining(quiet.URL, "u", "p", "esf1new")
	if job := jm.CreateJob(auxTestTemplate); job.AuxWork != nil || job.AuxPayTo != "esf1new" || mustDropWork(job2, job, false) {
		t.Fatalf("AUX-NO-WORK-SAME-ADDRESS: a job without 1175 work says it is for %q, and clean_jobs=%v", job.AuxPayTo, mustDropWork(job2, job, false))
	}

	jm.DisableMergeMining() // the address cleared
	if !due(job2) {
		t.Fatal("AUX-CLEARED-DUE: after the 1175 address was cleared no job is due until the periodic one")
	}
	if job3 := jm.CreateJob(auxTestTemplate); job3.AuxWork != nil || !mustDropWork(job2, job3, false) {
		t.Fatalf("AUX-CLEARED-CLEAN: the next job carries %+v, and clean_jobs=%v", job3.AuxWork, mustDropWork(job2, job3, false))
	}
}

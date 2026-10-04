package stratum

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/mergemining"
)

// fake1175 answers submitauxblock and getblock as a 1175 node does. It takes the block unless
// told otherwise, and a node that took a block no longer has its work: a second submit is -8.
type fake1175 struct {
	mu        sync.Mutex
	busy      int           // submits still to answer HTTP 503 "Work queue depth exceeded"; -1: always
	warm      int           // submits still to answer -28, as a starting node does
	slow      time.Duration // the first submit is taken, and answered this late
	verdict   string        // "gone": the work is not there (-8); "refuse": the block is refused (-25)
	taken     bool          // the block is on the node's chain
	submits   int
	getblocks int
}

func (n *fake1175) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string `json:"method"`
	}
	b, _ := io.ReadAll(r.Body)
	json.Unmarshal(b, &req)
	rpcErr := func(code int, msg string) {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]any{"result": nil, "error": map[string]any{"code": code, "message": msg}})
	}
	n.mu.Lock()
	switch req.Method {
	case "submitauxblock":
		n.submits++
		switch {
		case n.busy != 0:
			if n.busy > 0 {
				n.busy--
			}
			n.mu.Unlock()
			http.Error(w, "Work queue depth exceeded", http.StatusServiceUnavailable)
			return
		case n.warm > 0:
			n.warm--
			n.mu.Unlock()
			rpcErr(-28, "Loading block index…")
			return
		case n.taken || n.verdict == "gone":
			n.mu.Unlock()
			rpcErr(-8, "Block hash not found in pending work")
			return
		case n.verdict == "refuse":
			n.mu.Unlock()
			rpcErr(-25, "Block rejected: AuxPoW check failed")
			return
		}
		n.taken = true
		slow := n.slow
		n.mu.Unlock()
		time.Sleep(slow)
		io.WriteString(w, `{"result":true,"error":null}`)
	case "getblock":
		n.getblocks++
		taken := n.taken
		n.mu.Unlock()
		if !taken {
			rpcErr(-5, "Block not found")
			return
		}
		io.WriteString(w, `{"result":{"confirmations":1},"error":null}`)
	default:
		n.mu.Unlock()
		rpcErr(-32601, "Method not found")
	}
}

func (n *fake1175) counts() (submits, getblocks int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.submits, n.getblocks
}

// shortAuxRetries makes the 1175 submit retries take milliseconds.
func shortAuxRetries(t *testing.T) {
	t.Helper()
	a, b, c := auxRetryFirstWait, auxRetryMaxWait, auxSubmitRetryFor
	auxRetryFirstWait, auxRetryMaxWait, auxSubmitRetryFor = 5*time.Millisecond, 20*time.Millisecond, 2*time.Second
	t.Cleanup(func() { auxRetryFirstWait, auxRetryMaxWait, auxSubmitRetryFor = a, b, c })
}

// auxJob is a job committing to 1175 work that any header solves.
func auxJob(id string) *Job {
	j := soloTestJob(id)
	j.AuxWork = &mergemining.AuxWork{Hash: strings.Repeat("ab", 32), Height: 205, CoinbaseValue: 25_0000_0000,
		Target: strings.Repeat("f", 64)}
	return j
}

// auxServer is a solo server merge-mining against node, counting the 1175 blocks it reports found
// and the finders it reports them for.
func auxServer(t *testing.T, node *fake1175, timeout time.Duration) (*Server, *captureProcessor, *atomic.Int32, chan string) {
	t.Helper()
	srv := httptest.NewServer(node)
	t.Cleanup(srv.Close)
	s, cp := perJobServer()
	ac := mergemining.NewClient(srv.URL, "u", "p")
	if timeout > 0 {
		ac.HTTP.Timeout = timeout
	}
	s.EnableMergeMining(ac)
	var found atomic.Int32
	finders := make(chan string, 16)
	s.SetAuxBlockHandler(func(height int64, hash string, value int64, finder string, isSolo bool) {
		found.Add(1)
		finders <- finder
	})
	return s, cp, &found, finders
}

func submitAuxNow(s *Server, job *Job) {
	s.submitAux(job, "01000001", "0000000000000001", job.NTime, "00000000", "", testPayout, true)
}

// A solved 1175 block is not given up after one try. A 1175 node that is busy (503), still
// starting (-28) or slow to answer is asked again, and one that took the block but answered too
// late is asked whether it has it: the block was dropped after one failed request, though one more
// would have been accepted (shown on a regtest 1175 node).
func TestASolved1175BlockOutlastsABusyOrSlowNode(t *testing.T) {
	shortAuxRetries(t)

	busy := &fake1175{busy: 2}
	s, _, found, _ := auxServer(t, busy, 0)
	submitAuxNow(s, auxJob("a"))
	if n, _ := busy.counts(); found.Load() != 1 || n != 3 {
		t.Fatalf("AUX-RETRY-BUSY: a node busy for its first 2 requests: block found %d times after %d submits", found.Load(), n)
	}

	warm := &fake1175{warm: 2}
	s, _, found, _ = auxServer(t, warm, 0)
	submitAuxNow(s, auxJob("a"))
	if n, _ := warm.counts(); found.Load() != 1 || n != 3 {
		t.Fatalf("AUX-RETRY-WARMUP: a node starting for its first 2 requests: block found %d times after %d submits", found.Load(), n)
	}

	late := &fake1175{slow: 300 * time.Millisecond}
	s, _, found, _ = auxServer(t, late, 100*time.Millisecond)
	submitAuxNow(s, auxJob("a"))
	if found.Load() != 1 {
		n, g := late.counts()
		t.Fatalf("AUX-RETRY-LATE-ACCEPT: the node took the block but answered late: found %d times (%d submits, %d getblock)", found.Load(), n, g)
	}
}

// What the node decides is final: a block it refused, or work it no longer has (it restarted, so
// no retry can help), is not sent again and again; and a node that never answers is given up on.
func TestASolved1175BlockIsNotRetriedOnceTheNodeHasDecided(t *testing.T) {
	shortAuxRetries(t)

	refused := &fake1175{verdict: "refuse"}
	s, _, found, _ := auxServer(t, refused, 0)
	submitAuxNow(s, auxJob("a"))
	if n, _ := refused.counts(); found.Load() != 0 || n != 1 {
		t.Fatalf("AUX-RETRY-REFUSED: a refused block: found %d times, %d submits", found.Load(), n)
	}

	restarted := &fake1175{verdict: "gone"}
	s, _, found, _ = auxServer(t, restarted, 0)
	submitAuxNow(s, auxJob("a"))
	if n, g := restarted.counts(); found.Load() != 0 || n != 1 || g != 1 {
		t.Fatalf("AUX-RETRY-RESTARTED: work the node no longer has: found %d times, %d submits, %d getblock", found.Load(), n, g)
	}

	never := &fake1175{busy: -1}
	s, _, found, _ = auxServer(t, never, 0)
	done := make(chan struct{})
	go func() { submitAuxNow(s, auxJob("a")); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("AUX-RETRY-GIVES-UP: still trying a node that never answers, past the retry window")
	}
	if found.Load() != 0 {
		t.Fatal("AUX-RETRY-GIVES-UP: a block the node never took was reported found")
	}
}

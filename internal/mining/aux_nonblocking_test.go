package mining

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// A HANGING aux node must not delay BCH2 job building.
//
// This is the property the async aux poller exists for, and it is specifically NOT what a
// dead node tests: a refused connection returns in microseconds, so the old synchronous
// implementation survived that too. Only a node that accepts the connection and then never
// answers -- mid-restart, reindex, disk stall, all routine on an Umbrel -- exposes the
// difference, and nothing in this repo caught it. Hence this test.
//
// Under the old code every CreateJob blocked on the HTTP timeout, including the job that
// follows a new block, which is orphan exposure on the main chain caused by the optional
// side chain.
func TestCreateJobNeverBlocksOnAHangingAuxNode(t *testing.T) {
	hang := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-hang // accept, then never answer
	}))
	jm := &JobManager{pubkeyHash: make([]byte, 20)}
	// One defer, explicit order. httptest's Close waits for every connection to go idle and
	// the background poller keeps one active, so the poller has to be stopped and client
	// connections force-closed first, or Close blocks forever. (It did.)
	defer func() {
		jm.DisableMergeMining()
		close(hang)
		srv.CloseClientConnections()
		srv.Close()
	}()
	// EnableMergeMining primes once inline; that is the startup path, and it is bounded by
	// the client timeout. What must stay fast is every job built afterwards.
	jm.EnableMergeMining(srv.URL, "u", "p", "esf1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq")

	template := &BlockTemplate{
		Version:           0x20000000,
		PreviousBlockHash: "00000000000000000000000000000000000000000000000000000000000000ff",
		CoinbaseValue:     5000000000,
		Bits:              "207fffff",
		Height:            250,
		CurTime:           1000000,
		Target:            "7fffff0000000000000000000000000000000000000000000000000000000000",
	}

	const jobs = 50
	start := time.Now()
	for i := 0; i < jobs; i++ {
		if job := jm.CreateJob(template); job == nil {
			t.Fatalf("CreateJob returned nil on iteration %d", i)
		}
	}
	elapsed := time.Since(start)

	// Generous by three orders of magnitude against the failure it guards: the old code
	// would have spent the full HTTP timeout PER JOB here, i.e. minutes.
	if elapsed > 2*time.Second {
		t.Errorf("%d job builds took %v against a hanging aux node — the aux fetch is back on the job-build path", jobs, elapsed)
	}
	t.Logf("%d job builds against a hanging aux node in %v", jobs, elapsed)
}

// And a hanging aux node must not produce a commitment either: better no merge mining than
// a coinbase committing to work nobody can submit.
func TestHangingAuxNodeYieldsNoCommitment(t *testing.T) {
	hang := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-hang
	}))
	jm := &JobManager{pubkeyHash: make([]byte, 20)}
	defer func() {
		jm.DisableMergeMining()
		close(hang)
		srv.CloseClientConnections()
		srv.Close()
	}()
	jm.EnableMergeMining(srv.URL, "u", "p", "esf1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq")

	if work, commitment := jm.fetchAuxWork(); work != nil || commitment != nil {
		t.Errorf("hanging aux node yielded work=%v commitment=%v, want neither", work, commitment)
	}
	if h := jm.AuxHealth(); h.LastErr == "" {
		t.Error("aux health records no error after a hanging fetch; the fault would be invisible")
	}
}

// Re-pointing the 1175 payout address must not be overwritten by a poll that started for the
// OLD address. The window is the RPC duration plus one poll interval, and what is at stake
// is where a 1175 block pays -- so a generation counter invalidates in-flight work.
func TestRepointDiscardsWorkFetchedForTheOldAddress(t *testing.T) {
	// atomic, not a plain string: the handler runs on the server's goroutine while the
	// test body reads it, which is a data race the -race build is entitled to report.
	var served atomic.Value
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond) // in flight while the address changes
		served.Store("old")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"hash":"` + repeatStr("a", 64) + `","chainid":1175,"target":"7f"},"error":null,"id":1}`))
	}))
	hang := make(chan struct{})
	newSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-hang }))

	jm := &JobManager{pubkeyHash: make([]byte, 20)}
	defer func() {
		jm.DisableMergeMining()
		close(hang)
		slow.CloseClientConnections()
		newSrv.CloseClientConnections()
		slow.Close()
		newSrv.Close()
	}()

	jm.EnableMergeMining(slow.URL, "u", "p", "esf1old")
	// Immediately re-point at a different node/address. Any work still in flight for the
	// old one must be discarded rather than written back over the new generation.
	jm.EnableMergeMining(newSrv.URL, "u", "p", "esf1new")

	time.Sleep(700 * time.Millisecond) // let the old in-flight request land

	if h := jm.AuxHealth(); h.Payout != "esf1new" {
		t.Fatalf("payout = %q, want the re-pointed address", h.Payout)
	}
	if work, _ := jm.fetchAuxWork(); work != nil {
		t.Errorf("aux work is being served after a re-point (%v); it was fetched for the previous address (served=%v)", work, served.Load())
	}
}

// repeatStr, not named "strings": a helper shadowing a stdlib package name breaks the next
// person who adds an import to this file.
func repeatStr(c string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += c
	}
	return out
}

package mining

import (
	"net/http"
	"net/http/httptest"
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

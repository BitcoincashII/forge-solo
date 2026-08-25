package mining

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A re-point must never leave work fetched for the OLD payout address in place.
//
// auxRefreshLoop read client/payout in one lock acquisition and gen in a second.
// EnableMergeMining landing in that gap paired the OLD client and payout with the NEW
// generation, so refreshAuxOnce's guard passed -- gen matched, having been read after the
// increment -- and work fetched for the previous address was stored as current. A 1175
// block merge-mined on that work pays the address the operator just changed away from.
//
// This drives the interleaving directly rather than waiting on sleeps, so it fails
// reliably against the two-acquisition version instead of only on a loaded CI runner.
func TestRepointNeverStoresWorkFromTheOldAddress(t *testing.T) {
	var oldHits, newHits int64

	// The old node answers with a recognisable hash; the new one with a different hash.
	oldSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&oldHits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"hash":"` + repeatStr("a", 64) + `","chainid":1175,"target":"7f"},"error":null,"id":1}`))
	}))
	defer oldSrv.Close()
	newSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&newHits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"hash":"` + repeatStr("b", 64) + `","chainid":1175,"target":"7f"},"error":null,"id":1}`))
	}))
	defer newSrv.Close()

	jm := &JobManager{pubkeyHash: make([]byte, 20)}
	defer jm.DisableMergeMining()

	jm.EnableMergeMining(oldSrv.URL, "u", "p", "esf1old")

	// Hammer the re-point against the running refresh loop. If the loop can ever pair the
	// old client with the new generation, one of these iterations catches it.
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if i%2 == 0 {
				jm.EnableMergeMining(newSrv.URL, "u", "p", "esf1new")
			} else {
				jm.EnableMergeMining(oldSrv.URL, "u", "p", "esf1old")
			}
			jm.RefreshAuxNow()
		}
	}()

	// Read the payout address and the stored work under ONE lock acquisition.
	//
	// The invariant only holds instantaneously: refreshAuxOnce writes auxWork under the
	// lock while the generation still matches, and auxPayout changes only in
	// EnableMergeMining, which bumps the generation and clears auxWork in the same
	// critical section. Calling AuxHealth() and fetchAuxWork() separately observes a torn
	// pair across a re-point and reports a defect that is not there -- the same mistake
	// this test exists to catch in the production loop.
	snapshot := func() (string, string) {
		jm.mu.RLock()
		defer jm.mu.RUnlock()
		if !jm.auxEnabled || jm.auxWork == nil || jm.auxCommitment == nil {
			return jm.auxPayout, ""
		}
		if time.Since(jm.auxWorkAt) > auxWorkMaxAge {
			return jm.auxPayout, ""
		}
		return jm.auxPayout, jm.auxWork.Hash
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		payout, hash := snapshot()
		if hash == "" {
			continue
		}
		// The stored work must always belong to the address currently in effect.
		wantHash := repeatStr("a", 64)
		if payout == "esf1new" {
			wantHash = repeatStr("b", 64)
		}
		if hash != wantHash {
			close(stop)
			wg.Wait()
			t.Fatalf("payout is %q but the stored aux work came from the other node "+
				"(hash %s, want %s) — a 1175 block found on this work would pay the "+
				"previous address", payout, hash[:8], wantHash[:8])
		}
	}
	close(stop)
	wg.Wait()

	if atomic.LoadInt64(&oldHits) == 0 || atomic.LoadInt64(&newHits) == 0 {
		t.Fatalf("both nodes must have been polled for this to prove anything (old=%d new=%d)",
			atomic.LoadInt64(&oldHits), atomic.LoadInt64(&newHits))
	}
}

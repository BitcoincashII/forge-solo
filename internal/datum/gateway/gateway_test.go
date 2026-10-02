package gateway

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/datum/wire"
)

func testKey(t *testing.T) ed25519.PrivateKey {
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// The client New builds follows no redirect: the pool's address was checked to be https, and a
// redirect could send the request anywhere, plain http too.
func TestTheClientFollowsNoRedirect(t *testing.T) {
	var hits atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer elsewhere.Close()
	pool := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer pool.Close()
	c := New(pool.URL, testKey(t))
	if _, err := c.Snapshot(); err == nil {
		t.Fatal("TIDES3-CLIENT-REDIRECT: a redirected snapshot was taken")
	}
	c.Register(&wire.JobRequest{}, &Template{}, 1, 0)
	if n := hits.Load(); n != 0 {
		t.Fatalf("TIDES3-CLIENT-REDIRECT: %d requests followed the redirect", n)
	}
}

// A failed answer's page goes into the error as one short piece, not whole: errors are logged, and
// a container's log is kept until the container is replaced.
func TestAnErrorPageIsClipped(t *testing.T) {
	page := strings.Repeat("<p>error</p>", 100_000) // 1.2 MB
	pool := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, page, http.StatusInternalServerError)
	}))
	defer pool.Close()
	c := New(pool.URL, testKey(t))
	_, err := c.Snapshot()
	if n := errLen(err); n == 0 || n > 2*maxErrorText {
		t.Fatalf("TIDES5-SNAPSHOT-ERROR-TEXT: the error is %d bytes", n)
	}
	_, err = c.Register(&wire.JobRequest{}, &Template{}, 1, 0)
	if n := errLen(err); n == 0 || n > 2*maxErrorText {
		t.Fatalf("TIDES5-POST-ERROR-TEXT: the error is %d bytes", n)
	}
}

func errLen(err error) int {
	if err == nil {
		return 0
	}
	return len(err.Error())
}

// Register's wait between tries ends with its context, and there is no wait after the last try:
// the caller decides whether to ask again, and the job loop is waiting on it.
func TestRegisterWaitsNoLongerThanItMust(t *testing.T) {
	pool := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(wire.JobResponse{Error: "behind", Retry: true})
	}))
	defer pool.Close()
	c := New(pool.URL, testKey(t))

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.RegisterCtx(ctx, &wire.JobRequest{}, &Template{}, 2, 10*time.Second); err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("TIDES1-CTX-WAIT: the wait outlived its context: %v after %s", err, time.Since(start))
	}

	start = time.Now()
	if _, err := c.RegisterCtx(context.Background(), &wire.JobRequest{}, &Template{}, 1, 10*time.Second); err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("TIDES1-LAST-WAIT: Register waited after its last try: %v after %s", err, time.Since(start))
	}
}

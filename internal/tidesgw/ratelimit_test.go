package tidesgw

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/datum/wire"
)

// poolBucket is forge-pool's per-gateway registration limiter (internal/datum/http.go: 2 a second,
// burst 10), copied so the test holds the gateway to the pool's real budget.
type poolBucket struct {
	tokens float64
	last   time.Time
}

func (b *poolBucket) take(now time.Time) bool {
	const rate, burst = 2.0, 10.0
	if b.last.IsZero() {
		b.tokens = burst
	} else if b.tokens += now.Sub(b.last).Seconds() * rate; b.tokens > burst {
		b.tokens = burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// The pool answers "retry" for 3 s (its node a block behind), then takes the job, behind its own
// rate limit. Asking with no wait and then every 250 ms spent the burst in about 1.5 s; the pool
// answered "429 slow down", the registration ended, and on a new block the miners went solo for a
// minute. Paced, it registers well inside the 6 s deadline without ever being limited.
func TestRegisterRetriesWithinThePoolsRateLimit(t *testing.T) {
	var mu sync.Mutex
	var b poolBucket
	var posts, limited int
	var start time.Time
	snap := wire.Snapshot{Version: 7, Height: 83361, PrevHash: prevHash, Dust: 546,
		Work: map[string]float64{}, Carry: map[string]int64{}, At: time.Now()}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			json.NewEncoder(w).Encode(snap)
			return
		}
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		if start.IsZero() {
			start = time.Now()
		}
		ok := b.take(time.Now())
		posts++
		if !ok {
			limited++
		}
		elapsed := time.Since(start)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case !ok:
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]string{"error": "slow down"})
		case elapsed < 3*time.Second:
			json.NewEncoder(w).Encode(wire.JobResponse{Error: "the pool's node is still at height 83359", Retry: true})
		default:
			json.NewEncoder(w).Encode(wire.JobResponse{JobID: "late", ShareDifficulty: 1024})
		}
	}))
	defer srv.Close()
	_, key, _ := ed25519.GenerateKey(nil)
	g := New(Config{PoolURL: srv.URL, Key: key}) // production defaults: RegisterFor 6 s
	reg, err := g.Register(template(), me, nil)
	mu.Lock()
	defer mu.Unlock()
	if err != nil || reg == nil {
		t.Fatalf("not registered after %d posts (%d rate-limited): %v", posts, limited, err)
	}
	if limited != 0 {
		t.Fatalf("%d of %d registration posts were rate-limited", limited, posts)
	}
}

// A proxy's HTML error page is not the reason the dashboard shows.
func TestBriefReasonDropsHTMLAndCapsLength(t *testing.T) {
	page := errors.New("/datum/v1/jobs: 502 Bad Gateway: <html>\r\n<head><title>502 Bad Gateway</title></head>\r\n<body>nginx</body></html>")
	if got := briefReason(page); got != "/datum/v1/jobs: 502 Bad Gateway" {
		t.Fatalf("briefReason = %q", got)
	}
	long := errors.New("pool says: " + strings.Repeat("x", 5000))
	if got := briefReason(long); len([]rune(got)) != maxReason+1 || !strings.HasSuffix(got, "…") {
		t.Fatalf("a long reason is %d characters", len([]rune(got)))
	}
	if got := briefReason(errors.New("line one\nline two")); got != "line one line two" {
		t.Fatalf("briefReason = %q", got)
	}
}

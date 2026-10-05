package stratum

import (
	"bufio"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// A connection's own messages may write clientLogBudget lines a minute; what is left out is counted,
// and the next line written says how many.
func TestLogLimit(t *testing.T) {
	var l logLimit
	now := time.Unix(1_000_000, 0)
	for i := 0; i < clientLogBudget; i++ {
		if ok, _ := l.take(now, clientLogBudget); !ok {
			t.Fatalf("LOG-BUDGET: line %d of %d refused", i+1, clientLogBudget)
		}
	}
	for i := 0; i < 10; i++ {
		if ok, _ := l.take(now.Add(time.Second), clientLogBudget); ok {
			t.Fatalf("LOG-BUDGET: line %d past the budget was allowed", clientLogBudget+i+1)
		}
	}
	if ok, skipped := l.take(now.Add(61*time.Second), clientLogBudget); !ok || skipped != 10 {
		t.Fatalf("LOG-SUPPRESSED-COUNT: after the minute got (%v, %d), want (true, 10)", ok, skipped)
	}
}

// A count given back, because the line that was to say it was left out, is said with the next line.
func TestLogLimitSaysACountGivenBack(t *testing.T) {
	var l logLimit
	now := time.Unix(1_000_000, 0)
	for i := 0; i < clientLogBudget+3; i++ {
		l.take(now, clientLogBudget)
	}
	_, skipped := l.take(now.Add(61*time.Second), clientLogBudget)
	l.giveBack(skipped)
	if ok, skipped := l.take(now.Add(62*time.Second), clientLogBudget); !ok || skipped != 3 {
		t.Fatalf("LOGB-GIVE-BACK: got (%v, %d), want (true, 3)", ok, skipped)
	}
}

// The miners' budget holds a burst, then refills at its rate, never past the burst.
func TestLogBucketHoldsABurst(t *testing.T) {
	var b logBucket
	now := time.Unix(1_000_000, 0)
	for i := 0; i < minerLogBurst; i++ {
		if !b.take(now, minerLogBurst, minerLogRate) {
			t.Fatalf("LOGB-BUCKET-BURST: line %d of a burst of %d refused", i+1, minerLogBurst)
		}
	}
	if b.take(now, minerLogBurst, minerLogRate) {
		t.Fatal("LOGB-BUCKET-PAST: a line past the burst was allowed")
	}
	taken := func(at time.Time) int {
		n := 0
		for n <= 2*minerLogBurst && b.take(at, minerLogBurst, minerLogRate) {
			n++
		}
		return n
	}
	if n := taken(now.Add(30 * time.Second)); n != minerLogRate/2 {
		t.Fatalf("LOGB-BUCKET-RATE: half a minute on, %d lines allowed, want %d", n, minerLogRate/2)
	}
	// A caller that read the clock before the last one adds nothing, now or later.
	taken(now.Add(29 * time.Second))
	if n := taken(now.Add(60 * time.Second)); n != minerLogRate/2 {
		t.Fatalf("LOGB-BUCKET-CLOCK: after a line at an earlier time, half a minute on %d lines allowed, want %d", n, minerLogRate/2)
	}
	if n := taken(now.Add(time.Hour)); n != minerLogBurst {
		t.Fatalf("LOGB-BUCKET-FULL: an hour on, %d lines allowed, want the burst, %d", n, minerLogBurst)
	}
}

// A client sending refused submits as fast as it can writes no more than the budget to the log:
// about 39 KB sent used to replace the whole 30 MB log kept on Umbrel.
func TestClientMessagesWriteABudgetedLog(t *testing.T) {
	s, _ := perJobServer()
	core, logs := observer.New(zap.InfoLevel)
	s.logger = zap.New(core)
	c := perJobClient(t)
	for i := 0; i < 200; i++ {
		params, _ := json.Marshal([]string{"rig", "ff", "0000000000000001", "6aba7069", fmt.Sprintf("%08x", i)})
		s.handleSubmit(c, &Request{ID: 1, Method: MethodSubmit, Params: params})
	}
	if n := logs.FilterMessage("Job not found").Len(); n == 0 || n > clientLogBudget {
		t.Fatalf("LOG-FLOOD: 200 refused submits wrote %d lines, want 1..%d", n, clientLogBudget)
	}
}

// What a client says about itself is kept and logged only in part.
func TestUserAgentIsClipped(t *testing.T) {
	s, _ := perJobServer()
	c := perJobClient(t)
	params, _ := json.Marshal([]string{strings.Repeat("u", 60000)})
	s.handleSubscribe(c, &Request{ID: 1, Method: MethodSubscribe, Params: params})
	if len(c.UserAgent) > maxUserAgent+len("…") {
		t.Fatalf("LOG-UA-CLIP: kept a %d-byte user agent", len(c.UserAgent))
	}
}

// A subscribed client may send a few lines that are not JSON, then it is disconnected.
func TestLinesThatAreNotJSONDisconnect(t *testing.T) {
	c := dialFirstByte(t, startFirstByteServer(t))
	r := bufio.NewReader(c)
	c.Write([]byte(`{"id":1,"method":"mining.subscribe","params":[]}` + "\n"))
	readLine(t, r, c, "LOG-BADLINES-SUBSCRIBE")
	for i := 0; i < maxBadLines; i++ {
		c.Write([]byte("not json\n"))
	}
	if closedWithin(c, 500*time.Millisecond) {
		t.Fatalf("LOG-BADLINES-TOLERATED: disconnected after %d lines that were not JSON, want it kept", maxBadLines)
	}
	c.Write([]byte("not json\n"))
	if !closedWithin(c, 2*time.Second) {
		t.Fatalf("LOG-BADLINES-CLOSED: still connected after %d lines that were not JSON", maxBadLines+1)
	}
}

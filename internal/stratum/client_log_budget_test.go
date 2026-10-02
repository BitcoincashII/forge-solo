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
		if ok, _ := l.take(now); !ok {
			t.Fatalf("LOG-BUDGET: line %d of %d refused", i+1, clientLogBudget)
		}
	}
	for i := 0; i < 10; i++ {
		if ok, _ := l.take(now.Add(time.Second)); ok {
			t.Fatalf("LOG-BUDGET: line %d past the budget was allowed", clientLogBudget+i+1)
		}
	}
	if ok, skipped := l.take(now.Add(61 * time.Second)); !ok || skipped != 10 {
		t.Fatalf("LOG-SUPPRESSED-COUNT: after the minute got (%v, %d), want (true, 10)", ok, skipped)
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

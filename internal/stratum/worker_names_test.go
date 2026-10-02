package stratum

import (
	"encoding/json"
	"fmt"
	"testing"
)

func authorizeAs(s *Server, c *Client, name string) *Response {
	params, _ := json.Marshal([]string{name, "x"})
	return s.handleAuthorize(c, &Request{ID: 3, Method: MethodAuthorize, Params: params})
}

// A connection may authorize a few worker names, and the same one any number of times. Each new
// name is a worker entry on the dashboard, so cycling through new names with a junk share after
// each -- nearly half a million entries in 10 s before this -- must charge only those few.
func TestWorkerNamesPerConnectionAreCapped(t *testing.T) {
	s, _ := perJobServer()
	s.SetSoloPayoutAddress(testPayout)
	c := perJobClient(t)
	c.mu.Lock()
	c.Authorized, c.MinerID, c.WorkerName = false, "", ""
	c.mu.Unlock()
	charged := map[string]bool{}
	s.onInvalidShare = func(miner, worker, _ string) { charged[miner+":"+worker] = true }

	for i := 0; i < maxWorkerNamesPerConnection; i++ {
		if r := authorizeAs(s, c, fmt.Sprintf("rig%d", i)); r.Result != true {
			t.Fatalf("WORKER-NAMES-ALLOWED: name %d of %d refused: %+v", i+1, maxWorkerNamesPerConnection, r.Error)
		}
	}
	if r := authorizeAs(s, c, "one-too-many"); r.Result != false || r.Error != ErrTooManyWorkers {
		t.Fatalf("WORKER-NAMES-CAP: a new name past the limit got %+v, want %v", r, ErrTooManyWorkers)
	}
	if r := authorizeAs(s, c, "rig3"); r.Result != true {
		t.Fatalf("WORKER-NAMES-REPEAT: authorizing a known name again was refused: %+v", r.Error)
	}

	// The flood: a new name and a junk submit, a thousand times.
	for i := 0; i < 1000; i++ {
		authorizeAs(s, c, fmt.Sprintf("flood%d", i))
		params, _ := json.Marshal([]string{"x", "1", "0000000000000001", "6aba7069", "00000000"})
		s.handleSubmit(c, &Request{ID: 4, Method: MethodSubmit, Params: params})
	}
	if len(charged) > maxWorkerNamesPerConnection {
		t.Fatalf("WORKER-NAMES-FLOOD: refused shares were charged to %d different workers from one connection, want at most %d", len(charged), maxWorkerNamesPerConnection)
	}
}

package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// A node this launcher did not start is not asked to stop: the port may be another program's, and
// the request carries the node's password. One it started is asked, with its own login.
func TestStopNodesAsksOnlyNodesItStarted(t *testing.T) {
	var mu2 sync.Mutex
	var users []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _, _ := r.BasicAuth()
		mu2.Lock()
		users = append(users, user)
		mu2.Unlock()
		// The node stops: here, the helper standing in for it exits once its stdin closes.
		mu.Lock()
		if in := stdins["bch2"]; in != nil {
			_ = in.Close()
		}
		mu.Unlock()
		_, _ = w.Write([]byte(`{"result":"Bitcoin Cash II server stopping"}`))
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	savedB, savedA := bch2RPC, aux1175RPC
	bch2RPC, aux1175RPC = port, port
	t.Cleanup(func() {
		bch2RPC, aux1175RPC = savedB, savedA
		mu.Lock()
		delete(stdins, "bch2")
		mu.Unlock()
	})

	stopNodes()
	mu2.Lock()
	if len(users) != 0 {
		t.Fatalf("NODE-STOP-UNSTARTED: no node was started, yet %d stop requests were sent (%v)", len(users), users)
	}
	mu2.Unlock()

	startHelper(t, "bch2", "eof", t.TempDir())
	stopNodes()
	mu2.Lock()
	defer mu2.Unlock()
	if len(users) != 1 || users[0] != "forge" {
		t.Fatalf("NODE-STOP-STARTED: stop requests from %v, want one, from the BCH2 node's login", users)
	}
	if started("bch2") {
		t.Error("the BCH2 node is still tracked after stopping")
	}
}

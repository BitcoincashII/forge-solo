package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// A node closes a connection that has been idle for 30 s, and a question sent on one it is just
// closing fails with EOF: the api's client for its nodes drops an idle connection before the node
// does, and the next question opens a new one; one used again sooner is kept.
func TestNodeIdleConnectionIsDroppedFirst(t *testing.T) {
	if idle := nodeHTTP.Transport.(*http.Transport).IdleConnTimeout; idle <= 0 || idle >= 30*time.Second {
		t.Fatalf("RPC-IDLE: the api keeps an idle connection to a node for %s; the node closes it after 30 s", idle)
	}
	saved := nodeHTTP
	nodeHTTP = nodeClient(100 * time.Millisecond)
	t.Cleanup(func() { nodeHTTP = saved })
	const idle = 250 * time.Millisecond

	var conns atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"result":{"blocks":1},"error":null,"id":"api"}`)
	}))
	srv.Config.IdleTimeout = time.Hour // this node never closes an idle connection: only the client may
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()
	ask := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := rpcCallCtx(ctx, srv.URL, "u", "p", "getblockchaininfo", []interface{}{}); err != nil {
			t.Fatalf("RPC-IDLE: %v", err)
		}
	}
	ask()
	ask()
	if got := conns.Load(); got != 1 {
		t.Fatalf("RPC-IDLE: two questions in a row opened %d connections, want 1: the first is kept for the next", got)
	}
	time.Sleep(idle)
	ask()
	if got := conns.Load(); got != 2 {
		t.Fatalf("RPC-IDLE: a question after %s idle opened %d connections in all, want 2: the idle one was kept too long", idle, got)
	}
}

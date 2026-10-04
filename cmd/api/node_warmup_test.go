package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func useNode(t *testing.T, url string) {
	t.Helper()
	savedURL, savedUser, savedPass := rpcURL, rpcUser, rpcPass
	rpcURL, rpcUser, rpcPass = url, "u", "p"
	t.Cleanup(func() { rpcURL, rpcUser, rpcPass = savedURL, savedUser, savedPass })
}

// A node that is starting answers every call with -28 until it has loaded the chain. The
// dashboard is told it is starting, and what it is doing, not that it is syncing from block 0.
func TestNodeStatusWhileTheNodeWarmsUp(t *testing.T) {
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"result":null,"error":{"code":-28,"message":"Loading block index…"},"id":"api"}`))
	}))
	defer node.Close()
	useNode(t, node.URL)

	out := getJSON(t, getNodeStatus, "/node-status")
	if out["status"] != "starting" {
		t.Fatalf("NODE-WARMUP: a node loading its chain reads as %v (%v), want starting", out["status"], out["message"])
	}
	if out["message"] != "Loading block index…" {
		t.Fatalf("NODE-WARMUP-STEP: the dashboard is not told what the node is doing: %v", out["message"])
	}
	if _, err := rpcCall("getblockcount", []interface{}{}); err == nil {
		t.Fatal("NODE-WARMUP: rpcCall took the node's -28 error for a result")
	}
}

// A node that does not answer (stopped, crashed, between restarts, or answering with something
// that is not its own JSON) is not shown as starting: that read the same for hours as a node half
// a minute into its start, while nothing was being mined.
func TestNodeStatusWhenTheNodeIsNotAnswering(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()
	for _, tc := range []struct {
		code string
		h    http.HandlerFunc
	}{
		{"NODE-DOWN-REFUSED", nil},
		{"NODE-DOWN-503", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("<html><body>503 Service Unavailable</body></html>"))
		}},
		{"NODE-DOWN-AUTH", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }},
		{"NODE-DOWN-OTHER-ERROR", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"result":null,"error":{"code":-1,"message":"something else"},"id":"api"}`))
		}},
	} {
		url := closedURL
		if tc.h != nil {
			srv := httptest.NewServer(tc.h)
			defer srv.Close()
			url = srv.URL
		}
		useNode(t, url)
		out := getJSON(t, getNodeStatus, "/node-status")
		if out["status"] != "offline" {
			t.Errorf("%s: a node that is not answering reads as %v (%v), want offline", tc.code, out["status"], out["message"])
		}
		if m, _ := out["message"].(string); strings.Contains(strings.ToLower(m), "start") {
			t.Errorf("%s: the message says the node is starting: %q", tc.code, m)
		}
	}
}

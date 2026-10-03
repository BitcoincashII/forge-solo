package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A node that is starting answers every call with -28 until it has loaded the chain. The
// dashboard is told it is starting ("offline", which it shows as "Starting the BCH2 node…"), not
// that it is syncing from block 0.
func TestNodeStatusWhileTheNodeWarmsUp(t *testing.T) {
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"result":null,"error":{"code":-28,"message":"Loading block index…"},"id":"api"}`))
	}))
	defer node.Close()
	savedURL, savedUser, savedPass := rpcURL, rpcUser, rpcPass
	rpcURL, rpcUser, rpcPass = node.URL, "u", "p"
	t.Cleanup(func() { rpcURL, rpcUser, rpcPass = savedURL, savedUser, savedPass })

	out := getJSON(t, getNodeStatus, "/node-status")
	if out["status"] != "offline" {
		t.Fatalf("NODE-WARMUP: a node loading its chain reads as %v (%v), want offline (starting)", out["status"], out["message"])
	}
	if _, err := rpcCall("getblockcount", []interface{}{}); err == nil {
		t.Fatal("NODE-WARMUP: rpcCall took the node's -28 error for a result")
	}
}

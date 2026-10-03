package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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
		for _, key := range []string{"bch2", "aux1175"} {
			if in := stdins[key]; in != nil {
				_ = in.Close()
				delete(stdins, key)
			}
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
		delete(stdins, "aux1175")
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
	if len(users) != 1 || users[0] != "forge" {
		t.Fatalf("NODE-STOP-STARTED: stop requests from %v, want one, from the BCH2 node's login", users)
	}
	mu2.Unlock()
	if started("bch2") {
		t.Error("the BCH2 node is still tracked after stopping")
	}

	// The 1175 node alone: the BCH2 node not having been started does not keep it from being asked.
	startHelper(t, "aux1175", "eof", t.TempDir())
	stopNodes()
	mu2.Lock()
	defer mu2.Unlock()
	if len(users) != 2 || users[1] != "forge1175" {
		t.Fatalf("NODE-STOP-STARTED: stop requests from %v, want the 1175 node's login next", users)
	}
}

// fakeNode answers a node's stop request on its own port: it records who asked and when, and
// closes the stdin of the helper standing in for that node, which then exits.
func fakeNode(t *testing.T, key string, port *string, onStop func(user string)) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _, _ := r.BasicAuth()
		onStop(user)
		mu.Lock()
		if in := stdins[key]; in != nil {
			_ = in.Close()
			delete(stdins, key)
		}
		mu.Unlock()
		_, _ = w.Write([]byte(`{"result":"stopping"}`))
	}))
	saved := *port
	_, *port, _ = net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	t.Cleanup(func() {
		srv.Close()
		*port = saved
		mu.Lock()
		delete(stdins, key)
		mu.Unlock()
	})
}

// Both nodes are asked to stop at once, and waited for together: one waiting on the other only
// added to the time a closing Windows session has to give.
func TestStopNodesStopsBothAtOnce(t *testing.T) {
	var mu2 sync.Mutex
	var users []string
	record := func(user string) { mu2.Lock(); users = append(users, user); mu2.Unlock() }
	fakeNode(t, "bch2", &bch2RPC, record)
	fakeNode(t, "aux1175", &aux1175RPC, record)
	dirs := []string{t.TempDir(), t.TempDir()}
	startHelper(t, "bch2", "eof", dirs[0], "FS_HELPER_DELAY=2s")
	startHelper(t, "aux1175", "eof", dirs[1], "FS_HELPER_DELAY=2s")

	start := time.Now()
	stopNodes()
	took := time.Since(start)
	mu2.Lock()
	defer mu2.Unlock()
	if len(users) != 2 {
		t.Fatalf("NODE-STOP-BOTH: stop requests from %v, want one from each node", users)
	}
	if took > 3500*time.Millisecond {
		t.Fatalf("NODE-STOP-PARALLEL: two nodes that each take 2 s to stop took %v: one waited on the other", took)
	}
	for i, dir := range dirs {
		if _, err := os.Stat(filepath.Join(dir, "clean")); err != nil {
			t.Errorf("NODE-STOP-CLEAN: node %d was killed instead of given its time to stop", i)
		}
	}
	if started("bch2") || started("aux1175") {
		t.Error("a node is still tracked after stopping")
	}
}

// The miner stops before the nodes are asked to: a block it is still submitting needs the BCH2
// node.
func TestStopAllStopsTheMinerFirst(t *testing.T) {
	saved := installDir
	installDir = t.TempDir() // no pg_ctl here: stopping the database is not what this tests
	t.Cleanup(func() { installDir = saved })
	minerDir := t.TempDir()
	var mu2 sync.Mutex
	minerGone := map[string]bool{}
	fakeNode(t, "bch2", &bch2RPC, func(user string) {
		_, err := os.Stat(filepath.Join(minerDir, "clean"))
		mu2.Lock()
		minerGone[user] = err == nil
		mu2.Unlock()
	})
	startHelper(t, "stratum", "eof", minerDir, "FS_HELPER_DELAY=1s")
	startHelper(t, "bch2", "eof", t.TempDir())

	stopAll()
	mu2.Lock()
	defer mu2.Unlock()
	if gone, asked := minerGone["forge"]; !asked || !gone {
		t.Fatalf("STOP-MINER-FIRST: the BCH2 node was asked to stop (%v) while the miner was still running (stopped: %v)", asked, gone)
	}
}

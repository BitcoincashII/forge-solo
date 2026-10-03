package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// leftoverWorld stands in for the operating system: a list of leftover programs, the ports they
// listen on, which of them exit when asked, and a record of what was ended or signalled.
type leftoverWorld struct {
	mu       sync.Mutex
	progs    []runningProgram
	ports    map[int][]int
	exited   map[int]bool
	killed   []int
	stopsFor map[string][]string // node RPC port -> the logins that asked it to stop
}

// nodeServer is a fake node RPC on 127.0.0.1: a stop request makes pid exit, unless it ignores it.
func (w *leftoverWorld) nodeServer(t *testing.T, pid int, ignores bool) int {
	return w.loadingNodeServer(t, pid, ignores, time.Time{})
}

// loadingNodeServer is nodeServer for a node that refuses every request until ready, as a node
// still loading its blocks does.
func (w *leftoverWorld) loadingNodeServer(t *testing.T, pid int, ignores bool, ready time.Time) int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		user, _, _ := r.BasicAuth()
		_, port, _ := net.SplitHostPort(r.Host)
		w.mu.Lock()
		w.stopsFor[port] = append(w.stopsFor[port], user)
		loading := time.Now().Before(ready)
		if !ignores && !loading {
			w.exited[pid] = true
		}
		w.mu.Unlock()
		if loading {
			rw.WriteHeader(http.StatusInternalServerError)
			_, _ = rw.Write([]byte(`{"result":null,"error":{"code":-28,"message":"Loading block index…"},"id":"quit"}`))
			return
		}
		_, _ = rw.Write([]byte(`{"result":"stopping"}`))
	}))
	t.Cleanup(srv.Close)
	_, p, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	port, _ := strconv.Atoi(p)
	return port
}

func newLeftoverWorld(t *testing.T) *leftoverWorld {
	t.Helper()
	w := &leftoverWorld{ports: map[int][]int{}, exited: map[int]bool{}, stopsFor: map[string][]string{}}
	savedProgs, savedPorts, savedWait, savedKill := installedPrograms, loopbackPorts, waitPID, killPID
	savedPlan, savedData, savedSignal := portPlan, dataDir, signalPostgres
	savedGraces := []time.Duration{bch2StopGrace, auxStopGrace}
	t.Cleanup(func() {
		installedPrograms, loopbackPorts, waitPID, killPID = savedProgs, savedPorts, savedWait, savedKill
		portPlan, dataDir, signalPostgres = savedPlan, savedData, savedSignal
		bch2StopGrace, auxStopGrace = savedGraces[0], savedGraces[1]
	})
	bch2StopGrace, auxStopGrace = 300*time.Millisecond, 300*time.Millisecond
	dataDir = t.TempDir()
	md(dpath("pgdata"))
	installedPrograms = func() []runningProgram { return w.progs }
	loopbackPorts = func(pid int) []int { return w.ports[pid] }
	waitPID = func(pid int, d time.Duration) bool {
		for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
			w.mu.Lock()
			gone := w.exited[pid]
			w.mu.Unlock()
			if gone {
				return true
			}
		}
		return false
	}
	killPID = func(pid int) error {
		w.mu.Lock()
		w.killed = append(w.killed, pid)
		w.exited[pid] = true
		w.mu.Unlock()
		return nil
	}
	return w
}

// A launcher ended without stopping left both nodes, the database and the API running. The next
// start asks each node to stop on the RPC port it listens on in its window, with its own login,
// signals the database, ends the API, and ends no node that stopped.
func TestLeftoversAreStopped(t *testing.T) {
	w := newLeftoverWorld(t)
	bch2Port := w.nodeServer(t, 101, false)
	auxPort := w.nodeServer(t, 102, false)
	portPlan = []portSlot{{"the BCH2 node", &bch2RPC, bch2Port}, {"the 1175 node", &aux1175RPC, auxPort}}
	w.progs = []runningProgram{{101, "bitcoincashiid.exe"}, {102, "elevenseventyfived.exe"}, {103, "postgres.exe"},
		{104, "api.exe"}, {105, "postgres.exe"}}
	// Each node also listens on other ports (its block notices, its onion service); the RPC is the one in its window.
	w.ports[101] = []int{bch2Port + portWindow, bch2Port}
	w.ports[102] = []int{auxPort - 1, auxPort}
	if err := os.WriteFile(dpath("pgdata", "postmaster.pid"), []byte("103\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var signalled []int
	signalPostgres = func(pid int, sig byte) error {
		signalled = append(signalled, pid)
		return os.Remove(dpath("pgdata", "postmaster.pid"))
	}

	stopLeftovers()
	w.mu.Lock()
	defer w.mu.Unlock()
	if got := w.stopsFor[strconv.Itoa(bch2Port)]; len(got) != 1 || got[0] != "forge" {
		t.Errorf("LEFTOVER-NODES-ASKED: the BCH2 node's RPC got stop requests from %v, want one from forge", got)
	}
	if got := w.stopsFor[strconv.Itoa(auxPort)]; len(got) != 1 || got[0] != "forge1175" {
		t.Errorf("LEFTOVER-NODES-ASKED: the 1175 node's RPC got stop requests from %v, want one from forge1175", got)
	}
	if len(w.stopsFor) != 2 {
		t.Errorf("LEFTOVER-RPC-WINDOW: stop requests went to %v; only the two RPC ports should get one", w.stopsFor)
	}
	if len(signalled) != 1 || signalled[0] != 103 {
		t.Errorf("LEFTOVER-DB: signalled %v, want the postmaster 103", signalled)
	}
	if len(w.killed) != 1 || w.killed[0] != 104 {
		t.Errorf("LEFTOVER-API-ENDED / LEFTOVER-NOT-KILLED: ended %v, want only the API (104)", w.killed)
	}
}

// A node that does not stop when asked is ended after its grace; one that listens on no port in
// its window is ended without its password going anywhere.
func TestLeftoverNodesThatDoNotStop(t *testing.T) {
	w := newLeftoverWorld(t)
	bch2Port := w.nodeServer(t, 101, true)
	elsewhere := w.nodeServer(t, 102, false)
	portPlan = []portSlot{{"the BCH2 node", &bch2RPC, bch2Port}, {"the 1175 node", &aux1175RPC, elsewhere + 1}}
	w.progs = []runningProgram{{101, "bitcoincashiid.exe"}, {102, "elevenseventyfived.exe"}}
	w.ports[101] = []int{bch2Port}
	w.ports[102] = []int{elsewhere} // just below its window

	start := time.Now()
	stopLeftovers()
	took := time.Since(start)
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.stopsFor[strconv.Itoa(elsewhere)]) != 0 {
		t.Errorf("LEFTOVER-NO-PASSWORD-ELSEWHERE: a node's password went to port %d, outside its window", elsewhere)
	}
	killed := map[int]bool{}
	for _, p := range w.killed {
		killed[p] = true
	}
	if !killed[101] || !killed[102] || len(w.killed) != 2 {
		t.Errorf("LEFTOVER-KILL-AFTER-GRACE: ended %v, want both nodes", w.killed)
	}
	if took < 300*time.Millisecond {
		t.Errorf("LEFTOVER-KILL-AFTER-GRACE: the node that ignored its stop was ended after %v, before its grace", took)
	}
}

// A leftover node still loading its blocks refuses the first stop request: it is asked again until
// it stops, not ended when its grace runs out.
func TestLeftoverNodeAskedAgainWhileItLoads(t *testing.T) {
	w := newLeftoverWorld(t)
	bch2StopGrace = 5 * time.Second
	bch2Port := w.loadingNodeServer(t, 101, false, time.Now().Add(1500*time.Millisecond))
	portPlan = []portSlot{{"the BCH2 node", &bch2RPC, bch2Port}}
	w.progs = []runningProgram{{101, "bitcoincashiid.exe"}}
	w.ports[101] = []int{bch2Port}

	stopLeftovers()
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.killed) != 0 {
		t.Fatalf("LEFTOVER-STOP-RETRY: ended %v after %d stop requests; the node would have stopped once it had loaded", w.killed, len(w.stopsFor[strconv.Itoa(bch2Port)]))
	}
}

// A leftover that is ended is waited for until it is gone, before anything is started in its place.
func TestLeftoverEndedIsWaitedFor(t *testing.T) {
	w := newLeftoverWorld(t)
	w.progs = []runningProgram{{104, "api.exe"}}
	killPID = func(pid int) error {
		time.AfterFunc(300*time.Millisecond, func() {
			w.mu.Lock()
			w.exited[pid] = true
			w.mu.Unlock()
		})
		return nil
	}
	stopLeftovers()
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.exited[104] {
		t.Fatal("LEFTOVER-KILL-WAITS: the leftover API was ended, and its start in its place not held until it was gone")
	}
}

// With nothing left running nothing is done; a postmaster.pid naming a process that is not this
// install's database is left alone.
func TestNoLeftovers(t *testing.T) {
	w := newLeftoverWorld(t)
	if err := os.WriteFile(dpath("pgdata", "postmaster.pid"), []byte("777\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	signalled := 0
	signalPostgres = func(int, byte) error { signalled++; return nil }
	stopLeftovers()
	w.progs = []runningProgram{{104, "api.exe"}, {777, "notepad.exe"}}
	stopLeftovers()
	if signalled != 0 {
		t.Errorf("LEFTOVER-DB-OURS-ONLY: signalled a postmaster.pid process that is not this install's database")
	}
	if len(w.killed) != 1 || w.killed[0] != 104 {
		t.Errorf("LEFTOVER-NONE: ended %v, want only the leftover API", w.killed)
	}
}

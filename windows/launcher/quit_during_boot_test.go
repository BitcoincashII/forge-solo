//go:build !windows

package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// bootWorld puts stand-ins for every program boot starts where the launcher runs them from, and
// points the ports at stand-ins or free ports. It restores everything, the stop included, after
// the test.
func bootWorld(t *testing.T, scripts map[string]string) {
	t.Helper()
	savedInst, savedData, savedSec := installDir, dataDir, sec
	savedPorts := []string{pgPort, bch2RPC, bch2ZMQ, aux1175RPC, stratumInt, apiPort, webPort}
	savedGraces := []time.Duration{bch2StopGrace, auxStopGrace}
	savedBrowser, savedProgs, savedSignal, savedPublic := openBrowser, installedPrograms, signalPostgres, publicPorts
	publicPorts = publicPorts[:0:0] // this machine may run Forge Solo itself, on the real ones
	installDir, dataDir = t.TempDir(), t.TempDir()
	sec = secrets{BCH2Pass: "b", AuxPass: "a", DBPass: "d", Token: "t", Settings: "s"}
	webPort = freePort(t)
	openBrowser = func(string) {}
	t.Cleanup(func() {
		for _, k := range []string{"stratum", "api", "bch2", "aux1175"} {
			stop(k)
		}
		mu.Lock()
		stopping = false
		mu.Unlock()
		tipMu.Lock()
		stopShown = false
		tipMu.Unlock()
		dashboardOpen.Store(false)
		resetStartState()
		if dashboard != nil {
			_ = dashboard.Close()
			dashboard = nil
		}
		installDir, dataDir, sec = savedInst, savedData, savedSec
		pgPort, bch2RPC, bch2ZMQ, aux1175RPC, stratumInt, apiPort, webPort = savedPorts[0], savedPorts[1], savedPorts[2], savedPorts[3], savedPorts[4], savedPorts[5], savedPorts[6]
		bch2StopGrace, auxStopGrace = savedGraces[0], savedGraces[1]
		openBrowser, installedPrograms, signalPostgres, publicPorts = savedBrowser, savedProgs, savedSignal, savedPublic
	})
	for name, body := range scripts {
		// The launcher names them with Windows separators; on Linux that is one file name.
		if err := os.WriteFile(ipath(name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	md(dpath("pgdata"))
	if err := os.WriteFile(dpath("pgdata", "PG_VERSION"), []byte("16\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, p, _ := net.SplitHostPort(l.Addr().String())
	return p
}

// Quit while boot still waits for the 1175 node: the node's RPC is not up yet, so it keeps
// starting; it comes up while the stop waits on the nodes, and boot then started the miner after
// the stop had passed it. Forge Solo exited with stratum.exe still running, with no node and no
// database behind it. Nothing may start once the stop has begun.
func TestQuitDuringBootLeavesNothingRunning(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "stratum-started")
	t.Setenv("FS_MARKER", marker)
	bootWorld(t, map[string]string{
		"pgsql\\bin\\pg_ctl.exe":   "exit 0",
		"pgsql\\bin\\createdb.exe": "exit 0",
		"pgsql\\bin\\psql.exe":     "exit 0",
		"bitcoincashIId.exe":       "exec sleep 4",
		"elevenseventyfived.exe":   "exec sleep 30",
		"api.exe":                  "exec sleep 60",
		"stratum.exe":              `date > "$FS_MARKER"; exec sleep 60`,
	})
	bch2StopGrace, auxStopGrace = 6*time.Second, 3*time.Second
	bch2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":"stopping"}`))
	}))
	defer bch2.Close()
	api := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer api.Close()
	auxPort := freePort(t) // the 1175 node's RPC, not listening yet
	pgPort, bch2RPC, bch2ZMQ, aux1175RPC, stratumInt, apiPort = "1", portOf(bch2.URL), "2", auxPort, "3", portOf(api.URL)

	booted := make(chan struct{})
	go func() { boot(); close(booted) }()
	// Wait until boot has opened the dashboard; its miner goroutine then waits for the 1175 RPC.
	for end := time.Now().Add(10 * time.Second); !dashboardOpen.Load() && time.Now().Before(end); {
		time.Sleep(50 * time.Millisecond)
	}
	<-booted
	time.Sleep(500 * time.Millisecond)
	if started("stratum") {
		t.Fatal("setup: the miner started before Quit")
	}
	// The 1175 node finishes loading a second into the stop: its RPC begins to answer.
	go func() {
		time.Sleep(time.Second)
		if l, err := net.Listen("tcp", "127.0.0.1:"+auxPort); err == nil {
			t.Cleanup(func() { _ = l.Close() })
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				_ = c.Close()
			}
		}
	}()

	stopForExit() // what Quit runs, before the exit
	time.Sleep(time.Second)
	for _, k := range []string{"stratum", "api", "bch2", "aux1175"} {
		if started(k) {
			_, err := os.Stat(marker)
			t.Errorf("QUIT-DURING-BOOT: after the stop, %s is running (the miner was started: %v); the exit would leave it behind", k, err == nil)
		}
	}
	if err := run("late", exec.Command("true")); err != errStopping {
		t.Errorf("QUIT-REFUSES-STARTS: a start after the stop gave %v, want errStopping", err)
	}
}

// Quit while pg_ctl is still starting the database, before the server has written postmaster.pid:
// the stop waits for the server to appear and stops it, instead of finding none and leaving it
// running after the exit.
func TestQuitWhileTheDatabaseStarts(t *testing.T) {
	bootWorld(t, map[string]string{
		// pg_ctl -w: the server writes postmaster.pid a moment after it starts, and pg_ctl returns
		// once the server is ready.
		"pgsql\\bin\\pg_ctl.exe":   `: > "$FS_PGCTL"; sleep 1; printf '4242\n' > "$FS_PIDFILE"; sleep 1; exit 0`,
		"pgsql\\bin\\createdb.exe": "exit 0",
		"pgsql\\bin\\psql.exe":     "exit 0",
	})
	t.Setenv("FS_PIDFILE", dpath("pgdata", "postmaster.pid"))
	pgctlRan := filepath.Join(t.TempDir(), "pg_ctl-ran")
	t.Setenv("FS_PGCTL", pgctlRan)
	installedPrograms = func() []runningProgram { return []runningProgram{{4242, "postgres.exe"}} }
	signalled := make(chan struct{}, 1)
	signalPostgres = func(pid int, sig byte) error {
		signalled <- struct{}{}
		return os.Remove(dpath("pgdata", "postmaster.pid"))
	}
	pgPort = freePort(t)

	booted := make(chan struct{})
	go func() { boot(); close(booted) }()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(pgctlRan); err == nil {
			break
		}
	}
	if _, err := os.Stat(pgctlRan); err != nil {
		t.Fatal("setup: pg_ctl never started")
	}
	stopForExit()
	select {
	case <-signalled:
	default:
		t.Errorf("QUIT-DURING-DB-START: the stop found no database and did not stop the one starting")
	}
	<-booted
	if postmasterPID() != 0 {
		t.Errorf("QUIT-DURING-DB-START: postmaster.pid is still there: the database was left running")
	}
	if started("bch2") || started("api") {
		t.Errorf("QUIT-DURING-DB-START: boot went on to start the nodes or the API after the stop")
	}
}

func portOf(url string) string {
	_, p, _ := net.SplitHostPort(strings.TrimPrefix(url, "http://"))
	return p
}

// A program ended at once is waited for until it is gone: Windows ends a process only once its
// pending I/O is done, and one started in its place could otherwise still find its port or its data
// folder taken.
func TestStopWaitsForTheProcess(t *testing.T) {
	startHelper(t, "helper-stuck", "stuck", t.TempDir())
	mu.Lock()
	pid := procs["helper-stuck"].Process.Pid
	mu.Unlock()
	stop("helper-stuck")
	if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
		t.Fatalf("STOP-WAITS: stop returned with process %d still there (%v)", pid, err)
	}
}

// Another program holding a public port Forge Solo cannot mine without -- 3333 is the usual port of
// mining software -- stops the start, and the log and the tray say which port. The miner or the BCH2
// node exited at once without it, while the tray said "running". One holding an optional port
// (rentals, the 1175 node's peers) is only logged. A free port is let go again after the check.
func TestATakenPublicPortStopsTheStart(t *testing.T) {
	ran := filepath.Join(t.TempDir(), "pg_ctl-ran")
	t.Setenv("FS_PGCTL", ran)
	bootWorld(t, map[string]string{"pgsql\\bin\\pg_ctl.exe": `: > "$FS_PGCTL"; exit 1`})
	port := func(hold bool) string {
		l, err := net.Listen("tcp", "0.0.0.0:0")
		if err != nil {
			t.Fatal(err)
		}
		_, p, _ := net.SplitHostPort(l.Addr().String())
		if hold {
			t.Cleanup(func() { _ = l.Close() })
		} else {
			_ = l.Close()
		}
		return p
	}
	type pp = struct {
		port, what string
		required   bool
	}

	free := port(false)
	publicPorts = []pp{{free, "the miner port", true}}
	if err := checkPublicPorts(); err != nil {
		t.Fatalf("PUBLIC-PORT-FREE: a free port was reported taken: %v", err)
	}
	l, err := net.Listen("tcp", "0.0.0.0:"+free)
	if err != nil {
		t.Fatalf("PUBLIC-PORT-RELEASED: the check kept port %s, which the miner then could not take: %v", free, err)
	}
	_ = l.Close()

	optional := port(true)
	publicPorts = []pp{{optional, "the port for rented hashpower", false}}
	if err := checkPublicPorts(); err != nil {
		t.Fatalf("PUBLIC-PORT-OPTIONAL: an optional port taken stopped the start: %v", err)
	}

	required := port(true)
	publicPorts = []pp{{optional, "the port for rented hashpower", false}, {required, "the miner port", true}}
	boot()
	b, _ := os.ReadFile(dpath("launcher.log"))
	if !strings.Contains(string(b), "Forge Solo cannot start: another program uses port "+required+", the miner port") {
		t.Fatalf("PUBLIC-PORT-TAKEN: the log does not name the taken miner port %s:\n%s", required, b)
	}
	if !strings.Contains(string(b), "another program uses port "+optional+", the port for rented hashpower: that part is left out") {
		t.Errorf("PUBLIC-PORT-OPTIONAL-LOGGED: the taken rental port %s is not logged:\n%s", optional, b)
	}
	if _, err := os.Stat(ran); err == nil {
		t.Fatal("PUBLIC-PORT-STOPS-START: boot went on to start the database with the miner port taken")
	}
}

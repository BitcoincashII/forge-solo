//go:build sqlite && unix

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/dblock"
	"github.com/BitcoincashII/forge-solo/internal/migstatus"
	"github.com/BitcoincashII/forge-solo/internal/pgmigrate"
)

// standIn plays postgres for run: it writes down how it was started, says it is ready in
// postmaster.pid as PostgreSQL does, and on SIGINT (PostgreSQL's fast shutdown) writes that down,
// leaves the pg_control STANDIN_CONTROL_AFTER names, as a clean stop rewrites it, and ends.
const standIn = `#!/bin/sh
echo "pid $$" >> "$STANDIN_LOG"
echo "argv $*" >> "$STANDIN_LOG"
prev=""
for a in "$@"; do
  if [ "$prev" = "-D" ]; then D="$a"; fi
  prev="$a"
done
trap 'echo SIGINT >> "$STANDIN_LOG"; if [ -n "$STANDIN_CONTROL_AFTER" ]; then cp "$STANDIN_CONTROL_AFTER" "$D/global/pg_control"; fi; rm -f "$D/postmaster.pid"; exit 0' INT
trap 'echo SIGQUIT >> "$STANDIN_LOG"; rm -f "$D/postmaster.pid"; exit 1' QUIT
if [ "$STANDIN_NEVER_READY" != "1" ]; then
  printf '%s\n%s\n0\n5432\n/tmp\n\n0\nready   \n' "$$" "$D" > "$D/postmaster.pid"
fi
i=0
while [ $i -lt 2400 ]; do sleep 0.05; i=$((i+1)); done
`

// withStandIn is the environment that puts the stand-in first on PATH, logging to the returned file.
func withStandIn(t *testing.T, env map[string]string) (map[string]string, string) {
	t.Helper()
	bin := t.TempDir()
	must(t, os.WriteFile(filepath.Join(bin, "postgres"), []byte(standIn), 0o755))
	logFile := filepath.Join(bin, "standin.log")
	t.Cleanup(func() {
		for _, line := range strings.Split(readLog(logFile), "\n") {
			pid, err := strconv.Atoi(strings.TrimPrefix(line, "pid "))
			if err != nil || !strings.HasPrefix(line, "pid ") {
				continue
			}
			// Only the stand-in itself: its command line names this test's folder.
			if cl, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline"); err == nil && strings.Contains(string(cl), bin) {
				syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	out := map[string]string{"PATH": bin + ":" + os.Getenv("PATH"), "STANDIN_LOG": logFile}
	for k, v := range env {
		out[k] = v
	}
	return out, logFile
}

func readLog(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

func runArgs(l layout) []string {
	return []string{"run", "--db", l.db, "--pgdata", l.pgdata, "--owner", "10001:10001"}
}

// run moves the old data: the server starts as it must, read-only and private, and stops cleanly;
// the marker holds pg_control as the stop left it, so the next start does nothing.
func TestRunMoves(t *testing.T) {
	l := newLayout(t, "16", "pg_control-shutdown")
	env, logFile := withStandIn(t, map[string]string{"FORGE_MIGRATE_TEST_SOURCE": "small",
		"STANDIN_CONTROL_AFTER": fixturePath("pg_control-shutdown-2")})
	r := runCLI(t, env, runArgs(l)...)
	if r.code != 0 {
		t.Fatalf("MIG-RUN-MOVE: run exits %d: %s", r.code, r.stderr)
	}
	if s := status(t, l.db); s.State != migstatus.Done {
		t.Fatalf("MIG-RUN-MOVE: the status is %+v, want done", s)
	}
	if _, err := os.Stat(l.db); err != nil {
		t.Fatalf("MIG-RUN-MOVE: no database after the move: %v", err)
	}
	log := readLog(logFile)
	argv := ""
	for _, line := range strings.Split(log, "\n") {
		if strings.HasPrefix(line, "argv ") {
			argv = strings.TrimPrefix(line, "argv ")
		}
	}
	f := strings.Fields(argv)
	if len(f) < 4 || f[0] != "-D" || f[1] != l.pgdata {
		t.Fatalf("MIG-RUN-ARGV: the server was started as %q", argv)
	}
	var settings []string
	sock := ""
	for i := 2; i+1 < len(f); i += 2 {
		if f[i] != "-c" {
			t.Fatalf("MIG-RUN-ARGV: the server was started as %q", argv)
		}
		switch k, v, _ := strings.Cut(f[i+1], "="); k {
		case "unix_socket_directories":
			sock = v
			settings = append(settings, k+"=<socket>")
		case "hba_file":
			if v != filepath.Join(sock, "pg_hba.conf") || !strings.Contains(sock, "forge-migrate-") {
				t.Errorf("MIG-RUN-ARGV: hba_file %s, socket %s", v, sock)
			}
			settings = append(settings, k+"=<socket>/pg_hba.conf")
		default:
			settings = append(settings, f[i+1])
		}
	}
	want := "listen_addresses= unix_socket_directories=<socket> hba_file=<socket>/pg_hba.conf shared_preload_libraries= " +
		"shared_buffers=16MB max_connections=10 max_parallel_workers=0 autovacuum=off default_transaction_read_only=on " +
		"jit=off huge_pages=off ssl=off logging_collector=off archive_mode=off recovery_init_sync_method=syncfs"
	if got := strings.Join(settings, " "); got != want {
		t.Errorf("MIG-RUN-ARGV: the server's settings are\n%s\nwant\n%s", got, want)
	}
	if _, err := os.Stat(sock); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("MIG-RUN-ARGV: the server's socket folder %s is left behind", sock)
	}
	if !strings.Contains(log, "SIGINT") || strings.Contains(log, "SIGQUIT") {
		t.Errorf("MIG-RUN-STOP: the server was not stopped with SIGINT alone; its log:\n%s", log)
	}
	b, _ := os.ReadFile(filepath.Join(l.pgdata, "global", "pg_control"))
	sum := sha256.Sum256(b)
	var m pgmigrate.Marker
	mb, _ := os.ReadFile(pgmigrate.MarkerPath(l.db))
	json.Unmarshal(mb, &m)
	if m.PGControlSHA256 != hex.EncodeToString(sum[:]) || m.Version != "1.0.13-test" {
		t.Fatalf("MIG-RUN-HASH: the marker holds %s, but pg_control after the stop is %s", m.PGControlSHA256, hex.EncodeToString(sum[:]))
	}
	if d := pgmigrate.Plan(l.db, l.pgdata); d.Action != pgmigrate.ActionNone {
		t.Fatalf("MIG-RUN-HASH: after the move the next start says %s, want none", d)
	}
	if r := runCLI(t, env, runArgs(l)...); r.code != 0 || status(t, l.db).State != migstatus.None {
		t.Fatalf("MIG-RUN-AGAIN: the next start exits %d with status %+v, want 0 and none", r.code, status(t, l.db))
	}
}

// SIGTERM during the copy: the server is told SIGINT, nothing is committed, the status is left as
// it was, and run exits 3.
func TestRunInterrupted(t *testing.T) {
	l := newLayout(t, "16", "pg_control-shutdown")
	signal := filepath.Join(l.dir, "reading")
	env, logFile := withStandIn(t, map[string]string{"FORGE_MIGRATE_TEST_SOURCE": "stall", "FORGE_MIGRATE_TEST_SIGNAL": signal})
	before, _ := os.ReadFile("../../internal/migstatus/testdata/skipped.json")
	must(t, os.WriteFile(migstatus.Path(l.db), before, 0o600))
	cmd := command(env, runArgs(l)...)
	must(t, cmd.Start())
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, err := os.Stat(signal); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cmd.Process.Kill()
			t.Fatal("MIG-RUN-SIGTERM-SETUP: the copy never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	must(t, cmd.Process.Signal(syscall.SIGTERM))
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-time.After(60 * time.Second):
		cmd.Process.Kill()
		t.Fatal("MIG-RUN-SIGTERM-HANG: run did not end after SIGTERM")
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != pgmigrate.CodeInterrupted {
		t.Errorf("MIG-RUN-SIGTERM-CODE: run ended with %v after SIGTERM, want exit 3", err)
	}
	if log := readLog(logFile); !strings.Contains(log, "SIGINT") {
		t.Errorf("MIG-RUN-SIGTERM-FORWARD: the server was not told to stop; its log:\n%s", log)
	}
	for _, f := range []string{l.db, pgmigrate.MigratingPath(l.db)} {
		if _, err := os.Stat(f); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("MIG-RUN-SIGTERM-NODB: %s is there after an interrupted run", f)
		}
	}
	if after, _ := os.ReadFile(migstatus.Path(l.db)); string(after) != string(before) {
		t.Errorf("MIG-RUN-SIGTERM-STATUS: an interrupted run changed the status to\n%s", after)
	}
}

// A server that never gets ready fails the move after ReadyWait: recorded, stopped, and run exits 0
// so the dashboard can say what happened.
func TestRunServerNeverReady(t *testing.T) {
	l := newLayout(t, "16", "pg_control-shutdown")
	env, logFile := withStandIn(t, map[string]string{"FORGE_MIGRATE_TEST_SOURCE": "small", "STANDIN_NEVER_READY": "1",
		"FORGE_MIGRATE_TEST_READY_WAIT": "2s"})
	cmd := command(env, runArgs(l)...)
	done := make(chan error, 1)
	must(t, cmd.Start())
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("MIG-RUN-NOTREADY: run ended with %v, want exit 0", err)
		}
	case <-time.After(60 * time.Second):
		cmd.Process.Kill()
		t.Fatal("MIG-RUN-NOTREADY-HANG: run still waits for a server that never gets ready")
	}
	if s := status(t, l.db); s.State != migstatus.Failed || s.Code != pgmigrate.CodeSource {
		t.Fatalf("MIG-RUN-NOTREADY: the status is %+v, want failed with code 10", s)
	}
	if log := readLog(logFile); !strings.Contains(log, "SIGINT") {
		t.Errorf("MIG-RUN-NOTREADY-STOP: the server that never got ready was left running; its log:\n%s", log)
	}
}

// Each failure is recorded as failed with its code, nothing is replaced, and run exits 0.
func TestRunFailureClasses(t *testing.T) {
	for _, c := range []struct {
		name    string
		code    int
		source  string
		version string
		prepare func(l layout)
	}{
		{"verification", pgmigrate.CodeVerify, "nan", "16", nil},
		{"write", pgmigrate.CodeWrite, "small", "16", func(l layout) {
			must(t, os.MkdirAll(filepath.Join(pgmigrate.MigratingPath(l.db), "x"), 0o700))
		}},
		{"refused", pgmigrate.CodeRefused, "small", "15", nil},
		{"other", pgmigrate.CodeOther, "error", "16", nil},
		{"source", pgmigrate.CodeSource, "unreachable", "16", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			code := "MIG-RUN-CLASS-" + strconv.Itoa(c.code)
			l := newLayout(t, c.version, "pg_control-shutdown")
			if c.prepare != nil {
				c.prepare(l)
			}
			env, logFile := withStandIn(t, map[string]string{"FORGE_MIGRATE_TEST_SOURCE": c.source})
			r := runCLI(t, env, runArgs(l)...)
			if r.code != 0 {
				t.Fatalf("%s: run exits %d after a failure, want 0 so the api starts and explains: %s", code, r.code, r.stderr)
			}
			s := status(t, l.db)
			if s.State != migstatus.Failed || s.Code != c.code || s.Reason == "" || s.Detail == "" {
				t.Fatalf("%s: the status is %+v, want failed with code %d", code, s, c.code)
			}
			if _, err := os.Stat(l.db); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s: a failed run left a database", code)
			}
			if c.code == pgmigrate.CodeRefused && readLog(logFile) != "" {
				t.Fatalf("%s: run started PostgreSQL 16 on PostgreSQL 15 data", code)
			}
		})
	}
}

// Nothing to move, the user's skip, and a damaged old folder after a move: recorded, no server.
func TestRunRecordsWithoutMoving(t *testing.T) {
	for _, c := range []struct {
		code, want string
		layout     func(t *testing.T) layout
	}{
		{"MIG-RUN-NONE", migstatus.None, func(t *testing.T) layout { return newLayout(t, "", "") }},
		{"MIG-RUN-SKIP", migstatus.Skipped, func(t *testing.T) layout {
			l := newLayout(t, "16", "pg_control-shutdown")
			must(t, os.WriteFile(pgmigrate.SkipPath(l.db), nil, 0o600))
			return l
		}},
		{"MIG-RUN-DEGRADED", migstatus.Degraded, func(t *testing.T) layout {
			l := newLayout(t, "16", "pg_control-shutdown")
			must(t, os.WriteFile(l.db, nil, 0o600))
			must(t, os.WriteFile(pgmigrate.MarkerPath(l.db), []byte(`{"pg_control_sha256":"x"}`), 0o600))
			must(t, os.Remove(filepath.Join(l.pgdata, "global", "pg_control")))
			return l
		}},
	} {
		t.Run(c.code, func(t *testing.T) {
			l := c.layout(t)
			env, logFile := withStandIn(t, nil)
			r := runCLI(t, env, runArgs(l)...)
			if r.code != 0 {
				t.Fatalf("%s: run exits %d: %s", c.code, r.code, r.stderr)
			}
			if s := status(t, l.db); s.State != c.want {
				t.Fatalf("%s: the status is %+v, want %s", c.code, s, c.want)
			}
			if readLog(logFile) != "" {
				t.Fatalf("%s: run started PostgreSQL with nothing to move", c.code)
			}
		})
	}
}

// While the api has the database open the move is deferred: recorded, nothing replaced, exit 0.
func TestRunDeferredWhileInUse(t *testing.T) {
	l := newLayout(t, "16", "pg_control-shutdown")
	env, _ := withStandIn(t, map[string]string{"FORGE_MIGRATE_TEST_SOURCE": "small"})
	if r := runCLI(t, env, runArgs(l)...); r.code != 0 {
		t.Fatal(r.stderr)
	}
	l.setControl(t, "pg_control-shutdown-2") // 1.0.12 ran on the old data again: a merge is needed
	held, err := dblock.Shared(dblock.Path(l.db), 0)
	must(t, err)
	defer held.Release()
	b, _ := os.ReadFile(l.db)
	if r := runCLI(t, env, runArgs(l)...); r.code != 0 {
		t.Fatalf("MIG-RUN-DEFERRED: run exits %d while the database is in use: %s", r.code, r.stderr)
	}
	if s := status(t, l.db); s.State != migstatus.Deferred || s.Code != pgmigrate.CodeDeferred {
		t.Fatalf("MIG-RUN-DEFERRED: the status is %+v, want deferred", s)
	}
	if a, _ := os.ReadFile(l.db); string(a) != string(b) {
		t.Fatalf("MIG-RUN-DEFERRED: the deferred run changed forgesolo.db")
	}
}

// A status that cannot be written holds the services back: run exits 41.
func TestRunStatusUnwritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only folder")
	}
	l := newLayout(t, "16", "pg_control-shutdown")
	dir := filepath.Dir(l.db)
	must(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	env, _ := withStandIn(t, map[string]string{"FORGE_MIGRATE_TEST_SOURCE": "small"})
	if r := runCLI(t, env, runArgs(l)...); r.code != pgmigrate.CodeStatus {
		t.Fatalf("MIG-RUN-41: with the database folder unwritable run exits %d, want 41: %s", r.code, r.stderr)
	}
}

// Going back to 1.0.12 and forward again: run merges, keeps a copy of what it replaced, and the
// next start does nothing.
func TestRunMergesAfterGoingBack(t *testing.T) {
	l := newLayout(t, "16", "pg_control-shutdown")
	env, _ := withStandIn(t, map[string]string{"FORGE_MIGRATE_TEST_SOURCE": "small"})
	if r := runCLI(t, env, runArgs(l)...); r.code != 0 {
		t.Fatal(r.stderr)
	}
	l.setControl(t, "pg_control-shutdown-2")
	env, _ = withStandIn(t, map[string]string{"FORGE_MIGRATE_TEST_SOURCE": "bigger"})
	if r := runCLI(t, env, runArgs(l)...); r.code != 0 {
		t.Fatalf("MIG-RUN-MERGE: run exits %d: %s", r.code, r.stderr)
	}
	if s := status(t, l.db); s.State != migstatus.Done {
		t.Fatalf("MIG-RUN-MERGE: the status is %+v, want done", s)
	}
	var m pgmigrate.Marker
	mb, _ := os.ReadFile(pgmigrate.MarkerPath(l.db))
	json.Unmarshal(mb, &m)
	if m.Mode != pgmigrate.ModeMerge || m.Counts["blocks"] != 6 {
		t.Fatalf("MIG-RUN-MERGE: the marker says %+v, want a merge with 6 blocks", m)
	}
	copies, _ := filepath.Glob(l.db + ".before-merge-*")
	if len(copies) != 1 {
		t.Fatalf("MIG-RUN-MERGE: %d copies of what the merge replaced, want 1", len(copies))
	}
	if d := pgmigrate.Plan(l.db, l.pgdata); d.Action != pgmigrate.ActionNone {
		t.Fatalf("MIG-RUN-MERGE: after the merge the next start says %s", d)
	}
}

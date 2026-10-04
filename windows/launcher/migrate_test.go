//go:build !windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// migratorStandIn is forge-solo-migrate.exe for the tests. It notes how it was run in $FS_CALLS
// (and the old database's address, if its environment has one); plan prints $FS_PLAN; prepare and
// commit exit with $FS_PREPARE and $FS_COMMIT (0 by default), recording in the status file, as the
// real one does, a deferral (31) and the failures in $FS_RECORDS. A commit that succeeds puts a
// database in place and writes the marker, with the hash of pg_control as it is, and "done".
const migratorStandIn = `echo "migrate $*" >> "$FS_CALLS"
[ -n "$FORGE_MIGRATE_PG" ] && echo "migrate-env $FORGE_MIGRATE_PG" >> "$FS_CALLS"
cmd=$1; shift; db=; pg=
while [ $# -gt 0 ]; do case "$1" in --db) db=$2; shift;; --pgdata) pg=$2; shift;; esac; shift; done
st() { printf '{"state": "%s", "code": %s, "reason": "%s", "detail": "", "at": "2026-10-04T01:02:03Z", "version": "test"}\n' "$1" "$2" "$3" > "$(dirname "$db")/migration-status.json"; }
case "$cmd" in
plan) echo "$FS_PLAN";;
prepare|commit)
  [ "$cmd" = prepare ] && [ -n "$FS_PREPARE_WAIT" ] && exec sleep "$FS_PREPARE_WAIT"
  if [ "$cmd" = prepare ]; then code=${FS_PREPARE:-0}; else code=${FS_COMMIT:-0}; fi
  [ "$code" = 31 ] && st deferred 31 "the database is in use; the move finishes at the next start"
  case " $FS_RECORDS " in *" $code "*) st failed "$code" "the migrator says why ($code)";; esac
  if [ "$cmd" = commit ] && [ "$code" = 0 ]; then
    : > "$db"
    printf '{"pg_control_sha256": "%s"}\n' "$(sha256sum "$pg/global/pg_control" | cut -d' ' -f1)" > "$(dirname "$db")/postgres-migrated.json"
    st done 0 "the data of the earlier version is in forgesolo.db"
  fi
  exit "$code";;
esac`

// pgctlStandIn is the bundled pg_ctl.exe: it notes how it was run, and starts a "server" (a
// postmaster.pid naming process 4242, which signalPostgres stops) in the folder given with -D,
// unless it is to fail ($FS_PGCTL_EXIT).
const pgctlStandIn = `echo "pg_ctl $*" >> "$FS_CALLS"
d=; while [ $# -gt 0 ]; do [ "$1" = -D ] && d=$2; shift; done
[ "${FS_PGCTL_EXIT:-0}" = 0 ] || exit "$FS_PGCTL_EXIT"
printf '4242\n' > "$d/postmaster.pid"`

// moveWorld is an install that ran an earlier version, with a stand-in for every program a start
// runs: the old data in pgdata (PostgreSQL 16, with a real pg_control), its password in secrets,
// the bundled PostgreSQL, and a migrator whose plan is plan. Each program notes in the calls file
// that it ran, in order; "stop" is the old database being stopped.
func moveWorld(t *testing.T, plan string) (calls string, tp *tips) {
	t.Helper()
	calls = filepath.Join(t.TempDir(), "calls")
	t.Setenv("FS_CALLS", calls)
	t.Setenv("FS_PLAN", plan)
	note := func(name string) string { return `echo ` + name + ` >> "$FS_CALLS"; ` + sleeper }
	tp = startFailWorld(t, map[string]string{
		"bitcoincashIId.exe": note("bch2"), "elevenseventyfived.exe": note("aux1175"),
		"api.exe": note("api"), "stratum.exe": note("stratum"), migrateExe: migratorStandIn,
	})
	md(ipath("pgsql", "bin"))
	writeFile(t, ipath("pgsql", "bin", "pg_ctl.exe"), "#!/bin/sh\n"+pgctlStandIn+"\n", 0o755)
	writeFile(t, ipath("pgsql", "bin", "postgres.exe"), "#!/bin/sh\nexit 1\n", 0o755) // pg_ctl's stand-in is the server
	oldData(t, "pg_control-shutdown")
	sec.DBPass = "old-db-password"
	installedPrograms = func() []runningProgram {
		if postmasterPID() != 0 {
			return []runningProgram{{4242, "postgres.exe"}}
		}
		return nil
	}
	signalPostgres = func(pid int, sig byte) error {
		appendCall(t, calls, "stop")
		return os.Remove(dpath("pgdata", "postmaster.pid"))
	}
	savedLimit := migrateLimit
	t.Cleanup(func() { migrateLimit = savedLimit })
	return calls, tp
}

// oldData puts an earlier version's data in pgdata, with the pg_control fixture named.
func oldData(t *testing.T, control string) {
	t.Helper()
	md(dpath("pgdata", "global"))
	writeFile(t, dpath("pgdata", "PG_VERSION"), "16\n", 0o600)
	writeFile(t, dpath("pgdata", "global", "pg_control"), string(caseContent(t, "@"+control)), 0o600)
}

// moved lays out a move done when pg_control was the fixture named: forgesolo.db and the marker.
func moved(t *testing.T, control string) {
	t.Helper()
	writeFile(t, dbPath(), "", 0o600)
	writeFile(t, dpath(markerName), string(caseContent(t, "@marker:@"+control)), 0o600)
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func appendCall(t *testing.T, calls, line string) {
	f, err := os.OpenFile(calls, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Error(err)
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line + "\n")
}

// callsIn is what ran, in order: the first word of each line.
func callsIn(calls string) []string {
	b, _ := os.ReadFile(calls)
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if f := strings.Fields(l); len(f) > 0 {
			if f[0] == "migrate" && len(f) > 1 {
				out = append(out, "migrate "+f[1])
			} else {
				out = append(out, f[0])
			}
		}
	}
	return out
}

func count(list []string, s string) int {
	n := 0
	for _, l := range list {
		if l == s {
			n++
		}
	}
	return n
}

// bootAll boots and waits until the miner has been started.
func bootAll(t *testing.T) {
	t.Helper()
	boot()
	if !waitFor(10*time.Second, func() bool { return started("stratum") && started("api") && started("bch2") && started("aux1175") }) {
		t.Fatalf("MOVE-SERVICES-STARTED: the nodes, the API and the miner did not all start; log:\n%s", launcherLog())
	}
}

func readStatusT(t *testing.T) migrationStatus {
	t.Helper()
	s, ok, err := readStatus()
	if !ok || err != nil {
		t.Fatalf("no status file (%v); log:\n%s", err, launcherLog())
	}
	return s
}

// A fresh install has no old data: a start runs neither the migrator nor PostgreSQL, and writes no
// status file, as on Linux.
func TestAFreshInstallRunsNoMove(t *testing.T) {
	calls, _ := moveWorld(t, "none")
	if err := os.RemoveAll(dpath("pgdata")); err != nil {
		t.Fatal(err)
	}
	bootAll(t)
	for _, c := range callsIn(calls) {
		if strings.HasPrefix(c, "migrate") || c == "pg_ctl" || c == "initdb" {
			t.Errorf("MOVE-FRESH-NOTHING: a fresh install ran %s; calls %v", c, callsIn(calls))
		}
	}
	if _, err := os.Stat(statusPath()); !os.IsNotExist(err) {
		t.Errorf("MOVE-FRESH-NO-STATUS: a fresh install wrote %s (%v)", statusPath(), err)
	}
}

// After a move, with the old data unchanged since, a start tells so from the files alone: no
// migrator, no PostgreSQL. What only the move needed goes: the bundled PostgreSQL programs and its
// log, never the old data. A migrator an antivirus removed changes nothing.
func TestAMovedInstallStartsWithoutTheMigrator(t *testing.T) {
	for _, c := range []struct {
		code            string
		migratorRemoved bool
	}{{"MOVE-FASTPATH", false}, {"MOVE-FASTPATH-NO-MIGRATOR", true}} {
		t.Run(c.code, func(t *testing.T) {
			calls, tp := moveWorld(t, "move")
			moved(t, "pg_control-shutdown")
			writeFile(t, dpath("pglog.txt"), "old log\n", 0o600)
			if c.migratorRemoved {
				if err := os.Remove(ipath(migrateExe)); err != nil {
					t.Fatal(err)
				}
			}
			bootAll(t)
			got := callsIn(calls)
			if n := count(got, "migrate plan") + count(got, "pg_ctl"); n != 0 {
				t.Fatalf("%s: a moved install ran the migrator or PostgreSQL: %v", c.code, got)
			}
			if tp.has("Forge Solo could not move") {
				t.Errorf("%s: the tray says the move failed: %q", c.code, tp.all())
			}
			if _, err := os.Stat(ipath("pgsql")); !os.IsNotExist(err) {
				t.Errorf("MOVE-CLEANUP-PGSQL: after a checked move the bundled PostgreSQL is still installed (%v)", err)
			}
			if _, err := os.Stat(dpath("pglog.txt")); !os.IsNotExist(err) {
				t.Errorf("MOVE-CLEANUP-LOG: after a checked move pglog.txt is still there (%v)", err)
			}
			if _, err := os.Stat(dpath("pgdata", "global", "pg_control")); err != nil {
				t.Errorf("MOVE-CLEANUP-KEEPS-OLD-DATA: the old data went with the cleanup: %v", err)
			}
		})
	}
}

// After a move, the old folder damaged (pg_control deleted, say, by a delete cut short): Forge Solo
// starts as usual, without the migrator, and the status file and the tray say the old folder is
// not used. A damaged old folder never stops a start after a move.
func TestADamagedOldFolderAfterAMoveIsIgnored(t *testing.T) {
	calls, tp := moveWorld(t, "move")
	moved(t, "pg_control-shutdown")
	if err := os.Remove(dpath("pgdata", "global", "pg_control")); err != nil {
		t.Fatal(err)
	}
	bootAll(t)
	if got := callsIn(calls); count(got, "migrate plan")+count(got, "pg_ctl") != 0 {
		t.Fatalf("MOVE-DEGRADED-NO-MIGRATOR: %v", got)
	}
	if s := readStatusT(t); s.State != stateDegraded || s.Reason != "the old database's pg_control is missing" {
		t.Errorf("MOVE-DEGRADED-STATUS: %+v", s)
	}
	if !waitFor(5*time.Second, func() bool {
		return tp.last() == "Forge Solo: running. The old database's folder is damaged and is not used: see the dashboard."
	}) {
		t.Errorf("MOVE-DEGRADED-TRAY: the tray says %q", tp.last())
	}
	if _, err := os.Stat(ipath("pgsql")); err != nil {
		t.Errorf("MOVE-DEGRADED-KEEPS-PGSQL: %v", err)
	}
}

// The move runs in this order, before the nodes, the API and the miner: the migrator's plan, the
// old database started read-only and on this PC only, prepare (with the database's address in its
// environment, never on its command line), the old database stopped, commit. Once it is checked,
// what only the move needed goes.
func TestTheMoveRunsInOrder(t *testing.T) {
	for _, plan := range []string{"move", "merge"} {
		t.Run(plan, func(t *testing.T) {
			calls, tp := moveWorld(t, plan)
			if plan == "merge" {
				moved(t, "pg_control-shutdown-2")
			}
			bootAll(t)
			got := callsIn(calls)
			at := func(s string) int {
				for i, c := range got {
					if c == s {
						return i
					}
				}
				return -1
			}
			order := []string{"migrate plan", "pg_ctl", "migrate prepare", "stop", "migrate commit"}
			for i := 1; i < len(order); i++ {
				if at(order[i-1]) < 0 || at(order[i-1]) > at(order[i]) {
					t.Fatalf("MOVE-ORDER: %v, want %v first", got, order)
				}
			}
			for _, s := range []string{"bch2", "aux1175", "api", "stratum"} {
				if at(s) < at("migrate commit") {
					t.Errorf("MOVE-BEFORE-SERVICES: %s started before the move was done: %v", s, got)
				}
			}
			b, _ := os.ReadFile(calls)
			text := string(b)
			if !strings.Contains(text, "-o -p "+pgPort+" -h 127.0.0.1 -c default_transaction_read_only=on -c autovacuum=off -w -t 300 start") {
				t.Errorf("MOVE-PG-READONLY: pg_ctl was not run read-only and on 127.0.0.1 alone:\n%s", text)
			}
			if p, _ := strconv.Atoi(pgPort); p < pgPortFrom || p >= pgPortFrom+portWindow {
				t.Errorf("MOVE-PG-PORT: the old database's port %s is outside its window", pgPort)
			}
			if want := "migrate prepare --db " + dbPath(); !strings.Contains(text, want+map[string]string{"move": "\n", "merge": " --merge\n"}[plan]) {
				t.Errorf("MOVE-PREPARE-ARGS: want %q:\n%s", want, text)
			}
			if !strings.Contains(text, "migrate-env postgres://forge:old-db-password@127.0.0.1:"+pgPort+"/forgesolo?sslmode=disable") {
				t.Errorf("MOVE-DSN-ENV: prepare was not given the old database's address in its environment:\n%s", text)
			}
			for _, l := range strings.Split(text, "\n") {
				if strings.Contains(l, "old-db-password") && !strings.HasPrefix(l, "migrate-env ") {
					t.Errorf("MOVE-DSN-NOT-ARGV: the password is on a command line: %s", l)
				}
			}
			if s := readStatusT(t); s.State != stateDone {
				t.Errorf("MOVE-DONE: the status says %+v", s)
			}
			if _, err := os.Stat(ipath("pgsql")); !os.IsNotExist(err) {
				t.Errorf("MOVE-CLEANUP-AFTER: the bundled PostgreSQL is still installed after a checked move (%v)", err)
			}
			if tp.has("Forge Solo could not move") {
				t.Errorf("MOVE-NO-FAIL-TRAY: %q", tp.all())
			}
		})
	}
}

// A move that fails at any step replaces nothing and stops nothing else: the old database is
// stopped, the junctions are removed, the status file says failed with why (the migrator's own
// words when it recorded them), the tray says so, and the nodes, the API and the miner start all
// the same: the API serves the maintenance page, and the miner does not mine.
func TestAFailedMoveStillStartsEverything(t *testing.T) {
	for _, c := range []struct {
		code, plan  string
		env         map[string]string
		setup       func(t *testing.T)
		reason      string
		status      int
		ranPrepare  bool
		leftRunning bool // the old database could not be stopped
	}{
		{code: "MOVE-FAIL-NOT-STOPPED", plan: "move", setup: func(t *testing.T) {
			signalPostgres = func(int, byte) error { return errors.New("no pipe") }
		}, reason: "the old database did not shut down cleanly", status: codeRefused, ranPrepare: true, leftRunning: true},
		{code: "MOVE-FAIL-PGCTL", plan: "move", env: map[string]string{"FS_PGCTL_EXIT": "1"},
			reason: "the old database did not start", status: codeSource},
		{code: "MOVE-FAIL-PREPARE", plan: "move", env: map[string]string{"FS_PREPARE": "20", "FS_RECORDS": "20"},
			reason: "the migrator says why (20)", status: 20, ranPrepare: true},
		{code: "MOVE-FAIL-PREPARE-UNRECORDED", plan: "move", env: map[string]string{"FS_PREPARE": "21"},
			reason: migrateExe + " stopped with exit code 21", status: 21, ranPrepare: true},
		{code: "MOVE-FAIL-COMMIT", plan: "merge", env: map[string]string{"FS_COMMIT": "30", "FS_RECORDS": "30"},
			reason: "the migrator says why (30)", status: 30, ranPrepare: true},
		{code: "MOVE-FAIL-NO-MIGRATOR", plan: "merge", setup: func(t *testing.T) {
			moved(t, "pg_control-shutdown-2")
			if err := os.Remove(ipath(migrateExe)); err != nil {
				t.Fatal(err)
			}
		}, reason: migrateExe + " could not run", status: codeOther},
		{code: "MOVE-FAIL-NO-PASSWORD", plan: "move", setup: func(t *testing.T) { sec.DBPass = "" },
			reason: "secrets.env lacks the old database's password", status: codeSource},
		{code: "MOVE-FAIL-REFUSED", plan: "failed:the old database is not PostgreSQL 16 (its PG_VERSION says 15)",
			reason: "the old database is not PostgreSQL 16 (its PG_VERSION says 15)", status: codeRefused},
	} {
		t.Run(c.code, func(t *testing.T) {
			calls, tp := moveWorld(t, c.plan)
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			// The data folder's name is one PostgreSQL cannot take, with no short name: the move goes
			// through a junction.
			pd := folder(t, "ProgramData")
			t.Setenv("ProgramData", pd)
			westernCodePage(t, nil)
			moveDataDir(t, filepath.Join(t.TempDir(), cn))
			if c.setup != nil {
				c.setup(t)
			}
			bootAll(t)
			s := readStatusT(t)
			if s.State != stateFailed || s.Reason != c.reason || s.Code != c.status {
				t.Errorf("%s: the status is %+v, want failed (%d) because %q", c.code, s, c.status, c.reason)
			}
			if postmasterPID() != 0 && !c.leftRunning {
				t.Errorf("%s-PG-STOPPED: the old database was left running", c.code)
			}
			if got := callsIn(calls); count(got, "migrate prepare") != map[bool]int{false: 0, true: 1}[c.ranPrepare] || count(got, "migrate commit") > 1 {
				t.Errorf("%s-STEPS: %v", c.code, got)
			}
			if _, err := os.Lstat(filepath.Join(pd, "ForgeSolo", "links", "0123abcd")); !os.IsNotExist(err) {
				t.Errorf("%s-JUNCTIONS: the move's junctions are still there (%v)", c.code, err)
			}
			if !tp.has(moveFailedTip) || tp.has("Forge Solo: running") {
				t.Errorf("%s-TRAY: the tray said %q", c.code, tp.all())
			}
			if _, err := os.Stat(dbPath()); c.plan == "move" && !os.IsNotExist(err) {
				t.Errorf("%s-NOTHING-REPLACED: a forgesolo.db is there after a failed move", c.code)
			}
		})
	}
}

// A move needed when the bundled PostgreSQL is not installed: forgesolo.db deleted after a checked
// move (which removed PostgreSQL), or an earlier version's data copied in after an install that had
// none (a new PC). The status file says the move needs PostgreSQL and that running the installer
// again installs it, the tray says the move failed, and everything else starts. Once it is back, the
// next start moves the data.
func TestAMoveWithoutTheBundledPostgreSQLSaysHowToGetIt(t *testing.T) {
	for _, c := range []struct {
		code, plan string
		setup      func(t *testing.T)
		missing    []string
	}{
		{code: "MOVE-FAIL-NO-PGSQL", plan: "move", setup: func(t *testing.T) {
			moved(t, "pg_control-shutdown")
			prepareDatabase() // a checked move: the bundled PostgreSQL goes
			if _, err := os.Stat(ipath("pgsql")); !os.IsNotExist(err) {
				t.Fatalf("setup: the bundled PostgreSQL is still there (%v)", err)
			}
			if err := os.Remove(dbPath()); err != nil {
				t.Fatal(err)
			}
		}, missing: []string{"pg_ctl.exe", "postgres.exe"}},
		{code: "MOVE-FAIL-NO-PGSQL-COPIED", plan: "merge", setup: func(t *testing.T) {
			writeFile(t, dbPath(), "", 0o600) // the new install's database
			if err := os.RemoveAll(ipath("pgsql")); err != nil {
				t.Fatal(err)
			}
		}, missing: []string{"pg_ctl.exe", "postgres.exe"}},
		{code: "MOVE-FAIL-NO-POSTGRES-EXE", plan: "move", setup: func(t *testing.T) {
			if err := os.Remove(ipath("pgsql", "bin", "postgres.exe")); err != nil { // an antivirus took it
				t.Fatal(err)
			}
		}, missing: []string{"postgres.exe"}},
	} {
		t.Run(c.code, func(t *testing.T) {
			calls, tp := moveWorld(t, c.plan)
			c.setup(t)
			bootAll(t)
			s := readStatusT(t)
			if s.State != stateFailed || s.Code != codeSource || s.Reason != noPostgresReason {
				t.Fatalf("%s: the status is %+v, want failed (%d) because %q", c.code, s, codeSource, noPostgresReason)
			}
			for _, name := range []string{"pg_ctl.exe", "postgres.exe"} {
				named := strings.Contains(s.Detail, ipath("pgsql", "bin", name))
				if want := slices.Contains(c.missing, name); named != want {
					t.Errorf("%s-DETAIL: the detail names %s: %v, want %v: %q", c.code, name, named, want, s.Detail)
				}
			}
			if got := callsIn(calls); count(got, "pg_ctl")+count(got, "migrate prepare")+count(got, "migrate commit") != 0 {
				t.Errorf("%s-NOTHING-RUN: %v", c.code, got)
			}
			if !tp.has(moveFailedTip) || tp.has("Forge Solo: running") {
				t.Errorf("%s-TRAY: the tray said %q", c.code, tp.all())
			}
		})
	}
	t.Run("MOVE-FAIL-NO-PGSQL-REMEDY", func(t *testing.T) {
		_, tp := moveWorld(t, "move")
		saved := filepath.Join(t.TempDir(), "pgsql")
		if err := os.Rename(ipath("pgsql"), saved); err != nil {
			t.Fatal(err)
		}
		bootAll(t)
		if s := readStatusT(t); s.Reason != noPostgresReason {
			t.Fatalf("setup: %+v", s)
		}
		if err := os.Rename(saved, ipath("pgsql")); err != nil { // the installer run again
			t.Fatal(err)
		}
		prepareDatabase() // the next start
		if s := readStatusT(t); s.State != stateDone {
			t.Errorf("MOVE-FAIL-NO-PGSQL-REMEDY: with PostgreSQL back the next start gives %+v", s)
		}
		tp.add("something else")
		showRunning()
		if tp.last() != "Forge Solo: running" {
			t.Errorf("MOVE-FAIL-NO-PGSQL-REMEDY-TRAY: the tray says %q", tp.last())
		}
	})
}

// A start that needs nothing replaces what an earlier start recorded (a failed move, a damaged old
// folder, a move done, a status file that cannot be read) with "none", as forge-solo-migrate run
// does on Umbrel: the API leaves the maintenance page, the miner mines, and no banner stays for good.
func TestAStartThatNeedsNothingClearsAnEarlierStatus(t *testing.T) {
	for _, c := range []struct {
		name, earlier string
		moved         bool // a checked move; otherwise the old data was deleted
	}{
		{name: "failed", earlier: `{"state": "failed", "code": 10, "reason": "the old database did not start", "detail": "", "at": "2026-10-04T01:02:03Z", "version": "1.0.13"}`},
		{name: "degraded", earlier: `{"state": "degraded", "code": 0, "reason": "the old database's pg_control is missing", "detail": "", "at": "2026-10-04T01:02:03Z", "version": "1.0.13"}`},
		{name: "done", moved: true, earlier: `{"state": "done", "code": 0, "reason": "the data of the earlier version is in forgesolo.db", "detail": "", "at": "2026-10-04T01:02:03Z", "version": "1.0.13"}`},
		{name: "unreadable", earlier: `{"state": "fail`},
	} {
		t.Run(c.name, func(t *testing.T) {
			calls, tp := moveWorld(t, "move")
			if c.moved {
				moved(t, "pg_control-shutdown")
			} else if err := os.RemoveAll(dpath("pgdata")); err != nil {
				t.Fatal(err)
			}
			writeFile(t, statusPath(), c.earlier+"\n", 0o600)
			bootAll(t)
			if s, ok, err := readStatus(); !ok || err != nil || s.State != stateNone {
				t.Errorf("MOVE-STALE-STATUS-CLEARED: after an earlier %s, a start that needs nothing leaves %+v (%v, %v)", c.name, s, ok, err)
			}
			if got := callsIn(calls); count(got, "migrate plan")+count(got, "pg_ctl") != 0 {
				t.Errorf("MOVE-STALE-STATUS-NOTHING-RUN: %v", got)
			}
			if tp.has(moveFailedTip) || !waitFor(5*time.Second, func() bool { return tp.last() == "Forge Solo: running" }) {
				t.Errorf("MOVE-STALE-STATUS-TRAY: the tray said %q", tp.all())
			}
		})
	}
}

// A commit that reports success while the files say otherwise (no marker that matches the old
// data, so the next start would merge): nothing that a move may still need is removed.
func TestAMoveNotCheckedKeepsPostgreSQL(t *testing.T) {
	calls, _ := moveWorld(t, "merge")
	writeFile(t, dbPath(), "", 0o600)
	writeFile(t, dpath(markerName), `{"pg_control_sha256": "not this one"}`, 0o600)
	// A commit that says it succeeded and leaves the marker as it was.
	keepMarker := strings.Replace(migratorStandIn,
		`printf '{"pg_control_sha256": "%s"}\n' "$(sha256sum "$pg/global/pg_control" | cut -d' ' -f1)" > "$(dirname "$db")/postgres-migrated.json"`, ":", 1)
	if keepMarker == migratorStandIn {
		t.Fatal("setup: the stand-in no longer writes the marker as this test expects")
	}
	writeFile(t, ipath(migrateExe), "#!/bin/sh\n"+keepMarker+"\n", 0o755)
	bootAll(t)
	if count(callsIn(calls), "migrate commit") != 1 {
		t.Fatalf("setup: %v", callsIn(calls))
	}
	if _, err := os.Stat(ipath("pgsql", "bin", "pg_ctl.exe")); err != nil {
		t.Errorf("MOVE-CLEANUP-ONLY-CHECKED: a move the next start would not take for done removed the bundled PostgreSQL (%v)", err)
	}
	if !strings.Contains(launcherLog(), "the next start would not take it for done") {
		t.Errorf("MOVE-CLEANUP-ONLY-CHECKED-LOGGED:\n%s", launcherLog())
	}
}

// moveDataDir moves the data folder of a moveWorld to dir, old data and all.
func moveDataDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dataDir, dir); err != nil {
		t.Fatal(err)
	}
	dataDir = dir
}

// The database is in use (deferred, 31): the migrator recorded it, nothing was replaced, and the
// start goes on as usual on the database there; the next start finishes the move.
func TestADeferredMoveStartsNormally(t *testing.T) {
	calls, tp := moveWorld(t, "merge")
	moved(t, "pg_control-shutdown-2")
	t.Setenv("FS_PREPARE", "31")
	bootAll(t)
	if s := readStatusT(t); s.State != stateDeferred {
		t.Errorf("MOVE-DEFERRED-STATUS: %+v", s)
	}
	if got := callsIn(calls); count(got, "migrate commit") != 0 || count(got, "stop") != 1 {
		t.Errorf("MOVE-DEFERRED-STEPS: %v", got)
	}
	if tp.has(moveFailedTip) || !waitFor(5*time.Second, func() bool { return tp.last() == "Forge Solo: running" }) {
		t.Errorf("MOVE-DEFERRED-RUNNING: the tray said %q", tp.all())
	}
}

// Quit, or Windows ending the session, while the migrator copies the data: the migrator is ended,
// the old database stopped, nothing is put in place, nothing else is started, and the status is
// left as it was: the next start moves the data.
func TestQuitDuringTheMove(t *testing.T) {
	for _, sessionEnd := range []bool{false, true} {
		t.Run("session end "+strconv.FormatBool(sessionEnd), func(t *testing.T) {
			calls, tp := moveWorld(t, "move")
			t.Setenv("FS_PREPARE_WAIT", "60")
			sessionEnding.Store(sessionEnd)
			t.Cleanup(func() { sessionEnding.Store(false) })
			booted := make(chan struct{})
			go func() { boot(); close(booted) }()
			if !waitFor(10*time.Second, func() bool { return count(callsIn(calls), "migrate prepare") == 1 }) {
				t.Fatalf("setup: prepare never ran: %v", callsIn(calls))
			}
			start := time.Now()
			stopForExit()
			select {
			case <-booted:
			case <-time.After(10 * time.Second):
				t.Fatal("MOVE-QUIT-ENDS: boot went on waiting for the migrator after the stop")
			}
			if time.Since(start) > 8*time.Second {
				t.Errorf("MOVE-QUIT-QUICK: the stop took %v", time.Since(start))
			}
			got := callsIn(calls)
			if count(got, "migrate commit") != 0 || count(got, "bch2") != 0 || count(got, "api") != 0 {
				t.Errorf("MOVE-QUIT-NO-COMMIT: %v", got)
			}
			if postmasterPID() != 0 || count(got, "stop") == 0 {
				t.Errorf("MOVE-QUIT-PG-STOPPED: the old database was left running: %v", got)
			}
			if started("migrate") {
				t.Error("MOVE-QUIT-MIGRATOR-ENDED: the migrator still runs")
			}
			if _, ok, _ := readStatus(); ok {
				t.Errorf("MOVE-QUIT-STATUS-KEPT: the stop wrote a status")
			}
			if _, err := os.Stat(dbPath()); !os.IsNotExist(err) {
				t.Errorf("MOVE-QUIT-NOTHING-REPLACED: %v", err)
			}
			// While Windows ends the session the stop's tooltip is set on the side: it is there
			// before the test's tray goes.
			waitFor(5*time.Second, func() bool { return tp.has("Forge Solo: shutting down cleanly") })
		})
	}
}

// The migrator is never started again when it exits, as the programs that run all the time are:
// each of its steps runs once.
func TestTheMigratorIsNotStartedAgain(t *testing.T) {
	calls, _ := moveWorld(t, "move")
	t.Setenv("FS_PREPARE", "20")
	bootAll(t)
	time.Sleep(500 * time.Millisecond) // the first wait before a start again is 50 ms here
	if n := count(callsIn(calls), "migrate prepare"); n != 1 {
		t.Fatalf("MOVE-NOT-SUPERVISED: prepare ran %d times", n)
	}
	if strings.Contains(launcherLog(), "exited on its own") && strings.Contains(launcherLog(), "(migrate)") {
		t.Errorf("MOVE-NOT-SUPERVISED-LOG: the migrator's exit was taken for a program to start again:\n%s", launcherLog())
	}
}

// Each step of the migrator starts once the one before has ended, however soon: the one before
// is no longer taken for running ("already running"), whatever else follows its exit.
func TestTheMigratorRunsItsStepsOneAfterAnother(t *testing.T) {
	calls, _ := moveWorld(t, "move")
	supervising.Store(false) // nothing else then forgets a program that has ended
	for _, step := range []string{"prepare", "commit"} {
		if code, err := runMigrator(nil, step, "--db", dbPath(), "--pgdata", dpath("pgdata")); err != nil || code != 0 {
			t.Fatalf("MOVE-STEPS-IN-TURN: %s gave %d, %v; ran %v", step, code, err, callsIn(calls))
		}
	}
}

// A migrator step that does not end is ended after its time, and the move fails as any other.
func TestAMigratorThatHangsIsEnded(t *testing.T) {
	calls, _ := moveWorld(t, "move")
	t.Setenv("FS_PREPARE_WAIT", "60")
	migrateLimit = 500 * time.Millisecond
	bootAll(t)
	if s := readStatusT(t); s.State != stateFailed || s.Reason != migrateExe+" did not finish" {
		t.Errorf("MOVE-LIMIT: %+v", s)
	}
	if postmasterPID() != 0 || count(callsIn(calls), "migrate commit") != 0 {
		t.Errorf("MOVE-LIMIT-STOPPED: %v", callsIn(calls))
	}
}

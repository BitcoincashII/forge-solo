//go:build !windows

package main

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakePostgres puts stand-ins for the bundled PostgreSQL programs where the launcher runs them
// from. pg_ctl exits with pgctlExit; createdb and psql record that they ran.
func fakePostgres(t *testing.T, pgctlExit int) (calls string) {
	t.Helper()
	saved, savedData := installDir, dataDir
	installDir, dataDir = t.TempDir(), t.TempDir()
	t.Cleanup(func() { installDir, dataDir = saved, savedData })
	calls = filepath.Join(t.TempDir(), "calls")
	t.Setenv("FS_TEST_CALLS", calls)
	for name, body := range map[string]string{
		"pg_ctl.exe":   `echo "pg_ctl $*" >> "$FS_TEST_CALLS"; exit ` + strconv.Itoa(pgctlExit),
		"createdb.exe": `echo createdb >> "$FS_TEST_CALLS"`,
		"psql.exe":     `echo psql >> "$FS_TEST_CALLS"`,
	} {
		// The launcher names them with Windows separators; on Linux that is one file name.
		if err := os.WriteFile(ipath("pgsql\\bin\\"+name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	md(dpath("pgdata"))
	if err := os.WriteFile(dpath("pgdata", "PG_VERSION"), []byte("16\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return calls
}

// The database counts as up only when pg_ctl says the server it started is ready. Something else
// listening on the port is not the database, and is not handed the password.
func TestDatabaseIsUpOnlyWhenPgCtlSaysSo(t *testing.T) {
	foreign, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	saved := pgPort
	_, pgPort, _ = net.SplitHostPort(foreign.Addr().String())
	t.Cleanup(func() { pgPort = saved })

	calls := fakePostgres(t, 1)
	if startPostgres() {
		t.Fatal("DB-PGCTL-FAILED: pg_ctl could not start the server, yet the database counts as up (another program holds the port)")
	}
	if b, _ := os.ReadFile(calls); strings.Contains(string(b), "createdb") || strings.Contains(string(b), "psql") {
		t.Fatalf("DB-PGCTL-FAILED: the database tools connected to the port anyway:\n%s", b)
	}

	calls = fakePostgres(t, 0)
	if !startPostgres() {
		t.Fatal("DB-PGCTL-OK: pg_ctl started the server, yet the database counts as down")
	}
	if b, _ := os.ReadFile(calls); !strings.HasSuffix(string(b), "\ncreatedb\npsql\n") {
		t.Fatalf("DB-PGCTL-OK: the database was not created and loaded: %q", b)
	}
}

// userData puts the data folder under a user folder named user, and returns what pg_ctl was given
// as its data folder, after startPostgres has run.
func userData(t *testing.T, user string) (calls string) {
	t.Helper()
	calls = fakePostgres(t, 0)
	savedData := dataDir
	dataDir = filepath.Join(t.TempDir(), "Users", user, "AppData", "Roaming", "ForgeSolo")
	t.Cleanup(func() { dataDir = savedData })
	md(dpath("pgdata"))
	if err := os.WriteFile(dpath("pgdata", "PG_VERSION"), []byte("16\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return calls
}

// pgCtlData is the data folder pg_ctl was started with, from the calls the stand-ins recorded.
func pgCtlData(calls string) string {
	b, _ := os.ReadFile(calls)
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) > 2 && f[0] == "pg_ctl" && f[1] == "-D" {
			return f[2]
		}
	}
	return ""
}

// A user folder with a Polish l on a Western Windows, which keeps the short name PAWE~1 for it:
// the database starts from the short name.
func TestTheDatabaseStartsFromTheShortNameTheDriveKeeps(t *testing.T) {
	calls := userData(t, pawel)
	user := filepath.Dir(filepath.Dir(filepath.Dir(dataDir)))
	codePage(t, map[string]string{user: "PAWE~1"}, jose)
	want := filepath.Join(filepath.Dir(user), "PAWE~1", "AppData", "Roaming", "ForgeSolo", "pgdata")
	if !startPostgres() || pgCtlData(calls) != want {
		b, _ := os.ReadFile(dpath("launcher.log"))
		t.Fatalf("PGPATH-START-SHORT: pg_ctl was given %q, want %q:\n%s", pgCtlData(calls), want, b)
	}
}

// A user folder the code page holds, on a drive with short names off: the database starts from
// the long path, as it did in the release before.
func TestTheDatabaseStartsFromANameTheCodePageHolds(t *testing.T) {
	calls := userData(t, jose)
	codePage(t, nil, jose)
	if !startPostgres() || pgCtlData(calls) != dpath("pgdata") {
		b, _ := os.ReadFile(dpath("launcher.log"))
		t.Fatalf("PGPATH-START-CODE-PAGE: pg_ctl was given %q, want %q:\n%s", pgCtlData(calls), dpath("pgdata"), b)
	}
}

// A folder name PostgreSQL cannot take in any form: the database cannot start, and launcher.log
// names that folder and why, instead of blaming the drive or saying only that it did not start.
func TestADatabasePathItCannotTakeIsSaidSo(t *testing.T) {
	calls := userData(t, pawel)
	codePage(t, nil, jose)
	if startPostgres() {
		t.Fatal("PGPATH-NO-SHORT-SAID: the database counts as started from a path it cannot take")
	}
	b, _ := os.ReadFile(dpath("launcher.log"))
	if !strings.Contains(string(b), `the folder name "`+pawel+`"`) || strings.Contains(string(b), "this drive keeps no short names") {
		t.Fatalf("PGPATH-NAMES-FOLDER: launcher.log does not name the folder PostgreSQL cannot take:\n%s", b)
	}
	if _, err := os.Stat(calls); err == nil {
		t.Error("PGPATH-NO-SHORT-SAID: the database tools were run anyway")
	}
}

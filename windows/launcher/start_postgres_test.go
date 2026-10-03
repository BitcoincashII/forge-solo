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
		"pg_ctl.exe":   "exit " + strconv.Itoa(pgctlExit),
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
	if _, err := os.Stat(calls); err == nil {
		b, _ := os.ReadFile(calls)
		t.Fatalf("DB-PGCTL-FAILED: the database tools connected to the port anyway:\n%s", b)
	}

	calls = fakePostgres(t, 0)
	if !startPostgres() {
		t.Fatal("DB-PGCTL-OK: pg_ctl started the server, yet the database counts as down")
	}
	if b, _ := os.ReadFile(calls); string(b) != "createdb\npsql\n" {
		t.Fatalf("DB-PGCTL-OK: the database was not created and loaded: %q", b)
	}
}

// A path the bundled PostgreSQL cannot take, on a drive that keeps no short names: the database
// cannot start, and launcher.log says why, instead of only that it did not.
func TestADatabasePathWithNoShortNameIsSaidSo(t *testing.T) {
	calls := fakePostgres(t, 0)
	cn := string([]rune{0x6D4B, 0x8BD5})
	savedData := dataDir
	dataDir = filepath.Join(t.TempDir(), "data-"+cn)
	t.Cleanup(func() { dataDir = savedData })
	md(dpath("pgdata"))
	if err := os.WriteFile(dpath("pgdata", "PG_VERSION"), []byte("16\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	savedShort := shortPath
	shortPath = func(p string) (string, error) { return p, nil } // no short names on this drive
	t.Cleanup(func() { shortPath = savedShort })

	if startPostgres() {
		t.Fatal("PGPATH-NO-SHORT-SAID: the database counts as started from a path it cannot take")
	}
	b, _ := os.ReadFile(dpath("launcher.log"))
	if !strings.Contains(string(b), "have characters the bundled PostgreSQL cannot take") {
		t.Fatalf("PGPATH-NO-SHORT-SAID: launcher.log does not say why the database cannot start:\n%s", b)
	}
	if _, err := os.Stat(calls); err == nil {
		t.Error("PGPATH-NO-SHORT-SAID: the database tools were run anyway")
	}
}

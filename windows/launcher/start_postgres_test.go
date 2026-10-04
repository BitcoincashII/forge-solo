//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The old database counts as up only when pg_ctl says the server it started is ready. Something
// else listening on the port is not the database: the migrator is never given its address, and
// with it the password.
func TestTheOldDatabaseIsUpOnlyWhenPgCtlSaysSo(t *testing.T) {
	calls, _ := moveWorld(t, "move")
	t.Setenv("FS_PGCTL_EXIT", "1")
	bootAll(t)
	if b, _ := os.ReadFile(calls); strings.Contains(string(b), "migrate prepare") || strings.Contains(string(b), "migrate-env") {
		t.Fatalf("DB-PGCTL-FAILED: the migrator was run against a database pg_ctl did not start:\n%s", b)
	}
	if s := readStatusT(t); s.State != stateFailed || s.Reason != "the old database did not start" {
		t.Fatalf("DB-PGCTL-FAILED-STATUS: %+v", s)
	}
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

// userMove is a move from a data folder under a user folder named user.
func userMove(t *testing.T, user string) (calls, data string) {
	t.Helper()
	calls, _ = moveWorld(t, "move")
	moveDataDir(t, filepath.Join(t.TempDir(), "Users", user, "AppData", "Roaming", "ForgeSolo"))
	return calls, dataDir
}

// A user folder with a Polish l on a Western Windows, which keeps the short name PAWE~1 for it:
// the old database starts from the short name.
func TestTheOldDatabaseStartsFromTheShortNameTheDriveKeeps(t *testing.T) {
	calls, data := userMove(t, pawel)
	user := filepath.Dir(filepath.Dir(filepath.Dir(data)))
	westernCodePage(t, map[string]string{user: "PAWE~1"})
	bootAll(t)
	if want := filepath.Join(filepath.Dir(user), "PAWE~1", "AppData", "Roaming", "ForgeSolo", "pgdata"); pgCtlData(calls) != want {
		t.Fatalf("PGPATH-START-SHORT: pg_ctl was given %q, want %q:\n%s", pgCtlData(calls), want, launcherLog())
	}
}

// A user folder the code page holds: the old database starts from the long path, as it did in
// 1.0.12, whatever short name the drive keeps.
func TestTheOldDatabaseStartsFromANameTheCodePageHolds(t *testing.T) {
	calls, data := userMove(t, jose)
	westernCodePage(t, map[string]string{filepath.Dir(filepath.Dir(filepath.Dir(data))): "JOS~1"})
	bootAll(t)
	if pgCtlData(calls) != dpath("pgdata") {
		t.Fatalf("PGPATH-START-CODE-PAGE: pg_ctl was given %q, want %q:\n%s", pgCtlData(calls), dpath("pgdata"), launcherLog())
	}
}

// A folder name PostgreSQL cannot take in any form, on a drive that keeps no short names: the move
// goes through a junction under %ProgramData%, launcher.log says so, and the junction is gone
// afterwards.
func TestAFolderNamePostgreSQLCannotTakeGoesThroughAJunction(t *testing.T) {
	calls, data := userMove(t, cn)
	pd := folder(t, "ProgramData")
	t.Setenv("ProgramData", pd)
	westernCodePage(t, nil)
	bootAll(t)
	link := filepath.Join(pd, "ForgeSolo", "links", "0123abcd", "data")
	if pgCtlData(calls) != filepath.Join(link, "pgdata") {
		t.Fatalf("PGPATH-START-JUNCTION: pg_ctl was given %q, want it through %s:\n%s", pgCtlData(calls), link, launcherLog())
	}
	if !strings.Contains(launcherLog(), "PostgreSQL cannot take the name of "+data+", so it is given the junction "+link) {
		t.Errorf("PGPATH-JUNCTION-LOGGED: launcher.log does not say the move went through a junction:\n%s", launcherLog())
	}
	if s := readStatusT(t); s.State != stateDone {
		t.Errorf("PGPATH-JUNCTION-MOVED: %+v", s)
	}
	if _, err := os.Lstat(filepath.Dir(link)); !os.IsNotExist(err) {
		t.Errorf("PGPATH-JUNCTION-REMOVED: the junction is still there after the move (%v)", err)
	}
	if _, err := os.Stat(filepath.Join(data, "pgdata", "global", "pg_control")); err != nil {
		t.Errorf("PGPATH-JUNCTION-DATA-KEPT: %v", err)
	}
}

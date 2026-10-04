package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func withSecretsDir(t *testing.T) {
	t.Helper()
	savedData, savedSec := dataDir, sec
	dataDir, sec = t.TempDir(), secrets{}
	t.Cleanup(func() { dataDir, sec = savedData, savedSec })
}

// An install from 1.0.12 or before gains the Settings password, and keeps every other secret as it
// was: the database's password in particular must stay what the database has.
func TestSecretsGainTheSettingsPassword(t *testing.T) {
	withSecretsDir(t)
	old := "BCH2=b1\nAUX=a1\nDB=d1\nTOKEN=t1\n"
	if err := os.WriteFile(dpath("secrets.env"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := setupSecrets(); err != nil {
		t.Fatal(err)
	}
	if sec.BCH2Pass != "b1" || sec.AuxPass != "a1" || sec.DBPass != "d1" || sec.Token != "t1" {
		t.Fatalf("SECRETS-KEPT: the other secrets changed: %+v", sec)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(sec.Settings) {
		t.Fatalf("SECRETS-SETTINGS-ADDED: the Settings password is %q, want 64 hex characters", sec.Settings)
	}
	b, _ := os.ReadFile(dpath("secrets.env"))
	for _, line := range []string{"BCH2=b1", "AUX=a1", "DB=d1", "TOKEN=t1", "SETTINGS=" + sec.Settings} {
		if !strings.Contains(string(b), line+"\n") {
			t.Errorf("SECRETS-WRITTEN: secrets.env lacks %q:\n%s", line, b)
		}
	}
	first := sec.Settings
	sec = secrets{}
	if err := setupSecrets(); err != nil {
		t.Fatal(err)
	}
	if sec.Settings != first {
		t.Fatalf("SECRETS-SETTINGS-STABLE: the Settings password changed between starts")
	}
	if _, err := os.Stat(dpath("secrets.env.tmp")); !os.IsNotExist(err) {
		t.Error("SECRETS-WRITTEN: the temporary copy was left behind")
	}
}

// A fresh install makes all of them.
func TestSecretsFresh(t *testing.T) {
	withSecretsDir(t)
	if err := setupSecrets(); err != nil {
		t.Fatal(err)
	}
	if sec.BCH2Pass == "" || sec.AuxPass == "" || sec.DBPass == "" || sec.Token == "" || len(sec.Settings) != 64 {
		t.Fatalf("SECRETS-FRESH: %+v", sec)
	}
}

// secrets.env holds the database's password. A file that cannot be read (another program holding
// it a moment) is left as it is, and Forge Solo does not start, rather than replace it with new
// secrets: the database would never open again.
func TestSecretsUnreadableAreKept(t *testing.T) {
	withSecretsDir(t)
	if os.Geteuid() == 0 {
		t.Skip("root reads a file of mode 0")
	}
	if err := os.WriteFile(dpath("secrets.env"), []byte("BCH2=b1\nAUX=a1\nDB=d1\nTOKEN=t1\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dpath("secrets.env"), 0o600) })
	if err := setupSecrets(); err == nil {
		t.Fatalf("SECRETS-UNREADABLE-KEPT: an unreadable secrets.env did not stop the start: %+v", sec)
	}
	_ = os.Chmod(dpath("secrets.env"), 0o600)
	if b, _ := os.ReadFile(dpath("secrets.env")); string(b) != "BCH2=b1\nAUX=a1\nDB=d1\nTOKEN=t1\n" {
		t.Fatalf("SECRETS-UNREADABLE-KEPT: the file was replaced:\n%s", b)
	}
}

// DB= is the password of the PostgreSQL database 1.0.12 and before kept their data in, which only
// moving that data needs. A secrets.env without it, beside that database, no longer stops Forge
// Solo: it starts, and is not given a new one, which could never open the old database (going back
// to 1.0.12 needs the one it has). Only a move that is needed fails without it (MOVE-FAIL-NO-PASSWORD).
// A secret that is missing is made again alone, the others kept as they are; with no old database,
// DB= is made too, for 1.0.12 if it is installed again.
func TestSecretsWithoutTheDatabasePassword(t *testing.T) {
	withSecretsDir(t)
	if err := os.WriteFile(dpath("secrets.env"), []byte("BCH2=b1\nAUX=a1\nTOKEN=t1\nSETTINGS=s1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	md(dpath("pgdata"))
	if err := os.WriteFile(dpath("pgdata", "PG_VERSION"), []byte("16\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := setupSecrets(); err != nil {
		t.Fatalf("SECRETS-NO-DB-STARTS: a secrets.env without DB= beside the old database stops the start: %v", err)
	}
	if sec.DBPass != "" || sec.BCH2Pass != "b1" || sec.AuxPass != "a1" || sec.Token != "t1" || sec.Settings != "s1" {
		t.Fatalf("SECRETS-NO-DB-KEPT: %+v", sec)
	}
	if b, _ := os.ReadFile(dpath("secrets.env")); strings.Contains(string(b), "DB=") {
		t.Fatalf("SECRETS-NO-DB-NOT-MADE: a new old-database password was written:\n%s", b)
	}

	// Another secret missing: made again alone; DB= stays what it is.
	if err := os.WriteFile(dpath("secrets.env"), []byte("BCH2=b1\nDB=d1\nTOKEN=t1\nSETTINGS=s1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sec = secrets{}
	if err := setupSecrets(); err != nil || sec.AuxPass == "" || sec.BCH2Pass != "b1" || sec.DBPass != "d1" || sec.Token != "t1" {
		t.Fatalf("SECRETS-ONLY-MISSING: %v %+v", err, sec)
	}
	if b, _ := os.ReadFile(dpath("secrets.env")); !strings.Contains(string(b), "\nDB=d1\n") || !strings.Contains(string(b), "AUX="+sec.AuxPass+"\n") {
		t.Fatalf("SECRETS-DB-KEPT: DB= was rewritten:\n%s", b)
	}

	_ = os.RemoveAll(dpath("pgdata"))
	if err := os.WriteFile(dpath("secrets.env"), []byte("BCH2=b1\nAUX=a1\nTOKEN=t1\nSETTINGS=s1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sec = secrets{}
	if err := setupSecrets(); err != nil || sec.DBPass == "" || sec.BCH2Pass != "b1" {
		t.Fatalf("SECRETS-NO-DB-FRESH: with no old database DB= was not made, or the others changed: %v %+v", err, sec)
	}
}

// The old database's password missing, after a move whose old data is unchanged since: Forge Solo
// starts as usual. It is needed only for a move.
func TestAMovedInstallStartsWithoutTheOldPassword(t *testing.T) {
	withSecretsDir(t)
	if err := os.WriteFile(dpath("secrets.env"), []byte("BCH2=b1\nAUX=a1\nTOKEN=t1\nSETTINGS=s1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	md(dpath("pgdata", "global"))
	for name, spec := range map[string]string{"pgdata/PG_VERSION": "16\n", "pgdata/global/pg_control": "@pg_control-shutdown",
		"forgesolo.db": "", markerName: "@marker:@pg_control-shutdown"} {
		if err := os.WriteFile(dpath(filepath.FromSlash(name)), caseContent(t, spec), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := setupSecrets(); err != nil {
		t.Fatalf("SECRETS-MOVED-STARTS: %v", err)
	}
	prepareDatabase()
	if b, _ := os.ReadFile(dpath("launcher.log")); !strings.Contains(string(b), "old data: none") || strings.Contains(string(b), "status failed") {
		t.Fatalf("SECRETS-MOVED-NONE: without DB=, a moved install does not start as usual:\n%s", b)
	}
}

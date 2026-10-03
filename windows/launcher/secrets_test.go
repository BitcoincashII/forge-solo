package main

import (
	"os"
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

// A secrets.env without the database password, beside a database that exists, is not refilled with
// new secrets: a new password could never open that database. Without a database, nothing is lost
// by making them again.
func TestSecretsWithoutTheDatabasePassword(t *testing.T) {
	withSecretsDir(t)
	damaged := "BCH2=b1\nAUX=a1\nTOKEN=t1\n"
	if err := os.WriteFile(dpath("secrets.env"), []byte(damaged), 0o600); err != nil {
		t.Fatal(err)
	}
	md(dpath("pgdata"))
	if err := os.WriteFile(dpath("pgdata", "PG_VERSION"), []byte("16\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := setupSecrets(); err == nil {
		t.Fatalf("SECRETS-NO-DB-PASSWORD: started with a new database password for an existing database: %+v", sec)
	}
	if b, _ := os.ReadFile(dpath("secrets.env")); string(b) != damaged {
		t.Fatalf("SECRETS-NO-DB-PASSWORD: the file was replaced:\n%s", b)
	}

	_ = os.RemoveAll(dpath("pgdata"))
	sec = secrets{}
	if err := setupSecrets(); err != nil || sec.DBPass == "" || sec.BCH2Pass == "" || sec.AuxPass == "" || sec.Token == "" {
		t.Fatalf("SECRETS-NO-DB-FRESH: with no database the secrets were not made again: %v %+v", err, sec)
	}
}

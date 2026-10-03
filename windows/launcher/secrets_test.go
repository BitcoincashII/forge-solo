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
	setupSecrets()
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
	setupSecrets()
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
	setupSecrets()
	if sec.BCH2Pass == "" || sec.AuxPass == "" || sec.DBPass == "" || sec.Token == "" || len(sec.Settings) != 64 {
		t.Fatalf("SECRETS-FRESH: %+v", sec)
	}
}

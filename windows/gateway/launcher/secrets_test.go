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

var settingsLine = regexp.MustCompile(`(?m)^SETTINGS=([0-9a-f]{64})$`)

// A fresh install makes the Settings password: 64 hex characters, in secrets.env, made once and
// kept at every later start. The page's Settings and the tray's Copy Settings Password go by it.
func TestTheSettingsPasswordIsMadeOnce(t *testing.T) {
	withSecretsDir(t)
	if err := setupSecrets(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(dpath("secrets.env"))
	m := settingsLine.FindStringSubmatch(string(b))
	if m == nil || m[1] != sec.Settings || string(b) != "SETTINGS="+sec.Settings+"\n" {
		t.Fatalf("GWL-SECRETS-FRESH: secrets.env is %d bytes and does not hold the password made, as SETTINGS= and 64 hex characters", len(b))
	}
	first := sec.Settings
	sec = secrets{}
	if err := setupSecrets(); err != nil {
		t.Fatal(err)
	}
	if sec.Settings != first {
		t.Fatal("GWL-SECRETS-KEPT: the Settings password changed between starts")
	}
	if b2, _ := os.ReadFile(dpath("secrets.env")); string(b2) != string(b) {
		t.Fatal("GWL-SECRETS-KEPT: secrets.env was written again at a start that had its password")
	}
	if _, err := os.Stat(dpath("secrets.env.tmp")); !os.IsNotExist(err) {
		t.Error("GWL-SECRETS-NOTMP: the temporary copy was left behind")
	}
}

// Lines secrets.env holds besides the password are kept as they are when the password is added.
func TestSecretsKeepTheirOtherLines(t *testing.T) {
	withSecretsDir(t)
	writeFile(t, dpath("secrets.env"), "KEEP=1\r\nSETTINGS=\nOTHER=two=2\n")
	if err := setupSecrets(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(dpath("secrets.env"))
	if string(b) != "KEEP=1\nOTHER=two=2\nSETTINGS="+sec.Settings+"\n" || len(sec.Settings) != 64 {
		t.Fatalf("GWL-SECRETS-KEPT: secrets.env with other lines and an empty SETTINGS= became %q", strings.ReplaceAll(string(b), sec.Settings, "<made>"))
	}
	if _, err := os.Stat(dpath("secrets.env.tmp")); !os.IsNotExist(err) {
		t.Error("GWL-SECRETS-NOTMP: the temporary copy was left behind")
	}
}

// secrets.env that cannot be read (another program holding it a moment) is left as it is, and
// Forge Gateway does not start, rather than replace the password the user may have pasted
// somewhere.
func TestSecretsUnreadableAreKept(t *testing.T) {
	withSecretsDir(t)
	const kept = "SETTINGS=" + testPassword + "\n"
	writeFile(t, dpath("secrets.env"), kept)
	// Checked once the file is readable again, which unreadable's own clean-up does first.
	t.Cleanup(func() {
		if b, _ := os.ReadFile(dpath("secrets.env")); string(b) != kept {
			t.Errorf("GWL-SECRETS-UNREADABLE: the file was replaced (%d bytes)", len(b))
		}
	})
	unreadable(t, dpath("secrets.env"))
	err := setupSecrets()
	if err == nil || !strings.HasPrefix(err.Error(), "secrets.env cannot be read (") {
		t.Fatalf("GWL-SECRETS-UNREADABLE: an unreadable secrets.env gave %v, not a refusal to start", err)
	}
	if startWhy(err) != "secrets.env cannot be read" {
		t.Errorf("GWL-SECRETS-UNREADABLE: the tray says %q", startWhy(err))
	}
	if _, err := os.Stat(dpath("secrets.env.tmp")); !os.IsNotExist(err) {
		t.Error("GWL-SECRETS-NOTMP: a temporary copy was written")
	}
}

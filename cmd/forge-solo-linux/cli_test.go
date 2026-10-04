package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cliFixture is a release whose bitcoincashII-cli is script (sh), a data directory with its
// bch2.conf, and an installed service whose data is elsewhere.
func cliFixture(t *testing.T, script string) (cliRun, *strings.Builder) {
	t.Helper()
	base := t.TempDir()
	write := func(p, s string, mode os.FileMode) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), mode); err != nil {
			t.Fatal(err)
		}
	}
	inst, data := filepath.Join(base, "rel"), filepath.Join(base, "data")
	write(filepath.Join(inst, "bin", "bitcoincashII-cli"), "#!/bin/sh\n"+script+"\n", 0o755)
	write(filepath.Join(data, "bch2", "bch2.conf"), "rpcport=1\n", 0o600)
	unit := filepath.Join(base, "forge-solo.service")
	write(unit, "[Unit]\n", 0o644)
	stderr := &strings.Builder{}
	return cliRun{inst: inst, dataDir: data, svcData: filepath.Join(base, "svc"), unit: unit, args: []string{"getblockcount"},
		stdin: strings.NewReader(""), stdout: io.Discard, stderr: stderr}, stderr
}

// With no Forge Solo running on the data directory, cli says so instead of asking another node,
// and names the service's node when the service is installed.
func TestCLIWithNoForgeSoloRunning(t *testing.T) {
	r, _ := cliFixture(t, `echo ran > "$(dirname "$0")/ran"`)
	ran := filepath.Join(r.inst, "bin", "ran")
	for _, lockFile := range []bool{false, true} { // never run there, or run and stopped
		if lockFile {
			l, err := lockDataDir(r.dataDir)
			if err != nil {
				t.Fatal(err)
			}
			l.Close()
		}
		_, err := r.run()
		if err == nil || !strings.Contains(err.Error(), "no Forge Solo is running with the data directory "+r.dataDir) {
			t.Errorf("CLI-NOT-RUNNING: lock file %v: %v", lockFile, err)
		}
		if err == nil || !strings.Contains(err.Error(), "sudo /opt/forge-solo/forge-solo cli getblockcount") {
			t.Errorf("CLI-SERVICE-HINT: lock file %v: the service's node is not named: %v", lockFile, err)
		}
		if _, err := os.Stat(ran); err == nil {
			t.Errorf("CLI-NOT-RUN: lock file %v: bitcoincashII-cli was run", lockFile)
		}
	}
	r.unit = filepath.Join(t.TempDir(), "none.service")
	if _, err := r.run(); err == nil || strings.Contains(err.Error(), "sudo") {
		t.Errorf("CLI-NO-SERVICE: with no service, cli named one: %v", err)
	}
}

// With a Forge Solo running there, cli runs bitcoincashII-cli with its config and passes its exit
// code on. An authorization failure names the service's node too.
func TestCLIRunsAgainstTheRunningNode(t *testing.T) {
	r, _ := cliFixture(t, `echo "$@" > "$(dirname "$0")/args"; exit 0`)
	l, err := lockDataDir(r.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if code, err := r.run(); err != nil || code != 0 {
		t.Fatalf("CLI-RUN: %d %v", code, err)
	}
	b, _ := os.ReadFile(filepath.Join(r.inst, "bin", "args"))
	if want := "-datadir=" + filepath.Join(r.dataDir, "bch2") + " -conf=" + filepath.Join(r.dataDir, "bch2", "bch2.conf") + " getblockcount\n"; string(b) != want {
		t.Errorf("CLI-ARGS: %q, want %q", b, want)
	}

	r2, stderr := cliFixture(t, `echo 'error: Authorization failed: Incorrect rpcuser or rpcpassword' >&2; exit 1`)
	l2, err := lockDataDir(r2.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	if code, err := r2.run(); err != nil || code != 1 {
		t.Errorf("CLI-EXIT: %d %v, want exit 1", code, err)
	}
	if s := stderr.String(); !strings.Contains(s, "Authorization failed") || !strings.Contains(s, "sudo /opt/forge-solo/forge-solo cli getblockcount") {
		t.Errorf("CLI-AUTH-HINT: an authorization failure does not name the service's node:\n%s", s)
	}
	stderr.Reset()
	r2.svcData = r2.dataDir // asking the service's own node: nothing to add
	if _, err := r2.run(); err != nil || strings.Contains(stderr.String(), "sudo") {
		t.Errorf("CLI-AUTH-SERVICE: %v\n%s", err, stderr)
	}
}

// A data directory this user cannot read (the service's, as another user) says to run cli as root.
func TestCLIUnreadableDataDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}
	r, _ := cliFixture(t, "exit 0")
	r.svcData = r.dataDir
	if err := os.Chmod(r.dataDir, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(r.dataDir, 0o700)
	if _, err := r.run(); err == nil || !strings.Contains(err.Error(), "cannot be read") || !strings.Contains(err.Error(), "run cli as root") {
		t.Errorf("CLI-PERMISSION: %v", err)
	}
}

// The service's user has /var/lib/forge-solo as its data directory: cli, and run, as that user
// used to look in /var/lib/forge-solo/.local/share/forge-solo.
func TestServiceUsersDataDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root's is /var/lib/forge-solo anyway")
	}
	old := currentUser
	t.Cleanup(func() { currentUser = old })
	currentUser = func() string { return serviceUser }
	if d := defaultDataDir(); d != serviceData {
		t.Errorf("DATADIR-SERVICE-USER: the service's user gets %s", d)
	}
	currentUser = func() string { return "alice" }
	if d := defaultDataDir(); d == serviceData {
		t.Errorf("DATADIR-OTHER-USER: another user gets %s", d)
	}
}

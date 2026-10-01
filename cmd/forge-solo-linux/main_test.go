package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLockDataDirIsExclusive(t *testing.T) {
	dir := t.TempDir()
	a, err := lockDataDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := lockDataDir(dir); err == nil {
		b.Close()
		t.Fatal("a second lock on the same data directory succeeded")
	} else if !strings.Contains(err.Error(), "already running") {
		t.Fatalf("error %q", err)
	}
	a.Close()
	b, err := lockDataDir(dir)
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	b.Close()
}

func TestCheckRelease(t *testing.T) {
	dir := t.TempDir()
	if err := checkRelease(dir); err == nil || !strings.Contains(err.Error(), "bin/bitcoincashIId") {
		t.Fatalf("empty dir: %v", err)
	}
	for _, f := range []string{"bin/bitcoincashIId", "bin/bitcoincashII-cli", "bin/stratum", "bin/api", "web/solo.html"} {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := checkRelease(dir); err != nil {
		t.Fatal(err)
	}
}

func TestParseRunFlags(t *testing.T) {
	d, w, err := parseRunFlags([]string{"--data-dir", "rel/dir", "--web", "0.0.0.0:3090"})
	if err != nil || !filepath.IsAbs(d) || !strings.HasSuffix(d, "rel/dir") || w != "0.0.0.0:3090" {
		t.Fatalf("%q %q %v", d, w, err)
	}
	if _, w, err := parseRunFlags(nil); err != nil || w != defaultWeb {
		t.Fatalf("defaults: %q %v", w, err)
	}
	for _, bad := range [][]string{{"--web", "3080"}, {"extra"}, {"--nope"}} {
		if _, _, err := parseRunFlags(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// A public port another program holds is named before anything starts.
func TestCheckPublicPortsNamesTheTakenPort(t *testing.T) {
	l, err := net.Listen("tcp", ":8339")
	if err != nil {
		t.Skipf("port 8339 is in use on this machine (%v)", err)
	}
	defer l.Close()
	err = checkPublicPorts()
	if err == nil {
		// 3333 or 3335 may be the one taken on a dev machine; only a clean pass is wrong here.
		t.Fatal("no error with port 8339 taken")
	}
	if !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("error %q", err)
	}
}

func TestRestrictDatabase(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"forgesolo.db", "forgesolo.db-wal"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	restrictDatabase(dir) // forgesolo.db-shm is absent: no error, nothing created
	for _, f := range []string{"forgesolo.db", "forgesolo.db-wal"} {
		if st, err := os.Stat(filepath.Join(dir, f)); err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v, want 0600", f, st.Mode().Perm(), err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "forgesolo.db-shm")); err == nil {
		t.Fatal("created a file that was not there")
	}
}

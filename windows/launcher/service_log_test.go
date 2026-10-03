package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A service's log is moved to <name>.1 once it passes its limit, replacing the one before, and
// carries on in a fresh file.
func TestServiceLogRotates(t *testing.T) {
	dir := t.TempDir()
	l := &cappedLog{path: filepath.Join(dir, "stratum.log"), limit: 100}
	line := []byte(strings.Repeat("x", 39) + "\n") // 40 bytes
	for i := 0; i < 2; i++ {
		_, _ = l.Write(line)
	}
	if err := os.WriteFile(l.path+".1", []byte("older"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _ = l.Write(line) // 120 bytes would pass the limit: the 80 go to .1
	cur, _ := os.ReadFile(l.path)
	old, _ := os.ReadFile(l.path + ".1")
	if len(cur) != 40 || len(old) != 80 {
		t.Fatalf("LOG-ROTATE: %d bytes in the log, %d in .1; want 40 and the 80 before them", len(cur), len(old))
	}
}

// A write never fails, even when the log cannot be written: an error would stop the copying from
// the service's output, and the service with it once that pipe fills.
func TestServiceLogNeverFails(t *testing.T) {
	l := &cappedLog{path: filepath.Join(t.TempDir(), "no-such-dir", "api.log"), limit: 100}
	if n, err := l.Write([]byte("hello\n")); n != 6 || err != nil {
		t.Fatalf("LOG-NEVER-FAILS: Write = %d, %v; want 6, nil", n, err)
	}
}

// One log per service: a restarted service goes on in the same file.
func TestServiceLogIsShared(t *testing.T) {
	saved := dataDir
	dataDir = t.TempDir()
	t.Cleanup(func() { dataDir = saved })
	if serviceLog("stratum") != serviceLog("stratum") || serviceLog("stratum") == serviceLog("api") {
		t.Fatal("LOG-PER-SERVICE: serviceLog does not keep one log per service")
	}
}

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The database log is moved aside at start once it is past the limit; a small one is left alone.
func TestDatabaseLogIsRotatedAtStart(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "pglog.txt")
	if err := os.WriteFile(log, make([]byte, 100), 0o600); err != nil {
		t.Fatal(err)
	}
	rotateLog(log, 1000)
	if _, err := os.Stat(log); err != nil {
		t.Fatal("a log under the limit was moved")
	}
	if err := os.WriteFile(log, make([]byte, 2000), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(log+".1", []byte("older"), 0o600); err != nil {
		t.Fatal(err)
	}
	rotateLog(log, 1000)
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Error("a log past the limit was not moved aside")
	}
	if st, err := os.Stat(log + ".1"); err != nil || st.Size() != 2000 {
		t.Errorf("pglog.txt.1 should now be the moved log: %v", err)
	}
}

// launcher.log is moved aside once past its limit while Forge Solo runs, not only at start: a
// program that keeps failing to start is logged each time, for weeks if no one restarts the PC.
func TestLauncherLogIsRotatedWhileRunning(t *testing.T) {
	saved := dataDir
	dataDir = t.TempDir()
	t.Cleanup(func() { dataDir = saved })
	if err := os.WriteFile(dpath("launcher.log"), make([]byte, launcherLogLimit+1), 0o600); err != nil {
		t.Fatal(err)
	}
	logf("after the limit")
	cur, _ := os.ReadFile(dpath("launcher.log"))
	old, err := os.Stat(dpath("launcher.log.1"))
	if err != nil || old.Size() != launcherLogLimit+1 || len(cur) > 100 {
		t.Fatalf("LOG-LAUNCHER-ROTATE: launcher.log has %d bytes and launcher.log.1 %v; want the full log moved aside and a fresh one", len(cur), err)
	}
	logf("under the limit")
	if cur, _ := os.ReadFile(dpath("launcher.log")); !strings.Contains(string(cur), "after the limit") {
		t.Fatalf("LOG-LAUNCHER-KEEP: a log under its limit was moved aside:\n%s", cur)
	}
}

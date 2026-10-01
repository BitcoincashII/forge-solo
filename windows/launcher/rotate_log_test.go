package main

import (
	"os"
	"path/filepath"
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

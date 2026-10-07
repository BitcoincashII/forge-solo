//go:build !windows

package stats

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// forgesolo.db holds the payout address and the settings. A fresh one, and the -wal and -shm files
// SQLite makes beside it, are readable by the program's user alone whatever the umask: the api and
// the stratum made all three 0644 (in the Umbrel app's containers, whose umask is 0), and they
// stayed so until the next start's migrate made them 0600.
func TestAFreshDatabaseIsPrivate(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	path := filepath.Join(t.TempDir(), "forgesolo.db")
	if err := InitDB(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(CloseDB)
	for _, f := range []struct{ code, path string }{
		{"DB-MODE-MAIN", path}, {"DB-MODE-WAL", path + "-wal"}, {"DB-MODE-SHM", path + "-shm"},
	} {
		st, err := os.Stat(f.path)
		if err != nil {
			t.Errorf("%s: %v", f.code, err)
			continue
		}
		if m := st.Mode().Perm(); m != 0o600 {
			t.Errorf("%s: a fresh InitDB made %s %04o, not 0600", f.code, filepath.Base(f.path), m)
		}
	}
}

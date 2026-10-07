//go:build sqlite

package stats

import (
	"path/filepath"
	"testing"
)

// The database is the file DB_PATH names, which every platform sets.
func TestGetDBPathIsDBPath(t *testing.T) {
	want := filepath.Join(t.TempDir(), "forgesolo.db")
	t.Setenv("DB_PATH", want)
	if got := GetDBPath(); got != want {
		t.Fatalf("DB-PATH: GetDBPath() = %q, want DB_PATH %q", got, want)
	}
}

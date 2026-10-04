//go:build sqlite

package stats

import (
	"path/filepath"
	"testing"
)

func TestA1175BlockIsPutBack(t *testing.T) {
	if err := InitDB(filepath.Join(t.TempDir(), "restore.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(CloseDB)
	checkRestore1175(t, "bitcoincashii:qqrestore1175000000000000000000000000000", 700_000)
}

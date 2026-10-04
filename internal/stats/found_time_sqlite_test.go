//go:build sqlite

package stats

import (
	"path/filepath"
	"testing"
)

func TestFoundTimesAreKept(t *testing.T) {
	if err := InitDB(filepath.Join(t.TempDir(), "found.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(CloseDB)
	checkFoundTimes(t, "bitcoincashii:qqfoundtimes00000000000000000000000000000", 600_000)
}

//go:build sqlite

package stats

import (
	"path/filepath"
	"testing"
)

func TestMinerPayoutsList(t *testing.T) {
	if err := InitDB(filepath.Join(t.TempDir(), "payouts.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(CloseDB)
	checkMinerPayouts(t, "bitcoincashii:qqminerpayoutsa00000000000000000000000000",
		"bitcoincashii:qqminerpayoutsb00000000000000000000000000",
		"bitcoincashii:qqminerpayoutsc00000000000000000000000000", 600_000)
}

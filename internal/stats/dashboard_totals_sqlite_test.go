//go:build sqlite

package stats

import (
	"path/filepath"
	"testing"
)

func TestDashboardTotalsCoverEveryBlock(t *testing.T) {
	if err := InitDB(filepath.Join(t.TempDir(), "totals.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(CloseDB)
	checkDashboardTotals(t, "bitcoincashii:qqdashboardtotals0000000000000000000000000", 500_000)
}

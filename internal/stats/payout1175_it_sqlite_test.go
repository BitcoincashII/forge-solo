package stats

import (
	"path/filepath"
	"testing"
)

// payout1175TestDB is the database TestPayout1175Accounting runs on: a new file in every run.
func payout1175TestDB(t *testing.T) string {
	return filepath.Join(t.TempDir(), "payout1175.db")
}

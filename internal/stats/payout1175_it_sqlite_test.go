//go:build sqlite

package stats

import (
	"os"
	"path/filepath"
	"testing"
)

// payout1175TestDB is the database TestPayout1175Accounting runs on: MMTEST_DB when set, or else a
// new file. Umbrel and Windows keep the 1175 ledger in SQLite, so its accounting is checked there
// in every run, not only against Postgres.
func payout1175TestDB(t *testing.T) string {
	if path := os.Getenv("MMTEST_DB"); path != "" {
		return path
	}
	return filepath.Join(t.TempDir(), "payout1175.db")
}

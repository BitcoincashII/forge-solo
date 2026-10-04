//go:build sqlite

package pgmigrate

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/stats"
)

// The tests run five hours behind UTC, as a PC in the Americas does, so a time converted in the
// local zone instead of UTC reads five hours off.
func TestMain(m *testing.M) {
	time.Local = time.FixedZone("UTC-5", -5*60*60)
	os.Exit(m.Run())
}

// freshDB is a database this build's stats.InitDB has just made, as a new install has it.
func freshDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "forgesolo.db")
	if err := stats.InitDB(path); err != nil {
		t.Fatal(err)
	}
	stats.CloseDB()
	db, err := sql.Open("sqlite", stats.SQLiteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, path
}

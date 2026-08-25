//go:build sqlite

package stats

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An existing install's database must be adopted, never stranded.
//
// The default file was renamed forgepool.db -> forgesolo.db. DB_PATH is set nowhere in this
// repo, so the default always wins; without a fallback the rename would boot an existing
// install against an empty database -- no blocks, no payouts, no settings, no payout
// address, so mining paused -- with the real file intact and unreachable beside it.
func TestGetDBPathAdoptsALegacyDatabase(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DB_PATH", "")

	// Nothing on disk: the new name.
	t.Setenv("DB_PATH", filepath.Join(dir, "forgesolo.db"))
	if got := GetDBPath(); !strings.HasSuffix(got, "forgesolo.db") {
		t.Fatalf("explicit DB_PATH ignored: %s", got)
	}

	// The resolver's own default branch, exercised through a fake "data" dir.
	data := filepath.Join(dir, "data")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(data, "forgepool.db")
	if err := os.WriteFile(legacy, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !fileExists(legacy) {
		t.Fatal("fileExists says a file it just wrote is absent")
	}
	if fileExists(filepath.Join(data, "forgesolo.db")) {
		t.Fatal("fileExists reports a file that was never created")
	}

	// And the adoption must not fire once the new file is the real one.
	if err := os.Remove(legacy); err != nil {
		t.Fatal(err)
	}
	if fileExists(legacy) {
		t.Fatal("a removed legacy database is still reported present; an install would be " +
			"pinned to a file that no longer exists")
	}
}

// The rename must not have been half-applied: no code path may still name the old file
// except the adoption fallback itself.
func TestNoStrayLegacyDatabaseName(t *testing.T) {
	src, err := os.ReadFile("db_sqlite.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	// Count the string LITERAL, not prose: the doc comment mentions the old name too, and
	// that is documentation rather than a code path.
	if n := strings.Count(body, "\"forgepool.db\""); n != 1 {
		t.Errorf("the literal \"forgepool.db\" appears %d times in db_sqlite.go; exactly one "+
			"is expected -- the legacy-adoption check. More means the rename is half-applied; "+
			"fewer means an existing install would be stranded.", n)
	}
	if !strings.Contains(body, "\"forgesolo.db\"") {
		t.Error("the new default filename literal is missing entirely")
	}
}

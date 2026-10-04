//go:build sqlite

package forgesolo

import (
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/pgmigrate"
)

// A first start after a hard stop, on a slow or busy board, can spend minutes in PostgreSQL's
// crash recovery before the move's copy even begins. The api and the stratum wait for the move to
// exit, which compose does with no time limit of its own, and the move waits for its server at
// most pgmigrate.ReadyWait: long enough for that recovery, and bounded, so a server that never
// gets ready ends as a failure the dashboard shows instead of a start that never ends.
func TestTheMoveHasTimeToStartTheOldDatabase(t *testing.T) {
	if pgmigrate.ReadyWait < 5*time.Minute || pgmigrate.ReadyWait > 30*time.Minute {
		t.Errorf("PKG-DB-GRACE: the move waits %v for the old data's server, want between 5 and 30 minutes", pgmigrate.ReadyWait)
	}
	svcs := sqliteCompose(t)
	if svcs["migrate"].Healthcheck.Kind != 0 {
		t.Error("PKG-DB-GRACE-HEALTH: migrate has a healthcheck, whose deadline could cut a long move short")
	}
	for _, name := range []string{"api", "stratum"} {
		if c := svcs[name].dependsOn(t)["migrate"]; c != "service_completed_successfully" {
			t.Errorf("PKG-DB-GRACE-WAIT: %s waits for migrate with %q, not until it has finished", name, c)
		}
	}
}

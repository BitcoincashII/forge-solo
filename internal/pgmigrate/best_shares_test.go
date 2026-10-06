//go:build sqlite

package pgmigrate

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/stats"
)

// The workers' all-time best shares are 1.0.13's alone: after going back to 1.0.12 and forward
// again, the merge keeps forgesolo.db's, as they are.
func TestMergeCarriesBestShares(t *testing.T) {
	db := moved(t, t.TempDir(), source1012())
	seen := time.Date(2026, 10, 6, 12, 30, 15, 0, time.UTC)
	in(t, db, func() {
		must(t, stats.SaveBestSharesDB([]stats.BestShare{
			{Miner: addrA, Worker: "s19", Difficulty: 5.5e9, Seen: seen},
			{Miner: addrA, Worker: "bitaxe", Difficulty: 2048.25, Seen: seen.Add(time.Hour)},
		}, nil))
	})
	const rows = `SELECT miner_address || '|' || worker_name || '|' || difficulty FROM best_shares ORDER BY miner_address, worker_name`
	const times = `SELECT worker_name || '|' || typeof(seen_at) || '|' || seen_at FROM best_shares ORDER BY miner_address, worker_name`
	want, wantTimes := query(t, db, rows), query(t, db, times)
	p, err := mergeInto(t, source1012(), db)
	if err != nil {
		t.Fatal(err)
	}
	if got := query(t, MigratingPath(db), rows); len(want) != 2 || strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("MIG-MERGE-BEST-SHARES: the best shares after the merge are %v, want forgesolo.db's %v", got, want)
	}
	if got := query(t, MigratingPath(db), times); len(wantTimes) != 2 || strings.Join(got, "\n") != strings.Join(wantTimes, "\n") {
		t.Errorf("MIG-MERGE-BEST-TIME: the times last seen after the merge are %v, want forgesolo.db's %v, as stored", got, wantTimes)
	}
	if p.Merge.BestShares != 2 {
		t.Errorf("MIG-MERGE-BEST-REPORT: the merge reports %d best shares carried, want 2", p.Merge.BestShares)
	}
}

// A forgesolo.db an earlier 1.0.13 made has no best_shares table: the merge carries none and goes
// on, and the new database has the table, empty.
func TestMergeWithoutBestSharesTable(t *testing.T) {
	db := moved(t, t.TempDir(), source1012())
	raw, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`DROP TABLE best_shares`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	p, err := mergeInto(t, source1012(), db)
	if err != nil {
		t.Fatalf("MIG-MERGE-BEST-NO-TABLE: the merge of a forgesolo.db without best_shares failed: %v", err)
	}
	if got := query(t, MigratingPath(db), `SELECT COUNT(*) FROM best_shares`); p.Merge.BestShares != 0 || len(got) != 1 || got[0] != "0" {
		t.Errorf("MIG-MERGE-BEST-NO-TABLE: %d carried, the new database holds %v", p.Merge.BestShares, got)
	}
}

// A move from 1.0.12 makes up no best share: the shares it stored hold only the difficulty each
// was asked for, not the one it reached.
func TestMoveMakesUpNoBestShare(t *testing.T) {
	db := moved(t, t.TempDir(), source1012())
	if got := query(t, db, `SELECT COUNT(*) FROM best_shares`); len(got) != 1 || got[0] != "0" {
		t.Fatalf("MIG-MOVE-NO-BEST: the moved database holds %v best shares, want none", got)
	}
}

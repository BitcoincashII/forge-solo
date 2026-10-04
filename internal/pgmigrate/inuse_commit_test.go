//go:build sqlite

package pgmigrate

import (
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/stats"
)

// The commit never swaps the database under a program that has it open (the experiment the design
// rests on, as tests): the program's later writes would go to a file no longer in place, and be lost.

func init() {
	childParts["start-read-write"] = startReadWriteChild
}

const lateAddr = "bitcoincashii:qlatewrite00000000000000000000000000000000"

// startReadWriteChild plays the api starting: it opens the database (InitDB, which waits while a
// move holds it), says "read <payout address>", saves a miner's settings, says "wrote" and ends.
func startReadWriteChild(path string) int {
	log.SetOutput(io.Discard)
	if err := stats.InitDB(path); err != nil {
		fmt.Println("error:", err)
		return 1
	}
	defer stats.CloseDB()
	addr, _, _, err := stats.GetPoolConfig()
	if err != nil {
		fmt.Println("error:", err)
		return 1
	}
	fmt.Println("read " + addr)
	if err := stats.SaveMinerSettings(&stats.MinerSettings{Address: lateAddr, SoloMining: true}); err != nil {
		fmt.Println("error:", err)
		return 1
	}
	fmt.Println("wrote")
	return 0
}

// While the api has the database open, the commit is deferred and changes nothing, and what the
// api writes afterwards stays. A program with the database open but without the in-use lock is
// caught by SQLite's own lock.
func TestCommitDeferredWhileInUse(t *testing.T) {
	for _, c := range []struct{ part, code string }{
		{"hold-db", "MIG-INUSE-COMMIT"},
		{"raw-hold", "MIG-INUSE-COMMIT-RAW"},
		{"lock-only", "MIG-INUSE-COMMIT-LOCK"},
	} {
		t.Run(c.part, func(t *testing.T) {
			dir := t.TempDir()
			db, pgdata := filepath.Join(dir, "forgesolo.db"), oldData(t)
			src := mergeSetup(t, db, pgdata)
			prepareMerge(t, src, db)
			holder := startChild(t, c.part, db)
			holder.expect(t, "open", 30*time.Second, c.code+"-SETUP")
			before, _ := fileIdentity(db)
			_, err := commit(t, db, pgdata)
			if CodeOf(err) != CodeDeferred {
				t.Fatalf("%s: a commit while %s had the database gave %v, want deferred (31)", c.code, c.part, err)
			}
			if after, _ := fileIdentity(db); after.SHA256 != before.SHA256 {
				t.Fatalf("%s: the deferred commit changed forgesolo.db", c.code)
			}
			if c.part != "hold-db" {
				return
			}
			holder.say(t, "write "+lateAddr)
			holder.expect(t, "wrote", 30*time.Second, c.code)
			holder.release()
			holder.expect(t, "closed", 30*time.Second, c.code)
			if got := query(t, db, `SELECT CAST(COUNT(*) AS TEXT) FROM miners WHERE address = ?`, lateAddr); got[0] != "1" {
				t.Fatalf("%s: what the api wrote after the deferred commit is lost", c.code)
			}
		})
	}
}

// A program that starts while the commit is under way waits for it, then opens the new database:
// it reads the moved data, and what it writes stays.
func TestProgramStartingDuringCommit(t *testing.T) {
	dir := t.TempDir()
	db, pgdata := filepath.Join(dir, "forgesolo.db"), oldData(t)
	if _, err := prepare(t, source1012(), db); err != nil {
		t.Fatal(err)
	}
	locked := make(chan struct{})
	testStage = func(name string) error {
		if name == "renaming" {
			close(locked)
			time.Sleep(2 * time.Second)
		}
		return nil
	}
	defer func() { testStage = nil }()
	done := make(chan error, 1)
	go func() {
		_, err := commit(t, db, pgdata)
		done <- err
	}()
	<-locked
	api := startChild(t, "start-read-write", db)
	if err := <-done; err != nil {
		t.Fatalf("MIG-INUSE-START: the commit failed: %v", err)
	}
	line := api.expect(t, "read", 30*time.Second, "MIG-INUSE-START")
	if got := strings.TrimPrefix(line, "read "); got != addrA {
		t.Fatalf("MIG-INUSE-START: a program started during the commit read the payout address %q, want the moved %q", got, addrA)
	}
	api.expect(t, "wrote", 30*time.Second, "MIG-INUSE-START")
	if got := query(t, db, `SELECT CAST(COUNT(*) AS TEXT) FROM miners WHERE address = ?`, lateAddr); got[0] != "1" {
		t.Fatalf("MIG-INUSE-START: what a program started during the commit wrote is lost")
	}
}

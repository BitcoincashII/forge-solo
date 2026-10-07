package stats

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func init() {
	childParts["writer"] = writerChild
	childParts["initdb"] = initDBChild
}

// writerChild plays the api and the stratum writing all the time: it saves the settings and
// records a block, a millisecond apart, until the test closes its stdin. It then says
// "done <writes> <errors> <first error>".
func writerChild(path string) int {
	log.SetOutput(io.Discard)
	if err := InitDB(path); err != nil {
		fmt.Println("error:", err)
		return 1
	}
	defer CloseDB()
	stop := make(chan struct{})
	go func() {
		waitForStdin()
		close(stop)
	}()
	fmt.Println("ready")
	const miner = "bitcoincashii:qotherprogram00000000000000000000000000000"
	var writes, errs int
	var first error
	deadline := time.Now().Add(2 * time.Minute)
	for h := int64(900_000); time.Now().Before(deadline); h++ {
		select {
		case <-stop:
			fmt.Printf("done %d %d %v\n", writes, errs, first)
			return 0
		default:
		}
		for _, write := range []func() error{
			func() error { return SavePoolSettings(miner, "", "", PayoutModeSolo) },
			func() error { return SaveSoloBlockCoinbaseDirect(miner, h, 3.125, fmt.Sprintf("%064x", h)) },
		} {
			writes++
			if err := write(); err != nil {
				errs++
				if first == nil {
					first = err
				}
			}
			time.Sleep(time.Millisecond)
		}
	}
	fmt.Println("error: the test never said stop")
	return 1
}

// initDBChild opens the database once the test closes its stdin, and says "ok" or the error.
func initDBChild(path string) int {
	log.SetOutput(io.Discard)
	fmt.Println("ready")
	waitForStdin()
	if err := InitDB(path); err != nil {
		fmt.Println("error:", err)
		return 1
	}
	CloseDB()
	fmt.Println("ok")
	return 0
}

// The 1175 ledger is written while the other program writes all the time. Distribute1175Block
// read and then wrote inside one transaction, and SQLite failed the write at once with
// "database is locked" whenever the other program had written in between.
func TestDistribute1175UnderAnotherProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "twoproc.db")
	if err := InitDB(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(CloseDB)
	w := startChild(t, "writer", path)
	w.expect(t, "ready", 30*time.Second, "SQL-TWOPROC-SETUP")

	const finder = "bitcoincashii:qtwoprocfinder000000000000000000000000000"
	var fails int
	var first string
	for i := 0; i < 300; i++ {
		h := int64(800_000 + i)
		err := Record1175Block(h, fmt.Sprintf("%064x", h), 25, finder, true)
		if err == nil {
			err = Distribute1175Block(h, 0)
		}
		if err != nil {
			if fails++; first == "" {
				first = fmt.Sprintf("height %d: %v", h, err)
			}
		}
		// Both programs leave the lock free between writes, as the api and the stratum do: a
		// program that writes back to back keeps the other waiting, since SQLite does not queue
		// its waiters.
		time.Sleep(2 * time.Millisecond)
	}
	w.release()
	done := w.expect(t, "done", 30*time.Second, "SQL-TWOPROC-SETUP")
	var writes, errs int
	fmt.Sscanf(done, "done %d %d", &writes, &errs)
	t.Logf("the other program made %d writes meanwhile", writes)
	if fails > 0 {
		t.Fatalf("SQL-TWOPROC: %d of 300 1175 blocks failed to record or distribute while the other program wrote; first %s", fails, first)
	}
	if errs > 0 {
		t.Fatalf("SQL-TWOPROC-OTHER: %d of the other program's %d writes failed: %s", errs, writes, done)
	}
	if writes < 100 {
		t.Fatalf("SQL-TWOPROC-SETUP: the other program made only %d writes meanwhile", writes)
	}
	if n, paid, err := Miner1175Totals(finder, true); err != nil || n != 300 || paid != 300*25 {
		t.Fatalf("SQL-TWOPROC: the ledger holds %d blocks paying %v (%v), want 300 paying 7500", n, paid, err)
	}
}

// Six programs open a database file that does not exist yet, at once. The first switches it to
// WAL; SQLite answered the others "database is locked" at once, without waiting, and on a fresh
// install the api or the stratum failed its first start that way.
func TestFreshFileInitDBRace(t *testing.T) {
	dir := t.TempDir()
	var fails int
	var first string
	for round := 0; round < 20; round++ {
		path := filepath.Join(dir, fmt.Sprintf("fresh-%d.db", round))
		kids := make([]*child, 6)
		for i := range kids {
			kids[i] = startChild(t, "initdb", path)
		}
		for _, k := range kids {
			k.expect(t, "ready", 30*time.Second, "SQL-FRESH-SETUP")
		}
		for _, k := range kids {
			k.release()
		}
		for _, k := range kids {
			if line := k.next(t, 30*time.Second, "SQL-FRESH-SETUP"); line != "ok" {
				if fails++; first == "" {
					first = strings.TrimSpace(line)
				}
			}
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("SQL-FRESH-SETUP: round %d left no database: %v", round, err)
		}
	}
	if fails > 0 {
		t.Fatalf("SQL-FRESH-RACE: %d of 120 programs could not open a new database file another opened at the same moment; first: %s", fails, first)
	}
}

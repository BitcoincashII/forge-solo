//go:build sqlite

package stats

import (
	"bufio"
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/dblock"
)

func init() {
	childParts["tryexclusive"] = tryExclusiveChild
	childParts["move"] = moveChild
	childParts["share"] = shareChild
	childParts["hold"] = holdChild
}

// waitingLine is what a program logs while it waits for a move.
const waitingLine = "waiting: Forge Solo is moving its database"

// captureLog sends this program's log to a buffer until the test ends.
func captureLog(t *testing.T) *lockedBuffer {
	t.Helper()
	b := &lockedBuffer{}
	prev := log.Writer()
	log.SetOutput(b)
	t.Cleanup(func() { log.SetOutput(prev) })
	return b
}

// logEnd is the last lines of a captured log, for a failure message.
func logEnd(b *lockedBuffer) string {
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	return strings.Join(lines[max(0, len(lines)-10):], "\n")
}

// tryExclusiveChild tries to take the in-use lock as forge-solo-migrate does before it replaces
// the database, and says "taken", "busy" or the error.
func tryExclusiveChild(path string) int {
	l, err := dblock.TryExclusive(dblock.Path(path))
	switch {
	case errors.Is(err, dblock.ErrBusy):
		fmt.Println("busy")
	case err != nil:
		fmt.Println("error:", err)
		return 1
	default:
		l.Release()
		fmt.Println("taken")
	}
	return 0
}

// moveChild plays forge-solo-migrate's commit: it takes the in-use lock exclusively, says
// "locked", and 2 s later puts <db>.migrating in the database's place, removes the old file's WAL
// and lets the lock go. It then says "moved".
func moveChild(path string) int {
	l, err := dblock.TryExclusive(dblock.Path(path))
	if err != nil {
		fmt.Println("error:", err)
		return 1
	}
	fmt.Println("locked")
	time.Sleep(2 * time.Second)
	for _, f := range []string{path + "-wal", path + "-shm"} {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			fmt.Println("error:", err)
			return 1
		}
	}
	if err := os.Rename(path+".migrating", path); err != nil {
		fmt.Println("error:", err)
		return 1
	}
	if err := l.Release(); err != nil {
		fmt.Println("error:", err)
		return 1
	}
	fmt.Println("moved")
	return 0
}

// holdChild plays a move that does not end: it takes the in-use lock exclusively and says
// "locked"; when its stdin closes it lets the lock go and says "released".
func holdChild(path string) int {
	l, err := dblock.TryExclusive(dblock.Path(path))
	if err != nil {
		fmt.Println("error:", err)
		return 1
	}
	fmt.Println("locked")
	waitForStdin()
	if err := l.Release(); err != nil {
		fmt.Println("error:", err)
		return 1
	}
	fmt.Println("released")
	return 0
}

// shareChild plays the api or the stratum: it opens the database and says "open"; for each line
// "write <address>" it saves that miner's settings and says "wrote"; when its stdin closes it
// closes the database and says "closed".
func shareChild(path string) int {
	log.SetOutput(io.Discard)
	if err := InitDB(path); err != nil {
		fmt.Println("error:", err)
		return 1
	}
	fmt.Println("open")
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		addr, ok := strings.CutPrefix(sc.Text(), "write ")
		if !ok {
			continue
		}
		if err := SaveMinerSettings(&MinerSettings{Address: addr, SoloMining: true}); err != nil {
			fmt.Println("error:", err)
			return 1
		}
		fmt.Println("wrote")
	}
	CloseDB()
	fmt.Println("closed")
	return 0
}

// The in-use lock is held from InitDB to CloseDB, so forge-solo-migrate never replaces the
// database under a program that has it open.
func TestInUseHeldForLifetime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forgesolo.db")
	if err := InitDB(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(CloseDB)
	if got := startChild(t, "tryexclusive", path).next(t, 30*time.Second, "INUSE-HELD"); got != "busy" {
		t.Fatalf("INUSE-HELD: while this program had the database open, the move took its lock: %q", got)
	}
	if err := SavePoolConfig("bitcoincashii:qinuseheld000000000000000000000000000000", "", ""); err != nil {
		t.Fatal(err)
	}
	if got := startChild(t, "tryexclusive", path).next(t, 30*time.Second, "INUSE-HELD"); got != "busy" {
		t.Fatalf("INUSE-HELD-LATER: after a write, the move took the lock of a database still open: %q", got)
	}
	CloseDB()
	if got := startChild(t, "tryexclusive", path).next(t, 30*time.Second, "INUSE-RELEASED"); got != "taken" {
		t.Fatalf("INUSE-RELEASED: after CloseDB the move still could not take the lock: %q", got)
	}
}

// writeMarker makes a database at path holding one marker row.
func writeMarker(t *testing.T, path, v string) {
	t.Helper()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`CREATE TABLE move_marker (v TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO move_marker (v) VALUES (?)`, v); err != nil {
		t.Fatal(err)
	}
}

func markers(t *testing.T) []string {
	t.Helper()
	rows, err := db.Query(`SELECT v FROM move_marker ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

// A program that starts while the database is being moved waits, then opens the new database,
// and what it writes stays. Opened at once, it read the old file and wrote into one that was
// about to be replaced: the write was lost.
func TestInitDBWaitsForMove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forgesolo.db")
	writeMarker(t, path, "OLD")
	writeMarker(t, path+".migrating", "NEW")

	mover := startChild(t, "move", path)
	mover.expect(t, "locked", 30*time.Second, "INUSE-WAIT-SETUP")
	locked := time.Now()
	time.Sleep(500 * time.Millisecond)
	logged := captureLog(t)
	if err := InitDB(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(CloseDB)
	waited := time.Since(locked)
	mover.expect(t, "moved", 30*time.Second, "INUSE-WAIT-SETUP")
	if waited < 1500*time.Millisecond {
		t.Errorf("INUSE-WAIT-EARLY: the database was opened %v into a 2 s move", waited.Round(time.Millisecond))
	}
	if n := strings.Count(logged.String(), waitingLine); n != 1 {
		t.Errorf("INUSE-WAIT-LOG: while it waited for a 2 s move the program logged %q %d times, want once; its log ends:\n%s",
			waitingLine, n, logEnd(logged))
	}
	if got := markers(t); len(got) != 1 || got[0] != "NEW" {
		t.Fatalf("INUSE-WAIT-OLD: the program opened the database the move replaced: it reads %q, want [NEW]", got)
	}
	if _, err := db.Exec(`INSERT INTO move_marker (v) VALUES ('WRITTEN')`); err != nil {
		t.Fatal(err)
	}
	CloseDB()
	if err := InitDB(path); err != nil {
		t.Fatal(err)
	}
	if got := markers(t); len(got) != 2 || got[1] != "WRITTEN" {
		t.Fatalf("INUSE-WAIT-LOST: a write made after the move is not in the database: %q", got)
	}
}

// A move that does not end holds a program up for inUseWait, no longer: it says it is waiting
// every inUseLogEvery, then fails like any failed start, and once the move is over its next try
// opens the database.
func TestInitDBGivesUpOnALongMove(t *testing.T) {
	if inUseWait != 15*time.Minute || inUseLogEvery != 30*time.Second {
		t.Fatalf("INUSE-WAIT-PIN: a program waits %v for a move and says so every %v, want 15m0s and 30s",
			inUseWait, inUseLogEvery)
	}
	inUseWait, inUseLogEvery = 2*time.Second, 500*time.Millisecond
	t.Cleanup(func() { inUseWait, inUseLogEvery = 15*time.Minute, 30*time.Second })

	path := filepath.Join(t.TempDir(), "forgesolo.db")
	mover := startChild(t, "hold", path)
	mover.expect(t, "locked", 30*time.Second, "INUSE-GIVEUP-SETUP")
	logged := captureLog(t)
	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- InitDB(path) }()
	var err error
	select {
	case err = <-done:
	case <-time.After(15 * time.Second):
		t.Fatalf("INUSE-GIVEUP: InitDB still waits for the move after 15 s, want it to give up after %v", inUseWait)
	}
	took := time.Since(start)
	if err == nil {
		t.Fatal("INUSE-GIVEUP: InitDB opened the database while the move held it")
	}
	if took < inUseWait-100*time.Millisecond {
		t.Errorf("INUSE-GIVEUP-EARLY: InitDB gave up after %v, want %v", took.Round(time.Millisecond), inUseWait)
	}
	if n := strings.Count(logged.String(), waitingLine); n < 3 || n > 5 {
		t.Errorf("INUSE-WAIT-REPEAT: in a %v wait the program logged %q %d times, want once every %v; its log ends:\n%s",
			inUseWait, waitingLine, n, inUseLogEvery, logEnd(logged))
	}
	mover.release()
	mover.expect(t, "released", 30*time.Second, "INUSE-GIVEUP-SETUP")
	if err := InitDB(path); err != nil {
		t.Fatalf("INUSE-GIVEUP-RETRY: once the move was over, the next start failed: %v", err)
	}
	t.Cleanup(CloseDB)
}

// A database that will not open fails InitDB, and the api and the stratum try again every few
// seconds for as long as it does. A failed try keeps no in-use lock: otherwise the move would
// find the database in use until the program ended, and each try would keep a file open.
func TestFailedInitDBKeepsNoLock(t *testing.T) {
	for _, c := range []struct {
		name, code string
		tables     bool // InitDB fails making the tables, not opening the file
		prepare    func(t *testing.T, path string)
	}{
		{"not a database", "INUSE-FAIL-FREED", false, func(t *testing.T, path string) {
			if err := os.WriteFile(path, bytes.Repeat([]byte("not a database\n"), 512), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"tables cannot be made", "INUSE-FAIL-FREED-TABLES", true, func(t *testing.T, path string) {
			// The index on shares(miner_address) cannot be made on this shares table.
			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			if _, err := raw.Exec(`CREATE TABLE shares (x INTEGER)`); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "forgesolo.db")
			c.prepare(t, path)
			for i := 0; i < 5; i++ {
				err := InitDB(path)
				if err == nil {
					CloseDB()
					t.Fatal("INUSE-FAIL-SETUP: InitDB opened a database that cannot open")
				}
				if strings.Contains(err.Error(), "failed to create tables") != c.tables {
					t.Fatalf("INUSE-FAIL-SETUP: InitDB failed at another step than this case tests: %v", err)
				}
			}
			if got := startChild(t, "tryexclusive", path).next(t, 30*time.Second, c.code); got != "taken" {
				t.Fatalf("%s: after 5 failed starts the move found the database in use: %q", c.code, got)
			}
		})
	}
}

// The api and the stratum hold the lock together and both write; while either does, the move
// cannot take it.
func TestApiAndStratumShare(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forgesolo.db")
	api := startChild(t, "share", path)
	api.expect(t, "open", 30*time.Second, "INUSE-SHARE-SETUP")
	stratum := startChild(t, "share", path)
	stratum.expect(t, "open", 10*time.Second, "INUSE-SHARE")
	if l, err := dblock.TryExclusive(dblock.Path(path)); !errors.Is(err, dblock.ErrBusy) {
		l.Release()
		t.Fatalf("INUSE-SHARE-HELD: the move took the lock while the api and the stratum had the database open (%v)", err)
	}
	const apiAddr, stratumAddr = "bitcoincashii:qinuseapi0000000000000000000000000000000", "bitcoincashii:qinusestratum000000000000000000000000000"
	api.say(t, "write "+apiAddr)
	api.expect(t, "wrote", 30*time.Second, "INUSE-SHARE-WRITE")
	stratum.say(t, "write "+stratumAddr)
	stratum.expect(t, "wrote", 30*time.Second, "INUSE-SHARE-WRITE")
	api.release()
	stratum.release()
	api.expect(t, "closed", 30*time.Second, "INUSE-SHARE-SETUP")
	stratum.expect(t, "closed", 30*time.Second, "INUSE-SHARE-SETUP")

	if err := InitDB(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(CloseDB)
	got := LoadAllMinerSettings()
	if got[apiAddr] == nil || got[stratumAddr] == nil {
		t.Fatalf("INUSE-SHARE-WRITE: the database holds %d miners, want the api's and the stratum's", len(got))
	}
}

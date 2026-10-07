package stats

import (
	"context"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func init() {
	childParts["distribute-close"] = distributeWhileClosing
}

// distributeWhileClosing distributes a 1175 block recorded before blocks were marked solo, which
// takes the PPLNS path, while CloseDB asks for the database's lock. The distribution is held after
// it has taken its read lock: the program's one connection is in use. Once CloseDB waits for the
// lock, the connection is let go. It says "distributed <error>" if the distribution ends within
// 2 s, or "stuck", and then "closed" once CloseDB has. It runs in a process of its own because a
// distribution that is stuck holds the lock for good.
func distributeWhileClosing(path string) int {
	log.SetOutput(io.Discard)
	if err := InitDB(path); err != nil {
		fmt.Println("error:", err)
		return 1
	}
	const height = 5_000
	if err := Record1175Block(height, strings.Repeat("5", 64), 25, "bitcoincashii:qlegacyfinder0000", false); err != nil {
		fmt.Println("error:", err)
		return 1
	}
	for _, m := range []string{"bitcoincashii:qpplnsa0000", "bitcoincashii:qpplnsb0000"} {
		if _, err := db.Exec(`INSERT INTO shares (miner_address, worker_name, difficulty, is_solo) VALUES (?, 'w', 10, 0)`, m); err != nil {
			fmt.Println("error:", err)
			return 1
		}
	}
	d := db
	d.SetMaxOpenConns(1)
	ctx := context.Background()
	conn, err := d.Conn(ctx)
	if err != nil {
		fmt.Println("error:", err)
		return 1
	}

	distributed := make(chan error, 1)
	go func() { distributed <- Distribute1175Block(height, 100) }()
	if !waitUntil(func() bool { return d.Stats().WaitCount > 0 }) {
		fmt.Println("error: the distribution never asked for a connection")
		return 1
	}
	closed := make(chan struct{})
	go func() {
		CloseDB()
		close(closed)
	}()
	// A read lock is no longer granted once CloseDB waits for the lock.
	if !waitUntil(func() bool {
		if dbMu.TryRLock() {
			dbMu.RUnlock()
			return false
		}
		return true
	}) {
		fmt.Println("error: CloseDB never asked for the lock")
		return 1
	}
	conn.Close()

	select {
	case err := <-distributed:
		fmt.Println("distributed", err)
	case <-time.After(2 * time.Second):
		fmt.Println("stuck")
		return 1
	}
	select {
	case <-closed:
		fmt.Println("closed")
		return 0
	case <-time.After(5 * time.Second):
		fmt.Println("error: CloseDB never finished")
		return 1
	}
}

// waitUntil polls cond for up to 5 s.
func waitUntil(cond func() bool) bool {
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(time.Millisecond) {
		if cond() {
			return true
		}
	}
	return false
}

// A distribution read the PPLNS window through GetPPLNSShares, which takes the database's read lock
// again while Distribute1175Block holds it. A read lock waits for a CloseDB that asked for the lock
// first, and CloseDB waits for the first read lock: a stop that came in between hung the stratum,
// and every database call after it.
func TestDistributeDoesNotDeadlockWithQueuedWriter(t *testing.T) {
	c := startChild(t, "distribute-close", filepath.Join(t.TempDir(), "queued.db"))
	line := c.next(t, 30*time.Second, "LEDGER-LOCK-SETUP")
	if line == "stuck" {
		t.Fatalf("LEDGER-LOCK-DEADLOCK: the 1175 distribution and CloseDB waited for each other; its log:\n%s", c.stderr.String())
	}
	if line != "distributed <nil>" {
		t.Fatalf("LEDGER-LOCK-SETUP: the child said %q; its log:\n%s", line, c.stderr.String())
	}
	c.expect(t, "closed", 10*time.Second, "LEDGER-LOCK-CLOSE")
}

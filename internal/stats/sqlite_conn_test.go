//go:build sqlite

package stats

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Every connection a program keeps is in WAL mode, syncs each commit and waits 10 s for another
// program's lock, and a transaction takes the write lock when it starts.
func TestSQLitePragmasPinned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pragmas.db")
	if err := InitDB(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(CloseDB)
	if n := db.Stats().MaxOpenConnections; n != 4 {
		t.Fatalf("SQL-PIN-CONNS: a program keeps at most %d connections, want 4", n)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var conns []*sql.Conn
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()
	for i := 0; i < 4; i++ {
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("SQL-PIN-CONNS: connection %d of 4: %v", i+1, err)
		}
		conns = append(conns, c)
	}
	for i, c := range conns {
		var mode string
		var sync, busy int
		if err := c.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
			t.Fatal(err)
		}
		if err := c.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&sync); err != nil {
			t.Fatal(err)
		}
		if err := c.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busy); err != nil {
			t.Fatal(err)
		}
		if mode != "wal" || sync != 2 || busy != 10000 {
			t.Errorf("SQL-PIN-PRAGMAS: connection %d has journal_mode %s, synchronous %d, busy_timeout %d; want wal, 2 (FULL), 10000",
				i+1, mode, sync, busy)
		}
	}

	// A transaction holds the write lock from its start: another connection that does not wait
	// cannot take it meanwhile.
	tx, err := conns[0].BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", path+"?_busy_timeout=0")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	oc, err := other.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer oc.Close()
	_, err = oc.ExecContext(ctx, `BEGIN IMMEDIATE`)
	if !isBusy(err) {
		t.Errorf("SQL-PIN-TXLOCK: a transaction had begun, and another connection still took the write lock (%v)", err)
	}
	if err == nil {
		oc.ExecContext(ctx, `ROLLBACK`)
	}
	tx.Rollback()

	// synchronous FULL is also SQLite's default, so only the DSN shows it is asked for.
	for _, pin := range []string{"_busy_timeout=10000", "_journal_mode=WAL", "_txlock=immediate", "_pragma=synchronous(FULL)"} {
		if !strings.Contains(SQLiteDSN(path), pin) {
			t.Errorf("SQL-PIN-DSN: SQLiteDSN does not set %s", pin)
		}
	}
}

// While a write waits for another program's lock, the dashboard's reads and the health ping
// still answer at once. With one connection they all waited behind the write: the dashboard said
// "database is not answering" and the miner status said the database was down.
func TestReadsDoNotQueueBehindAWaitingWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stall.db")
	if err := InitDB(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(CloseDB)
	const miner = "bitcoincashii:qstalledwrite0000000000000000000000000000"
	if err := SavePoolConfig(miner, "", ""); err != nil {
		t.Fatal(err)
	}

	// The other program holds the write lock for 3 s.
	ctx := context.Background()
	other, err := sql.Open("sqlite", path+"?_busy_timeout=10000")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	holder, err := other.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if _, err := holder.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	held := time.Now()
	if _, err := holder.ExecContext(ctx, `INSERT INTO shares (miner_address, difficulty) VALUES ('other', 1)`); err != nil {
		t.Fatal(err)
	}

	wrote := make(chan error, 1)
	go func() { wrote <- SaveSoloBlockCoinbaseDirect(miner, 700, 3.125, strings.Repeat("7", 64)) }()
	time.Sleep(300 * time.Millisecond)
	select {
	case err := <-wrote:
		t.Fatalf("SQL-STALL-SETUP: the write did not wait for the other program's lock (%v)", err)
	default:
	}

	read := func(name string, f func() error) {
		start := time.Now()
		err := f()
		if d := time.Since(start); err != nil || d > 200*time.Millisecond {
			t.Errorf("SQL-STALL-READ: %s took %v (error %v) while a write waited for the other program", name, d.Round(time.Millisecond), err)
		}
	}
	read("SoloBlocksSummary", func() error { _, _, _, err := SoloBlocksSummary(miner); return err })
	read("IsDBConnected", func() error {
		if !IsDBConnected() {
			return errors.New("IsDBConnected is false")
		}
		return nil
	})
	read("GetPoolConfig", func() error { _, _, _, err := GetPoolConfig(); return err })

	time.Sleep(time.Until(held.Add(3 * time.Second)))
	if _, err := holder.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-wrote:
		if err != nil {
			t.Fatalf("SQL-STALL-WRITE: the waiting write failed once the lock was free: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SQL-STALL-WRITE: the waiting write never finished once the lock was free")
	}
	if blocks, _, _, err := SoloBlocksSummary(miner); err != nil || len(blocks) != 1 {
		t.Fatalf("SQL-STALL-WRITE: the block is not listed after the write: %+v, %v", blocks, err)
	}
}

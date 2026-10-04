package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/migstatus"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// What the stratum logs while the move of the old data has failed.
const notMiningLine = "not mining: moving the data to the new database failed (see the dashboard)"

// While the move has failed the stratum says it is not mining, again every notMiningLogEvery, reads
// the status every moveStatusPollEvery, and returns once the move is no longer failed. The program
// as a whole is tested in maintenance_sqlite_test.go.
func TestWaitOutFailedMove(t *testing.T) {
	if moveStatusPollEvery != 10*time.Second || notMiningLogEvery != 10*time.Minute {
		t.Errorf("MAINT-STRATUM-TIMES: the status is read every %v and the line logged every %v, want 10s and 10m", moveStatusPollEvery, notMiningLogEvery)
	}
	core, logs := observer.New(zap.InfoLevel)
	prevLogger, prevPoll, prevLog := logger, moveStatusPollEvery, notMiningLogEvery
	logger, moveStatusPollEvery, notMiningLogEvery = zap.New(core), 20*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() { logger, moveStatusPollEvery, notMiningLogEvery = prevLogger, prevPoll, prevLog })

	db := filepath.Join(t.TempDir(), "forgesolo.db")
	if err := migstatus.Write(db, migstatus.Status{State: migstatus.Failed, Code: 10, Reason: "the old database could not be reached"}); err != nil {
		t.Fatal(err)
	}
	st, blocked := migstatus.Blocked(db)
	if !blocked {
		t.Fatal("MAINT-STRATUM-BLOCKED: a failed move does not block the database")
	}
	done := make(chan struct{})
	go func() {
		waitOutFailedMove(db, st)
		close(done)
	}()
	time.Sleep(450 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("MAINT-STRATUM-STAYS: the stratum stopped waiting while the move still failed")
	default:
	}
	said := logs.FilterMessage(notMiningLine)
	if said.Len() < 3 {
		t.Errorf("MAINT-STRATUM-REPEAT: %q logged %d times in 450 ms at one every 100 ms", notMiningLine, said.Len())
	} else if f := said.All()[0].ContextMap(); f["reason"] != "the old database could not be reached" || f["code"] != int64(10) {
		t.Errorf("MAINT-STRATUM-REASON: the line carries %v, want the failure's code and reason", f)
	}

	if err := migstatus.Write(db, migstatus.Status{State: migstatus.Skipped}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("MAINT-STRATUM-EXIT: the stratum still waits after the move stopped failing")
	}
}

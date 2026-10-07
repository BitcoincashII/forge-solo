package pgmigrate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/dblock"
	"github.com/BitcoincashII/forge-solo/internal/migstatus"
)

// How long run waits for the private server: to be ready (crash recovery of a large cluster on a
// slow board takes minutes), and to stop after SIGINT before it is told SIGQUIT. Variables so a
// test need not wait.
var (
	ReadyWait = 10 * time.Minute
	StopWait  = 2 * time.Minute
)

// RunOptions says what run moves.
type RunOptions struct {
	DB      string // forgesolo.db
	PGData  string // the earlier version's PostgreSQL data
	Owner   *Owner // the app's user, who must own the database's files
	Version string
	Logf    func(format string, args ...any)
	// Open opens the server run starts; OpenPostgres when nil.
	Open func(dsn string) (Source, error)
}

// Run is the whole move as the Umbrel container makes it, before the api and the stratum start:
//  1. the database's folder and files go to the app's user, and Plan decides; none, skipped and
//     degraded are recorded as they are;
//  2. nobody may have the database open (the in-use lock, held to the end, and SQLite's own lock):
//     otherwise deferred;
//  3. a private PostgreSQL server starts on the old data as its owner, read-only, without TCP
//     (StartServer), and prepare copies it;
//  4. the server is stopped, cleanly, and commit puts the copy in place.
//
// Every outcome is recorded in the status file. A failure is recorded as failed, with its code,
// reason and the end of the server's log, and run still exits 0: the api then starts in
// maintenance mode and the dashboard says what happened. Only an interruption (3: nothing
// committed, the status left as it was) and a status file that cannot be written (41) end
// non-zero, which holds the services back.
func Run(ctx context.Context, o RunOptions) int {
	logf := func(format string, args ...any) {
		if o.Logf != nil {
			o.Logf(format, args...)
		}
	}
	var srv *Server
	record := func(s migstatus.Status) int {
		s.Version = o.Version
		if err := migstatus.Write(o.DB, s); err != nil {
			logf("the status file could not be written: %v", err)
			return CodeStatus
		}
		if err := EnsureOwner(o.DB, o.Owner); err != nil {
			logf("the database's files could not be given to the app's user: %v", err)
		}
		line := "status " + s.State
		if s.Reason != "" {
			line += ": " + s.Reason
		}
		logf("%s", line)
		return CodeOK
	}
	var d Decision
	fail := func(err error) int {
		if ctx.Err() != nil || CodeOf(err) == CodeInterrupted {
			logf("interrupted: nothing was replaced")
			return CodeInterrupted
		}
		code := CodeOf(err)
		if code == CodeDeferred {
			return record(migstatus.Status{State: migstatus.Deferred, Code: code, Reason: ReasonOf(err), Detail: err.Error()})
		}
		logf("the move failed (%d): %v", code, err)
		detail := err.Error()
		if tail := srv.LogTail(20); tail != "" {
			detail += "\n" + tail
		}
		state := migstatus.Failed
		if dbThere, _ := exists(o.DB); dbThere && d.Action != ActionMerge {
			if marker, _ := exists(MarkerPath(o.DB)); marker {
				state = migstatus.Degraded // not a needed merge: start on the database there
			}
		}
		return record(migstatus.Status{State: state, Code: code, Reason: ReasonOf(err), Detail: detail})
	}

	if err := EnsureOwner(o.DB, o.Owner); err != nil {
		return fail(newErr(CodeWrite, "the database folder could not be given to the app's user", err))
	}
	d = Plan(o.DB, o.PGData)
	logf("plan: %s", d)
	switch d.Action {
	case ActionNone:
		return record(migstatus.Status{State: migstatus.None, Reason: "there is nothing to move"})
	case ActionSkipped:
		return record(migstatus.Status{State: migstatus.Skipped,
			Reason: SkipName + " is there: Forge Solo started without the data of the earlier version"})
	case ActionDegraded:
		return record(migstatus.Status{State: migstatus.Degraded, Reason: d.Reason})
	case ActionFailed:
		return fail(refused(d.Reason, nil))
	}

	lock, err := dblock.TryExclusive(dblock.Path(o.DB))
	if errors.Is(err, dblock.ErrBusy) {
		return fail(deferred(fmt.Errorf("a program holds %s", dblock.Path(o.DB))))
	}
	if err != nil {
		return fail(newErr(CodeOther, "the database's in-use lock could not be taken", err))
	}
	defer lock.Release()
	if there, _ := exists(o.DB); there {
		db, conn, err := exclusive(ctx, o.DB)
		if err != nil {
			return fail(err)
		}
		conn.Close()
		db.Close()
	}
	if _, err := ReadControl(o.PGData); err != nil {
		return fail(refused("the old data's pg_control is damaged or missing", err))
	}

	if srv, err = StartServer(ctx, o.PGData, logf); err != nil {
		return fail(err)
	}
	open := o.Open
	if open == nil {
		open = OpenPostgres
	}
	var p *Prepared
	src, err := open(srv.DSN())
	if err == nil {
		p, err = Prepare(ctx, src, PrepareOptions{DB: o.DB, Merge: d.Action == ActionMerge, Version: o.Version, Logf: o.Logf, InUse: lock})
		src.Close()
	}
	stopErr := srv.Stop()
	if err != nil {
		return fail(err)
	}
	if ctx.Err() != nil {
		return fail(ctx.Err())
	}
	if stopErr != nil {
		return fail(refused("the old data's server did not shut down cleanly", stopErr))
	}
	// Past this point the move finishes even if a signal comes: the commit is quick, and a commit
	// cut short would only be redone at the next start.
	c, err := Commit(context.Background(), CommitOptions{DB: o.DB, PGData: o.PGData, Owner: o.Owner, Version: o.Version, Logf: o.Logf, InUse: lock})
	if err != nil {
		return fail(err)
	}
	var counts []string
	for _, t := range []string{"blocks", "payouts", "blocks_1175", "payouts_1175", "miners"} {
		counts = append(counts, fmt.Sprintf("%s %d", t, p.Counts[t]))
	}
	logf("done: %s from %s; %s", c.Mode, p.Source, strings.Join(counts, ", "))
	return CodeOK
}

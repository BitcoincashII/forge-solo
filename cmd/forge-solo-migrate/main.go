// Command forge-solo-migrate moves an earlier Forge Solo's PostgreSQL data into forgesolo.db, once,
// and merges what each version recorded after a return to 1.0.12 and back. On Umbrel it is a
// one-shot container that runs before the api and the stratum (run); on Windows the launcher runs
// it step by step around the PostgreSQL it starts (plan, prepare, commit). See internal/pgmigrate.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/BitcoincashII/forge-solo/internal/migstatus"
	"github.com/BitcoincashII/forge-solo/internal/pgmigrate"
)

// version is set at build time: -ldflags "-X main.version=1.0.13".
var version = "dev"

// openSource opens the old database; a test gives another.
var openSource = pgmigrate.OpenPostgres

const usage = `usage:
  forge-solo-migrate plan --db FILE --pgdata DIR
      says what a start does: none, move, merge, skipped, degraded:<why> or failed:<why>
  forge-solo-migrate prepare --db FILE [--merge]
      copies the old database (its address in FORGE_MIGRATE_PG) into FILE.migrating
  forge-solo-migrate commit --db FILE --pgdata DIR [--owner UID:GID]
      puts the prepared copy in place, PostgreSQL stopped
  forge-solo-migrate run --db FILE --pgdata DIR --owner UID:GID
      all of it, starting a private PostgreSQL (the Umbrel container)
  forge-solo-migrate noop
      does nothing (the image of the stopped database service)

Exit codes: 0 done or nothing to do; 3 interrupted; 10 the old database could not be reached;
20 the copy did not check out; 21 a disk or file error; 30 refused; 31 deferred, the database
is in use; 40 other; 41 (run) the status file could not be written. run records every other
outcome in migration-status.json and exits 0.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	logf := log.New(stderr, "forge-solo-migrate: ", log.LstdFlags).Printf
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	db := fs.String("db", "", "the database: forgesolo.db")
	pgdata := fs.String("pgdata", "", "the earlier version's PostgreSQL data folder")
	merge := fs.Bool("merge", false, "prepare: merge the database that is there")
	owner := fs.String("owner", "", "uid:gid of the app's programs, who must own the database's files")
	need := func(names ...string) int {
		if err := fs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		for _, n := range names {
			if fs.Lookup(n).Value.String() == "" {
				fmt.Fprintf(stderr, "%s needs --%s\n%s", args[0], n, usage)
				return 2
			}
		}
		if fs.NArg() > 0 {
			fmt.Fprintf(stderr, "%s: unexpected %q\n%s", args[0], fs.Arg(0), usage)
			return 2
		}
		return -1
	}
	parseOwner := func() (*pgmigrate.Owner, int) {
		if *owner == "" {
			return nil, -1
		}
		o, err := pgmigrate.ParseOwner(*owner)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return nil, 2
		}
		return o, -1
	}

	switch args[0] {
	case "noop":
		return 0
	case "plan":
		if c := need("db", "pgdata"); c >= 0 {
			return c
		}
		fmt.Fprintln(stdout, pgmigrate.Plan(*db, *pgdata))
		return 0
	case "prepare":
		if c := need("db"); c >= 0 {
			return c
		}
		dsn := os.Getenv("FORGE_MIGRATE_PG")
		if dsn == "" {
			fmt.Fprintln(stderr, "prepare reads the old database's address from FORGE_MIGRATE_PG, which is not set")
			return 2
		}
		src, err := openSource(dsn)
		if err == nil {
			defer src.Close()
			var p *pgmigrate.Prepared
			if p, err = pgmigrate.Prepare(ctx, src, pgmigrate.PrepareOptions{DB: *db, Merge: *merge, Version: version, Logf: logf}); err == nil {
				fmt.Fprintf(stdout, "prepared: %s from %s, %d blocks, %d payouts\n", p.Mode, p.Source, p.Counts["blocks"], p.Counts["payouts"])
			}
		}
		return finish(ctx, *db, err, stderr)
	case "commit":
		if c := need("db", "pgdata"); c >= 0 {
			return c
		}
		o, c := parseOwner()
		if c >= 0 {
			return c
		}
		cm, err := pgmigrate.Commit(ctx, pgmigrate.CommitOptions{DB: *db, PGData: *pgdata, Owner: o, Version: version, Logf: logf})
		if err == nil {
			fmt.Fprintf(stdout, "committed: %s\n", cm.Mode)
		}
		return finish(ctx, *db, err, stderr)
	case "run":
		if c := need("db", "pgdata", "owner"); c >= 0 {
			return c
		}
		o, c := parseOwner()
		if c >= 0 {
			return c
		}
		return pgmigrate.Run(ctx, pgmigrate.RunOptions{DB: *db, PGData: *pgdata, Owner: o, Version: version, Logf: logf, Open: openSource})
	case "-h", "-help", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	fmt.Fprintf(stderr, "unknown command %q\n%s", args[0], usage)
	return 2
}

// finish is prepare's and commit's exit code. Like run, it records a deferral or a failure in the
// status file, for the Windows launcher and the dashboard to show; an interruption leaves it.
func finish(ctx context.Context, db string, err error, stderr io.Writer) int {
	if err == nil {
		return 0
	}
	code := pgmigrate.CodeOf(err)
	if ctx.Err() != nil || code == pgmigrate.CodeInterrupted {
		fmt.Fprintln(stderr, "interrupted: nothing was replaced")
		return pgmigrate.CodeInterrupted
	}
	fmt.Fprintln(stderr, err)
	st := migstatus.Status{State: migstatus.Failed, Code: code, Reason: pgmigrate.ReasonOf(err), Detail: err.Error(), Version: version}
	if code == pgmigrate.CodeDeferred {
		st.State = migstatus.Deferred
	}
	if werr := migstatus.Write(db, st); werr != nil {
		fmt.Fprintln(stderr, "the status file could not be written:", werr)
	}
	return code
}

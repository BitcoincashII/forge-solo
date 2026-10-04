//go:build sqlite

package main

import (
	"bytes"
	"context"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/dblock"
	"github.com/BitcoincashII/forge-solo/internal/migstatus"
	"github.com/BitcoincashII/forge-solo/internal/pgmigrate"
)

// The tests run the command as a program: a copy of this test binary that runs main, with the old
// database a test names in FORGE_MIGRATE_TEST_SOURCE and the waits a test shortens.
const mainEnv = "FORGE_MIGRATE_TEST_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(mainEnv) == "1" {
		openSource = testSource
		if w, err := time.ParseDuration(os.Getenv("FORGE_MIGRATE_TEST_READY_WAIT")); err == nil {
			pgmigrate.ReadyWait = w
		}
		if w, err := time.ParseDuration(os.Getenv("FORGE_MIGRATE_TEST_STOP_WAIT")); err == nil {
			pgmigrate.StopWait = w
		}
		version = "1.0.13-test"
		main()
	}
	os.Exit(m.Run())
}

var types = map[string]map[string]string{
	"pool_config": {"id": "integer", "pool_address": "text", "payout_address_1175": "text", "coinbase_tag": "text",
		"payout_mode": "text", "updated_at": "timestamp with time zone"},
	"blocks": {"id": "bigint", "height": "bigint", "hash": "character varying", "miner_address": "character varying",
		"reward": "numeric", "status": "character varying", "is_solo": "boolean", "created_at": "timestamp with time zone",
		"confirmed_at": "timestamp with time zone"},
	"payouts": {"id": "bigint", "miner_address": "character varying", "block_height": "bigint", "amount": "numeric",
		"confirmed": "boolean", "txid": "character varying", "status": "character varying",
		"created_at": "timestamp with time zone", "paid_at": "timestamp with time zone"},
	"blocks_1175": {"height": "bigint", "hash": "text", "gross_reward": "double precision", "is_solo": "boolean",
		"finder": "text", "distributed": "boolean", "status": "text", "created_at": "timestamp with time zone"},
}

const minerAddr = "bitcoincashii:qqminera00000000000000000000000000000000000000"

// oldDatabase is an earlier version's database with n solo blocks.
func oldDatabase(n int64) *pgmigrate.MemSource {
	m := pgmigrate.NewMemSource(pgmigrate.Info{ServerVersion: "16.6", SystemIdentifier: "1"})
	for name, t := range types {
		m.AddTable(name, t)
	}
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	m.Insert("pool_config", map[string]any{"id": int64(1), "pool_address": minerAddr, "payout_address_1175": "",
		"coinbase_tag": "", "payout_mode": "solo", "updated_at": at})
	for h := int64(1); h <= n; h++ {
		m.Insert("blocks", map[string]any{"id": h, "height": h, "hash": strings.Repeat("a", 63) + string(rune('0'+h)),
			"miner_address": minerAddr, "reward": "3.12500000", "status": "confirmed", "is_solo": true,
			"created_at": at.Add(time.Duration(h) * time.Hour), "confirmed_at": nil})
		m.Insert("payouts", map[string]any{"id": h, "miner_address": minerAddr, "block_height": h, "amount": "3.12500000",
			"confirmed": true, "txid": "coinbase-direct", "status": "paid", "created_at": at, "paid_at": at})
	}
	return m
}

// testSource is the old database FORGE_MIGRATE_TEST_SOURCE names.
func testSource(dsn string) (pgmigrate.Source, error) {
	switch os.Getenv("FORGE_MIGRATE_TEST_SOURCE") {
	case "small":
		return oldDatabase(3), nil
	case "bigger":
		return oldDatabase(6), nil
	case "after-ready": // reachable only once the stand-in server says in its log it is ready itself
		m := oldDatabase(3)
		m.BeforeRead = func(ctx context.Context, table string) error {
			if b, _ := os.ReadFile(os.Getenv("STANDIN_LOG")); !strings.Contains(string(b), "really ready") {
				return errors.New("pq: the database system is not yet accepting connections (57P03)")
			}
			return nil
		}
		return m, nil
	case "stall": // reads until it is interrupted, having said so in a file
		m := oldDatabase(3)
		m.BeforeRead = func(ctx context.Context, table string) error {
			os.WriteFile(os.Getenv("FORGE_MIGRATE_TEST_SIGNAL"), nil, 0o600)
			<-ctx.Done()
			return ctx.Err()
		}
		return m, nil
	case "nan": // a NaN reward in a double precision column: SQLite stores NULL, and the read-back fails
		m := oldDatabase(0)
		floatReward := map[string]string{}
		for k, v := range types["blocks"] {
			floatReward[k] = v
		}
		floatReward["reward"] = "double precision"
		m.AddTable("blocks", floatReward)
		m.Insert("blocks", map[string]any{"id": int64(1), "height": int64(1), "hash": "h", "miner_address": minerAddr,
			"reward": math.NaN(), "status": "pending", "is_solo": true, "created_at": time.Now(), "confirmed_at": nil})
		return m, nil
	case "error":
		m := oldDatabase(3)
		m.BeforeRead = func(context.Context, string) error { return errors.New("the disk under the old database failed") }
		return m, nil
	case "unreachable":
		return nil, &pgmigrate.Error{Code: pgmigrate.CodeSource, Reason: "the old database could not be reached", Err: errors.New("connection refused")}
	}
	return nil, errors.New("no test source named")
}

type result struct {
	code           int
	stdout, stderr string
}

// command is the program with args, with env added.
func command(env map[string]string, args ...string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), mainEnv+"=1")
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	return cmd
}

func runCLI(t *testing.T, env map[string]string, args ...string) result {
	t.Helper()
	cmd := command(env, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	r := result{stdout: out.String(), stderr: errb.String()}
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		r.code = ee.ExitCode()
	case err != nil:
		t.Fatal(err)
	}
	return r
}

// layout is a database folder and an old-data folder.
type layout struct{ dir, db, pgdata string }

func newLayout(t *testing.T, pgVersion, control string) layout {
	t.Helper()
	dir := t.TempDir()
	l := layout{dir: dir, db: filepath.Join(dir, "data", "forgesolo.db"), pgdata: filepath.Join(dir, "pgdata")}
	must(t, os.MkdirAll(filepath.Dir(l.db), 0o700))
	if pgVersion == "" {
		return l
	}
	must(t, os.MkdirAll(filepath.Join(l.pgdata, "global"), 0o700))
	must(t, os.WriteFile(filepath.Join(l.pgdata, "PG_VERSION"), []byte(pgVersion+"\n"), 0o600))
	l.setControl(t, control)
	return l
}

func (l layout) setControl(t *testing.T, fixture string) {
	t.Helper()
	b, err := os.ReadFile(fixturePath(fixture))
	must(t, err)
	must(t, os.WriteFile(filepath.Join(l.pgdata, "global", "pg_control"), b, 0o600))
}

func fixturePath(name string) string {
	p, _ := filepath.Abs(filepath.Join("..", "..", "internal", "pgmigrate", "testdata", name))
	return p
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func status(t *testing.T, db string) migstatus.Status {
	t.Helper()
	s, ok, err := migstatus.Read(db)
	if err != nil || !ok {
		t.Fatalf("the status file: %v (there: %v)", err, ok)
	}
	return s
}

func TestNoopAndUsage(t *testing.T) {
	if r := runCLI(t, nil, "noop"); r.code != 0 {
		t.Errorf("MIG-CLI-NOOP: noop exits %d, want 0", r.code)
	}
	if r := runCLI(t, nil); r.code != 2 || !strings.Contains(r.stderr, "usage:") {
		t.Errorf("MIG-CLI-USAGE: no command exits %d (%s), want 2 and the usage", r.code, r.stderr)
	}
	if r := runCLI(t, nil, "frobnicate"); r.code != 2 {
		t.Errorf("MIG-CLI-USAGE: an unknown command exits %d, want 2", r.code)
	}
	if r := runCLI(t, nil, "plan", "--help"); r.code != 0 || !strings.Contains(r.stderr, "usage:") {
		t.Errorf("MIG-CLI-HELP: plan --help exits %d, want 0 and the usage", r.code)
	}
	if r := runCLI(t, nil, "plan", "--db", "x"); r.code != 2 || !strings.Contains(r.stderr, "--pgdata") {
		t.Errorf("MIG-CLI-USAGE: plan without --pgdata exits %d (%s), want 2", r.code, r.stderr)
	}
	if r := runCLI(t, nil, "run", "--db", "x", "--pgdata", "y"); r.code != 2 || !strings.Contains(r.stderr, "--owner") {
		t.Errorf("MIG-CLI-USAGE: run without --owner exits %d, want 2", r.code)
	}
	if r := runCLI(t, nil, "commit", "--db", "x", "--pgdata", "y", "--owner", "root"); r.code != 2 {
		t.Errorf("MIG-CLI-USAGE: a malformed --owner exits %d, want 2", r.code)
	}
}

func TestPlanPrints(t *testing.T) {
	fresh := newLayout(t, "", "")
	if r := runCLI(t, nil, "plan", "--db", fresh.db, "--pgdata", fresh.pgdata); r.code != 0 || r.stdout != "none\n" {
		t.Errorf("MIG-CLI-PLAN: a fresh install prints %q (%d), want none", r.stdout, r.code)
	}
	old := newLayout(t, "16", "pg_control-shutdown")
	if r := runCLI(t, nil, "plan", "--db", old.db, "--pgdata", old.pgdata); r.code != 0 || r.stdout != "move\n" {
		t.Errorf("MIG-CLI-PLAN: an earlier version's data prints %q (%d), want move", r.stdout, r.code)
	}
	v15 := newLayout(t, "15", "pg_control-shutdown")
	if r := runCLI(t, nil, "plan", "--db", v15.db, "--pgdata", v15.pgdata); r.code != 0 || !strings.HasPrefix(r.stdout, "failed:") {
		t.Errorf("MIG-CLI-PLAN: PostgreSQL 15 data prints %q (%d), want failed:<why>", r.stdout, r.code)
	}
}

// The Windows launcher's steps: prepare with the address in the environment, then commit.
func TestPrepareAndCommit(t *testing.T) {
	l := newLayout(t, "16", "pg_control-shutdown")
	if r := runCLI(t, map[string]string{"FORGE_MIGRATE_TEST_SOURCE": "small"}, "prepare", "--db", l.db); r.code != 2 || !strings.Contains(r.stderr, "FORGE_MIGRATE_PG") {
		t.Fatalf("MIG-CLI-DSN: prepare without FORGE_MIGRATE_PG exits %d (%s), want 2", r.code, r.stderr)
	}
	env := map[string]string{"FORGE_MIGRATE_TEST_SOURCE": "small", "FORGE_MIGRATE_PG": "host=127.0.0.1 password=secret"}
	if r := runCLI(t, env, "prepare", "--db", l.db); r.code != 0 || !strings.Contains(r.stdout, "prepared: move from postgres, 3 blocks") {
		t.Fatalf("MIG-CLI-PREPARE: prepare exits %d: %s %s", r.code, r.stdout, r.stderr)
	}
	if r := runCLI(t, nil, "commit", "--db", l.db, "--pgdata", l.pgdata); r.code != 0 {
		t.Fatalf("MIG-CLI-COMMIT: commit exits %d: %s", r.code, r.stderr)
	}
	if s := status(t, l.db); s.State != migstatus.Done || s.Version != "1.0.13-test" {
		t.Fatalf("MIG-CLI-COMMIT: the status is %+v, want done", s)
	}
	if d := pgmigrate.Plan(l.db, l.pgdata); d.Action != pgmigrate.ActionNone {
		t.Fatalf("MIG-CLI-COMMIT: after the move the next start says %s", d)
	}
}

// A failed or deferred prepare is recorded for the launcher and the dashboard, with its code.
func TestPrepareOutcomeIsRecorded(t *testing.T) {
	l := newLayout(t, "16", "pg_control-shutdown")
	r := runCLI(t, map[string]string{"FORGE_MIGRATE_TEST_SOURCE": "nan", "FORGE_MIGRATE_PG": "x"}, "prepare", "--db", l.db)
	if r.code != pgmigrate.CodeVerify {
		t.Fatalf("MIG-CLI-FAILED: a copy that does not check out exits %d, want 20: %s", r.code, r.stderr)
	}
	if s, ok, err := migstatus.Read(l.db); !ok || err != nil || s.State != migstatus.Failed || s.Code != pgmigrate.CodeVerify || s.Reason == "" {
		t.Fatalf("MIG-CLI-FAILED: the status is %+v (there: %v, %v), want failed with code 20", s, ok, err)
	}

	env := map[string]string{"FORGE_MIGRATE_TEST_SOURCE": "small", "FORGE_MIGRATE_PG": "x"}
	if r := runCLI(t, env, "prepare", "--db", l.db); r.code != 0 {
		t.Fatal(r.stderr)
	}
	if r := runCLI(t, nil, "commit", "--db", l.db, "--pgdata", l.pgdata); r.code != 0 {
		t.Fatal(r.stderr)
	}
	held, err := dblock.Shared(dblock.Path(l.db), 0)
	must(t, err)
	defer held.Release()
	if r := runCLI(t, env, "prepare", "--db", l.db, "--merge"); r.code != pgmigrate.CodeDeferred {
		t.Fatalf("MIG-CLI-DEFERRED: a merge while the api holds the database exits %d, want 31: %s", r.code, r.stderr)
	}
	if s := status(t, l.db); s.State != migstatus.Deferred || s.Code != pgmigrate.CodeDeferred {
		t.Fatalf("MIG-CLI-DEFERRED: the status is %+v, want deferred", s)
	}
}

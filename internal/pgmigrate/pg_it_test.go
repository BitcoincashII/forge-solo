//go:build sqlite && it

package pgmigrate

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"
)

// Against a real PostgreSQL 16: FORGE_MIGRATE_IT_PG is a superuser's DSN on a server the test may
// create databases on. The lib/pq Source must read exactly what the in-memory model the unit tests
// use says it reads, so the unit tests stand for the real thing.

func itDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("FORGE_MIGRATE_IT_PG")
	if dsn == "" {
		t.Fatal("MIG-IT-SETUP: FORGE_MIGRATE_IT_PG is not set; this test needs a PostgreSQL 16 server")
	}
	return dsn
}

// withDB is dsn on another database.
func withDB(t *testing.T, dsn, name string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

// loadIntoPostgres makes a database named name holding 1.0.12's schema and m's rows, with the
// server's TimeZone for it set to zone.
func loadIntoPostgres(t *testing.T, m *MemSource, name, zone string) string {
	t.Helper()
	admin, err := sql.Open("postgres", itDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	for _, q := range []string{
		`DROP DATABASE IF EXISTS ` + pq.QuoteIdentifier(name),
		`CREATE DATABASE ` + pq.QuoteIdentifier(name),
		`ALTER DATABASE ` + pq.QuoteIdentifier(name) + ` SET TimeZone = ` + pq.QuoteLiteral(zone),
	} {
		if _, err := admin.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	dsn := withDB(t, itDSN(t), name)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema, err := os.ReadFile("testdata/pg-1.0.12-schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatalf("1.0.12 schema: %v", err)
	}
	var names []string
	for n := range m.tables {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		tb := m.tables[n]
		if _, err := db.Exec(`DELETE FROM ` + n); err != nil {
			t.Fatal(err)
		}
		for _, r := range tb.Rows {
			var cols, ph []string
			var args []any
			for c, v := range r {
				cols = append(cols, pq.QuoteIdentifier(c))
				ph = append(ph, fmt.Sprintf("$%d", len(ph)+1))
				args = append(args, v)
			}
			q := `INSERT INTO ` + n + ` (` + strings.Join(cols, ", ") + `) VALUES (` + strings.Join(ph, ", ") + `)`
			if _, err := db.Exec(q, args...); err != nil {
				t.Fatalf("%s %v: %v", q, args, err)
			}
		}
	}
	return dsn
}

// The same rows, read through lib/pq from a real server in three time zones and from the in-memory
// model, make the same database.
func TestPostgresSourceReadsAsTheModel(t *testing.T) {
	want := filepath.Join(t.TempDir(), "forgesolo.db")
	if _, err := prepare(t, source1012(), want); err != nil {
		t.Fatal(err)
	}
	for i, zone := range []string{"UTC", "America/Chicago", "Pacific/Chatham"} {
		t.Run(zone, func(t *testing.T) {
			dsn := loadIntoPostgres(t, source1012(), fmt.Sprintf("forgesolo_it_%d", i), zone)
			src, err := OpenPostgres(dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer src.Close()
			db := filepath.Join(t.TempDir(), "forgesolo.db")
			p, err := prepare(t, src, db)
			if err != nil {
				t.Fatalf("MIG-IT-PREPARE: %v", err)
			}
			if p.Source != "postgres" {
				t.Fatalf("MIG-IT-PREPARE: source %q", p.Source)
			}
			if a, b := dump(t, MigratingPath(want)), dump(t, MigratingPath(db)); a != b {
				t.Fatalf("MIG-IT-MODEL: read through lib/pq with TimeZone %s, the copy differs from the model's:\n%s\nwant:\n%s", zone, b, a)
			}
			meta, _ := ReadMeta(context.Background(), MigratingPath(db))
			if !strings.HasPrefix(meta["server_version"], "16.") || meta["system_identifier"] == "" {
				t.Errorf("MIG-IT-META: server %q, system %q", meta["server_version"], meta["system_identifier"])
			}
		})
	}
}

// A 1.0.0 database read through lib/pq moves as the model's does.
func TestPostgres100Shape(t *testing.T) {
	want := filepath.Join(t.TempDir(), "forgesolo.db")
	if _, err := prepare(t, source100(), want); err != nil {
		t.Fatal(err)
	}
	m := source100()
	admin, err := sql.Open("postgres", itDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.Exec(`DROP DATABASE IF EXISTS forgesolo_it_100`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`CREATE DATABASE forgesolo_it_100`); err != nil {
		t.Fatal(err)
	}
	dsn := withDB(t, itDSN(t), "forgesolo_it_100")
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE miners (id BIGSERIAL PRIMARY KEY, address VARCHAR(255) UNIQUE NOT NULL, solo_mining BOOLEAN DEFAULT FALSE,
			min_payout DECIMAL(20,8) DEFAULT 0.1, manual_diff DECIMAL(20,8) DEFAULT 0, created_at TIMESTAMPTZ DEFAULT NOW(), updated_at TIMESTAMPTZ DEFAULT NOW())`,
		`CREATE TABLE blocks (id BIGSERIAL PRIMARY KEY, height BIGINT NOT NULL UNIQUE, hash VARCHAR(64) NOT NULL UNIQUE,
			miner_address VARCHAR(255) NOT NULL, reward DECIMAL(20,8) NOT NULL DEFAULT 50.0, difficulty DECIMAL(30,8) DEFAULT 0,
			status VARCHAR(20) DEFAULT 'confirmed', confirmations INT DEFAULT 0, is_solo BOOLEAN DEFAULT FALSE,
			created_at TIMESTAMPTZ DEFAULT NOW(), confirmed_at TIMESTAMPTZ)`,
		`CREATE TABLE payouts (id BIGSERIAL PRIMARY KEY, miner_address VARCHAR(255) NOT NULL, block_height BIGINT NOT NULL,
			amount DECIMAL(20,8) NOT NULL, confirmed BOOLEAN DEFAULT FALSE, txid VARCHAR(128), created_at TIMESTAMPTZ DEFAULT NOW(),
			paid_at TIMESTAMPTZ, UNIQUE(miner_address, block_height))`,
		`CREATE TABLE shares (id BIGSERIAL, time TIMESTAMPTZ NOT NULL DEFAULT NOW(), miner_address VARCHAR(255) NOT NULL,
			worker_name VARCHAR(255) NOT NULL, job_id VARCHAR(64), difficulty DECIMAL(20,8) NOT NULL, is_valid BOOLEAN NOT NULL DEFAULT TRUE,
			is_block BOOLEAN DEFAULT FALSE, is_solo BOOLEAN DEFAULT FALSE, block_hash VARCHAR(64), PRIMARY KEY (id, time))`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"miners", "blocks", "payouts"} {
		for _, r := range m.tables[n].Rows {
			var cols, ph []string
			var args []any
			for c, v := range r {
				cols = append(cols, c)
				ph = append(ph, fmt.Sprintf("$%d", len(ph)+1))
				args = append(args, v)
			}
			if _, err := db.Exec(`INSERT INTO `+n+` (`+strings.Join(cols, ", ")+`) VALUES (`+strings.Join(ph, ", ")+`)`, args...); err != nil {
				t.Fatal(err)
			}
		}
	}
	src, err := OpenPostgres(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	got := filepath.Join(t.TempDir(), "forgesolo.db")
	if _, err := prepare(t, src, got); err != nil {
		t.Fatalf("MIG-IT-100: %v", err)
	}
	if a, b := dump(t, MigratingPath(want)), dump(t, MigratingPath(got)); a != b {
		t.Fatalf("MIG-IT-100: the 1.0.0 database read through lib/pq differs from the model's:\n%s\nwant:\n%s", b, a)
	}
}

// A cluster without the forgesolo database gives an empty database, and a server that cannot be
// reached is code 10.
func TestPostgresMissingAndUnreachable(t *testing.T) {
	src, err := OpenPostgres(withDB(t, itDSN(t), "forgesolo_it_absent"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	db := filepath.Join(t.TempDir(), "forgesolo.db")
	if p, err := prepare(t, src, db); err != nil || p.Source != "none" {
		t.Fatalf("MIG-IT-MISSING: %v %+v", err, p)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bad, _ := OpenPostgres("host=127.0.0.1 port=1 user=forge dbname=forgesolo sslmode=disable connect_timeout=5")
	_, err = Prepare(ctx, bad, PrepareOptions{DB: filepath.Join(t.TempDir(), "forgesolo.db")})
	if CodeOf(err) != CodeSource {
		t.Fatalf("MIG-IT-UNREACHABLE: gave %v (code %d), want 10", err, CodeOf(err))
	}
	u := withDB(t, itDSN(t), "forgesolo_it_0")
	pu, _ := url.Parse(u)
	pu.User = url.UserPassword(pu.User.Username(), "wrong-password")
	wrong, _ := OpenPostgres(pu.String())
	if _, err := Prepare(ctx, wrong, PrepareOptions{DB: filepath.Join(t.TempDir(), "forgesolo.db")}); CodeOf(err) != CodeSource {
		t.Fatalf("MIG-IT-AUTH: a wrong password gave %v (code %d), want 10", err, CodeOf(err))
	}
}

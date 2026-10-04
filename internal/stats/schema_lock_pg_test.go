//go:build !sqlite

package stats

import (
	"bytes"
	"database/sql"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
)

// Two processes creating the schema of a fresh database at the same moment (the api and the
// stratum on a fresh install) both succeed: one of them failed on Postgres's catalog
// ("duplicate key value violates unique constraint pg_type_typname_nsp_index"), which the
// install's log showed as a database error. scripts/it-postgres.sh runs this; TIDES_PG_DB is a
// connection URL whose user may create databases.
func TestPostgresSchemaIsCreatedOneProcessAtATime(t *testing.T) {
	connStr := os.Getenv("TIDES_PG_DB")
	if connStr == "" {
		t.Skip("TIDES_PG_DB not set; skipping the concurrent schema test on Postgres")
	}
	admin, err := sql.Open("postgres", connStr)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	base, err := url.Parse(connStr)
	if err != nil {
		t.Fatal(err)
	}

	// A migration that fails is only logged, not returned: the log must stay clean too.
	var logged bytes.Buffer
	log.SetOutput(&logged)
	defer log.SetOutput(os.Stderr)

	const rounds = 10
	failed := 0
	for r := 0; r < rounds; r++ {
		name := fmt.Sprintf("schema_lock_%d_%d", os.Getpid(), r)
		if _, err := admin.Exec(`CREATE DATABASE ` + name); err != nil {
			t.Fatal(err)
		}
		u := *base
		u.Path = "/" + name
		var pools [2]*sql.DB
		for i := range pools {
			if pools[i], err = sql.Open("postgres", u.String()); err != nil {
				t.Fatal(err)
			}
			if err := pools[i].Ping(); err != nil {
				t.Fatal(err)
			}
		}
		start := make(chan struct{})
		errs := make([]error, len(pools))
		var wg sync.WaitGroup
		for i, p := range pools {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs[i] = initPostgresSchema(p)
			}()
		}
		close(start)
		wg.Wait()
		for i, e := range errs {
			if e != nil {
				failed++
				t.Logf("round %d, process %d: %v", r, i, e)
			}
		}
		var made sql.NullString
		if err := pools[0].QueryRow(`SELECT to_regclass('blocks_1175')::text`).Scan(&made); err != nil || !made.Valid {
			t.Fatalf("SCHEMA-1175-TABLES: no 1175 ledger table after the schema was made (%v)", err)
		}
		for _, p := range pools {
			p.Close()
		}
		if _, err := admin.Exec(`DROP DATABASE ` + name + ` WITH (FORCE)`); err != nil {
			t.Fatal(err)
		}
	}
	if failed > 0 {
		t.Fatalf("SCHEMA-LOCK: %d of %d schema creations failed with two processes at once", failed, 2*rounds)
	}
	if strings.Contains(logged.String(), "Warning") {
		t.Fatalf("SCHEMA-LOCK-WARNING: a migration failed with two processes at once:\n%s", logged.String())
	}
}

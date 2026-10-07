package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures this launcher shares with forge-solo-migrate, in the main module.
const (
	pgmigrateTestdata = "../../internal/pgmigrate/testdata"
	migstatusTestdata = "../../internal/migstatus/testdata"
)

type planCase struct {
	Name       string            `json:"name"`
	Files      map[string]string `json:"files"`
	Unreadable []string          `json:"unreadable"`
	Want       string            `json:"want"`
}

func planCases(t *testing.T) []planCase {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(pgmigrateTestdata, "plan-cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Cases []planCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Cases
}

// caseContent is a file's content as plan-cases.json describes it.
func caseContent(t *testing.T, spec string) []byte {
	t.Helper()
	switch {
	case strings.HasPrefix(spec, "@marker:"):
		sum := sha256.Sum256(caseContent(t, strings.TrimPrefix(spec, "@marker:")))
		return []byte(`{"pg_control_sha256": "` + hex.EncodeToString(sum[:]) + `", "pg_version": "16", "mode": "move"}`)
	case strings.HasPrefix(spec, "@flip:"):
		b := caseContent(t, "@"+strings.TrimPrefix(spec, "@flip:"))
		b[100] ^= 0xff
		return b
	case strings.HasPrefix(spec, "@"):
		b, err := os.ReadFile(filepath.Join(pgmigrateTestdata, strings.TrimPrefix(spec, "@")))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	return []byte(spec)
}

// layOut makes a case's files under root, its unreadable ones unreadable, and returns the database
// and the old-data paths.
func layOut(t *testing.T, root string, c planCase) (db, pgdata string) {
	t.Helper()
	for name, spec := range c.Files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, caseContent(t, spec), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range c.Unreadable {
		unreadable(t, filepath.Join(root, filepath.FromSlash(name)))
	}
	return filepath.Join(root, "data", "forgesolo.db"), filepath.Join(root, "pgdata")
}

// The launcher decides a start without the migrator only where the files alone tell, and then as
// forge-solo-migrate plan does, case for case of the table both follow. Everything that needs no
// move is decided here: after a move, a migrator that an antivirus holds or removed, an old folder
// half deleted or damaged, or a lost database password never stops a start. A move, a merge, and
// old data the migrator refuses are left to it.
func TestFastPathDecidesAsTheMigrator(t *testing.T) {
	cases := planCases(t)
	if len(cases) < 20 {
		t.Fatalf("FASTPATH-CASES: plan-cases.json has %d cases", len(cases))
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			action, reason, verified := quickPlan(layOut(t, t.TempDir(), c))
			switch c.Want {
			case planNone, planSkipped, planDegraded:
				if action != c.Want {
					t.Fatalf("FASTPATH-DECIDES: %s: the launcher says %q, want %s without the migrator", c.Name, action, c.Want)
				}
			default:
				if action != "" {
					t.Fatalf("FASTPATH-LEAVES: %s: the launcher says %q itself; only the migrator can (%s)", c.Name, action, c.Want)
				}
			}
			if (reason != "") != (action == planDegraded) {
				t.Fatalf("FASTPATH-REASON: %s: %q with reason %q", c.Name, action, reason)
			}
			// verified: a move recorded, and the old data unchanged since.
			if want := c.Want == planNone && c.Files["data/postgres-migrated.json"] != "" && c.Files["pgdata/PG_VERSION"] != ""; verified != want {
				t.Fatalf("FASTPATH-VERIFIED: %s: verified %v, want %v", c.Name, verified, want)
			}
		})
	}
}

// The status file is written as internal/migstatus writes it, byte for byte, in every state: the
// api reads it with that package, and refuses a field it does not know.
func TestStatusFileIsMigstatus(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(migstatusTestdata, "*.json"))
	if err != nil || len(files) != 6 {
		t.Fatalf("STATUS-FIXTURES: %d fixtures (%v), want one per state", len(files), err)
	}
	saved, savedVersion := dataDir, version
	t.Cleanup(func() { dataDir, version = saved, savedVersion })
	for _, f := range files {
		want, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var s migrationStatus
		dec := json.NewDecoder(bytes.NewReader(want))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&s); err != nil {
			t.Fatalf("STATUS-FIELDS: %s: %v", f, err)
		}
		if s.State != strings.TrimSuffix(filepath.Base(f), ".json") {
			t.Fatalf("STATUS-STATE: %s reads as %q", f, s.State)
		}
		dataDir = t.TempDir()
		version = "not-this"
		if err := writeStatus(s); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(statusPath()); !bytes.Equal(got, want) {
			t.Errorf("STATUS-BYTES: written as\n%s\nwant\n%s", got, want)
		}
		if got, ok, err := readStatus(); !ok || err != nil || got != s {
			t.Errorf("STATUS-READ: %+v %v %v", got, ok, err)
		}
	}
	dataDir, version = t.TempDir(), "1.0.13-test"
	if err := writeStatus(migrationStatus{State: stateFailed, Code: 40, Reason: "x"}); err != nil {
		t.Fatal(err)
	}
	if s, _, _ := readStatus(); s.Version != "1.0.13-test" || !strings.HasSuffix(s.At, "Z") || len(s.At) != len("2026-10-04T01:02:08Z") {
		t.Errorf("STATUS-STAMPED: version %q at %q, want this launcher's version and the time in UTC", s.Version, s.At)
	}
	if err := writeStatus(migrationStatus{State: "paused"}); err == nil {
		t.Error("STATUS-STATES: an unknown state was written")
	}
}

// pg_control is read as forge-solo-migrate reads it: a PostgreSQL 16 file whose checksum holds,
// hashed whole.
func TestControlHash(t *testing.T) {
	dir := t.TempDir()
	write := func(b []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, "global"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "global", "pg_control"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := controlHash(dir); !os.IsNotExist(err) {
		t.Fatalf("CONTROL-MISSING: %v", err)
	}
	good := caseContent(t, "@pg_control-shutdown")
	write(good)
	sum := sha256.Sum256(good)
	if h, err := controlHash(dir); err != nil || h != hex.EncodeToString(sum[:]) {
		t.Fatalf("CONTROL-HASH: %q %v", h, err)
	}
	other := append([]byte(nil), good...)
	other[8] = 0x15 // pg_control_version 1301: not PostgreSQL 16
	for _, bad := range [][]byte{caseContent(t, "@flip:pg_control-shutdown"), []byte("garbage"), other} {
		write(bad)
		if _, err := controlHash(dir); err != errControlDamaged {
			t.Fatalf("CONTROL-DAMAGED: read as %v", err)
		}
	}
}

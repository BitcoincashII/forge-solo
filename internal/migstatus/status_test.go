package migstatus

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The fixtures are the file as every writer must write it, one per state: the Windows launcher,
// which cannot import this package, is checked against the same files.
func TestFixtures(t *testing.T) {
	files, err := filepath.Glob("testdata/*.json")
	if err != nil || len(files) != 6 {
		t.Fatalf("MIGSTATUS-FIXTURE: %d fixtures (%v), want one per state", len(files), err)
	}
	for _, f := range files {
		want, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		db := filepath.Join(t.TempDir(), "forgesolo.db")
		if err := os.WriteFile(Path(db), want, 0o600); err != nil {
			t.Fatal(err)
		}
		s, ok, err := Read(db)
		if err != nil || !ok {
			t.Fatalf("MIGSTATUS-FIXTURE: %s does not read: %v", f, err)
		}
		if s.State != strings.TrimSuffix(filepath.Base(f), ".json") {
			t.Errorf("MIGSTATUS-FIXTURE: %s reads as state %q", f, s.State)
		}
		got, err := Encode(s)
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("MIGSTATUS-FIXTURE: %s written again is\n%s\nwant\n%s", f, got, want)
		}
		if err := Write(db, s); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(Path(db)); !bytes.Equal(b, want) {
			t.Errorf("MIGSTATUS-FIXTURE: Write wrote\n%s\nwant\n%s", b, want)
		}
	}
}

func TestWriteAndRead(t *testing.T) {
	db := filepath.Join(t.TempDir(), "forgesolo.db")
	if _, ok, err := Read(db); ok || err != nil {
		t.Fatalf("MIGSTATUS-ABSENT: no file reads as %v %v, want nothing and no error", ok, err)
	}
	want := Status{State: Failed, Code: 10, Reason: "the old database could not be reached", Detail: "dial unix: no such file", Version: "1.0.13"}
	if err := Write(db, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := Read(db)
	if err != nil || !ok {
		t.Fatalf("MIGSTATUS-RW: %v %v", ok, err)
	}
	at, err := time.Parse(time.RFC3339, got.At)
	if err != nil || time.Since(at) > time.Minute || !strings.HasSuffix(got.At, "Z") {
		t.Fatalf("MIGSTATUS-RW: the time written is %q, want now in UTC", got.At)
	}
	got.At = ""
	if got != want {
		t.Fatalf("MIGSTATUS-RW: read %+v, want %+v", got, want)
	}
	// A later status replaces the whole file.
	if err := Write(db, Status{State: Done}); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := Read(db); got.State != Done || got.Code != 0 || got.Reason != "" {
		t.Fatalf("MIGSTATUS-RW: after a second write the file reads %+v", got)
	}
	ents, _ := os.ReadDir(filepath.Dir(db))
	if len(ents) != 1 {
		t.Fatalf("MIGSTATUS-RW: the folder holds %d files after two writes, want the status file alone", len(ents))
	}
	if fi, _ := os.Stat(Path(db)); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("MIGSTATUS-RW: the status file is %v, want 0600", fi.Mode().Perm())
	}
}

// A file that is not a status is an error, never a guess: the api must not run normally on a
// garbled failed status, nor in maintenance on a garbled one.
func TestReadRefusesWhatItDoesNotKnow(t *testing.T) {
	for _, bad := range []string{
		``,
		`{"state": "failed"`,
		`{"state": "paused", "code": 0, "reason": "", "detail": "", "at": "", "version": ""}`,
		`{"state": "done", "extra": 1}`,
	} {
		db := filepath.Join(t.TempDir(), "forgesolo.db")
		if err := os.WriteFile(Path(db), []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if s, ok, err := Read(db); err == nil {
			t.Errorf("MIGSTATUS-BAD: %q read as %+v (%v)", bad, s, ok)
		}
	}
	if err := Write(filepath.Join(t.TempDir(), "forgesolo.db"), Status{State: "paused"}); err == nil {
		t.Error("MIGSTATUS-BAD: an unknown state was written")
	}
}

// Only a failed move keeps the api and the stratum off the database, and a status file that cannot
// be read, which may hide one.
func TestBlocked(t *testing.T) {
	if _, blocked := Blocked(""); blocked {
		t.Error("MIGSTATUS-BLOCKED-NODB: a build without a database file is blocked")
	}
	db := filepath.Join(t.TempDir(), "forgesolo.db")
	if _, blocked := Blocked(db); blocked {
		t.Error("MIGSTATUS-BLOCKED-ABSENT: no status file blocks the database")
	}
	for _, state := range []string{None, Done, Skipped, Deferred, Degraded, Failed} {
		if err := Write(db, Status{State: state, Code: 20, Reason: "why"}); err != nil {
			t.Fatal(err)
		}
		s, blocked := Blocked(db)
		if blocked != (state == Failed) {
			t.Errorf("MIGSTATUS-BLOCKED: a %s move blocks the database: %v", state, blocked)
		}
		if state == Failed && (s.Code != 20 || s.Reason != "why") {
			t.Errorf("MIGSTATUS-BLOCKED-WHY: a failed move reads %+v", s)
		}
	}
	if err := os.WriteFile(Path(db), []byte(`{"state": "done"`), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, blocked := Blocked(db); !blocked || s.State != Failed || s.Reason != "migration-status.json cannot be read" || s.Detail == "" {
		t.Errorf("MIGSTATUS-BLOCKED-GARBLED: a status file that cannot be read gives %+v, blocked %v", s, blocked)
	}
	if got := SkipPath(db); got != filepath.Join(filepath.Dir(db), "SKIP-POSTGRES-MIGRATION") {
		t.Errorf("MIGSTATUS-SKIP: the skip file is %s", got)
	}
}

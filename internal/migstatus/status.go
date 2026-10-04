// Package migstatus is the record of the one-time move of an earlier version's PostgreSQL data into
// forgesolo.db: migration-status.json, beside the database. forge-solo-migrate writes it, and so does
// the Windows launcher when the migrator could not run; the api and the stratum read it when they
// start, and again while a failed move keeps them waiting (Blocked). Linux never has one.
//
// The file is small JSON with fixed fields, so a program in another module (the Windows launcher)
// can write the same bytes; testdata holds one file per state.
package migstatus

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// FileName is the status file's name, beside the database.
const FileName = "migration-status.json"

// SkipName is the user's choice to start without the old data, beside the database: the
// dashboard's button writes it, and so can anyone by hand. While it is there the move is skipped.
const SkipName = "SKIP-POSTGRES-MIGRATION"

// SkipPath is the skip file beside the database at db.
func SkipPath(db string) string { return filepath.Join(filepath.Dir(db), SkipName) }

// The states. None, Done or no file at all: normal. Skipped, Deferred and Degraded: normal, with a
// note on the dashboard. Failed: the move was needed and did not happen, so the api serves the
// maintenance page and the stratum does not mine.
const (
	None     = "none"     // there was nothing to move
	Done     = "done"     // the old data is in forgesolo.db
	Skipped  = "skipped"  // SKIP-POSTGRES-MIGRATION is there: the old data is left out
	Deferred = "deferred" // the database was in use; the next start finishes the move
	Degraded = "degraded" // the old database folder is damaged or partly deleted, and ignored
	Failed   = "failed"   // the move was needed and failed; nothing was replaced
)

// Status is what the file holds. Every field is always written.
type Status struct {
	State   string `json:"state"`
	Code    int    `json:"code"`    // the migrator's exit code for a failure; 0 otherwise
	Reason  string `json:"reason"`  // one plain sentence for the dashboard
	Detail  string `json:"detail"`  // what went wrong, for the log and for support
	At      string `json:"at"`      // when, UTC, RFC 3339
	Version string `json:"version"` // the Forge Solo version that wrote it
}

// Path is the status file beside the database at db.
func Path(db string) string { return filepath.Join(filepath.Dir(db), FileName) }

// Valid reports whether state is one of the states above.
func Valid(state string) bool {
	switch state {
	case None, Done, Skipped, Deferred, Degraded, Failed:
		return true
	}
	return false
}

// Encode is the file's exact content for s.
func Encode(s Status) ([]byte, error) {
	if !Valid(s.State) {
		return nil, fmt.Errorf("migration status: unknown state %q", s.State)
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Write records s beside the database at db, durably: a crash leaves the old file or the new one,
// never half of either. An empty At is filled with the time now.
func Write(db string, s Status) error {
	if s.At == "" {
		s.At = time.Now().UTC().Format(time.RFC3339)
	}
	b, err := Encode(s)
	if err != nil {
		return err
	}
	return WriteDurably(Path(db), b)
}

// Read returns the status beside the database at db. ok is false when there is no file.
func Read(db string) (s Status, ok bool, err error) {
	b, err := os.ReadFile(Path(db))
	if errors.Is(err, os.ErrNotExist) {
		return Status{}, false, nil
	}
	if err != nil {
		return Status{}, false, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return Status{}, false, fmt.Errorf("migration status %s: %w", Path(db), err)
	}
	if !Valid(s.State) {
		return Status{}, false, fmt.Errorf("migration status %s: unknown state %q", Path(db), s.State)
	}
	return s, true, nil
}

// Blocked reports whether the api and the stratum must keep off the database at db: the move of
// the earlier version's data was needed and failed, so a database opened now would start empty
// in its place. A status file that cannot be read counts as failed, since it may hide a failure;
// s then says so. db "" (a build without a database file) is never blocked.
func Blocked(db string) (s Status, blocked bool) {
	if db == "" {
		return Status{}, false
	}
	s, ok, err := Read(db)
	switch {
	case err != nil:
		return Status{State: Failed, Reason: FileName + " cannot be read", Detail: err.Error()}, true
	case !ok:
		return Status{}, false
	}
	return s, s.State == Failed
}

// WriteDurably puts data at path through a file beside it that is synced and then renamed over
// path, and syncs the folder, so a crash or a power cut leaves either the old content or the new.
// The file is 0600.
func WriteDurably(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return SyncDir(filepath.Dir(path))
}

// SyncDir makes a rename or a new file in dir durable. Windows has no such call for a folder, and
// its renames are durable once they return.
func SyncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}

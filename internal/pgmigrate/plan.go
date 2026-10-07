package pgmigrate

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BitcoincashII/forge-solo/internal/migstatus"
)

// The files beside forgesolo.db that the move reads and writes.
const (
	// MarkerName records a finished move: the hash of the old data's pg_control as the move left
	// it, so a later start can tell, without starting PostgreSQL, whether 1.0.12 ran on it since.
	MarkerName = "postgres-migrated.json"
	// SkipName is the user's choice to start without the old data: the dashboard's button writes
	// it, and so can anyone by hand.
	SkipName = migstatus.SkipName
)

// MarkerPath and SkipPath are those files beside the database at db.
func MarkerPath(db string) string { return filepath.Join(filepath.Dir(db), MarkerName) }
func SkipPath(db string) string   { return migstatus.SkipPath(db) }

// Marker is the content of postgres-migrated.json.
type Marker struct {
	PGControlSHA256 string           `json:"pg_control_sha256"`
	PGVersion       string           `json:"pg_version"`
	Mode            string           `json:"mode"`
	Counts          map[string]int64 `json:"counts"`
	MigratedAt      string           `json:"migrated_at"`
	Version         string           `json:"version"`
}

// What a start does about the old data.
const (
	ActionNone     = "none"     // nothing to move, or moved and unchanged since
	ActionMove     = "move"     // move the old data into a new forgesolo.db
	ActionMerge    = "merge"    // the old data and forgesolo.db both hold data: merge them
	ActionSkipped  = "skipped"  // the user chose to start without the old data
	ActionDegraded = "degraded" // moved before, but the old folder is damaged: start, ignore it, warn
	ActionFailed   = "failed"   // a move is needed and cannot be done: see the reason
)

// Decision is Plan's answer.
type Decision struct {
	Action string
	Reason string // for degraded and failed
}

func (d Decision) String() string {
	if d.Reason != "" {
		return d.Action + ":" + d.Reason
	}
	return d.Action
}

// Plan decides what a start does about the old PostgreSQL data in pgdata, for the database at db.
// It is the one table both platforms follow (testdata/plan-cases.json holds its cases, which the
// Windows launcher's own tests run too):
//  1. SKIP-POSTGRES-MIGRATION beside the database: skipped.
//  2. No pgdata/PG_VERSION: none (a fresh install, or the old folder deleted).
//  3. forgesolo.db and the marker both there:
//     - the old folder damaged or partly deleted (PG_VERSION unreadable or not 16, pg_control
//     missing, unreadable or damaged, or any error reading them): degraded, never an error, so
//     an old folder can never stop a start after a move;
//     - pg_control's hash equal to the marker's: none;
//     - otherwise (1.0.12 ran on the old data since): merge.
//  4. PG_VERSION unreadable or not 16: failed.
//  5. No forgesolo.db: move.
//  6. forgesolo.db without a marker (a skip taken back, a move cut short after its rename, or a
//     database the services made empty): merge.
func Plan(db, pgdata string) Decision {
	if _, err := os.Lstat(SkipPath(db)); err == nil {
		return Decision{Action: ActionSkipped}
	}
	ver, verErr := os.ReadFile(filepath.Join(pgdata, "PG_VERSION"))
	if errors.Is(verErr, fs.ErrNotExist) {
		return Decision{Action: ActionNone}
	}
	dbThere, dbErr := exists(db)
	markerThere, markerErr := exists(MarkerPath(db))
	if dbErr != nil || markerErr != nil {
		if markerThere || markerErr != nil {
			return Decision{Action: ActionDegraded, Reason: "the database folder could not be read"}
		}
		return Decision{Action: ActionFailed, Reason: "the database folder could not be read"}
	}
	if dbThere && markerThere {
		switch {
		case verErr != nil:
			return Decision{Action: ActionDegraded, Reason: "the old database folder cannot be read"}
		case strings.TrimSpace(string(ver)) != "16":
			return Decision{Action: ActionDegraded, Reason: "the old database folder's PG_VERSION is damaged"}
		}
		ctl, err := ReadControl(pgdata)
		switch {
		case errors.Is(err, ErrControlMissing):
			return Decision{Action: ActionDegraded, Reason: "the old database's pg_control is missing"}
		case errors.Is(err, ErrControlDamaged):
			return Decision{Action: ActionDegraded, Reason: "the old database's pg_control is damaged"}
		case err != nil:
			return Decision{Action: ActionDegraded, Reason: "the old database's pg_control cannot be read"}
		}
		m, err := readMarker(db)
		if err != nil && !errors.As(err, new(*json.SyntaxError)) && !errors.As(err, new(*json.UnmarshalTypeError)) {
			return Decision{Action: ActionDegraded, Reason: "postgres-migrated.json cannot be read"}
		}
		if m.PGControlSHA256 == ctl.SHA256 {
			return Decision{Action: ActionNone}
		}
		return Decision{Action: ActionMerge}
	}
	switch {
	case verErr != nil:
		return Decision{Action: ActionFailed, Reason: "the old database folder cannot be read"}
	case strings.TrimSpace(string(ver)) != "16":
		v := strings.TrimSpace(string(ver))
		if len(v) > 16 {
			v = v[:16]
		}
		return Decision{Action: ActionFailed, Reason: "the old database is not PostgreSQL 16 (its PG_VERSION says " + v + ")"}
	case !dbThere:
		return Decision{Action: ActionMove}
	}
	return Decision{Action: ActionMerge}
}

// exists reports whether path is there; an error other than its absence is returned.
func exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	}
	return false, err
}

func readMarker(db string) (Marker, error) {
	var m Marker
	b, err := os.ReadFile(MarkerPath(db))
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(b, &m)
	return m, err
}

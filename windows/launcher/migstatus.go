package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// migration-status.json, beside forgesolo.db, says what became of an earlier version's PostgreSQL
// data at this start. forge-solo-migrate.exe writes it when it runs; the launcher writes it when it
// decides without the migrator, or when the migrator could not run. The api reads it before it opens
// the database: on "failed" it serves the dashboard's maintenance page, and the miner does not mine.
// It is the file internal/migstatus writes on Umbrel, byte for byte: the launcher is a module of
// its own and cannot use that package, so its tests check this against the package's fixtures.

// The states, as internal/migstatus names them.
const (
	stateNone     = "none"     // there was nothing to move
	stateDone     = "done"     // the old data is in forgesolo.db
	stateSkipped  = "skipped"  // SKIP-POSTGRES-MIGRATION is there: the old data is left out
	stateDeferred = "deferred" // the database was in use; the next start finishes the move
	stateDegraded = "degraded" // the old database folder is damaged or partly deleted, and ignored
	stateFailed   = "failed"   // the move was needed and failed; nothing was replaced
)

// migrationStatus is the file's content. Every field is always written, in this order.
type migrationStatus struct {
	State   string `json:"state"`
	Code    int    `json:"code"`
	Reason  string `json:"reason"`
	Detail  string `json:"detail"`
	At      string `json:"at"`
	Version string `json:"version"`
}

func statusPath() string { return dpath("migration-status.json") }

// encodeStatus is the file's exact content for s.
func encodeStatus(s migrationStatus) ([]byte, error) {
	switch s.State {
	case stateNone, stateDone, stateSkipped, stateDeferred, stateDegraded, stateFailed:
	default:
		return nil, fmt.Errorf("migration status: unknown state %q", s.State)
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// writeStatus records s, durably: a crash leaves the old file or the new one. An empty At is the
// time now, and the version is this launcher's.
func writeStatus(s migrationStatus) error {
	if s.At == "" {
		s.At = time.Now().UTC().Format(time.RFC3339)
	}
	if s.Version == "" {
		s.Version = version
	}
	b, err := encodeStatus(s)
	if err != nil {
		return err
	}
	return writeDurably(statusPath(), string(b))
}

// readStatus is the status file's content; ok is false when there is none.
func readStatus() (s migrationStatus, ok bool, err error) {
	b, err := os.ReadFile(statusPath())
	if errors.Is(err, os.ErrNotExist) {
		return s, false, nil
	}
	if err != nil {
		return s, false, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return migrationStatus{}, false, err
	}
	return s, true, nil
}

// recordStatus writes s and logs it, and what went wrong if it could not be written.
func recordStatus(s migrationStatus) {
	line := "old data: status " + s.State
	if s.Reason != "" {
		line += ": " + s.Reason
	}
	logf("%s", line)
	if err := writeStatus(s); err != nil {
		logf("old data: %s could not be written: %v", statusPath(), err)
	}
}

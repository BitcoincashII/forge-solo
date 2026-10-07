package pgmigrate

import (
	"context"
	"errors"
)

// The migrator's exit codes. The Windows launcher and the status file use the same numbers.
const (
	CodeOK          = 0
	CodeInterrupted = 3  // stopped by a signal: nothing was committed
	CodeSource      = 10 // the old database could not be reached, or refused the login
	CodeVerify      = 20 // the copy did not check out
	CodeWrite       = 21 // the new database could not be written: a disk or file error
	CodeRefused     = 30 // the move would not be safe: see the reason
	CodeDeferred    = 31 // the database is in use: the next start finishes the move
	CodeOther       = 40
	CodeStatus      = 41 // run only: the status file could not be written
)

// Error is a failure with its exit code and a plain sentence for the dashboard.
type Error struct {
	Code   int
	Reason string
	Err    error
}

func (e *Error) Error() string {
	if e.Err == nil {
		return e.Reason
	}
	return e.Reason + ": " + e.Err.Error()
}

func (e *Error) Unwrap() error { return e.Err }

func newErr(code int, reason string, err error) *Error {
	return &Error{Code: code, Reason: reason, Err: err}
}

func refused(reason string, err error) *Error { return newErr(CodeRefused, reason, err) }

func deferred(err error) *Error {
	return newErr(CodeDeferred, "the database is in use; the move finishes at the next start", err)
}

// CodeOf is the exit code for err: 0 for nil, 3 for a cancelled context, the code an *Error
// carries, and 40 for anything else.
func CodeOf(err error) int {
	var e *Error
	switch {
	case err == nil:
		return CodeOK
	case errors.As(err, &e):
		return e.Code
	case errors.Is(err, context.Canceled):
		return CodeInterrupted
	}
	return CodeOther
}

// ReasonOf is the plain sentence for err.
func ReasonOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Reason
	}
	if errors.Is(err, context.Canceled) {
		return "the move was interrupted; nothing was replaced"
	}
	return "the move failed"
}

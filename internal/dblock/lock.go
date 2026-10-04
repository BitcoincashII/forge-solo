// Package dblock is the in-use lock beside a SQLite database file. Every process that opens the
// database holds a shared lock on <db>.inuse for as long as it runs; a process that replaces the
// database file takes it exclusively first, so it never swaps the file under a process that has
// it open, and a process that starts during the swap waits and then opens the new file.
//
// The lock is the operating system's own (flock on Unix, LockFileEx on Windows) and belongs to
// the open file: it ends when the file is closed or the process ends, so a crash never leaves it
// held. Two locks taken in one process through two opens behave as two processes would.
package dblock

import (
	"errors"
	"os"
	"time"
)

// ErrBusy means another holder has the lock in a way that conflicts with the one asked for.
var ErrBusy = errors.New("the database is in use")

// Path is the lock file for the database at db.
func Path(db string) string { return db + ".inuse" }

// pollEvery is how often a waiting Shared tries again.
const pollEvery = 50 * time.Millisecond

// Lock is a held lock. Release ends it.
type Lock struct {
	f *os.File
}

// Shared takes a shared lock on the lock file at path, creating the file (0600) when it is not
// there. Any number of shared locks are held at once. While the lock is held exclusively, Shared
// tries again until wait has passed and then returns ErrBusy; a wait of 0 tries once.
func Shared(path string, wait time.Duration) (*Lock, error) {
	f, err := open(path)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		err = lockShared(f)
		if err == nil {
			return &Lock{f: f}, nil
		}
		left := time.Until(deadline)
		if !errors.Is(err, ErrBusy) || left <= 0 {
			f.Close()
			return nil, err
		}
		time.Sleep(min(pollEvery, left))
	}
}

// TryExclusive takes the lock on the lock file at path exclusively, without waiting. It returns
// ErrBusy while anyone else holds it, shared or exclusive.
func TryExclusive(path string) (*Lock, error) {
	f, err := open(path)
	if err != nil {
		return nil, err
	}
	if err := lockExclusive(f); err != nil {
		f.Close()
		return nil, err
	}
	return &Lock{f: f}, nil
}

// Release ends the lock. The lock file stays: it is never renamed or deleted, so every process
// locks the same file.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := unlock(l.f)
	if cerr := l.f.Close(); err == nil {
		err = cerr
	}
	l.f = nil
	return err
}

// open opens the lock file for reading, which is all either lock needs, so a lock file another
// account created still locks.
func open(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|os.O_CREATE, 0o600)
}

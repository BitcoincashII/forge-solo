//go:build windows

package dblock

import (
	"os"

	"golang.org/x/sys/windows"
)

// Both locks are on byte 0 of the lock file. A shared lock there lets other shared locks in and
// keeps an exclusive one out; LOCKFILE_FAIL_IMMEDIATELY makes a conflict an error, not a wait.
func lockShared(f *os.File) error {
	return lockFile(f, windows.LOCKFILE_FAIL_IMMEDIATELY)
}

func lockExclusive(f *os.File) error {
	return lockFile(f, windows.LOCKFILE_FAIL_IMMEDIATELY|windows.LOCKFILE_EXCLUSIVE_LOCK)
}

func lockFile(f *os.File, flags uint32) error {
	err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, new(windows.Overlapped))
	switch err {
	case nil:
		return nil
	case windows.ERROR_LOCK_VIOLATION:
		return ErrBusy
	default:
		return &os.PathError{Op: "LockFileEx", Path: f.Name(), Err: err}
	}
}

// unlock ends the lock at once; closing the file would also end it, but Windows may take a
// moment to do that.
func unlock(f *os.File) error {
	if err := windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, new(windows.Overlapped)); err != nil {
		return &os.PathError{Op: "UnlockFileEx", Path: f.Name(), Err: err}
	}
	return nil
}

//go:build unix

package dblock

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockShared(f *os.File) error    { return flock(f, unix.LOCK_SH|unix.LOCK_NB) }
func lockExclusive(f *os.File) error { return flock(f, unix.LOCK_EX|unix.LOCK_NB) }
func unlock(f *os.File) error        { return flock(f, unix.LOCK_UN) }

func flock(f *os.File, how int) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var ferr error
	if err := rc.Control(func(fd uintptr) {
		for {
			if ferr = unix.Flock(int(fd), how); ferr != unix.EINTR {
				return
			}
		}
	}); err != nil {
		return err
	}
	switch ferr {
	case nil:
		return nil
	case unix.EWOULDBLOCK:
		return ErrBusy
	default:
		return &os.PathError{Op: "flock", Path: f.Name(), Err: ferr}
	}
}

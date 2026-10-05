package main

import (
	"os"
	"sync"
	"testing"

	"golang.org/x/sys/windows"
)

// holdOnWindows opens path as a log viewer does, sharing read and write access, and delete access
// too if shareDelete is set, until the test ends or the returned func is called.
func holdOnWindows(t *testing.T, path string, shareDelete bool) func() {
	t.Helper()
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	share := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE)
	if shareDelete {
		share |= windows.FILE_SHARE_DELETE
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, share, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatalf("LOG-WIN-HOLD: %s cannot be opened as a viewer would: %v", path, err)
	}
	var once sync.Once
	release := func() { once.Do(func() { _ = windows.CloseHandle(h) }) }
	t.Cleanup(release)
	return release
}

// The cases the tests with the stand-in for Windows cover, with real viewers' handles and a real
// read-only file: what the stand-in takes Windows to do is checked here.
func TestHeldLogOnWindows(t *testing.T) {
	t.Run("viewer without delete sharing", func(t *testing.T) {
		l := heldTestLog(t)
		writeLines(l, 20, heldLine)
		release := holdOnWindows(t, l.path, false)
		writeLines(l, 7, heldLine)
		if !olderKept(l) {
			t.Fatalf("LOG-WIN-HELD-OLD: stratum.log.1 is gone or changed though stratum.log could not be moved (%d bytes)", sizeOf(l.path+".1"))
		}
		if got := sizeOf(l.path); got != 27*40 {
			t.Fatalf("LOG-WIN-HELD-LINES: stratum.log has %d bytes, want all %d", got, 27*40)
		}
		if _, err := os.Stat(l.path + ".rotating"); !os.IsNotExist(err) {
			t.Fatalf("LOG-WIN-HELD-STRAY: stratum.log.rotating is left over (%v)", err)
		}
		release()
		writeLines(l, 3, heldLine)
		if old, cur := sizeOf(l.path+".1"), sizeOf(l.path); old != 27*40 || cur != 3*40 {
			t.Fatalf("LOG-WIN-HELD-RELEASED: once the viewer let go, stratum.log.1 has %d bytes and stratum.log %d; want %d and %d", old, cur, 27*40, 3*40)
		}
	})

	t.Run("read-only log before", func(t *testing.T) {
		l := heldTestLog(t)
		if err := os.Chmod(l.path+".1", 0o444); err != nil {
			t.Fatal(err)
		}
		writeLines(l, 30, heldLine)
		if old, cur := sizeOf(l.path+".1"), sizeOf(l.path); old != 25*40 || cur != 5*40 {
			t.Fatalf("LOG-WIN-READONLY: with a read-only stratum.log.1, it has %d bytes and stratum.log %d; want %d and %d", old, cur, 25*40, 5*40)
		}
	})

	t.Run("viewer sharing delete", func(t *testing.T) {
		l := heldTestLog(t)
		writeLines(l, 1, heldLine)
		holdOnWindows(t, l.path, true)
		writeLines(l, 54, heldLine)
		if old, cur := sizeOf(l.path+".1"), sizeOf(l.path); old != 25*40 || cur != 5*40 {
			t.Fatalf("LOG-WIN-POLITE: with a viewer on stratum.log.1 that shares delete, it has %d bytes and stratum.log %d; want %d and %d", old, cur, 25*40, 5*40)
		}
	})

	t.Run("viewer on the log before", func(t *testing.T) {
		l := heldTestLog(t)
		release := holdOnWindows(t, l.path+".1", false)
		writeLines(l, 30, heldLine)
		if !olderKept(l) {
			t.Fatalf("LOG-WIN-ROLLBACK-OLD: a held stratum.log.1 is gone or changed (%d bytes)", sizeOf(l.path+".1"))
		}
		if got := sizeOf(l.path); got != 30*40 {
			t.Fatalf("LOG-WIN-ROLLBACK-LINES: stratum.log has %d bytes, want all %d", got, 30*40)
		}
		if _, err := os.Stat(l.path + ".rotating"); !os.IsNotExist(err) {
			t.Fatalf("LOG-WIN-ROLLBACK-STRAY: stratum.log.rotating is left over (%v)", err)
		}
		release()
		writeLines(l, 3, heldLine)
		old, cur := sizeOf(l.path+".1"), sizeOf(l.path)
		if olderKept(l) || old < 30*40 || old+cur != 33*40 {
			t.Fatalf("LOG-WIN-ROLLBACK-RELEASED: once the viewer let go, stratum.log.1 has %d bytes and stratum.log %d; want the %d before moved aside and no line lost", old, cur, 30*40)
		}
	})
}

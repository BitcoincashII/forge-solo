package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// winFiles stands in for what Windows does to a rename or a delete of a file that a log viewer
// has open, so that the tests show it on Linux. A file held without delete sharing (a .NET
// FileShare.ReadWrite reader, or Go's os.Open) can be neither renamed nor deleted. One held with
// it can be renamed, the viewer following it, and deleted. A rename does not replace a held file,
// whatever its sharing, nor a read-only one, which os.Remove deletes.
type winFiles struct {
	held     map[string]bool // whether the viewer holding the path shares delete access
	readOnly map[string]bool
	tries    map[string]int // renames asked of each path
}

var (
	errInUse  = errors.New("the file is being used by another process")
	errDenied = errors.New("access is denied")
)

// useWinFiles puts the stand-in in place of os.Rename and os.Remove for the test.
func useWinFiles(t *testing.T) *winFiles {
	t.Helper()
	w := &winFiles{held: map[string]bool{}, readOnly: map[string]bool{}, tries: map[string]int{}}
	savedRename, savedRemove := renameFile, removeFile
	renameFile, removeFile = w.rename, w.remove
	t.Cleanup(func() { renameFile, removeFile = savedRename, savedRemove })
	return w
}

func (w *winFiles) rename(from, to string) error {
	w.tries[from]++
	if shares, ok := w.held[from]; ok && !shares {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: errInUse}
	}
	if _, err := os.Stat(to); err == nil {
		if _, ok := w.held[to]; ok || w.readOnly[to] {
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: errDenied}
		}
	}
	if err := os.Rename(from, to); err != nil {
		return err
	}
	delete(w.held, to)
	delete(w.readOnly, to)
	if shares, ok := w.held[from]; ok {
		w.held[to] = shares
		delete(w.held, from)
	}
	if w.readOnly[from] {
		w.readOnly[to] = true
		delete(w.readOnly, from)
	}
	return nil
}

func (w *winFiles) remove(name string) error {
	if shares, ok := w.held[name]; ok && !shares {
		return &os.PathError{Op: "remove", Path: name, Err: errInUse}
	}
	if err := os.Remove(name); err != nil {
		return err
	}
	// The name is free at once; a viewer reads on from the deleted file.
	delete(w.held, name)
	delete(w.readOnly, name)
	return nil
}

const heldLimit = 1000

var heldLine = []byte(strings.Repeat("x", 39) + "\n") // 40 bytes

// heldTestLog is a service log with a 1000-byte limit, and a stratum.log.1 from before.
func heldTestLog(t *testing.T) *cappedLog {
	t.Helper()
	l := &cappedLog{path: filepath.Join(t.TempDir(), "stratum.log"), limit: heldLimit}
	t.Cleanup(func() {
		if l.f != nil {
			_ = l.f.Close()
		}
	})
	if err := os.WriteFile(l.path+".1", []byte("older\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return l
}

func writeLines(l *cappedLog, n int, line []byte) {
	for i := 0; i < n; i++ {
		_, _ = l.Write(line)
	}
}

func sizeOf(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return st.Size()
}

func olderKept(l *cappedLog) bool {
	b, err := os.ReadFile(l.path + ".1")
	return err == nil && string(b) == "older\n"
}

// A viewer holding the log without delete sharing keeps Windows from moving it aside. The log
// before stays in .1, the log keeps every line, and it is moved aside soon after the viewer lets
// go, not a whole limit later. The move after that is made at the limit again.
func TestServiceLogHeldByAViewer(t *testing.T) {
	w := useWinFiles(t)
	l := heldTestLog(t)
	writeLines(l, 20, heldLine) // 800 bytes
	w.held[l.path] = false
	writeLines(l, 7, heldLine) // the sixth passes the limit
	if !olderKept(l) {
		t.Fatalf("LOG-HELD-OLD: stratum.log.1 is gone or changed though stratum.log could not be moved (%d bytes)", sizeOf(l.path+".1"))
	}
	if got := sizeOf(l.path); got != 27*40 {
		t.Fatalf("LOG-HELD-LINES: stratum.log has %d bytes, want all %d", got, 27*40)
	}
	if _, err := os.Stat(l.path + ".rotating"); !os.IsNotExist(err) {
		t.Fatalf("LOG-HELD-STRAY: stratum.log.rotating is left over (%v)", err)
	}
	delete(w.held, l.path)
	writeLines(l, 3, heldLine)
	if old, cur := sizeOf(l.path+".1"), sizeOf(l.path); old != 27*40 || cur != 3*40 {
		t.Fatalf("LOG-HELD-RELEASED: once the viewer let go, stratum.log.1 has %d bytes and stratum.log %d; want the %d held and the %d after", old, cur, 27*40, 3*40)
	}
	most := int64(0)
	for i := 0; i < 40; i++ {
		_, _ = l.Write(heldLine)
		most = max(most, sizeOf(l.path))
	}
	if most > heldLimit {
		t.Fatalf("LOG-HELD-NEXT: after a move held up by a viewer, stratum.log grew to %d bytes before the next; want at most the limit, %d", most, heldLimit)
	}
}

// Each try closes and reopens the log. While a viewer holds it, it is tried again once the log has
// grown by a twentieth of its limit, not at every line.
func TestServiceLogHeldIsNotTriedAtEveryLine(t *testing.T) {
	w := useWinFiles(t)
	l := heldTestLog(t)
	writeLines(l, 20, heldLine) // 800 bytes
	w.held[l.path] = false
	short := []byte("123456789\n")
	writeLines(l, 400, short)
	past := 800 + 400*int64(len(short)) - heldLimit
	most := int(past/(heldLimit/20)) + 1
	if got := w.tries[l.path]; got > most || got < 2 {
		t.Fatalf("LOG-HELD-TRIES: stratum.log was tried %d times in %d bytes past the limit; want 2 to %d", got, past, most)
	}
}

// A read-only stratum.log.1 cannot be replaced by a rename on Windows, but can be deleted first.
// Without that, the log would never be moved aside again.
func TestServiceLogReplacesAReadOnlyOne(t *testing.T) {
	w := useWinFiles(t)
	l := heldTestLog(t)
	w.readOnly[l.path+".1"] = true
	writeLines(l, 30, heldLine) // moved aside at the 26th
	if old, cur := sizeOf(l.path+".1"), sizeOf(l.path); old != 25*40 || cur != 5*40 {
		t.Fatalf("LOG-READONLY-OLD: with a read-only stratum.log.1, it has %d bytes and stratum.log %d; want %d and %d", old, cur, 25*40, 5*40)
	}
}

// A viewer that shares delete access follows the log to stratum.log.1 when it is moved aside. The
// next move must still replace it.
func TestServiceLogWithAViewerSharingDelete(t *testing.T) {
	w := useWinFiles(t)
	l := heldTestLog(t)
	writeLines(l, 1, heldLine)
	w.held[l.path] = true
	writeLines(l, 25, heldLine) // moved aside at the 26th
	if _, ok := w.held[l.path+".1"]; !ok {
		t.Fatal("LOG-POLITE-SETUP: the viewer did not follow the log to stratum.log.1")
	}
	writeLines(l, 29, heldLine) // and at the 51st
	if old, cur := sizeOf(l.path+".1"), sizeOf(l.path); old != 25*40 || cur != 5*40 {
		t.Fatalf("LOG-POLITE-VIEWER: with a viewer on stratum.log.1 that shares delete, it has %d bytes and stratum.log %d; want %d and %d", old, cur, 25*40, 5*40)
	}
}

// A viewer holding stratum.log.1 without delete sharing keeps it from being replaced: the log is
// put back with every line, and moved aside once the viewer lets go.
func TestServiceLogIsPutBackWhenTheOneBeforeIsHeld(t *testing.T) {
	w := useWinFiles(t)
	l := heldTestLog(t)
	w.held[l.path+".1"] = false
	writeLines(l, 30, heldLine)
	if !olderKept(l) {
		t.Fatalf("LOG-ROLLBACK-OLD: a held stratum.log.1 is gone or changed (%d bytes)", sizeOf(l.path+".1"))
	}
	if got := sizeOf(l.path); got != 30*40 {
		t.Fatalf("LOG-ROLLBACK-LINES: stratum.log has %d bytes, want all %d", got, 30*40)
	}
	if _, err := os.Stat(l.path + ".rotating"); !os.IsNotExist(err) {
		t.Fatalf("LOG-ROLLBACK-STRAY: stratum.log.rotating is left over (%v)", err)
	}
	delete(w.held, l.path+".1")
	writeLines(l, 3, heldLine)
	old, cur := sizeOf(l.path+".1"), sizeOf(l.path)
	if olderKept(l) || old < 30*40 || old+cur != 33*40 {
		t.Fatalf("LOG-ROLLBACK-RELEASED: once the viewer let go, stratum.log.1 has %d bytes and stratum.log %d; want the %d before moved aside and no line lost", old, cur, 30*40)
	}
}

// Each try while a viewer holds stratum.log.1 renames the log twice and reopens it. It is tried
// again once the log has grown by a twentieth of its limit, not at every line.
func TestServiceLogPutBackIsNotTriedAtEveryLine(t *testing.T) {
	w := useWinFiles(t)
	l := heldTestLog(t)
	w.held[l.path+".1"] = false
	writeLines(l, 20, heldLine) // 800 bytes
	short := []byte("123456789\n")
	writeLines(l, 400, short)
	past := 800 + 400*int64(len(short)) - heldLimit
	most := int(past/(heldLimit/20)) + 1
	if got := w.tries[l.path]; got > most || got < 2 {
		t.Fatalf("LOG-ROLLBACK-TRIES: with stratum.log.1 held, stratum.log was moved and put back %d times in %d bytes past the limit; want 2 to %d", got, past, most)
	}
}

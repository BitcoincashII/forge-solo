package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The log file never grows past the limit: the old part moves to .1 and the newest lines stay in
// the file itself.
func TestLogFileIsCapped(t *testing.T) {
	saved := logFileLimit
	logFileLimit = 1000
	t.Cleanup(func() { logFileLimit = saved })
	path := filepath.Join(t.TempDir(), "forge-gateway.log")
	r, err := openRotating(path)
	if err != nil {
		t.Fatal(err)
	}
	// Closed before the TempDir is removed (cleanups run last first): Windows does not delete a
	// file that is open.
	t.Cleanup(func() { r.f.Close() })
	line := bytes.Repeat([]byte("x"), 99)
	for i := 0; i < 50; i++ {
		if _, err := r.Write(append(line, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Write([]byte("last line\n")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, path + ".1"} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.Size() > logFileLimit {
			t.Errorf("%s is %d bytes, over the %d limit", filepath.Base(p), st.Size(), logFileLimit)
		}
	}
	if b, _ := os.ReadFile(path); !bytes.HasSuffix(b, []byte("last line\n")) {
		t.Error("the newest line is not at the end of the log file")
	}
	// Reopened, it picks up the size already there rather than starting the count from nothing.
	r2, err := openRotating(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r2.f.Close() })
	if st, _ := os.Stat(path); r2.size != st.Size() {
		t.Errorf("reopened size %d, file is %d", r2.size, st.Size())
	}
}

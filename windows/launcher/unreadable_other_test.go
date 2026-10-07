//go:build !windows

package main

import (
	"os"
	"testing"
)

// unreadable keeps p from being read until the test ends: mode 0000. Root reads a file whatever
// its mode, so as root the test skips.
func unreadable(t *testing.T, p string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root reads a file whatever its mode: an unreadable file cannot be made as root")
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(p, fi.Mode().Perm()) })
}

//go:build windows

package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// The code page check never takes a look-alike for the name itself: on a Western code page (1252)
// an e acute is held and a Polish l is not.
func TestCodePageHoldsOnWindows(t *testing.T) {
	if !codePageHolds("ForgeSolo") {
		t.Fatal("PGPATH-WINDOWS-ACP: plain ASCII is not held")
	}
	if acp := windows.GetACP(); acp != 1252 {
		t.Skipf("code page %d: the 1252 cases do not apply", acp)
	}
	if !codePageHolds(jose) || codePageHolds(pawel) {
		t.Fatalf("PGPATH-WINDOWS-ACP: code page 1252 holds %q: %v, %q: %v; want true, false", jose, codePageHolds(jose), pawel, codePageHolds(pawel))
	}
}

// The short name of a folder is read as the drive keeps it: ASCII, or none when the drive keeps
// no short names.
func TestShortNameOnWindows(t *testing.T) {
	dir := filepath.Join(t.TempDir(), pawel)
	md(dir)
	s, err := shortName(dir)
	if err != nil || (s != "" && !isASCII(s)) {
		t.Fatalf("PGPATH-WINDOWS-SHORT: the short name of %q is %q (%v)", dir, s, err)
	}
	t.Logf("short name of %q: %q", pawel, s)
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		t := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(t, 0o755)
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(t)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

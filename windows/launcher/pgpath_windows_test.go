//go:build windows

package main

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// On Windows, with the real bundled PostgreSQL (FS_PGSQL: an installed pgsql folder), from an
// install folder and a data folder whose names have characters outside the code page, as under a
// user name in another script: the database starts, and Quit's stop finds it as this install's.
func TestPostgresRunsFromAFolderOutsideTheCodePage(t *testing.T) {
	src := os.Getenv("FS_PGSQL")
	if src == "" {
		t.Skip("FS_PGSQL (an installed pgsql folder) is not set")
	}
	cn := string([]rune{0x6D4B, 0x8BD5})
	root := filepath.Join(t.TempDir(), "fs-"+cn)
	if err := copyTree(src, filepath.Join(root, "pgsql")); err != nil {
		t.Fatal(err)
	}
	savedInst, savedData, savedSec, savedPort := installDir, dataDir, sec, pgPort
	installDir, dataDir = root, filepath.Join(root, "data-"+cn)
	sec = secrets{DBPass: "test-password"}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	_, pgPort, _ = net.SplitHostPort(l.Addr().String())
	l.Close()
	md(dataDir)
	t.Cleanup(func() {
		stopDatabase()
		installDir, dataDir, sec, pgPort = savedInst, savedData, savedSec, savedPort
	})

	if !startPostgres() {
		b, _ := os.ReadFile(filepath.Join(dataDir, "pglog.txt"))
		t.Fatalf("PGPATH-WINDOWS-START: the database did not start from %s:\n%s", root, b)
	}
	pid := postmasterPID()
	if pid == 0 || !runs(installedPrograms(), pid, "postgres.exe") {
		t.Fatalf("PGPATH-WINDOWS-OURS: postmaster %d is not found as this install's database", pid)
	}
	stopDatabase()
	for end := time.Now().Add(10 * time.Second); postmasterPID() != 0 && time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
	}
	if postmasterPID() != 0 {
		t.Fatal("PGPATH-WINDOWS-STOP: Quit's stop left the database running")
	}
	b, _ := os.ReadFile(filepath.Join(dataDir, "launcher.log"))
	if strings.Contains(string(b), "not this install's database") {
		t.Fatalf("PGPATH-WINDOWS-STOP: the stop did not take the database for this install's:\n%s", b)
	}
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

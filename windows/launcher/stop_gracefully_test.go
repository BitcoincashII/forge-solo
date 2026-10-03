package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestHelperProcess is the child the tests below start. FS_HELPER=eof: exit 0 as soon as stdin
// closes (as the stratum does with FORGE_STOP_ON_STDIN_EOF=1), or FS_HELPER_DELAY after, leaving
// a marker that it did. FS_HELPER=stuck: ignore stdin and write a heartbeat until killed.
func TestHelperProcess(t *testing.T) {
	dir := os.Getenv("FS_HELPER_DIR")
	switch os.Getenv("FS_HELPER") {
	case "eof":
		_, _ = io.Copy(io.Discard, os.Stdin)
		if d, err := time.ParseDuration(os.Getenv("FS_HELPER_DELAY")); err == nil {
			time.Sleep(d)
		}
		_ = os.WriteFile(filepath.Join(dir, "clean"), []byte("1"), 0o600)
		os.Exit(0)
	case "stuck":
		for i := 0; ; i++ {
			_ = os.WriteFile(filepath.Join(dir, "beat"), []byte(strconv.Itoa(i)), 0o600)
			time.Sleep(100 * time.Millisecond)
		}
	}
}

func startHelper(t *testing.T, key, mode, dir string, env ...string) {
	t.Helper()
	c := exec.Command(os.Args[0], "-test.run=TestHelperProcess")
	c.Env = append(append(os.Environ(), "FS_HELPER="+mode, "FS_HELPER_DIR="+dir), env...)
	w, err := c.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	stdins[key] = w
	mu.Unlock()
	if err := run(key, c); err != nil {
		t.Fatal(err)
	}
}

// The stratum is asked to stop by closing its stdin and must be left to exit by itself -- it
// sends its queued TIDES shares on the way -- rather than killed, as it was until now.
func TestStopGracefullyLetsTheProcessExit(t *testing.T) {
	dir := t.TempDir()
	startHelper(t, "helper-eof", "eof", dir)
	time.Sleep(500 * time.Millisecond) // let it start reading stdin
	start := time.Now()
	stopGracefully("helper-eof", 10*time.Second)
	if took := time.Since(start); took > 8*time.Second {
		t.Fatalf("stopGracefully took %v for a process that exits on stdin EOF", took)
	}
	if _, err := os.Stat(filepath.Join(dir, "clean")); err != nil {
		t.Fatal("the process did not exit by itself on stdin EOF (no marker): it was killed")
	}
}

// A process that does not stop in time is killed once the grace runs out.
func TestStopGracefullyKillsAfterTheGrace(t *testing.T) {
	dir := t.TempDir()
	startHelper(t, "helper-stuck", "stuck", dir)
	time.Sleep(500 * time.Millisecond)
	start := time.Now()
	stopGracefully("helper-stuck", 2*time.Second)
	if took := time.Since(start); took < 2*time.Second {
		t.Fatalf("stopped after %v, before its grace", took)
	}
	time.Sleep(500 * time.Millisecond)
	a, _ := os.ReadFile(filepath.Join(dir, "beat"))
	time.Sleep(time.Second)
	b, _ := os.ReadFile(filepath.Join(dir, "beat"))
	if string(a) != string(b) {
		t.Fatalf("the process is still running after the grace (heartbeat %s -> %s)", a, b)
	}
}

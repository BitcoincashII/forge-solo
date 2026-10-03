//go:build !windows

package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// What the miner prints goes to stratum.log in the data folder.
func TestStratumOutputIsLogged(t *testing.T) {
	savedInst, savedData := installDir, dataDir
	installDir, dataDir = t.TempDir(), t.TempDir()
	t.Cleanup(func() { installDir, dataDir = savedInst, savedData; stop("stratum") })
	if err := os.WriteFile(ipath("stratum.exe"), []byte("#!/bin/sh\necho started; echo oops >&2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	startStratum()
	var b []byte
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if b, _ = os.ReadFile(dpath("stratum.log")); strings.Contains(string(b), "oops") {
			break
		}
	}
	if !strings.Contains(string(b), "started") || !strings.Contains(string(b), "oops") {
		t.Fatalf("LOG-STRATUM-WIRED: stratum.log has %q, want its output and its errors", b)
	}
}

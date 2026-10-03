//go:build !windows

package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// Until boot has started the miner there is none to restart. Restart Mining must not start one
// early: boot would then start a second.
func TestRestartMinerNeedsAStartedMiner(t *testing.T) {
	saved := installDir
	installDir = t.TempDir()
	t.Cleanup(func() { installDir = saved; stop("stratum") })
	if err := os.WriteFile(ipath("stratum.exe"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	restartMiner()
	if started("stratum") {
		t.Fatal("RESTART-UNSTARTED: Restart Mining started a miner that boot had not started yet")
	}
}

// Restart Mining runs off the tray's menu loop, so two clicks can overlap: the second does nothing
// while the first runs. Both restarting started two miners, one of them no longer tracked.
func TestRestartMinerOneAtATime(t *testing.T) {
	saved, savedData := installDir, dataDir
	installDir, dataDir = t.TempDir(), t.TempDir()
	t.Cleanup(func() { installDir, dataDir = saved, savedData; stop("stratum") })
	starts := t.TempDir() + "/starts"
	t.Setenv("FS_STARTS", starts)
	if err := os.WriteFile(ipath("stratum.exe"), []byte("#!/bin/sh\necho start >> \"$FS_STARTS\"\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	startHelper(t, "stratum", "eof", t.TempDir())
	done := make(chan struct{})
	go func() { restartMiner(); close(done) }()
	restartMiner()
	<-done
	// The miner is started by then, but its stand-in may not have run yet.
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if b, _ := os.ReadFile(starts); len(b) > 0 {
			break
		}
	}
	time.Sleep(500 * time.Millisecond)
	if b, _ := os.ReadFile(starts); string(b) != "start\n" {
		t.Fatalf("RESTART-ONE-AT-A-TIME: two overlapping restarts started the miner %d times", strings.Count(string(b), "start"))
	}
}

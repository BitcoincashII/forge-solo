//go:build !windows

package main

import (
	"os"
	"testing"
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

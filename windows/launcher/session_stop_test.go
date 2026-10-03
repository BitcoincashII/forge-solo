package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// stopTest starts helpers for the miner and the BCH2 node (behind a fake RPC), each taking `delay`
// to stop once asked, runs stopEverything, and reports whether the miner had stopped when the node
// was asked, and how long it all took.
func stopTest(t *testing.T, ending bool, delay string) (minerGoneWhenNodeAsked bool, took time.Duration) {
	t.Helper()
	savedInst, savedData := installDir, dataDir
	installDir, dataDir = t.TempDir(), t.TempDir() // no pg_ctl here: the database is not what this tests
	t.Cleanup(func() { installDir, dataDir = savedInst, savedData })
	sessionEnding.Store(ending)
	t.Cleanup(func() { sessionEnding.Store(false) })
	minerDir := t.TempDir()
	var mu2 sync.Mutex
	asked := false
	fakeNode(t, "bch2", &bch2RPC, func(string) {
		_, err := os.Stat(filepath.Join(minerDir, "clean"))
		mu2.Lock()
		if !asked { // the first request; the node is asked again until it has stopped
			asked, minerGoneWhenNodeAsked = true, err == nil
		}
		mu2.Unlock()
	})
	startHelper(t, "stratum", "eof", minerDir, "FS_HELPER_DELAY="+delay)
	startHelper(t, "bch2", "eof", t.TempDir(), "FS_HELPER_DELAY="+delay)
	start := time.Now()
	stopEverything()
	took = time.Since(start)
	mu2.Lock()
	defer mu2.Unlock()
	if !asked {
		t.Fatal("the BCH2 node was never asked to stop")
	}
	return minerGoneWhenNodeAsked, took
}

// Windows gives a closing session's programs about 5 s, so the nodes are asked to stop at once,
// while the miner is still stopping, instead of after it.
func TestSessionEndStopsTheNodesAtOnce(t *testing.T) {
	minerGone, took := stopTest(t, true, "3s")
	if minerGone {
		t.Error("SESSION-NODES-AT-ONCE: the BCH2 node was asked to stop only after the miner had stopped")
	}
	// Each takes 3 s (plus up to 1 s for a race-detector build to exit): together about 3-4 s, one
	// after the other 6 s or more.
	if took > 5*time.Second {
		t.Errorf("SESSION-PARALLEL: stopping took %v; the miner and the node were stopped one after the other", took)
	}
}

// Quit has no deadline and keeps the safe order: the miner first, since a block it is still
// submitting needs the BCH2 node.
func TestQuitStopsTheMinerFirst(t *testing.T) {
	if minerGone, _ := stopTest(t, false, "1s"); !minerGone {
		t.Error("STOP-QUIT-ORDERED: on Quit the BCH2 node was asked to stop while the miner was still running")
	}
}

// The launcher's log goes to launcher.log in the data folder, one timestamped line per entry.
func TestLauncherLog(t *testing.T) {
	saved := dataDir
	dataDir = t.TempDir()
	t.Cleanup(func() { dataDir = saved })
	logf("one %d", 1)
	logf("two")
	b, err := os.ReadFile(dpath("launcher.log"))
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if err != nil || len(lines) != 2 || !strings.HasSuffix(lines[0], " one 1") || !strings.HasSuffix(lines[1], " two") {
		t.Fatalf("LOG-WRITTEN: %v %q", err, b)
	}
	if _, err := time.Parse("2006-01-02T15:04:05.000Z", strings.SplitN(lines[0], " ", 2)[0]); err != nil {
		t.Errorf("LOG-TIME: %q does not start with the time: %v", lines[0], err)
	}
}

// waitStopped gives up after its time when the stop has not finished.
func TestWaitStoppedGivesUp(t *testing.T) {
	start := time.Now()
	if waitStopped(200 * time.Millisecond) {
		t.Fatal("WAIT-STOPPED: reported a stop that never ran as finished")
	}
	if took := time.Since(start); took < 200*time.Millisecond || took > 2*time.Second {
		t.Fatalf("WAIT-STOPPED: gave up after %v, want about 200 ms", took)
	}
}

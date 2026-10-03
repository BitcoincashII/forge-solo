//go:build !windows

package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// reindexWorld puts a stand-in BCH2 node in place, with body for its script (it gets the node's
// arguments; D is its data folder), and starts it as boot does, with starting programs again on.
// It returns the data folder, where the stand-in notes each start in "starts".
func reindexWorld(t *testing.T, body string) string {
	t.Helper()
	startSupervising(t)
	savedInst, savedData := installDir, dataDir
	installDir, dataDir = t.TempDir(), t.TempDir()
	restartMu.Lock()
	a, b, q := restartFirstWait, restartMaxWait, restartQuick
	restartFirstWait, restartMaxWait, restartQuick = 50*time.Millisecond, 200*time.Millisecond, time.Hour
	restartMu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		stopping = true // nothing more is started
		mu.Unlock()
		restartsUnderWay.Wait()
		stop("bch2")
		mu.Lock()
		stopping = false
		mu.Unlock()
		restartMu.Lock()
		delete(restartWaits, "bch2")
		restartFirstWait, restartMaxWait, restartQuick = a, b, q
		restartMu.Unlock()
		logEndMu.Lock()
		delete(logEnds, "bch2")
		delete(repaired, "bch2")
		logEndMu.Unlock()
		installDir, dataDir = savedInst, savedData
	})
	script := "#!/bin/sh\nfor a in \"$@\"; do case $a in -datadir=*) D=${a#-datadir=};; esac; done\necho \"$*\" >> \"$D/starts\"\n" + body + "\n"
	if err := os.WriteFile(ipath("bitcoincashIId.exe"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	md(dpath("bch2"))
	return dpath("bch2")
}

const damaged = `printf 'Corrupted block database detected.\nPlease restart with -reindex or -reindex-chainstate to recover.\n' >> "$D/debug.log"`

func startsOf(t *testing.T, dir string, want int) []string {
	t.Helper()
	var lines []string
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		b, _ := os.ReadFile(dir + "/starts")
		if lines = strings.Split(strings.TrimSpace(string(b)), "\n"); len(lines) >= want && lines[0] != "" {
			break
		}
	}
	time.Sleep(300 * time.Millisecond)
	b, _ := os.ReadFile(dir + "/starts")
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// A node that stops saying its chain data is damaged is started once with -reindex, which rebuilds
// it: Windows has no command line to do that, and it stopped at every start.
func TestADamagedChainIsRebuiltOnce(t *testing.T) {
	dir := reindexWorld(t, `case " $* " in *" -reindex "*) exec sleep 60;; esac`+"\n"+damaged+"\nexit 1")
	if err := startBCH2(); err != nil {
		t.Fatal(err)
	}
	starts := startsOf(t, dir, 2)
	if len(starts) != 2 || strings.Contains(starts[0], "-reindex") || !strings.Contains(starts[1], "-reindex") {
		t.Fatalf("SUPERVISE-REINDEX: the starts were %q; want one plain, then one with -reindex", starts)
	}
	b, _ := os.ReadFile(dpath("launcher.log"))
	if !strings.Contains(string(b), "rebuilds it from the blocks on disk") {
		t.Errorf("SUPERVISE-REINDEX-LOGGED: launcher.log does not say the chain is rebuilt:\n%s", b)
	}
}

// What the node wrote in an earlier run does not count: only what it wrote since it was started.
func TestAnOldDamageMessageIsNotTakenForANewOne(t *testing.T) {
	dir := reindexWorld(t, `case " $* " in *" -reindex "*) exec sleep 60;; esac`+"\n"+`[ -f "$D/once" ] && exec sleep 60; : > "$D/once"; exit 1`)
	if err := os.WriteFile(dir+"/debug.log", []byte("Corrupted block database detected.\nPlease restart with -reindex\n...repaired since\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := startBCH2(); err != nil {
		t.Fatal(err)
	}
	starts := startsOf(t, dir, 2)
	if len(starts) != 2 || strings.Contains(starts[1], "-reindex") {
		t.Fatalf("SUPERVISE-REINDEX-OLD-LOG: the starts were %q; an old message brought on a -reindex", starts)
	}
}

// Rebuilt once and still damaged: no second rebuild in the run, and the tray says what to delete.
func TestADamagedChainIsRebuiltOnlyOnce(t *testing.T) {
	dir := reindexWorld(t, damaged+"\nexit 1")
	if err := startBCH2(); err != nil {
		t.Fatal(err)
	}
	starts := startsOf(t, dir, 4)
	n := 0
	for _, s := range starts {
		if strings.Contains(s, "-reindex") {
			n++
		}
	}
	if len(starts) < 3 || n != 1 {
		t.Fatalf("SUPERVISE-REINDEX-ONCE: %d starts, %d of them with -reindex; want exactly one rebuild", len(starts), n)
	}
	b, _ := os.ReadFile(dpath("launcher.log"))
	if !strings.Contains(string(b), `delete the blocks and chainstate folders in bch2`) {
		t.Errorf("SUPERVISE-REINDEX-ADVICE: launcher.log does not say what to delete:\n%s", b)
	}
}

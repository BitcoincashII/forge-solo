package forgesolo

// Chain data damaged by a power cut, a full disk or a crash mid-write stops a node at every start,
// and Docker starts it again, for ever: mining stops (or merge mining, for the 1175 node), and on
// Umbrel nothing short of a shell could add the -reindex that repairs it. Both node entrypoints now
// read what the node's last run wrote to debug.log and add -reindex once, as Forge Solo for Windows
// does.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// nodeImage is one of the two node images whose entrypoint these tests run.
type nodeImage struct {
	dir     string // the image's folder under docker/, and the node's folder in the app's data
	datadir string // the data folder the entrypoint uses in the image
	daemon  string // the program the entrypoint execs
	name    string // the node's name in the entrypoint's log lines
}

var nodeImages = []nodeImage{
	{"node", "/data/.bch2", "/usr/local/bin/bitcoincashIId", "BCH2 node"},
	{"node1175", "/data/.elevenseventyfive", "/usr/local/bin/elevenseventyfived", "1175 node"},
}

// entrypointHarness writes a copy of img's entrypoint that runs here against datadir: what it
// sources comes from the repo, the 1175 address learner (a loop that never ends) is not started, and
// the daemon's exec prints the arguments the daemon would have been given.
func entrypointHarness(t *testing.T, img nodeImage, datadir string) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("docker", img.dir, "entrypoint.sh"))
	if err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.Abs(filepath.Join("docker", img.dir))
	if err != nil {
		t.Fatal(err)
	}
	swaps := [][2]string{
		{`DATADIR="` + img.datadir + `"`, `DATADIR="` + datadir + `"`},
		{". /damaged-chain.sh", `. "` + dir + `/damaged-chain.sh"`},
		{"exec " + img.daemon + ` "$@"`, `printf '%s\n' "$@"`},
	}
	if img.dir == "node1175" {
		swaps = append(swaps, [2]string{". /peeraddr.sh", `. "` + dir + `/peeraddr.sh"`}, [2]string{"learn_external_ip &", ":"})
	}
	script := string(src)
	for _, s := range swaps {
		if strings.Count(script, s[0]) != 1 {
			t.Fatalf("REINDEX-HARNESS: docker/%s/entrypoint.sh no longer has exactly one %q; update this test rather than let it test nothing", img.dir, s[0])
		}
		script = strings.Replace(script, s[0], s[1], 1)
	}
	path := filepath.Join(t.TempDir(), "entrypoint.sh")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// A stand-in node for the tests: start runs the entrypoint as Docker does at each container start,
// and run appends to debug.log what one run of the node writes there.
type standIn struct {
	t       *testing.T
	img     nodeImage
	datadir string
	script  string
}

func newStandIn(t *testing.T, img nodeImage) *standIn {
	datadir := t.TempDir()
	return &standIn{t, img, datadir, entrypointHarness(t, img, datadir)}
}

// start reports whether the node was started with -reindex, and what the entrypoint logged.
func (n *standIn) start() (bool, string) {
	n.t.Helper()
	cmd := exec.Command("sh", n.script, "-datadir="+n.img.datadir, "-printtoconsole")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		n.t.Fatalf("REINDEX-HARNESS: the entrypoint failed: %v\n%s", err, stderr.String())
	}
	args := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(args) < 2 || args[0] != "-datadir="+n.img.datadir || args[1] != "-printtoconsole" {
		n.t.Fatalf("REINDEX-PASSTHROUGH: the daemon was given %q", args)
	}
	reindex := 0
	for _, a := range args {
		if a == "-reindex" {
			reindex++
		}
	}
	if reindex > 1 {
		n.t.Fatalf("REINDEX-PASSTHROUGH: the daemon was given -reindex %d times", reindex)
	}
	// Where debug.log ends now is noted, so the next start reads only what this run writes.
	var size int64
	if st, err := os.Stat(filepath.Join(n.datadir, "debug.log")); err == nil {
		size = st.Size()
	}
	if b, _ := os.ReadFile(filepath.Join(n.datadir, "debug-log-start")); strings.TrimSpace(string(b)) != strconv.FormatInt(size, 10) {
		n.t.Fatalf("REINDEX-NOTES-END: debug.log is %d bytes, but the start noted %q", size, b)
	}
	return reindex == 1, stderr.String()
}

// The node writes five empty lines each time it opens debug.log, then its run.
func (n *standIn) run(lines ...string) {
	n.t.Helper()
	n.append("\n\n\n\n\n" + strings.Join(lines, "\n") + "\n")
}

func (n *standIn) append(s string) {
	n.t.Helper()
	f, err := os.OpenFile(filepath.Join(n.datadir, "debug.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		n.t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		n.t.Fatal(err)
	}
}

// What the nodes write: once they have loaded the chain; for a block file they cannot read and for a
// block index they cannot load, as real runs of both nodes wrote it; for a rebuild (a run with
// -reindex) that fails, the error without the advice to reindex; and for a run that stops before it
// loads the chain, for another reason.
var (
	nodeStarts      = "2026-10-03T12:00:00Z Using data directory"
	nodeLoaded      = "2026-10-03T12:00:09Z init message: Done loading"
	stoppedEarly    = "2026-10-03T12:00:01Z Error: Disk space is too low!"
	unreadableBlock = []string{
		"2026-10-03T12:00:01Z Verification error: ReadBlockFromDisk failed at 60",
		"2026-10-03T12:00:01Z : Corrupted block database detected.",
		"Please restart with -reindex or -reindex-chainstate to recover.",
		"2026-10-03T12:00:01Z Aborted block database rebuild. Exiting.",
	}
	unloadableIndex = []string{
		"2026-10-03T12:00:01Z ERROR: LoadBlockIndex: block index is non-contiguous, index of height 4 missing",
		"2026-10-03T12:00:01Z : Error loading block database.",
		"Please restart with -reindex or -reindex-chainstate to recover.",
		"2026-10-03T12:00:01Z Aborted block database rebuild. Exiting.",
	}
	failedRebuild = []string{"2026-10-03T12:00:01Z Error: Corrupted block database detected"}
)

func damaged(lines []string) []string { return append([]string{nodeStarts}, lines...) }

func TestNodeWithDamagedChainIsRebuiltOnce(t *testing.T) {
	for _, img := range nodeImages {
		t.Run(img.dir, func(t *testing.T) {
			n := newStandIn(t, img)
			tried := filepath.Join(n.datadir, "reindex-tried")
			if reindex, _ := n.start(); reindex {
				t.Fatal("REINDEX-FRESH: a first start, with no debug.log yet, added -reindex")
			}
			n.run(nodeStarts, nodeLoaded)
			if reindex, _ := n.start(); reindex {
				t.Fatal("REINDEX-CLEAN-RUN: a start after a run that loaded its chain added -reindex")
			}

			n.run(damaged(unloadableIndex)...)
			reindex, log := n.start()
			if !reindex || !strings.Contains(log, "the "+img.name+" says its chain data is damaged: starting it once with -reindex") {
				t.Fatalf("REINDEX-DAMAGED: after a run that could not load its block index: -reindex %v, log %q", reindex, log)
			}

			n.run(damaged(failedRebuild)...)
			reindex, log = n.start()
			if reindex {
				t.Fatal("REINDEX-ONCE: a rebuild that failed was started again in the same container")
			}
			if !strings.Contains(log, "the "+img.name+" still finds its chain data damaged after rebuilding it") ||
				!strings.Contains(log, "app-data/bch2-apps-forge-solo/"+img.dir+",") {
				t.Fatalf("REINDEX-WHAT-TO-DELETE: after a failed rebuild the log does not say what to delete: %q", log)
			}
			n.run(damaged(unloadableIndex)...)
			if reindex, _ := n.start(); reindex {
				t.Fatal("REINDEX-ONCE-STAYS: a later start in the same container rebuilt again")
			}

			// A restart of the app makes a new container, which tries the rebuild once more, as a new
			// run of the Windows launcher does.
			if err := os.WriteFile(tried, []byte("an-earlier-container\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			n.run(damaged(unloadableIndex)...)
			if reindex, _ := n.start(); !reindex {
				t.Fatal("REINDEX-NEW-CONTAINER: a new container did not try the rebuild")
			}

			n.run(nodeStarts, nodeLoaded)
			if reindex, _ := n.start(); reindex {
				t.Fatal("REINDEX-REPAIRED: a start after the rebuild worked added -reindex again")
			}
			if _, err := os.Stat(tried); !os.IsNotExist(err) {
				t.Fatalf("REINDEX-REPAIRED: the rebuild's marker is kept after a run that loaded its chain (stat err %v)", err)
			}
			n.run(damaged(unreadableBlock)...)
			if reindex, _ := n.start(); !reindex {
				t.Fatal("REINDEX-AGAIN: chain data damaged again later, after a rebuild that worked, is not rebuilt")
			}
		})
	}
}

// A rebuild that stops before the chain is loaded, for another reason (a full disk, say), is still
// the one rebuild this container tries, as a rebuild on Windows is tried once per run of Forge Solo:
// only a run that loads the chain clears the marker.
func TestNodeRebuildThatStopsEarlyCounts(t *testing.T) {
	for _, img := range nodeImages {
		t.Run(img.dir, func(t *testing.T) {
			n := newStandIn(t, img)
			tried := filepath.Join(n.datadir, "reindex-tried")
			n.run(damaged(unreadableBlock)...)
			if reindex, _ := n.start(); !reindex {
				t.Fatal("REINDEX-DAMAGED: after a run that could not read a block file, no -reindex")
			}
			n.run(nodeStarts, stoppedEarly)
			if reindex, _ := n.start(); reindex {
				t.Fatal("REINDEX-STOPPED-EARLY: a run that stopped without saying its chain data is damaged brought on -reindex")
			}
			if _, err := os.Stat(tried); err != nil {
				t.Fatalf("REINDEX-STOPPED-EARLY: a rebuild that stopped before the chain was loaded no longer counts as tried (stat err %v)", err)
			}
			n.run(damaged(unloadableIndex)...)
			if reindex, log := n.start(); reindex || !strings.Contains(log, "the "+img.name+" still finds its chain data damaged after rebuilding it") {
				t.Fatalf("REINDEX-STOPPED-EARLY: after a rebuild that stopped early, the same container rebuilt again: -reindex %v, log %q", reindex, log)
			}

			// The same when the node trimmed debug.log: a run before the last one loaded the chain.
			m := newStandIn(t, img)
			marker := filepath.Join(m.datadir, "reindex-tried")
			m.append("...end of a line\n\n\n\n\n" + nodeStarts + "\n" + nodeLoaded + "\n\n\n\n\n\n" + nodeStarts + "\n" + stoppedEarly + "\n")
			if err := os.WriteFile(filepath.Join(m.datadir, "debug-log-start"), []byte("99999999\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(marker, []byte("this-container\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if reindex, _ := m.start(); reindex {
				t.Fatal("REINDEX-TRIMMED-STOPPED: a last run that stopped early brought on -reindex")
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("REINDEX-TRIMMED-STOPPED: a run before the last one, which loaded the chain, cleared the marker (stat err %v)", err)
			}

			// A run that loaded the chain and then said it is damaged is a damaged run.
			d := newStandIn(t, img)
			d.run(append([]string{nodeStarts, nodeLoaded}, unreadableBlock...)...)
			if reindex, _ := d.start(); !reindex {
				t.Fatal("REINDEX-DAMAGED-AFTER-LOADING: a run that loaded the chain and then said it is damaged brought on no -reindex")
			}
		})
	}
}

// Only the node's last run counts: what it wrote before the last start does not bring on a rebuild,
// also when the node trimmed debug.log as it started.
func TestNodeRebuildReadsOnlyTheLastRun(t *testing.T) {
	for _, img := range nodeImages {
		t.Run(img.dir, func(t *testing.T) {
			// A damaged run, and a start noted after it, whose run wrote nothing.
			n := newStandIn(t, img)
			n.run(damaged(unreadableBlock)...)
			st, err := os.Stat(filepath.Join(n.datadir, "debug.log"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(n.datadir, "debug-log-start"), []byte(strconv.FormatInt(st.Size(), 10)+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if reindex, _ := n.start(); reindex {
				t.Fatal("REINDEX-OLD-MESSAGE: a message from before the last start added -reindex")
			}

			// debug.log is shorter than it was at the last start: the node kept its last 10 MB, and
			// its last run follows the last five empty lines.
			for _, tc := range []struct {
				code, log string
				want      bool
			}{
				{"REINDEX-TRIMMED-OLD", "...end of a line\n\n\n\n\n" + strings.Join(damaged(unreadableBlock), "\n") + "\n\n\n\n\n\n" + nodeStarts + "\n" + nodeLoaded + "\n", false},
				{"REINDEX-TRIMMED-NEW", "...end of a line\n\n\n\n\n" + nodeStarts + "\n" + nodeLoaded + "\n\n\n\n\n\n" + strings.Join(damaged(unreadableBlock), "\n") + "\n", true},
				// A power cut can leave a run of zero bytes where the last lines were.
				{"REINDEX-ZEROS", nodeStarts + "\n" + strings.Repeat("\x00", 4096) + "\n\n\n\n\n" + strings.Join(damaged(unloadableIndex), "\n") + "\n", true},
			} {
				n := newStandIn(t, img)
				n.append(tc.log)
				if err := os.WriteFile(filepath.Join(n.datadir, "debug-log-start"), []byte("99999999\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if reindex, _ := n.start(); reindex != tc.want {
					t.Errorf("%s: -reindex %v, want %v", tc.code, reindex, tc.want)
				}
			}
		})
	}
}

// Both images carry the same script, and each copies it to where its entrypoint sources it.
func TestBothNodeImagesCarryTheRebuild(t *testing.T) {
	var first []byte
	for _, img := range nodeImages {
		b, err := os.ReadFile(filepath.Join("docker", img.dir, "damaged-chain.sh"))
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = b
		} else if !bytes.Equal(first, b) {
			t.Errorf("REINDEX-SAME-SCRIPT: docker/%s/damaged-chain.sh differs from docker/%s's", img.dir, nodeImages[0].dir)
		}
		df, err := os.ReadFile(filepath.Join("docker", img.dir, "Dockerfile"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(df), "\nCOPY damaged-chain.sh /damaged-chain.sh\n") {
			t.Errorf("REINDEX-IN-IMAGE: docker/%s/Dockerfile does not copy damaged-chain.sh to /, so its entrypoint stops where it reads it and the node never starts", img.dir)
		}
	}
}

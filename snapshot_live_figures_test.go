package forgesolo

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The dashboard snapshot leaves out what changes from one minute to the next. The miner's answer
// carries the round's effort (roundEffort), a live figure like the round's work; listed, it made a
// 1.0.13 snapshot differ from 1.0.12's in a line that is not the move, and the move's check that
// 1.0.12's dashboard reads the same failed. Both snapshot scripts leave it out, with the same list.
func TestSnapshotLeavesOutTheRoundsEffort(t *testing.T) {
	dir := t.TempDir()
	kept, err := filepath.Glob("testdata/migrate/dashboard/*.json")
	if err != nil || len(kept) == 0 {
		t.Fatalf("no kept answers in testdata/migrate/dashboard: %v", err)
	}
	for _, f := range kept {
		b := string(mustRead(t, f))
		if strings.HasSuffix(f, "-miner.json") {
			b = strings.Replace(b, `"totalWork":0,`, `"totalWork":0,"roundEffort":1.25,`, 1)
			if !strings.Contains(b, `"roundEffort":1.25`) {
				t.Fatalf("%s has no totalWork to put the round's effort beside", f)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(f)), []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) (string, error) {
		out, err := exec.Command("bash", append([]string{"scripts/dashboard-snapshot.sh"}, args...)...).CombinedOutput()
		return string(out), err
	}
	snap := filepath.Join(dir, "snapshot.json")
	if out, err := run("--from", dir, snap); err != nil {
		t.Fatalf("SNAPSHOT-LIVE-READ: the snapshot of the kept answers failed: %v\n%s", err, out)
	}
	if strings.Contains(string(mustRead(t, snap)), "roundEffort") {
		t.Error("SNAPSHOT-LIVE-SH: dashboard-snapshot.sh lists the round's effort, which changes with every share")
	}
	if out, err := run("--compare", "testdata/migrate/dashboard-1012-snapshot.json", snap); err != nil {
		t.Errorf("SNAPSHOT-LIVE-COMPARE: 1.0.12's snapshot and one with the round's effort differ: %v\n%s", err, out)
	}

	// The Windows script writes the same file: it leaves out the same.
	sh := regexp.MustCompile(`(?s)"miner": \[(.*?)\],`).FindStringSubmatch(string(mustRead(t, "scripts/dashboard-snapshot.sh")))
	ps := regexp.MustCompile(`(?s)'miner'\s*= @\((.*?)\)\n`).FindStringSubmatch(string(mustRead(t, "scripts/windows/dashboard-snapshot.ps1")))
	if sh == nil || ps == nil {
		t.Fatal("SNAPSHOT-LIVE-LISTS: a snapshot script's list for the miner's answer is not where this test reads it")
	}
	patterns := func(s string, re *regexp.Regexp) []string {
		var out []string
		for _, m := range re.FindAllStringSubmatch(s, -1) {
			out = append(out, m[1])
		}
		return out
	}
	shList, psList := patterns(sh[1], regexp.MustCompile(`r"([^"]*)"`)), patterns(ps[1], regexp.MustCompile(`'([^']*)'`))
	if strings.Join(shList, "|") != strings.Join(psList, "|") || !strings.Contains(strings.Join(psList, "|"), "roundEffort") {
		t.Errorf("SNAPSHOT-LIVE-PS1: the scripts leave out different figures of the miner's answer:\n sh  %q\n ps1 %q", shList, psList)
	}
}

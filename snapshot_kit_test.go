package forgesolo

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// seedPending1175 is the heights of the 1175 blocks testdata/migrate/seed-1012.sql has found and
// not yet distributed.
func seedPending1175(t *testing.T) []string {
	t.Helper()
	seed := string(mustRead(t, "testdata/migrate/seed-1012.sql"))
	_, aux, _ := strings.Cut(seed, "INSERT INTO blocks_1175 ")
	aux, _, _ = strings.Cut(aux, ";")
	var heights []string
	for _, r := range regexp.MustCompile(`\((\d+), md5\('[^']*'\), [\d.]+, (?:true|false), '[^']*', false, '[a-z]+'`).FindAllStringSubmatch(aux, -1) {
		heights = append(heights, r[1])
	}
	if len(heights) == 0 {
		t.Fatal("SNAPSHOT-1175-SEED: seed-1012.sql has no 1175 block left to distribute, or not in the form this test reads")
	}
	return heights
}

// kitHelp is a snapshot script's help as one line of text.
func kitHelp(t *testing.T, path string) string {
	t.Helper()
	return flat(regexp.MustCompile(`(?m)^# ?`).ReplaceAllString(string(mustRead(t, path)), ""))
}

// testdata/migrate/seed-1012.sql has a 1175 block found and not yet distributed. 1.0.13's miner
// distributes it in its first round of 1175 payouts, two minutes after it starts, so a dashboard
// snapshot taken later shows it where 1.0.12's did not, and an owner comparing the two took that
// for a fault of the move. Where the kit says how to compare, it says so, for each such block.
func TestSnapshotKitSaysWhatTheMinerChangesAfterTheMove(t *testing.T) {
	heights := seedPending1175(t)
	stratum := string(mustRead(t, "cmd/stratum/main.go"))
	_, proc, _ := strings.Cut(stratum, "\nfunc start1175PayoutProcessor() {\n")
	proc, _, _ = strings.Cut(proc, "\n}\n")
	// The only round is the one on each tick: none comes at the start.
	tick, round := strings.Index(proc, "\t\tcase <-ticker.C:\n"), strings.Index(proc, "\t\t\trun1175PayoutCycle()\n")
	if !strings.HasPrefix(proc, "\tticker := time.NewTicker(120 * time.Second)\n") || strings.Count(proc, "run1175PayoutCycle(") != 1 ||
		tick < 0 || round < tick || !strings.Contains(stratum, "\nvar run1175Processor = start1175PayoutProcessor\n") {
		t.Error("SNAPSHOT-1175-TICK: the miner's first 1175 payout round no longer comes two minutes after it starts, as the kit says")
	}
	for _, f := range []struct{ code, path string }{
		{"SNAPSHOT-1175-SH", "scripts/dashboard-snapshot.sh"},
		{"SNAPSHOT-1175-PS1", "scripts/windows/dashboard-snapshot.ps1"},
	} {
		text := kitHelp(t, f.path)
		for _, h := range heights {
			for _, want := range []string{
				"The seed has 1175 block " + h + " found and not yet distributed",
				"distributes it in its first round of 1175 payouts, two minutes after it starts",
				"A later snapshot differs from 1.0.12's in those lines by design: that is not the move.",
			} {
				if !strings.Contains(text, want) {
					t.Errorf("%s: %s does not say %q", f.code, f.path, want)
				}
			}
		}
	}
}

// Linux runs no 1175 node, and its API says merge-mining is not available. 1.0.12 for Linux still
// gave the stored 1175 address with the payout settings and 1.0.13 gives none, so the snapshots of
// one Linux install before and after the update differed in that line, and the compare that checks
// the update failed. Where merge-mining is not available both scripts leave the address out; where
// it is, as on Umbrel and Windows, it is compared as before. Both helps say what Linux shows.
func TestSnapshotKitLeavesOutThe1175AddressWithoutA1175Node(t *testing.T) {
	dir := t.TempDir()
	const esf = "esf1quhj7te09uhj7te09uhj7te09uhj7te09dnlk6x"
	const line = `"pool-config.payout_address_1175": `
	// kept copies a set of kept answers into dir/name, with the payout settings changed.
	kept := func(set, name string, change func(string) string) string {
		files, err := filepath.Glob(filepath.Join("testdata/migrate", set, "*.json"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no kept answers in testdata/migrate/%s: %v", set, err)
		}
		d := filepath.Join(dir, name)
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			b := string(mustRead(t, f))
			if filepath.Base(f) == "pool-config.json" {
				was := b
				if b = change(b); b == was {
					t.Fatalf("%s: the payout settings in %s were not changed", name, f)
				}
			}
			if err := os.WriteFile(filepath.Join(d, filepath.Base(f)), []byte(b), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return d
	}
	address := func(b, a string) string {
		return strings.Replace(b, `"payout_address_1175":"`+esf+`"`, `"payout_address_1175":"`+a+`"`, 1)
	}
	// As Linux answers: merge-mining not available, and the 1175 address a.
	onLinux := func(a string) func(string) string {
		return func(b string) string {
			b = strings.Replace(b, `"merge_mining_available":true`, `"merge_mining_available":false`, 1)
			return address(strings.Replace(b, `"platform":"umbrel"`, `"platform":"linux"`, 1), a)
		}
	}
	run := func(args ...string) (string, int) {
		out, err := exec.Command("bash", append([]string{"scripts/dashboard-snapshot.sh"}, args...)...).CombinedOutput()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return string(out), exit.ExitCode()
		}
		if err != nil {
			t.Fatalf("dashboard-snapshot.sh did not run: %v", err)
		}
		return string(out), 0
	}
	snap := func(src, name string) string {
		out := filepath.Join(dir, name)
		if o, code := run("--from", src, out); code != 0 {
			t.Fatalf("SNAPSHOT-NO-1175-READ: the snapshot of %s failed (exit %d):\n%s", src, code, o)
		}
		return out
	}

	// One Linux install, on 1.0.12 and then on 1.0.13.
	before := snap(kept("dashboard-1012", "linux-1012", onLinux(esf)), "linux-1012.json")
	after := snap(kept("dashboard", "linux-1013", onLinux("")), "linux-1013.json")
	if strings.Contains(string(mustRead(t, after)), line) {
		t.Error("SNAPSHOT-NO-1175-SH: dashboard-snapshot.sh lists the 1175 address where merge-mining is not available")
	}
	if out, code := run("--compare", before, after); code != 0 {
		t.Errorf("SNAPSHOT-NO-1175-COMPARE: the snapshots of one Linux install before and after the update differ (exit %d):\n%s", code, out)
	}

	// Umbrel and Windows run a 1175 node: there the address is read as before, and a lost one is
	// still found.
	if got := snap("testdata/migrate/dashboard", "umbrel.json"); string(mustRead(t, got)) != string(mustRead(t, "testdata/migrate/dashboard-snapshot.json")) {
		t.Error("SNAPSHOT-NO-1175-UMBREL: the kept answers of an install with a 1175 node no longer read as testdata/migrate/dashboard-snapshot.json")
	}
	lost := snap(kept("dashboard", "lost", func(b string) string { return address(b, "") }), "lost.json")
	if out, code := run("--compare", "testdata/migrate/dashboard-1012-snapshot.json", lost); code != 1 || !strings.Contains(out, "+ "+line+`""`) {
		t.Errorf("SNAPSHOT-NO-1175-LOST: a 1175 address lost on an install with a 1175 node is not found (exit %d):\n%s", code, out)
	}

	// The Windows script writes the same file: it leaves out the same, on the same condition.
	sh, ps := string(mustRead(t, "scripts/dashboard-snapshot.sh")), string(mustRead(t, "scripts/windows/dashboard-snapshot.ps1"))
	shTable := regexp.MustCompile(`(?s)\nNO_1175_NODE = \{(.*?)\}\n`).FindStringSubmatch(sh)
	psTable := regexp.MustCompile(`(?s)\n\$no1175Node = @\{(.*?)\n\}\n`).FindStringSubmatch(ps)
	if shTable == nil || psTable == nil {
		t.Fatal("SNAPSHOT-NO-1175-LISTS: a snapshot script's list for an install without a 1175 node is not where this test reads it")
	}
	table := func(s string, entry, pattern *regexp.Regexp) string {
		var out []string
		for _, e := range entry.FindAllStringSubmatch(s, -1) {
			var p []string
			for _, m := range pattern.FindAllStringSubmatch(e[2], -1) {
				p = append(p, m[1])
			}
			out = append(out, e[1]+"="+strings.Join(p, "|"))
		}
		return strings.Join(out, " ")
	}
	shList := table(shTable[1], regexp.MustCompile(`"([a-z-]+)": \[([^\]]*)\]`), regexp.MustCompile(`r"([^"]*)"`))
	psList := table(psTable[1], regexp.MustCompile(`'([a-z-]+)'\s*= @\(([^)]*)\)`), regexp.MustCompile(`'([^']*)'`))
	if shList == "" || shList != psList ||
		!strings.Contains(ps, "\n    if ($no1175Node.ContainsKey($kind) -and $doc.merge_mining_available -is [bool] -and -not $doc.merge_mining_available) {\n        $patterns = $patterns + $no1175Node[$kind]\n    }\n") {
		t.Errorf("SNAPSHOT-NO-1175-PS1: dashboard-snapshot.ps1 does not leave out what the .sh does where merge-mining is not available:\n sh  %q\n ps1 %q", shList, psList)
	}

	// What the helps say of Linux holds: its launcher tells the API merge-mining is not available,
	// its miner's config has none, and without it the miner starts no 1175 payouts.
	launcher, setup := string(mustRead(t, "cmd/forge-solo-linux/main.go")), string(mustRead(t, "cmd/forge-solo-linux/setup.go"))
	stratum := string(mustRead(t, "cmd/stratum/main.go"))
	if !strings.Contains(launcher, `"MERGE_MINING_AVAILABLE=0"`) {
		t.Error("SNAPSHOT-NO-1175-API: the Linux launcher no longer tells the API that merge-mining is not available")
	}
	if !strings.Contains(setup, "\nmergemining:\n  enabled: false\n") ||
		!strings.Contains(stratum, "\nfunc has1175Node(cfg *viper.Viper) bool {\n\treturn cfg.GetBool(\"mergemining.enabled\") &&") ||
		!strings.Contains(stratum, "\nfunc start1175Ledger(cfg *viper.Viper) {\n\tif !has1175Node(cfg) {\n\t\treturn\n\t}\n") {
		t.Error("SNAPSHOT-NO-1175-NODE: the Linux miner may start 1175 payouts, which the helps say it does not")
	}
	for _, f := range []struct{ code, path string }{
		{"SNAPSHOT-NO-1175-HELP-SH", "scripts/dashboard-snapshot.sh"},
		{"SNAPSHOT-NO-1175-HELP-PS1", "scripts/windows/dashboard-snapshot.ps1"},
	} {
		text := kitHelp(t, f.path)
		for _, h := range seedPending1175(t) {
			for _, want := range []string{
				"Linux, which runs no 1175 node, shows less of 1175 (below).",
				"Where it says so, the 1175 address in the payout settings is left out",
				"So on Linux the 1175 address is not shown, and block " + h + " stays undistributed: 1.0.13 for Linux runs no 1175 payouts",
			} {
				if !strings.Contains(text, want) {
					t.Errorf("%s: %s does not say %q", f.code, f.path, want)
				}
			}
		}
	}
}

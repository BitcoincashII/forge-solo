package forgesolo

import (
	"regexp"
	"strings"
	"testing"
)

// testdata/migrate/seed-1012.sql has a 1175 block found and not yet distributed. 1.0.13's miner
// distributes it in its first round of 1175 payouts, two minutes after it starts, so a dashboard
// snapshot taken later shows it where 1.0.12's did not, and an owner comparing the two took that
// for a fault of the move. Where the kit says how to compare, it says so, for each such block.
func TestSnapshotKitSaysWhatTheMinerChangesAfterTheMove(t *testing.T) {
	seed := string(mustRead(t, "testdata/migrate/seed-1012.sql"))
	_, aux, _ := strings.Cut(seed, "INSERT INTO blocks_1175 ")
	aux, _, _ = strings.Cut(aux, ";")
	rows := regexp.MustCompile(`\((\d+), md5\('[^']*'\), [\d.]+, (?:true|false), '[^']*', false, '[a-z]+'`).FindAllStringSubmatch(aux, -1)
	if len(rows) == 0 {
		t.Fatal("SNAPSHOT-1175-SEED: seed-1012.sql has no 1175 block left to distribute, or not in the form this test reads")
	}
	_, proc, _ := strings.Cut(string(mustRead(t, "cmd/stratum/main.go")), "\nfunc start1175PayoutProcessor() {\n")
	proc, _, _ = strings.Cut(proc, "\n}\n")
	if !strings.HasPrefix(proc, "\tticker := time.NewTicker(120 * time.Second)\n") || !strings.Contains(proc, "\t\trun1175PayoutCycle()\n") {
		t.Error("SNAPSHOT-1175-TICK: the miner's 1175 payout round no longer comes every two minutes, as the kit says")
	}
	for _, f := range []struct{ code, path string }{
		{"SNAPSHOT-1175-SH", "scripts/dashboard-snapshot.sh"},
		{"SNAPSHOT-1175-PS1", "scripts/windows/dashboard-snapshot.ps1"},
	} {
		text := flat(regexp.MustCompile(`(?m)^# ?`).ReplaceAllString(string(mustRead(t, f.path)), ""))
		for _, r := range rows {
			for _, want := range []string{
				"The seed has 1175 block " + r[1] + " found and not yet distributed",
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

package forgesolo

// The BCH2 node runs unpruned.
//
// A pruned node announces NODE_NETWORK_LIMITED instead of NODE_NETWORK. The DNS seeders list
// only NODE_NETWORK nodes and a node still syncing never dials a limited one, so under
// -prune=2000 a Forge Solo node went without inbound peers however well the router was
// forwarded -- while the pruning itself saved nothing, the whole chain being ~90 MB. Nothing
// about "-prune" looks wrong in a compose file, which is how it would come back.

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// composeService returns one service's block from docker-compose.yml: from its two-space
// "  name:" line up to the next service or the end of the file.
func composeService(t *testing.T, name string) string {
	t.Helper()
	compose, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatalf("read docker-compose.yml: %v", err)
	}
	s := string(compose)
	start := strings.Index(s, "\n  "+name+":\n")
	if start < 0 {
		t.Fatalf("no %q service in docker-compose.yml", name)
	}
	rest := s[start+1:]
	if next := regexp.MustCompile(`\n  [a-z0-9_]+:\n`).FindStringIndex(rest[1:]); next != nil {
		rest = rest[:next[0]+1]
	}
	return rest
}

func TestBCH2NodeIsStartedUnpruned(t *testing.T) {
	node := composeService(t, "node")
	if !strings.Contains(node, "-listen=1") {
		t.Fatal("NOT-PRUNED-SCOPE: the node service block was not found whole (no -listen=1 in it)")
	}
	if regexp.MustCompile(`(?m)^\s*-\s*-prune`).MatchString(node) {
		t.Fatal("NOT-PRUNED: the BCH2 node is started with -prune. A pruned node announces " +
			"NODE_NETWORK_LIMITED, which the DNS seeders never list, so it gets no inbound peers.")
	}
}

// runEntrypoint runs the real entrypoint against a scratch datadir, with the daemon's exec
// swapped for printing the arguments it would have been given.
func runEntrypoint(t *testing.T) (args string, datadir string) {
	t.Helper()
	src, err := os.ReadFile("docker/node/entrypoint.sh")
	if err != nil {
		t.Fatalf("read entrypoint: %v", err)
	}
	script := string(src)
	const dataLine = `DATADIR="/data/.bch2"`
	const execLine = `exec /usr/local/bin/bitcoincashIId "$@"`
	if strings.Count(script, dataLine) != 1 || strings.Count(script, execLine) != 1 {
		t.Fatalf("NOT-PRUNED-HARNESS: the entrypoint no longer has exactly one %q and one %q line; "+
			"update this test rather than let it test nothing", dataLine, execLine)
	}
	dir := t.TempDir()
	datadir = filepath.Join(dir, "data")
	if err := os.MkdirAll(filepath.Join(datadir, "blocks"), 0o755); err != nil {
		t.Fatal(err)
	}
	// What a node synced by an earlier release has: its chain, and the address it pinned.
	for name, content := range map[string]string{"blocks/blk00000.dat": "", "external-ip": "203.0.113.9\n"} {
		if err := os.WriteFile(filepath.Join(datadir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script = strings.Replace(script, dataLine, `DATADIR="`+datadir+`"`, 1)
	script = strings.Replace(script, execLine, `printf '%s\n' "$@"`, 1)
	path := filepath.Join(dir, "entrypoint.sh")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sh", path, "-datadir=/data/.bch2", "-listen=1").Output()
	if err != nil {
		t.Fatalf("entrypoint failed: %v", err)
	}
	return string(out), datadir
}

// An address pinned by an earlier release switches discovery off and goes stale when the ISP
// changes it, so the entrypoint must neither pass it nor keep it -- and it must not bring
// pruning back on its own.
func TestEntrypointPassesThroughWithoutPinningOrPruning(t *testing.T) {
	args, datadir := runEntrypoint(t)
	if !strings.Contains(args, "-listen=1\n") {
		t.Fatalf("NOT-PINNED-PASSTHROUGH: the compose arguments did not reach the daemon: %q", args)
	}
	if strings.Contains(args, "-externalip") {
		t.Fatalf("NOT-PINNED: the entrypoint still passes -externalip: %q", args)
	}
	if strings.Contains(args, "-prune") {
		t.Fatalf("NOT-PRUNED-ENTRYPOINT: the entrypoint adds -prune: %q", args)
	}
	if _, err := os.Stat(filepath.Join(datadir, "external-ip")); !os.IsNotExist(err) {
		t.Fatalf("NOT-PINNED-FILE: the saved external-ip file is still there (stat err %v)", err)
	}
}

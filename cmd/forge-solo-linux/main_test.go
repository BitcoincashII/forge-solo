package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestLockDataDirIsExclusive(t *testing.T) {
	dir := t.TempDir()
	a, err := lockDataDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := lockDataDir(dir); err == nil {
		b.Close()
		t.Fatal("a second lock on the same data directory succeeded")
	} else if !strings.Contains(err.Error(), "already running") {
		t.Fatalf("error %q", err)
	}
	a.Close()
	b, err := lockDataDir(dir)
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	b.Close()
}

func TestCheckRelease(t *testing.T) {
	dir := t.TempDir()
	if err := checkRelease(dir); err == nil || !strings.Contains(err.Error(), "bin/bitcoincashIId") {
		t.Fatalf("empty dir: %v", err)
	}
	for _, f := range []string{"bin/bitcoincashIId", "bin/bitcoincashII-cli", "bin/stratum", "bin/api", "web/solo.html"} {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := checkRelease(dir); err != nil {
		t.Fatal(err)
	}
}

func TestParseRunFlags(t *testing.T) {
	o, err := parseRunFlags([]string{"--data-dir", "rel/dir", "--web", "0.0.0.0:3090"})
	if err != nil || !filepath.IsAbs(o.dataDir) || !strings.HasSuffix(o.dataDir, "rel/dir") || o.web != "0.0.0.0:3090" || o.reindex {
		t.Fatalf("%+v %v", o, err)
	}
	if o, err := parseRunFlags(nil); err != nil || o.web != defaultWeb || o.reindex {
		t.Fatalf("defaults: %+v %v", o, err)
	}
	if o, err := parseRunFlags([]string{"--reindex"}); err != nil || !o.reindex {
		t.Errorf("REINDEX-FLAG: --reindex gave %+v %v", o, err)
	}
	for _, bad := range [][]string{{"--web", "3080"}, {"extra"}, {"--nope"}} {
		if _, err := parseRunFlags(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// A run as root on a data directory another account owns -- the service's, most often -- is
// refused before anything is written there: root's files broke the service's node for good.
func TestRootIsRefusedAnotherAccountsDataDir(t *testing.T) {
	dir := t.TempDir()
	if os.Geteuid() == 0 { // as root, hand the directory to another account
		if err := os.Chown(dir, 65534, 65534); err != nil {
			t.Fatal(err)
		}
	}
	if err := checkRootDataDir(0, dir); !errors.Is(err, errRootForeignDataDir) {
		t.Errorf("ROOT-REFUSE: root on another account's data directory: %v", err)
	}
	if err := checkRootDataDir(1000, dir); err != nil {
		t.Errorf("ROOT-ONLY-ROOT: an ordinary user was refused: %v", err)
	}
	if err := checkRootDataDir(0, filepath.Join(dir, "new")); err != nil {
		t.Errorf("ROOT-NEW-DIR: root on a directory that does not exist yet: %v", err)
	}
	if err := checkRootDataDir(0, "/"); err != nil {
		t.Errorf("ROOT-OWN-DIR: root on its own directory: %v", err)
	}

	// runCmd refuses before it touches the directory.
	old := geteuid
	geteuid = func() int { return 0 }
	defer func() { geteuid = old }()
	if err := runCmd([]string{"--data-dir", dir}); !errors.Is(err, errRootForeignDataDir) {
		t.Errorf("ROOT-WIRING: forge-solo run as root on another account's data directory: %v", err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Errorf("ROOT-WIRING: the refused run wrote %v", left)
	}
}

// --reindex goes to the node, at its first start only.
func TestNodeChildReindex(t *testing.T) {
	n := nodeChild("/opt/forge-solo", "/data", "/data/logs", true)
	if !reflect.DeepEqual(n.onceArgs, []string{"-reindex"}) {
		t.Errorf("REINDEX-WIRING: --reindex gave the node first-start arguments %q", n.onceArgs)
	}
	for _, a := range n.args {
		if a == "-reindex" {
			t.Errorf("REINDEX-EVERY-START: -reindex is in the arguments of every start: %q", n.args)
		}
	}
	if n := nodeChild("/opt/forge-solo", "/data", "/data/logs", false); len(n.onceArgs) != 0 {
		t.Errorf("REINDEX-UNASKED: a run without --reindex gave the node %q", n.onceArgs)
	}
}

// A public port another program holds is named before anything starts.
func TestCheckPublicPortsNamesTheTakenPort(t *testing.T) {
	taken := usePublicPorts(t)
	err := checkPublicPorts()
	if err == nil || !strings.Contains(err.Error(), "The BCH2 peer port "+strconv.Itoa(taken)+" is already in use") {
		t.Fatalf("PUBLIC-PORT-NAMED: with the peer port %d taken: %v", taken, err)
	}
}

// usePublicPorts points the public ports at free ones, the last of them held by another listener
// for the test's duration, and returns that one. A machine running Forge Solo holds the real ones.
func usePublicPorts(t *testing.T) (taken int) {
	t.Helper()
	saved := publicPorts
	t.Cleanup(func() { publicPorts = saved })
	publicPorts = nil
	for i, what := range []string{"The miner port", "The rental port", "The BCH2 peer port"} {
		l, err := net.Listen("tcp", ":0")
		if err != nil {
			t.Fatal(err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		if i < 2 {
			_ = l.Close()
		} else {
			t.Cleanup(func() { _ = l.Close() })
			taken = port
		}
		publicPorts = append(publicPorts, struct {
			port int
			what string
		}{port, what})
	}
	return taken
}

func TestRestrictDatabase(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"forgesolo.db", "forgesolo.db-wal"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	restrictDatabase(dir) // forgesolo.db-shm is absent: no error, nothing created
	for _, f := range []string{"forgesolo.db", "forgesolo.db-wal"} {
		if st, err := os.Stat(filepath.Join(dir, f)); err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v, want 0600", f, st.Mode().Perm(), err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "forgesolo.db-shm")); err == nil {
		t.Fatal("created a file that was not there")
	}
}

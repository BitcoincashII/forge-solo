package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnitFile(t *testing.T) {
	u := unitFile("0.0.0.0:3080")
	for _, want := range []string{
		"User=forge-solo\n",
		"ExecStart=/opt/forge-solo/forge-solo run --data-dir /var/lib/forge-solo --web 0.0.0.0:3080\n",
		"KillMode=mixed\n", "TimeoutStopSec=180\n", "Restart=on-failure\n", "WantedBy=multi-user.target\n",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit lacks %q:\n%s", want, u)
		}
	}
	if strings.Contains(u, "Group=") {
		t.Error("Group= set: the user's own group must apply")
	}
}

// The stop timeout must cover the launcher's own stop allowances, or systemd kills the node
// before it has flushed its chain state.
func TestUnitStopTimeoutCoversGraces(t *testing.T) {
	if total := stratumGrace + apiGrace + nodeGrace; total.Seconds() >= 180 {
		t.Fatalf("graces add up to %s, TimeoutStopSec is 180s", total)
	}
}

func TestReplaceDirInstallsAndUpgrades(t *testing.T) {
	base := t.TempDir()
	src, dst := filepath.Join(base, "rel"), filepath.Join(base, "opt", "forge-solo")
	write := func(p, s string, mode os.FileMode) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(src, "forge-solo"), "v1", 0o700)
	write(filepath.Join(src, "bin", "api"), "api1", 0o755)
	write(filepath.Join(src, "web", "solo.html"), "page1", 0o600)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceDir(src, dst); err != nil {
		t.Fatal(err)
	}
	for p, mode := range map[string]os.FileMode{"forge-solo": 0o755, "bin/api": 0o755, "web/solo.html": 0o644} {
		st, err := os.Stat(filepath.Join(dst, p))
		if err != nil || st.Mode().Perm() != mode {
			t.Fatalf("%s: %v %v, want mode %v", p, st, err, mode)
		}
	}

	// An upgrade replaces the files and drops what the new release no longer has.
	write(filepath.Join(dst, "bin", "stale"), "x", 0o755)
	write(filepath.Join(src, "forge-solo"), "v2", 0o700)
	if err := replaceDir(src, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "forge-solo")); string(b) != "v2" {
		t.Fatalf("not upgraded: %q", b)
	}
	if _, err := os.Stat(filepath.Join(dst, "bin", "stale")); err == nil {
		t.Fatal("a file the release no longer has survived the upgrade")
	}
	for _, p := range []string{dst + ".new", dst + ".old"} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("%s left behind", p)
		}
	}
}

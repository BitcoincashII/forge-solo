package forgesolo

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// exports.sh is sourced into umbreld's own script (which runs set -euo pipefail), so it must not
// change that shell's umask; it must make four 64-hex secrets in a 0600 file once, and keep them.
func TestExportsMakesSecretsOnceWithoutTouchingTheCaller(t *testing.T) {
	dir := t.TempDir()
	src, err := os.ReadFile("exports.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "exports.sh"), src, 0o644); err != nil {
		t.Fatal(err)
	}
	run := func() (umask, mode, secrets string) {
		cmd := exec.Command("bash", "-c", `set -euo pipefail; umask 022; . ./exports.sh; umask; stat -c %a .secrets.env; `+
			`printf '%s %s %s %s\n' "$APP_NODE_RPC_PASSWORD" "$APP_1175_RPC_PASSWORD" "$APP_DB_PASSWORD" "$APP_INTERNAL_API_TOKEN"`)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "APP_DATA_DIR="+dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("sourcing exports.sh: %v\n%s", err, out)
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) != 3 {
			t.Fatalf("unexpected output:\n%s", out)
		}
		return lines[0], lines[1], lines[2]
	}
	umask, mode, first := run()
	if umask != "0022" {
		t.Errorf("sourcing exports.sh changed the caller's umask to %s", umask)
	}
	if mode != "600" {
		t.Errorf(".secrets.env is mode %s, want 600", mode)
	}
	hex := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, s := range strings.Fields(first) {
		if !hex.MatchString(s) {
			t.Errorf("secret %q is not 64 hex", s)
		}
	}
	if len(strings.Fields(first)) != 4 {
		t.Fatalf("want 4 secrets, got %q", first)
	}
	if _, _, again := run(); again != first {
		t.Error("a second run changed the secrets: the database and nodes would no longer accept them")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".secrets.env.tmp.*")); len(left) != 0 {
		t.Errorf("temporary files left behind: %v", left)
	}

	// A first install cut short left empty values: remade, since no database exists yet.
	broken := "APP_NODE_RPC_PASSWORD=\nAPP_1175_RPC_PASSWORD=\nAPP_DB_PASSWORD=\nAPP_INTERNAL_API_TOKEN=\n"
	if err := os.WriteFile(filepath.Join(dir, ".secrets.env"), []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, remade := run(); len(strings.Fields(remade)) != 4 || !hex.MatchString(strings.Fields(remade)[0]) {
		t.Errorf("unusable secrets were not remade before the database existed: %q", remade)
	}
	// Once the database exists, an unusable file is left alone: new passwords could not open it.
	if err := os.MkdirAll(filepath.Join(dir, "postgres"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "postgres", "PG_VERSION"), []byte("16\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".secrets.env"), []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", `set -euo pipefail; . ./exports.sh`)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "APP_DATA_DIR="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sourcing exports.sh beside an existing database: %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".secrets.env")); string(b) != broken {
		t.Error("secrets were remade although the database already exists")
	}
}

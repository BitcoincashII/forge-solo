package forgesolo

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// linuxArches are the downloads SHA256SUMS-linux lists.
var linuxArches = []string{"x86_64", "aarch64", "armv7l", "armv6l", "i686", "riscv64"}

// startCommand is the line of the Linux README's "Start it" block that has want in it, without
// its comment.
func startCommand(t *testing.T, want string) string {
	t.Helper()
	_, block, _ := strings.Cut(string(mustRead(t, "packaging/linux/README.md")), "\n## Start it\n")
	block, _, _ = strings.Cut(block, "\n## ")
	for _, l := range strings.Split(block, "\n") {
		if strings.Contains(l, want) {
			cmd, _, _ := strings.Cut(l, "#")
			return strings.TrimSpace(cmd)
		}
	}
	return ""
}

// downloads makes dir as a user's downloads folder holds them: the x86_64 download alone, and
// SHA256SUMS-linux of all six.
func downloads(t *testing.T, dir string) string {
	t.Helper()
	file := "forge-solo-1.0.13-linux-x86_64.tar.gz"
	body := []byte("the x86_64 download")
	if err := os.WriteFile(filepath.Join(dir, file), body, 0o644); err != nil {
		t.Fatal(err)
	}
	var sums strings.Builder
	for _, a := range linuxArches {
		h := sha256.Sum256([]byte("the " + a + " download"))
		fmt.Fprintf(&sums, "%s  forge-solo-1.0.13-linux-%s.tar.gz\n", hex.EncodeToString(h[:]), a)
	}
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS-linux"), []byte(sums.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

// The Linux README's check of a download works with BusyBox's sha256sum as well as GNU's. Alpine,
// which the README names among the systems without systemd, has BusyBox's: it has no
// --ignore-missing and stopped at it, and without it the check failed for the five downloads
// that are not there. The check gives sha256sum the download's own line of SHA256SUMS-linux.
func TestLinuxDownloadCheckWorksWithBusyBox(t *testing.T) {
	// BusyBox's sha256sum takes -c, -s and -w, and a list on standard input as "-".
	busyboxOpts := map[string]bool{"-c": true, "-s": true, "-w": true, "-": true}
	for _, f := range []string{"packaging/linux/README.md", "packaging/linux/RELEASE_NOTES.md", "README.md", ".github/workflows/release.yml"} {
		for _, l := range strings.Split(string(mustRead(t, f)), "\n") {
			code, _, _ := strings.Cut(l, "#")
			_, args, ok := strings.Cut(code, "sha256sum ")
			if !ok || !strings.Contains(code, "SHA256SUMS-linux") {
				continue
			}
			args, _, _ = strings.Cut(args, "|")
			if parts := strings.FieldsFunc(args, func(r rune) bool { return r == ';' || r == '&' || r == '`' || r == ',' }); len(parts) > 0 {
				args = parts[0]
			}
			for _, a := range strings.Fields(args) {
				if strings.HasPrefix(a, "-") && !busyboxOpts[a] {
					t.Errorf("DOCS-LINUX-SUM-GNU: %s checks a download with sha256sum %s, which BusyBox's sha256sum does not have: %q", f, a, strings.TrimSpace(l))
				}
			}
			if regexp.MustCompile(`sha256sum\s+(-\S+\s+)*SHA256SUMS-linux`).MatchString(code) {
				t.Errorf("DOCS-LINUX-SUM-ALL: %s checks every download SHA256SUMS-linux lists, and a user has one: %q", f, strings.TrimSpace(l))
			}
		}
	}

	cmd := startCommand(t, "sha256sum")
	if !strings.Contains(cmd, "SHA256SUMS-linux") {
		t.Fatalf("DOCS-LINUX-SUM-MISSING: the README's Start it no longer checks the download against SHA256SUMS-linux: %q", cmd)
	}
	run := func(dir string, env []string) (string, error) {
		c := exec.Command("sh", "-c", cmd)
		c.Dir, c.Env = dir, env
		out, err := c.CombinedOutput()
		return string(out), err
	}
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Fatalf("no sha256sum here to run the README's check with: %v", err)
	}
	dir := t.TempDir()
	file := downloads(t, dir)
	if out, err := run(dir, os.Environ()); err != nil || !strings.Contains(out, file+": OK") {
		t.Errorf("DOCS-LINUX-SUM-RUN: the README's check %q of a good download said %q (%v)", cmd, out, err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte("a damaged download"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := run(dir, os.Environ()); err == nil {
		t.Errorf("DOCS-LINUX-SUM-CATCHES: the README's check %q passed a damaged download: %q", cmd, out)
	}

	// With BusyBox's grep and sha256sum alone, where there is a BusyBox to run them.
	bb, err := exec.LookPath("busybox")
	if err != nil {
		t.Log("no busybox here: the README's check ran with GNU's sha256sum only")
		return
	}
	bin := t.TempDir()
	for _, applet := range []string{"sh", "grep", "sha256sum"} {
		if err := os.Symlink(bb, filepath.Join(bin, applet)); err != nil {
			t.Fatal(err)
		}
	}
	dir = t.TempDir()
	downloads(t, dir)
	c := exec.Command(filepath.Join(bin, "sh"), "-c", cmd)
	c.Dir, c.Env = dir, []string{"PATH=" + bin}
	out, err := c.CombinedOutput()
	if err != nil || !strings.Contains(string(out), file+": OK") {
		t.Errorf("DOCS-LINUX-SUM-BUSYBOX: with BusyBox, the README's check %q of a good download said %q (%v)", cmd, out, err)
	}
}

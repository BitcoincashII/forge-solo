package forgesolo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Forge Solo for Linux's downloads are built by hand on the dev box, and the Go programs and the
// dashboard in them come from the working tree. scripts/linux/build-release.sh therefore builds only a
// clean checkout of the release's tag, at the version umbrel-app.yml gives.
func TestLinuxReleaseBuildsOnlyTheTag(t *testing.T) {
	for _, tool := range []string{"git", "sh"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("BUILD-RELEASE-HARNESS: this test needs %s: %v", tool, err)
		}
	}
	repo, work := t.TempDir(), t.TempDir()
	put := func(dir, name, content string, mode os.FileMode) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"scripts/linux/build-release.sh", "scripts/linux/build-node.sh"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		put(repo, f, string(b), 0o755)
	}
	put(repo, "umbrel-app.yml", "manifestVersion: 1\nversion: \"9.9.9\"\n", 0o644)
	put(repo, ".gitignore", "/dist/\n", 0o644)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.invalid",
			"-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("BUILD-RELEASE-HARNESS: git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", "-A")
	git("commit", "-q", "-m", "release")
	git("tag", "v9.9.9")

	// The node source and a node binary whose SHA-256 is not the pinned one: a build that gets past
	// the checks stops there, before anything slow, with a message of its own.
	if err := os.MkdirAll(filepath.Join(work, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"bitcoincashIId", "bitcoincashII-cli"} {
		put(work, "out/x86_64/"+p, "not the node\n", 0o755)
	}
	const pastChecks = "but the release ships"
	build := func(version string) string {
		t.Helper()
		cmd := exec.Command("sh", filepath.Join(repo, "scripts/linux/build-release.sh"), version, "x86_64")
		cmd.Env = append(os.Environ(), "WORK="+work)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("BUILD-RELEASE-HARNESS: a build with a node binary that is not the pinned one succeeded:\n%s", out)
		}
		return string(out)
	}
	refused := func(code, version, want string) {
		t.Helper()
		if out := build(version); !strings.Contains(out, want) || strings.Contains(out, pastChecks) {
			t.Errorf("%s: building %s did not stop with %q:\n%s", code, version, want, out)
		}
	}

	if out := build("9.9.9"); !strings.Contains(out, pastChecks) {
		t.Fatalf("BUILD-RELEASE-TAG-OK: a clean checkout of v9.9.9 did not get past the checks:\n%s", out)
	}
	git("tag", "v9.9.8")
	refused("BUILD-RELEASE-VERSION", "9.9.8", "umbrel-app.yml says version 9.9.9, not 9.9.8")

	put(repo, "scripts/linux/build-node.sh", "# a change not yet committed\n", 0o755)
	refused("BUILD-RELEASE-CLEAN", "9.9.9", "the working tree has changes v9.9.9 does not")
	git("checkout", "--", "scripts/linux/build-node.sh")
	put(repo, "notes.txt", "a file not yet committed\n", 0o644)
	refused("BUILD-RELEASE-CLEAN-UNTRACKED", "9.9.9", "the working tree has changes v9.9.9 does not")

	git("add", "notes.txt")
	git("commit", "-q", "-m", "after the release")
	refused("BUILD-RELEASE-AT-TAG", "9.9.9", "the checkout is not v9.9.9")

	put(repo, "umbrel-app.yml", "manifestVersion: 1\nversion: \"9.9.7\"\n", 0o644)
	git("commit", "-q", "-am", "9.9.7, not tagged")
	refused("BUILD-RELEASE-NO-TAG", "9.9.7", "there is no tag v9.9.7")
}

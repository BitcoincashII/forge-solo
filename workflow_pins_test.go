package forgesolo

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The installer is compiled in a third-party image. It is pinned by digest, because a tag can be
// moved to a different image by its owner at any time, and every place that runs it uses the same
// one: the release, the CI check and the README's local build.
func TestInnoSetupImageIsPinned(t *testing.T) {
	pinned := regexp.MustCompile(`amake/innosetup:[\w.-]+@sha256:[0-9a-f]{64}`)
	release, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^  INNOSETUP_IMAGE: '([^']+)'$`).FindSubmatch(release)
	if m == nil || !pinned.Match(m[1]) {
		t.Fatalf("INNOSETUP-PINNED: release.yml's INNOSETUP_IMAGE is not an image pinned by digest: %q", m)
	}
	image := string(m[1])
	for _, f := range []string{".github/workflows/release.yml", ".github/workflows/test.yml", "windows/README.md"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		uses := regexp.MustCompile(`amake/innosetup\S*`).FindAllString(string(b), -1)
		if len(uses) == 0 && f != ".github/workflows/release.yml" {
			t.Errorf("INNOSETUP-SAME: %s no longer names the image", f)
		}
		for _, u := range uses {
			if strings.TrimRight(u, `'"`) != image {
				t.Errorf("INNOSETUP-SAME: %s runs %s, not the pinned %s", f, u, image)
			}
		}
	}
}

// workflow is the part of a GitHub Actions workflow these tests read.
type workflow struct {
	Jobs map[string]struct {
		If             string `yaml:"if"`
		TimeoutMinutes int    `yaml:"timeout-minutes"`
		Environment    string `yaml:"environment"`
		Steps          []struct {
			Name string         `yaml:"name"`
			ID   string         `yaml:"id"`
			If   string         `yaml:"if"`
			Uses string         `yaml:"uses"`
			Run  string         `yaml:"run"`
			With map[string]any `yaml:"with"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func loadWorkflow(t *testing.T, path string) workflow {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var w workflow
	if err := yaml.Unmarshal(b, &w); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return w
}

// stepRun is the script of the named step of a job, failing the test if there is none.
func stepRun(t *testing.T, w workflow, job, step string) string {
	t.Helper()
	for _, s := range w.Jobs[job].Steps {
		if s.Name == step {
			return s.Run
		}
	}
	t.Fatalf("job %s has no step %q", job, step)
	return ""
}

// The release is signed on the runner: the certificate never goes into the third-party image, a
// signature carries a timestamp, and an installer signed by any certificate but Forge Solo's is
// not published.
func TestReleaseSigning(t *testing.T) {
	b, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if regexp.MustCompile(`--entrypoint osslsigncode`).MatchString(s) {
		t.Error("SIGN-ON-RUNNER: the installer is signed inside a container")
	}
	if !regexp.MustCompile(`osslsigncode sign -pkcs12 "\$KEY" -readpass "\$PASS"`).MatchString(s) {
		t.Error("SIGN-ON-RUNNER: no osslsigncode sign on the runner, with the password read from a file")
	}
	if !regexp.MustCompile(`-h sha256 -ts "\$tsa"`).MatchString(s) {
		t.Error("SIGN-TIMESTAMP: the signature is not timestamped")
	}
	if !regexp.MustCompile(`(?m)^  SIGNING_CERT_SHA256: '([0-9A-F]{2}:){31}[0-9A-F]{2}'$`).MatchString(s) ||
		!strings.Contains(s, `if [ "$FP" != "$SIGNING_CERT_SHA256" ]; then`) {
		t.Error("SIGN-CERT-PINNED: the signer's certificate is not checked against the pinned fingerprint")
	}
}

// Every check in the unit job runs once Go is set up, whatever an earlier check did. On a release
// commit TestCompose fails by design until CI has re-pinned the image digests, and that used to
// skip every suite after it.
func TestUnitJobRunsEveryCheck(t *testing.T) {
	w := loadWorkflow(t, ".github/workflows/test.yml")
	const cond = "${{ !cancelled() && steps.go.outcome == 'success' }}"
	setUp := false
	for _, s := range w.Jobs["unit"].Steps {
		switch {
		case strings.HasPrefix(s.Uses, "actions/setup-go@"):
			setUp = true
			if s.ID != "go" {
				t.Errorf("UNIT-RUNS-EVERY-CHECK: the Go setup step's id is %q, not go", s.ID)
			}
		case setUp && s.If != cond:
			t.Errorf("UNIT-RUNS-EVERY-CHECK: %q runs only if every check before it passed (if: %q)", s.Name, s.If)
		}
	}
	if !setUp {
		t.Error("UNIT-RUNS-EVERY-CHECK: the unit job does not set up Go")
	}
}

// PostgreSQL's programs need Microsoft's Visual C++ runtime, which a fresh Windows does not have;
// without it the database never starts. The release puts the runtime's DLLs beside them.
func TestReleaseBundlesTheVCRuntime(t *testing.T) {
	b, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	pg := strings.Index(s, "- name: Fetch PostgreSQL")
	vc := strings.Index(s, "python3 scripts/windows/vcruntime.py windows/pgsql/bin")
	build := strings.Index(s, "- name: Compile the installer")
	if pg < 0 || vc < 0 || build < 0 || !(pg < vc && vc < build) {
		t.Fatalf("VCRT-IN-RELEASE: the release does not add the Visual C++ runtime to windows/pgsql/bin after fetching PostgreSQL and before compiling the installer (at %d, %d, %d)", pg, vc, build)
	}
	script, err := os.ReadFile("scripts/windows/vcruntime.py")
	if err != nil {
		t.Fatal(err)
	}
	for _, dll := range []string{"vcruntime140.dll", "vcruntime140_1.dll", "msvcp140.dll"} {
		if !regexp.MustCompile(`"` + regexp.QuoteMeta(dll) + `": "[0-9a-f]{64}"`).Match(script) {
			t.Errorf("VCRT-PINNED: vcruntime.py does not pin %s by SHA-256", dll)
		}
	}
	if !regexp.MustCompile(`VCREDIST_SHA256 = "[0-9a-f]{64}"`).Match(script) {
		t.Error("VCRT-PINNED: vcruntime.py does not pin Microsoft's installer by SHA-256")
	}
}

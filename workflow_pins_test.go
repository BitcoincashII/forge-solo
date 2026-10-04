package forgesolo

import (
	"fmt"
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
// signature carries a timestamp, and an installer is published only if it verifies as signed by
// Forge Solo's certificate with a timestamp that verifies too.
func TestReleaseSigning(t *testing.T) {
	b, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`--entrypoint osslsigncode`).Match(b) {
		t.Error("SIGN-ON-RUNNER: the installer is signed inside a container")
	}
	if !regexp.MustCompile(`(?m)^  SIGNING_CERT_SHA256: '([0-9A-F]{2}:){31}[0-9A-F]{2}'$`).Match(b) {
		t.Error("SIGN-CERT-PINNED: release.yml pins no SHA-256 fingerprint for the signing certificate")
	}
	w := loadWorkflow(t, ".github/workflows/release.yml")

	sign := stepRun(t, w, "publish", "Sign the installer")
	if !strings.Contains(sign, `osslsigncode sign -pkcs12 "$KEY" -readpass "$PASS"`) {
		t.Error("SIGN-ON-RUNNER: no osslsigncode sign on the runner, with the password read from a file")
	}
	if !strings.Contains(sign, `-h sha256 -ts "$tsa"`) {
		t.Error("SIGN-TIMESTAMP: the signature is not timestamped")
	}
	// osslsigncode waits for ever on a timestamp service that accepts the connection and never
	// answers, and the fallback service would never be tried.
	if !regexp.MustCompile(`if timeout [1-9][0-9]* osslsigncode sign `).MatchString(sign) {
		t.Error("SIGN-TIME-LIMIT: a signing attempt has no time limit")
	}
	// An attempt that is cut off can leave a partial signed.exe, which the next one cannot overwrite.
	if !regexp.MustCompile(`for tsa in [^\n]*; do\n\s*rm -f signed\.exe\n\s*if timeout`).MatchString(sign) {
		t.Error("SIGN-RETRY-CLEAN: a signing attempt does not start by removing a previous attempt's signed.exe")
	}

	verify := stepRun(t, w, "publish", "Verify the signature")
	// osslsigncode itself compares the signer's certificate with the pin, whatever other
	// certificates the signature carries.
	if !strings.Contains(verify, `LEAF="sha256:$(printf '%s' "$SIGNING_CERT_SHA256" | tr -d :)"`) ||
		!strings.Contains(verify, `-require-leaf-hash "$LEAF"`) {
		t.Error("SIGN-CERT-PINNED: the signer's certificate is not checked against the pinned fingerprint")
	}
	// osslsigncode exits 0 when the timestamp is missing or does not verify.
	if !strings.Contains(verify, `grep -qx 'Timestamp Server Signature verification: ok' "$OUT"`) {
		t.Error("VERIFY-TIMESTAMP: an installer whose timestamp is missing or does not verify can be published")
	}
	// Verifying the timestamp downloads the service's CRL.
	if !regexp.MustCompile(`if timeout [1-9][0-9]* osslsigncode verify `).MatchString(verify) {
		t.Error("VERIFY-TIME-LIMIT: verifying the signature has no time limit")
	}
}

// Only a pushed v* tag publishes, and what it makes is a draft: the page names the Umbrel update
// and the Linux files, which exist only after the store sync and the Linux upload. The unsigned
// installer a public run leaves behind does not stay downloadable.
func TestReleasePublishing(t *testing.T) {
	w := loadWorkflow(t, ".github/workflows/release.yml")
	pub := w.Jobs["publish"]
	if pub.If != "github.event_name == 'push' && github.ref_type == 'tag'" {
		t.Errorf("PUBLISH-TAG-PUSH-ONLY: the publish job runs when %q; a manual run started on a tag would sign and publish", pub.If)
	}
	if pub.Environment != "release" {
		t.Errorf("PUBLISH-ENVIRONMENT: the publish job runs in %q, not the release environment", pub.Environment)
	}
	if pub.TimeoutMinutes < 1 || pub.TimeoutMinutes > 60 {
		t.Errorf("PUBLISH-TIME-LIMIT: the publish job's time limit is %d minutes; a stuck service would hold it for GitHub's six hours", pub.TimeoutMinutes)
	}
	if run := stepRun(t, w, "publish", "Publish the release"); !regexp.MustCompile(`gh release create [^\n]*--draft`).MatchString(run) {
		t.Error("RELEASE-DRAFT: the release page is published at once, before the store and the Linux files have the version")
	}
	kept := false
	for _, s := range w.Jobs["installer"].Steps {
		if strings.HasPrefix(s.Uses, "actions/upload-artifact@") {
			kept = true
			if fmt.Sprint(s.With["retention-days"]) != "1" {
				t.Errorf("UNSIGNED-RETENTION: the unsigned installer is kept for %v days, not 1", s.With["retention-days"])
			}
		}
	}
	if !kept {
		t.Error("UNSIGNED-RETENTION: the installer job uploads no installer")
	}
}

// No checkout keeps the job's GitHub token in .git/config: the release and the installer check
// mount the checkout into a third-party image, and the image build runs third-party actions and a
// privileged emulator image. The one exception is the re-pin, which pushes its commit to main.
func TestCheckoutsKeepNoToken(t *testing.T) {
	pushes := map[string]bool{".github/workflows/docker-build.yml repin": true}
	for _, f := range []string{".github/workflows/release.yml", ".github/workflows/test.yml", ".github/workflows/docker-build.yml"} {
		w := loadWorkflow(t, f)
		n := 0
		for job, j := range w.Jobs {
			for _, s := range j.Steps {
				if !strings.HasPrefix(s.Uses, "actions/checkout@") || pushes[f+" "+job] {
					continue
				}
				n++
				if s.With["persist-credentials"] != false {
					t.Errorf("CHECKOUT-NO-TOKEN: %s job %s keeps the GitHub token in its checkout", f, job)
				}
			}
		}
		if n == 0 {
			t.Errorf("CHECKOUT-NO-TOKEN: %s has no checkout", f)
		}
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

package forgesolo

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// fakeGH is gh as the release page tests need it: each call is written to $FAKE_GH_DIR/calls, and
// GitHub's side is played as FAKE_GH_MODE says: exists (a release for the tag is there already),
// create-fails (the upload breaks after the draft is made), wrong-digest (GitHub holds another
// installer), lost-file (SHA256SUMS-windows is not on the draft), no-digest (GitHub gives no
// SHA-256 of a file), or nothing wrong.
const fakeGH = `#!/usr/bin/env bash
set -u
d=$FAKE_GH_DIR
mode=${FAKE_GH_MODE:-}
printf '%s\n' "$*" >>"$d/calls"
case "$1" in
release)
  case "$2" in
  view)
    if [[ $mode == exists || -f $d/draft ]]; then
      case "$*" in
      *isDraft*) echo true ;;
      *databaseId*) echo 4242 ;;
      *) echo "{\"tagName\":\"$3\"}" ;;
      esac
      exit 0
    fi
    echo "release not found" >&2
    exit 1
    ;;
  create)
    shift 3
    mkdir -p "$d/assets"
    touch "$d/draft"
    while (($#)); do
      case $1 in
      --notes-file) cp "$2" "$d/notes.md"; shift 2 ;;
      --title) shift 2 ;;
      --*) shift ;;
      *)
        cp "$1" "$d/assets/"
        if [[ $mode == create-fails ]]; then
          echo "HTTP 502: upload failed" >&2
          exit 1
        fi
        shift
        ;;
      esac
    done
    ;;
  delete)
    rm -rf "$d/draft" "$d/assets"
    ;;
  esac
  ;;
api)
  printf '{"id":4242,"draft":true,"assets":['
  sep=
  for f in "$d"/assets/*; do
    [[ -e $f ]] || continue
    n=${f##*/}
    if [[ $mode == lost-file && $n == SHA256SUMS-windows ]]; then continue; fi
    digest="\"sha256:$(sha256sum "$f" | cut -d' ' -f1)\""
    if [[ $mode == wrong-digest && $n == ForgeSolo-* ]]; then digest="\"sha256:$(printf '%064d' 0)\""; fi
    if [[ $mode == no-digest ]]; then digest=null; fi
    printf '%s{"name":"%s","size":%s,"state":"uploaded","digest":%s}' "$sep" "$n" "$(wc -c <"$f" | tr -d ' ')" "$digest"
    sep=,
  done
  printf ']}\n'
  ;;
esac
`

// publishRun is one run of release.yml's "Publish the release" step, as GitHub runs a step with no
// shell given (bash -e), in a checkout with RELEASE_NOTES.md and scripts/publish-release.sh, the
// installer (none when it is nil) and a fake gh, with the workflow's env, the job's V and what GitHub
// gives a tag build.
type publishRun struct {
	out   string
	err   error
	dir   string // the checkout
	calls []string
	notes string // the page's text, as gh was given it
	draft bool   // a draft is there after the run
}

func runPublish(t *testing.T, version string, installer []byte, mode string) publishRun {
	t.Helper()
	for _, tool := range []string{"bash", "sha256sum", "jq", "awk", "sed"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("no %s here to run the publish step with: %v", tool, err)
		}
	}
	var raw struct {
		Env  map[string]string `yaml:"env"`
		Jobs map[string]struct {
			Env map[string]string `yaml:"env"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(mustRead(t, ".github/workflows/release.yml"), &raw); err != nil {
		t.Fatal(err)
	}
	if v := raw.Jobs["publish"].Env["V"]; v != "${{ needs.installer.outputs.version }}" {
		t.Fatalf("RELEASE-PAGE-V: the publish job's V is %q, not the installer job's version", v)
	}
	run := stepRun(t, loadWorkflow(t, ".github/workflows/release.yml"), "publish", "Publish the release")

	dir, bin, gh := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	for src, dst := range map[string]string{"RELEASE_NOTES.md": "RELEASE_NOTES.md", "scripts/publish-release.sh": "scripts/publish-release.sh"} {
		if err := os.WriteFile(filepath.Join(dir, dst), mustRead(t, src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if installer != nil {
		if err := os.WriteFile(filepath.Join(dir, "ForgeSolo-Setup-"+version+".exe"), installer, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeGH), 0o755); err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME=" + t.TempDir(), "LC_ALL=C",
		"FAKE_GH_DIR=" + gh, "FAKE_GH_MODE=" + mode, "V=" + version, "GH_TOKEN=fake",
		"GITHUB_REPOSITORY=BitcoincashII/forge-solo", "GITHUB_SHA=" + strings.Repeat("c0", 20),
		"GITHUB_REF_NAME=v" + version, "GITHUB_REF_TYPE=tag", "RUNNER_TEMP=" + t.TempDir()}
	for k, v := range raw.Env {
		env = append(env, k+"="+v)
	}
	cmd := exec.Command("bash", "-e", "-c", run)
	cmd.Dir, cmd.Env = dir, env
	out, err := cmd.CombinedOutput()
	r := publishRun{out: string(out), err: err, dir: dir}
	if b, err := os.ReadFile(filepath.Join(gh, "calls")); err == nil {
		r.calls = strings.Split(strings.TrimSpace(string(b)), "\n")
	}
	if b, err := os.ReadFile(filepath.Join(gh, "notes.md")); err == nil {
		r.notes = string(b)
	}
	_, statErr := os.Stat(filepath.Join(gh, "draft"))
	r.draft = statErr == nil
	return r
}

func (r publishRun) called(prefix string) bool {
	for _, c := range r.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// fakeInstaller is what the tests upload: a file that starts like a Windows program.
var fakeInstaller = []byte("MZ a fake installer, made by release_page_test.go\n")

// The release page gives the signed installer's SHA-256 in plain text, the line of
// SHA256SUMS-windows, computed from the very file uploaded, and says how to check a download: on
// Windows with Get-FileHash, or the signer BCH2 Software in the installer's Digital Signatures; on
// Linux against SHA256SUMS-linux, which comes with the Linux files later. After the 1.0.15 release
// a user checked the installer with Get-FileHash and had nothing on the page to compare it with.
func TestReleasePageGivesTheInstallersSHA256(t *testing.T) {
	r := runPublish(t, "1.0.16", fakeInstaller, "")
	if r.err != nil {
		t.Fatalf("RELEASE-PAGE-RUN: the publish step failed (%v):\n%s", r.err, r.out)
	}
	sum := sha256.Sum256(fakeInstaller)
	hash := hex.EncodeToString(sum[:])
	const setup = "ForgeSolo-Setup-1.0.16.exe"
	if got := string(mustRead(t, filepath.Join(r.dir, "SHA256SUMS-windows"))); got != hash+"  "+setup+"\n" {
		t.Errorf("RELEASE-PAGE-SUMS: SHA256SUMS-windows is %q, want the installer's line %q", got, hash+"  "+setup)
	}

	// The page: the version's notes, then the downloads, the check and the build.
	notes := r.notes
	if section := releaseSection(t, "1.0.16"); !strings.HasPrefix(notes, section+"\n\n---\n\n## Downloads\n") {
		t.Errorf("RELEASE-PAGE-NOTES: the page does not start with RELEASE_NOTES.md's ## 1.0.16 and then the downloads:\n%s", notes)
	}
	cert := regexp.MustCompile(`(?m)^  SIGNING_CERT_SHA256: '([0-9A-F:]+)'$`).FindSubmatch(mustRead(t, ".github/workflows/release.yml"))
	if cert == nil {
		t.Fatal("RELEASE-PAGE-CERT: release.yml pins no signing certificate")
	}
	text := flat(notes)
	for _, want := range []string{
		"Windows: " + setup + ", signed by the certificate with SHA-256 fingerprint " + string(cert[1]) + ".",
		"The installer's SHA-256 is " + hash + ", as in SHA256SUMS-windows.",
		"Linux: forge-solo-1.0.16-linux-<arch>.tar.gz, where <arch> is what uname -m prints; checksums in SHA256SUMS-linux.",
		"## Check your download",
		`Windows: in PowerShell, in the folder you downloaded it to, run Get-FileHash .\` + setup + ".",
		"The Hash it shows must be the installer's SHA-256 above (PowerShell shows it in capital letters).",
		"Or right-click the installer, choose Properties, then Digital Signatures: the signer is BCH2 Software.",
		"Linux: in the folder with your download and SHA256SUMS-linux, run grep x86_64 SHA256SUMS-linux | sha256sum -c -, with what uname -m prints in place of x86_64: it says OK.",
		"| Services built from | BitcoincashII/forge-solo@" + strings.Repeat("c0", 20) + " |",
		"| BCH2 node | 27.0.2 |", "| 1175 node | 29.1.0 |", "| PostgreSQL | 16.15: only to move an earlier version's database |",
		"Every bundled component is fetched during the build and sha256-verified. SHA256SUMS-windows covers the installer.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("RELEASE-PAGE-TEXT: the page does not say %q:\n%s", want, notes)
		}
	}
	if strings.Contains(notes, "{{") {
		t.Errorf("RELEASE-PAGE-FILLED: the page has a value left out:\n%s", notes)
	}

	// The page's Linux check, run where the downloads are, as the README's is.
	m := regexp.MustCompile("`(grep [^`]*sha256sum[^`]*)`").FindStringSubmatch(notes)
	if m == nil {
		t.Fatal("RELEASE-PAGE-LINUX: the page gives no command to check a Linux download")
	}
	dl := t.TempDir()
	file := downloads(t, dl)
	c := exec.Command("sh", "-c", m[1])
	c.Dir = dl
	if out, err := c.CombinedOutput(); err != nil || !strings.Contains(string(out), file+": OK") {
		t.Errorf("RELEASE-PAGE-LINUX-RUN: the page's check %q of a good download said %q (%v)", m[1], out, err)
	}
	if want := startCommand(t, "sha256sum"); m[1] != strings.TrimSpace(want) {
		t.Errorf("RELEASE-PAGE-LINUX-README: the page checks a Linux download with %q, the Linux README with %q", m[1], want)
	}

	// gh: is there a release already, create the draft with the two files, read it back. Nothing
	// deleted.
	want := []string{
		"release view v1.0.16 --json tagName",
		"release create v1.0.16 " + setup + " SHA256SUMS-windows --verify-tag --draft --title Forge Solo 1.0.16 --notes-file ",
		"release view v1.0.16 --json databaseId --jq .databaseId",
		"api repos/BitcoincashII/forge-solo/releases/4242",
	}
	if len(r.calls) != len(want) {
		t.Fatalf("RELEASE-PAGE-GH: gh was called %d times, want %d: %q", len(r.calls), len(want), r.calls)
	}
	for i, w := range want {
		if !strings.HasPrefix(r.calls[i], w) {
			t.Errorf("RELEASE-PAGE-GH: call %d is %q, want %q", i+1, r.calls[i], w)
		}
	}
	if !r.draft || !strings.Contains(r.out, "Draft v1.0.16 created: "+setup) {
		t.Errorf("RELEASE-PAGE-DRAFT: the run did not end with the draft (draft there: %v):\n%s", r.draft, r.out)
	}
}

// A failure leaves nothing half made: before anything is uploaded, a missing or wrong installer, or
// no notes for the version, stops the step with nothing created; a draft this run created that
// failed its upload, or is not what was uploaded, is deleted again; and a release that was there
// before is left as it is.
func TestReleasePageLeavesNothingHalfMade(t *testing.T) {
	for _, c := range []struct {
		name, version, mode string
		installer           []byte
		ok, created         bool   // the run succeeds; it called gh release create
		deleted             bool   // it deleted the draft
		says                string // in its output
	}{
		{name: "no installer", version: "1.0.16", says: "ForgeSolo-Setup-1.0.16.exe is not here"},
		{name: "not a Windows program", version: "1.0.16", installer: []byte("PK not a program"), says: "is not a Windows program"},
		{name: "no notes for the version", version: "1.0.99", installer: fakeInstaller, says: "RELEASE_NOTES.md has no section headed '## 1.0.99'"},
		{name: "a release is there already", version: "1.0.16", mode: "exists", installer: fakeInstaller, says: "a release for v1.0.16 is there already"},
		{name: "the upload fails", version: "1.0.16", mode: "create-fails", installer: fakeInstaller, created: true, deleted: true,
			says: "the release could not be created with ForgeSolo-Setup-1.0.16.exe and SHA256SUMS-windows on it"},
		{name: "GitHub holds another installer", version: "1.0.16", mode: "wrong-digest", installer: fakeInstaller, created: true, deleted: true,
			says: "the new draft is not as uploaded"},
		{name: "a file is not on the draft", version: "1.0.16", mode: "lost-file", installer: fakeInstaller, created: true, deleted: true,
			says: "the new draft is not as uploaded"},
		{name: "GitHub gives no SHA-256", version: "1.0.16", mode: "no-digest", installer: fakeInstaller, ok: true, created: true,
			says: "GitHub's digest: not given"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := runPublish(t, c.version, c.installer, c.mode)
			if (r.err == nil) != c.ok {
				t.Errorf("RELEASE-FAIL-RESULT: the run ended %v, want ok=%v:\n%s", r.err, c.ok, r.out)
			}
			if !strings.Contains(r.out, c.says) {
				t.Errorf("RELEASE-FAIL-SAYS: the run does not say %q:\n%s", c.says, r.out)
			}
			if created := r.called("release create"); created != c.created {
				t.Errorf("RELEASE-FAIL-CREATE: gh release create called: %v, want %v (%q)", created, c.created, r.calls)
			}
			if deleted := r.called("release delete v" + c.version + " --yes"); deleted != c.deleted {
				t.Errorf("RELEASE-FAIL-DELETE: the draft deleted: %v, want %v (%q)", deleted, c.deleted, r.calls)
			}
			if r.called("release delete") && strings.Contains(strings.Join(r.calls, "\n"), "--cleanup-tag") {
				t.Errorf("RELEASE-FAIL-TAG: the tag was deleted with the draft: %q", r.calls)
			}
			if r.draft != c.ok {
				t.Errorf("RELEASE-FAIL-LEFT: a draft is there after the run: %v, want %v", r.draft, c.ok)
			}
			if !c.ok && !c.created {
				if _, err := os.Stat(filepath.Join(r.dir, "SHA256SUMS-windows")); err == nil && c.installer == nil {
					t.Error("RELEASE-FAIL-SUMS: SHA256SUMS-windows was written with no installer")
				}
			}
		})
	}
}

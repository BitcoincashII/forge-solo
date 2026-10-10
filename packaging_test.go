package forgesolo

// The repo's docker-compose.yml is what the Umbrel store entry is copied from, but nothing
// forces the two to move together: the 1.0.8 release bumped umbrel-app.yml and left the
// compose pinned at 1.0.0, so for eight releases the file in this repo would have installed
// the ORIGINAL images. That is silent -- a user who "updates" gets a version number and
// none of the fixes -- so assert the two agree.
//
// Release order: bump umbrel-app.yml, tag, and CI does the rest -- the repin job in
// docker-build.yml rewrites the compose pins and the table below from the digests it just
// published, and refuses to push unless this test passes. Re-pinning by hand is what used to
// be forgotten; this test is now the backstop rather than the reminder.

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var (
	manifestVersionRe = regexp.MustCompile(`(?m)^version:\s*"([0-9.]+)"`)
	composeImageRe    = regexp.MustCompile(`image:\s*ghcr\.io/bitcoincashii/(forge-solo-[a-z0-9]+):([0-9.]+)@sha256:([0-9a-f]{64})`)
)

func TestComposeImagesMatchManifestVersion(t *testing.T) {
	manifest, err := os.ReadFile("umbrel-app.yml")
	if err != nil {
		t.Fatalf("read umbrel-app.yml: %v", err)
	}
	m := manifestVersionRe.FindSubmatch(manifest)
	if m == nil {
		t.Fatal("umbrel-app.yml has no version: field")
	}
	want := string(m[1])

	compose, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatalf("read docker-compose.yml: %v", err)
	}
	found := composeImageRe.FindAllSubmatch(compose, -1)
	if got := imageNames(found); len(got) != len(appImages) {
		t.Fatalf("found %d pinned forge-solo images in docker-compose.yml (%v), want %d (%v)", len(got), got, len(appImages), appImages)
	}
	for _, f := range found {
		if got := string(f[2]); got != want {
			t.Errorf("%s is pinned to %s but umbrel-app.yml says the app is %s — the store entry copied from this file would ship the wrong images", f[1], got, want)
		}
	}
}

// The app's images are the two nodes, the api, the stratum, the dashboard and the migrator. The
// database image of earlier releases is gone: 1.0.13 runs no database server, and a postgres
// image left pinned would be pulled and never used.
func TestComposePinsTheAppImages(t *testing.T) {
	compose, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	if got := imageNames(composeImageRe.FindAllSubmatch(compose, -1)); !slices.Equal(got, appImages) {
		t.Errorf("PKG-IMAGES: docker-compose.yml pins %v, want %v", got, appImages)
	}
	for image := range releaseDigests {
		if !slices.Contains(appImages, image) {
			t.Errorf("PKG-IMAGES-TABLE: releaseDigests lists %s, which the app does not use", image)
		}
	}
}

// releaseDigestsForVersion is the app version the digests below were published for.
//
// This constant is what makes the table work. Docker resolves name:tag@digest by DIGEST, so
// bumping only the tag ships the PREVIOUS release under a new version number -- and a table
// of current digests cannot notice, because nothing changed. Tying the table to a version
// means the release that bumps umbrel-app.yml must come back here, and re-pinning the
// digests is the only way to make this pass.
const releaseDigestsForVersion = "1.0.14"

// The digests actually shipped by releaseDigestsForVersion. Update both, together, from the
// digests CI published for the new tag.
var releaseDigests = map[string]string{
	"forge-solo-node":     "984d2fd578d77c7a4606741cbeb3491c883d15bd90f9968975889f3c5066f543",
	"forge-solo-node1175": "e9a7a2317b9d9fb1bedebf2efe766dc677804eff4c7c6bc4026f4906a7e58d3f",
	"forge-solo-api":      "f704c67b74118b8eabef99f4c10f4ca84fb931ef31b6fe89f8ae8892b59d697e",
	"forge-solo-stratum":  "c2e57b6c682f9aef0d694964efad1b0a9a1a0aa92c9b4d9095dec69658603d89",
	"forge-solo-web":      "6f8a19169053987bcdc697d601d329aa53496304c7157e3a9dbb870262f6b916",
	"forge-solo-migrate":  "4e16af24c96c071b3342fa734a7739b071d2ef38cd3ed439d7644acef85a93f0",
}

// appImages are the images the compose pins, sorted. The migrate image is pinned twice: by the
// migrate service and by the postgres service that stands in for the old database server.
var appImages = []string{"forge-solo-api", "forge-solo-migrate", "forge-solo-node", "forge-solo-node1175", "forge-solo-stratum", "forge-solo-web"}

// imageNames are the distinct image names among composeImageRe's matches, sorted.
func imageNames(found [][][]byte) []string {
	var names []string
	for _, f := range found {
		if n := string(f[1]); !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	return names
}

func TestComposeDigestsAreTheOnesThisReleasePublished(t *testing.T) {
	manifest, err := os.ReadFile("umbrel-app.yml")
	if err != nil {
		t.Fatalf("read umbrel-app.yml: %v", err)
	}
	m := manifestVersionRe.FindSubmatch(manifest)
	if m == nil {
		t.Fatal("umbrel-app.yml has no version: field")
	}
	if version := string(m[1]); version != releaseDigestsForVersion {
		t.Fatalf("umbrel-app.yml is version %s but releaseDigests still describes %s.\n"+
			"Re-pin docker-compose.yml to the digests CI published for %s and update "+
			"releaseDigests/releaseDigestsForVersion to match. Bumping the tag alone ships "+
			"the OLD images: Docker resolves name:tag@digest by digest.",
			version, releaseDigestsForVersion, version)
	}

	compose, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatalf("read docker-compose.yml: %v", err)
	}
	found := composeImageRe.FindAllSubmatch(compose, -1)
	if got := imageNames(found); len(got) != len(releaseDigests) {
		t.Fatalf("found %d pinned images (%v), want %d", len(got), got, len(releaseDigests))
	}
	for _, f := range found {
		image, digest := string(f[1]), string(f[3])
		want, ok := releaseDigests[image]
		if !ok {
			t.Errorf("%s is pinned in docker-compose.yml but absent from releaseDigests", image)
			continue
		}
		if digest != want {
			t.Errorf("%s digest\n got: %s\nwant: %s\nIf this is a new release, update releaseDigests "+
				"to the digests CI published — do not just bump the tag, Docker resolves by digest.",
				image, digest, want)
		}
	}
}

// The re-pin writes the digests CI published into the compose and into releaseDigests above. The
// migrate image is pinned on two lines, by migrate and by postgres (which stands in for the old
// database server), and both must get its digest.
func TestRepinPinsEveryImageLine(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Fatalf("REPIN-HARNESS: this test needs bash: %v", err)
	}
	m := manifestVersionRe.FindSubmatch(mustRead(t, "umbrel-app.yml"))
	if m == nil {
		t.Fatal("umbrel-app.yml has no version: field")
	}
	version := string(m[1])
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"scripts/repin-release.sh", "docker-compose.yml", "packaging_test.go", "umbrel-app.yml"} {
		if err := os.WriteFile(filepath.Join(dir, f), mustRead(t, f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	order := []string{"node", "node1175", "api", "stratum", "web", "migrate"}
	digests := map[string]string{}
	args := []string{"scripts/repin-release.sh", version}
	for i, k := range order {
		digests["forge-solo-"+k] = strings.Repeat(string(rune('1'+i)), 64)
		args = append(args, "sha256:"+digests["forge-solo-"+k])
	}
	cmd := exec.Command("bash", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("REPIN-RUN: repin-release.sh %v: %v\n%s", args[1:], err, out)
	}
	found := composeImageRe.FindAllStringSubmatch(string(mustRead(t, filepath.Join(dir, "docker-compose.yml"))), -1)
	if len(found) != 7 {
		t.Fatalf("REPIN-LINES: %d pinned image lines after the re-pin, want 7", len(found))
	}
	for _, f := range found {
		if f[2] != version || f[3] != digests[f[1]] {
			t.Errorf("REPIN-PINNED: %s is pinned to %s@%s after the re-pin, want %s@%s", f[1], f[2], f[3], version, digests[f[1]])
		}
	}
	table := string(mustRead(t, filepath.Join(dir, "packaging_test.go")))
	for image, d := range digests {
		if !regexp.MustCompile(`"` + image + `": *"` + d + `"`).MatchString(table) {
			t.Errorf("REPIN-TABLE: releaseDigests does not say %s is %s after the re-pin", image, d)
		}
	}
	if !strings.Contains(table, `const releaseDigestsForVersion = "1.0.14"`) {
		t.Errorf("REPIN-TABLE-VERSION: releaseDigestsForVersion is not %s after the re-pin", version)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Every app image must carry a digest as well as a tag: a tag is mutable, and an Umbrel
// user re-pulling a moved tag would get an image nobody reviewed.
func TestComposeImagesAreDigestPinned(t *testing.T) {
	compose, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatalf("read docker-compose.yml: %v", err)
	}
	if loose := imagesWithoutDigest(compose); len(loose) > 0 {
		t.Errorf("DIGEST-PINNED: %d image(s) pinned by tag only, no @sha256 digest: %q", len(loose), loose)
	}
}

var (
	// (?m) is load-bearing: without it ^ matches only at the start of the file, and no image
	// line is ever looked at.
	composeImageLineRe = regexp.MustCompile(`(?m)^[ \t]*image:[ \t]*(\S+)`)
	digestPinnedRe     = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)
)

// imagesWithoutDigest returns the image lines of a compose file that carry no @sha256 digest.
func imagesWithoutDigest(compose []byte) []string {
	var loose []string
	for _, m := range composeImageLineRe.FindAllSubmatch(compose, -1) {
		if !digestPinnedRe.Match(m[1]) {
			loose = append(loose, string(m[0]))
		}
	}
	return loose
}

// The check above finds an image without a digest wherever it is in the file. An earlier version
// matched only the file's last line, so it passed whatever the compose pinned.
func TestImagesWithoutDigestCanFail(t *testing.T) {
	pinned := "@sha256:" + strings.Repeat("a", 64)
	compose := "services:\n" +
		"  api:\n    image: ghcr.io/bitcoincashii/forge-solo-api:1.0.13\n    restart: unless-stopped\n" +
		"  web:\n    image: ghcr.io/bitcoincashii/forge-solo-web:1.0.13" + pinned + "\n" +
		"  node:\n    image: ghcr.io/bitcoincashii/forge-solo-node:1.0.13  # a comment\n" +
		"  stratum:\n    image: ghcr.io/bitcoincashii/forge-solo-stratum:1.0.13" + pinned + "\n"
	got := imagesWithoutDigest([]byte(compose))
	if len(got) != 2 || !strings.Contains(got[0], "forge-solo-api:1.0.13") || !strings.Contains(got[1], "forge-solo-node:1.0.13") {
		t.Errorf("DIGEST-PINNED-CHECK: in a compose with two images pinned by tag only, the check found %q", got)
	}
}

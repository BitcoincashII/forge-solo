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
	"regexp"
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
	if len(found) != 5 {
		t.Fatalf("found %d pinned forge-solo images in docker-compose.yml, want 5 (node, node1175, api, stratum, web)", len(found))
	}
	for _, f := range found {
		if got := string(f[2]); got != want {
			t.Errorf("%s is pinned to %s but umbrel-app.yml says the app is %s — the store entry copied from this file would ship the wrong images", f[1], got, want)
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
const releaseDigestsForVersion = "1.0.11"

// The digests actually shipped by releaseDigestsForVersion. Update both, together, from the
// digests CI published for the new tag.
var releaseDigests = map[string]string{
	"forge-solo-node":     "14ef6bc6a723affc7f4fc89f9adad7237996f286d8e89fc9d35a1059009b2aed",
	"forge-solo-node1175": "abb5a85d51b624da761fb8d9ceab7ae9b511e6a024fd04f9bbe49149105435fa",
	"forge-solo-api":      "ce62d7b87ebe762b0a89dec1a584cb1ee4e9a19777f123b2952fac883cc51d8e",
	"forge-solo-stratum":  "ab95240531b213da14951aba7dc8897d5a1e27ddd70ca0ab5b791aea897731b7",
	"forge-solo-web":      "79065d9ef1172a6cd487d0bb19d35fbefc593e83be1bfae68d2667f90d0ce0b9",
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
	if len(found) != len(releaseDigests) {
		t.Fatalf("found %d pinned images, want %d", len(found), len(releaseDigests))
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

// Every app image must carry a digest as well as a tag: a tag is mutable, and an Umbrel
// user re-pulling a moved tag would get an image nobody reviewed.
func TestComposeImagesAreDigestPinned(t *testing.T) {
	compose, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatalf("read docker-compose.yml: %v", err)
	}
	// (?m) is load-bearing: without it `$` matches only end-of-text, so this pattern could
	// never fire and the test passed unconditionally.
	loose := regexp.MustCompile(`image:\s*ghcr\.io/bitcoincashii/forge-solo-[a-z0-9]+:[0-9.]+\s*$`)
	if m := loose.FindAll(compose, -1); len(m) > 0 {
		t.Errorf("%d forge-solo image(s) pinned by tag only, no @sha256 digest: %q", len(m), m)
	}
}

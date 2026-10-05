package forgesolo

import (
	"regexp"
	"slices"
	"testing"
)

// The Umbrel size figures count the same way on both sides: with umbrelOS's Tor image, which umbreld
// pulls for every app, for 1.0.13 as for 1.0.12. The notes said 220 MB instead of 900 MB and
// 0.5 GB instead of 2.3 GB, counting the Tor image for 1.0.12 alone. Measured on amd64, each
// download counted once, 1.0.13 with Tor downloads 237.5 MB and takes 620.0 MB of disk; 1.0.12
// with Tor, 934.2 MB and 2348.9 MB. Measure again on the published images when they change.
func TestUmbrelSizesCountTheSameWay(t *testing.T) {
	want := []string{"240 MB", "930 MB", "0.6 GB", "2.3 GB"}
	sizes := regexp.MustCompile(`downloads about (\d+ MB) instead of (\d+ MB); its images take about ([\d.]+ GB) of disk instead of ([\d.]+ GB)`)
	for _, d := range []struct{ code, name, text string }{
		{"DOCS-UMBREL-SIZE-NOTES", "RELEASE_NOTES.md ## 1.0.13", releaseSection(t, "1.0.13")},
		{"DOCS-UMBREL-SIZE-STORE", "umbrel-app.yml releaseNotes", readUmbrelManifest(t).ReleaseNotes},
	} {
		m := sizes.FindStringSubmatch(flat(d.text))
		if m == nil || !slices.Equal(m[1:], want) {
			t.Errorf("%s: %s does not give the Umbrel sizes counted the same way on both sides (%v): %q", d.code, d.name, want, m)
		}
	}
}

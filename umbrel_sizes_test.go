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
// 1.0.13's release page gives them; the store's update notes are a later version's and give none,
// and a release text that gives them gives these.
func TestUmbrelSizesCountTheSameWay(t *testing.T) {
	want := []string{"240 MB", "930 MB", "0.6 GB", "2.3 GB"}
	sizes := regexp.MustCompile(`downloads about (\d+ MB) instead of (\d+ MB); its images take about ([\d.]+ GB) of disk instead of ([\d.]+ GB)`)
	for _, d := range releaseTexts(t) {
		m := sizes.FindStringSubmatch(flat(d.text))
		if m == nil && d.name != "RELEASE_NOTES.md ## 1.0.13" {
			continue
		}
		if m == nil || !slices.Equal(m[1:], want) {
			t.Errorf("DOCS-UMBREL-SIZE: %s does not give the Umbrel sizes counted the same way on both sides (%v): %q", d.name, want, m)
		}
	}
}

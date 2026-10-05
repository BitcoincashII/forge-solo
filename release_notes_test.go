package forgesolo

import (
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// From 1.0.13 every platform keeps its data in forgesolo.db, and Umbrel and Windows move their old
// PostgreSQL data into it once, keeping the old database for going back to 1.0.12. Text written
// before that switch said otherwise: a smaller PostgreSQL image as the database Umbrel runs, the
// stored shares cleared from every disk, the old database kept as it is. A merge or a copy from an
// older draft can bring any of it back. These tests read the text users see of a release: its
// section of RELEASE_NOTES.md (the release page) and the Umbrel store's description and update
// notes, with the READMEs where a claim spans them all.

// releaseSection is the section of RELEASE_NOTES.md headed "## <version>", cut as release.yml cuts
// it for the release page: up to the next "## " heading.
func releaseSection(t *testing.T, version string) string {
	t.Helper()
	var out []string
	on := false
	for _, l := range strings.Split(string(mustRead(t, "RELEASE_NOTES.md")), "\n") {
		switch {
		case l == "## "+version:
			on = true
			continue
		case strings.HasPrefix(l, "## "):
			on = false
		}
		if on {
			out = append(out, l)
		}
	}
	if strings.TrimSpace(strings.Join(out, "")) == "" {
		t.Fatalf("DOCS-SECTION: RELEASE_NOTES.md has no section headed ## %s", version)
	}
	return strings.Join(out, "\n")
}

// umbrelManifest is the part of umbrel-app.yml the Umbrel store shows.
type umbrelManifest struct {
	Version      string `yaml:"version"`
	Description  string `yaml:"description"`
	ReleaseNotes string `yaml:"releaseNotes"`
}

func readUmbrelManifest(t *testing.T) umbrelManifest {
	t.Helper()
	var m umbrelManifest
	if err := yaml.Unmarshal(mustRead(t, "umbrel-app.yml"), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// releaseTexts are the 1.0.13 texts: the release page's section, and the Umbrel store's.
func releaseTexts(t *testing.T) []docText {
	t.Helper()
	m := readUmbrelManifest(t)
	return []docText{
		{"RELEASE_NOTES.md ## 1.0.13", releaseSection(t, "1.0.13")},
		{"umbrel-app.yml releaseNotes", m.ReleaseNotes},
		{"umbrel-app.yml description", m.Description},
	}
}

// allDocs are the release texts and the READMEs.
func allDocs(t *testing.T) []docText {
	t.Helper()
	docs := releaseTexts(t)
	for _, f := range readmes {
		docs = append(docs, docText{f, string(mustRead(t, f))})
	}
	return docs
}

// Nothing in the 1.0.13 text presents PostgreSQL as the database Umbrel or Windows runs, says the
// move leaves the old database untouched (PostgreSQL rewrites its own files when it starts and
// stops), or claims the shares 1.0.12 stored are cleared on Umbrel or Windows: they stay in the old
// database, which the move never empties.
func TestReleaseNotesDoNotDescribeThePostgreSQLSetup(t *testing.T) {
	stale := []struct {
		code string
		re   *regexp.Regexp
		why  string
	}{
		{"DOCS-PG-RUNS", regexp.MustCompile(`(?i)PostgreSQL 16\.15 \(was|PostgreSQL 16\.15 and nginx|TimescaleDB|database image|telemetry|sized for this app`),
			"presents PostgreSQL as the database Forge Solo runs; it only reads the old data during the move"},
		{"DOCS-PG-UNTOUCHED", regexp.MustCompile(`(?i)\buntouched\b|nothing (is|gets) written|kept as it is|\bread-only\b`),
			"says the old database is left as it is; PostgreSQL updates its own bookkeeping files when it starts and stops"},
		{"DOCS-WIN-NAMES", regexp.MustCompile(`(?i)outside the system's language|characters outside`),
			"says 1.0.12's database failed only under names outside the system's language; it failed under every name with a letter beyond plain English"},
	}
	cleared := regexp.MustCompile(`(?i)\bclear(ed|s)?\b`)
	shares := regexp.MustCompile(`(?i)\bshares?\b|what was stored`)
	beyondLinux := regexp.MustCompile(`Umbrel|Windows|Every platform`)
	for _, d := range releaseTexts(t) {
		text := flat(d.text)
		for _, s := range stale {
			if m := s.re.FindString(text); m != "" {
				t.Errorf("%s: %s has %q: it %s", s.code, d.name, m, s.why)
			}
		}
		// Only Linux clears the shares an earlier version stored, at its first start.
		for _, s := range sentences(d.text) {
			if cleared.MatchString(s) && shares.MatchString(s) && (!strings.Contains(s, "Linux") || beyondLinux.MatchString(s)) {
				t.Errorf("DOCS-SHARES-CLEARED: %s says stored shares are cleared beyond Linux: %q", d.name, s)
			}
		}
	}
}

// The release page says what the switch does and what users can do about it: where the data is
// now, that the old database is kept and why, that deleting it later is safe, and the way out when
// the move fails.
func TestReleaseNotesTellTheMove(t *testing.T) {
	sec := flat(releaseSection(t, "1.0.13"))
	for _, c := range []struct{ code, want string }{
		{"DOCS-SWITCH-FILE", "keeps your settings, blocks and payouts in one file, forgesolo.db"},
		{"DOCS-SWITCH-ONCE", "moves your data from the old database into forgesolo.db, once"},
		{"DOCS-SWITCH-KEPT", "the old database stays where it was, for going back to 1.0.12"},
		{"DOCS-SWITCH-BOOKKEEPING", "PostgreSQL updates its own bookkeeping files when it starts and stops"},
		{"DOCS-SWITCH-SHARES-KEPT", "the shares 1.0.12 stored stay in it"},
		{"DOCS-SWITCH-MERGE-COPY", "forgesolo.db.before-merge-<time>"},
		{"DOCS-SWITCH-DELETE-SAFE", "deleting it, even half way, never stops Forge Solo"},
		{"DOCS-SWITCH-WAY-OUT", "Start without the old data"},
		{"DOCS-SWITCH-SKIP-FILE", "SKIP-POSTGRES-MIGRATION"},
		{"DOCS-SWITCH-NOTHING-LOST", "if the move fails, nothing is lost"},
		{"DOCS-SWITCH-WIN-FRESH", "A fresh install puts no PostgreSQL on disk"},
		{"DOCS-SWITCH-PG-ONLY", "PostgreSQL 16.15 is used only to read the old database"},
		{"DOCS-SWITCH-WIN-NAMES", "1.0.12 never created its database under a name with a letter beyond plain English"},
	} {
		if !strings.Contains(sec, c.want) {
			t.Errorf("%s: RELEASE_NOTES.md ## 1.0.13 no longer says %q", c.code, c.want)
		}
	}
	notes := flat(readUmbrelManifest(t).ReleaseNotes)
	for _, want := range []string{"db/forgesolo.db", "postgres/", "going back to 1.0.12", "nothing is lost", "start without the old data"} {
		if !strings.Contains(notes, want) {
			t.Errorf("DOCS-UMBREL-SWITCH: umbrel-app.yml releaseNotes no longer says %q", want)
		}
	}
}

// When another program holds 3335 on Windows or Linux, Forge Solo starts without the rental port.
// Every text says the same about it: what is wrong and how to put it right, and never that
// NiceHash or MiningRigRentals should use 3333 instead, where a whole order would start at the
// 1,024 floor. A sentence that names one of them names 3333 only in the forms below: Braiins's
// port, your own miners' port beside 3335 for the marketplaces, and an order moved off 3333. A
// sentence about 3335 being taken names 3335 too, so naming 3335 is no pass.
func TestRentalPortTakenIsToldOneWay(t *testing.T) {
	marketplace := regexp.MustCompile(`(?i)NiceHash|MiningRigRentals|\bMRR\b`)
	allowed3333 := regexp.MustCompile(`\b3333\s*\(Braiins\)|\bport 3333 for your miners, 3335 for NiceHash\b|\bpoints at 3333, move it to 3335\b`)
	port3333 := regexp.MustCompile(`\b3333\b`)
	docs := allDocs(t)
	for _, d := range docs {
		for _, s := range sentences(d.text) {
			if marketplace.MatchString(s) && port3333.MatchString(allowed3333.ReplaceAllString(s, "")) {
				t.Errorf("DOCS-RENTAL-3333: %s points NiceHash or MiningRigRentals at 3333: %q", d.name, s)
			}
		}
	}
	// Umbrel cannot start without 3335 (its install stops), so the Umbrel store's text has no word
	// on it; the release page and the READMEs do.
	for _, d := range docs {
		if strings.HasPrefix(d.name, "umbrel-app.yml") {
			continue
		}
		text := flat(d.text)
		for _, want := range []*regexp.Regexp{
			regexp.MustCompile(`(?i)another program uses port 3335, the rental port`),
			regexp.MustCompile(`(?i)rentals have no port of their own`),
			regexp.MustCompile(`(?i)stop (it|that program), then restart Forge Solo`),
		} {
			if !want.MatchString(text) {
				t.Errorf("DOCS-3335-WORDING: %s does not say %q", d.name, want)
			}
		}
	}
}

// On Windows the rental port can also be one Windows keeps for itself (a range reserved for
// Hyper-V, WSL or Docker), which a restart alone does not bring back. The dashboard, the stratum
// and the launcher say so in one sentence, and so do the texts that cover Windows.
func TestRentalPortWindowsKeepsIsTold(t *testing.T) {
	want := "rentals have no port of their own until Windows lets it go and you restart Forge Solo"
	for _, d := range []struct{ code, name, text string }{
		{"DOCS-3335-RESERVED-RELEASE", "RELEASE_NOTES.md ## 1.0.13", releaseSection(t, "1.0.13")},
		{"DOCS-3335-RESERVED-README", "README.md", string(mustRead(t, "README.md"))},
		{"DOCS-3335-RESERVED-WIN", "windows/README.md", string(mustRead(t, "windows/README.md"))},
		{"DOCS-3335-RESERVED-CODE", "web/dist/js/common.js", string(mustRead(t, "web/dist/js/common.js"))},
	} {
		if !strings.Contains(flat(d.text), want) {
			t.Errorf("%s: %s does not say %q", d.code, d.name, want)
		}
	}
}

// The store's description and update notes keep their lists one item to a line. In a folded
// block, lines at the block's own indentation are joined into one: the store showed "Includes: - A
// built-in BCH2 full node ... - A built-in 1175 (ESF) node ...". The update screen is short.
func TestUmbrelStoreTextReadsAsWritten(t *testing.T) {
	m := readUmbrelManifest(t)
	if m.Version != "1.0.13" {
		t.Errorf("DOCS-UMBREL-VERSION: umbrel-app.yml is version %q, not 1.0.13", m.Version)
	}
	joined := regexp.MustCompile(`\S[ \t]+-[ \t]+[A-Z]`)
	item := regexp.MustCompile(`(?m)^[ \t]*- \S`)
	for _, d := range []docText{{"description", m.Description}, {"releaseNotes", m.ReleaseNotes}} {
		for _, l := range strings.Split(d.text, "\n") {
			if joined.MatchString(l) {
				t.Errorf("DOCS-UMBREL-LIST: %s has list items run together on one line: %q", d.name, l)
			}
		}
		if n := len(item.FindAllString(d.text, -1)); n < 5 {
			t.Errorf("DOCS-UMBREL-LIST: %s has %d list items on lines of their own, want at least 5", d.name, n)
		}
		if strings.Contains(d.text, " ,") {
			t.Errorf("DOCS-UMBREL-COMMA: %s has a space before a comma", d.name)
		}
	}
	if n := len(strings.Fields(m.ReleaseNotes)); n > 230 {
		t.Errorf("DOCS-UMBREL-NOTES-LONG: releaseNotes is %d words, over the 230 Umbrel's update screen shows well", n)
	}
	desc := flat(m.Description)
	for _, want := range []string{`installing stops with "exit code 1"`, "keep running in the background",
		"free the port and install Forge Solo again", "Bleskomat Server uses 3333", "Bitmagnet uses 3335"} {
		if !strings.Contains(desc, want) {
			t.Errorf("DOCS-UMBREL-PORTS: umbrel-app.yml description no longer says %q", want)
		}
	}
}

// The release texts use plain punctuation, as the READMEs do.
func TestReleaseTextsUsePlainPunctuation(t *testing.T) {
	checkPlainPunctuation(t, releaseTexts(t))
}

// The release texts do not say the Settings password keeps out programs on the computer.
func TestReleaseTextsDoNotOverstateThePassword(t *testing.T) {
	checkPasswordScope(t, releaseTexts(t))
}

// 1.0.13 is the first Windows release on this repository's release page. A Windows user who lands
// there learns before the download what Windows asks: Smart App Control off on Windows 11, and
// More info, then Run anyway, at SmartScreen's warning; and where the steps are. The page resolves
// a relative link against its own address, under /releases/tag/, where no file of the repository
// is, so its links are whole addresses.
func TestReleaseNotesTellWindowsUsersHowToInstall(t *testing.T) {
	sec := releaseSection(t, "1.0.13")
	text := flat(sec)
	for _, want := range []string{
		"on Windows 11, Smart App Control must be Off",
		"choose More info, then Run anyway",
		"[Install on Windows](" + repoURL + "#install-on-windows)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("DOCS-RELEASE-WIN-INSTALL: RELEASE_NOTES.md ## 1.0.13 does not say %q", want)
		}
	}
	for _, m := range regexp.MustCompile(`\]\(([^)\s]*)\)`).FindAllStringSubmatch(sec, -1) {
		if !strings.HasPrefix(m[1], "https://") {
			t.Errorf("DOCS-RELEASE-LINK: RELEASE_NOTES.md ## 1.0.13 links to %q, which the release page cannot open", m[1])
		}
	}
}

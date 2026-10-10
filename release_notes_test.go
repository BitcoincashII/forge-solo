package forgesolo

import (
	"regexp"
	"slices"
	"strconv"
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

// releaseHeading is the heading of a version's section of RELEASE_NOTES.md.
var releaseHeading = regexp.MustCompile(`(?m)^## ([0-9.]+)$`)

// releaseTexts are the texts users see of the releases: each version's section of RELEASE_NOTES.md
// (its release page), and the Umbrel store's.
func releaseTexts(t *testing.T) []docText {
	t.Helper()
	var docs []docText
	for _, h := range releaseHeading.FindAllStringSubmatch(string(mustRead(t, "RELEASE_NOTES.md")), -1) {
		docs = append(docs, docText{"RELEASE_NOTES.md ## " + h[1], releaseSection(t, h[1])})
	}
	m := readUmbrelManifest(t)
	return append(docs, docText{"umbrel-app.yml releaseNotes", m.ReleaseNotes}, docText{"umbrel-app.yml description", m.Description})
}

// tellsTheRentalPort reports whether the text named name tells how the rental port works: the
// READMEs do, and the release page of 1.0.13, which changed it. Later release pages need not tell
// it again, and the Umbrel store's text has no word on it.
func tellsTheRentalPort(name string) bool {
	return name == "RELEASE_NOTES.md ## 1.0.13" || slices.Contains(readmes, name)
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

// On Linux a 1175 address in the settings no longer turns merge-mining on, and the start asks for a
// payout address only when none is saved. The API's workers list gives a connected worker with no
// share yet the time it connected and a null last share, and the miner's answer gives a null last
// share where it gave the year 1. The release page says each.
func TestReleaseNotesTellTheLinuxAndWorkerChanges(t *testing.T) {
	sec := flat(releaseSection(t, "1.0.13"))
	for _, c := range []struct{ code, want string }{
		{"DOCS-NOTES-LINUX-1175", "a 1175 address in the settings no longer switches 1175 merge-mining on"},
		{"DOCS-NOTES-LINUX-1175-API", "on Linux the API refuses a 1175 address"},
		{"DOCS-NOTES-LINUX-PAYOUT", "the start says mining waits for a BCH2 payout address only when none is saved"},
		{"DOCS-NOTES-WORKERS-NO-SHARE", "also lists a worker that is connected and has no share yet, with the time it connected and a last share of null"},
		{"DOCS-NOTES-WORKERS-CONNECTED", "A connected worker's connectedAt is when its connection began"},
		{"DOCS-NOTES-MINER-NULL", "A miner with no share yet has a lastShare of null"},
	} {
		if !strings.Contains(sec, c.want) {
			t.Errorf("%s: RELEASE_NOTES.md ## 1.0.13 does not say %q", c.code, c.want)
		}
	}
}

// A new block's work waits for Forge Pool at most newBlockWait, then goes out solo until the pool
// registers it, and a refresh on the same block no longer runs ahead of it: the notes said it could
// wait up to about 12 seconds behind another registration. While the BCH2 node catches up with the
// chain a new block's work goes out at most every catchUpEvery, where every old block got a job, and
// TIDES comes back with the block that brings the node level, where it stayed on solo for a minute
// and the log and the dashboard blamed the pool. The release page says each, with the figures
// job_loop.go has and the words the gateway logs.
func TestReleaseNotesTellWhenANewBlocksWorkWaits(t *testing.T) {
	src := string(mustRead(t, "cmd/stratum/job_loop.go"))
	secs := func(re string) string {
		t.Helper()
		m := regexp.MustCompile(re).FindStringSubmatch(src)
		if m == nil {
			t.Fatalf("DOCS-JOBLOOP-CODE: cmd/stratum/job_loop.go has nothing matching %s", re)
		}
		return m[1]
	}
	wait := secs(`(?m)^const newBlockWait = (\d+) \* time\.Second$`)
	every := secs(`(?m)^\s*catchUpEvery\s*=\s*(\d+) \* time\.Second$`)
	behind := "this BCH2 node is not on Forge Pool's block yet"
	if !strings.Contains(string(mustRead(t, "internal/tidesgw/gateway.go")), behind) {
		t.Errorf("DOCS-NOTES-BEHIND-CODE: internal/tidesgw/gateway.go no longer logs %q, which the release notes quote", behind)
	}
	sec := flat(releaseSection(t, "1.0.13"))
	for _, c := range []struct{ code, want string }{
		{"DOCS-NOTES-POOL-WAIT", "a slow or unresponsive Forge Pool holds a new block's work back for " + wait + " seconds at most, not up to 40 as before"},
		{"DOCS-NOTES-POOL-SOLO", "if the pool has not registered the block's work within " + wait + " seconds, miners get solo work for it, then switch to the pool's job (with clean_jobs) as soon as it registers"},
		{"DOCS-NOTES-POOL-REFRESH", "A refresh on the same block no longer runs ahead of a new block's work"},
		{"DOCS-NOTES-CATCHUP-EVERY", "miners get a new block's work at most every " + every + " seconds instead of one job per old block"},
		{"DOCS-NOTES-CATCHUP-LEVEL", "The block that brings the node level goes out at once, and blocks that come " + every + " seconds or more apart, as at the tip, go out as before"},
		{"DOCS-NOTES-TIDES-LEVEL", "TIDES comes back with the block that brings the node level instead of a minute later"},
		{"DOCS-NOTES-TIDES-BEHIND", "The log and the dashboard now say that " + behind},
	} {
		if !strings.Contains(sec, c.want) {
			t.Errorf("%s: RELEASE_NOTES.md ## 1.0.13 does not say %q", c.code, c.want)
		}
	}
	old := regexp.MustCompile(`(?i)up to about 12 seconds|another registration is already under way`)
	for _, d := range releaseTexts(t) {
		if m := old.FindString(flat(d.text)); m != "" {
			t.Errorf("DOCS-NOTES-POOL-12S: %s says a new block's work waits %q; it waits for the pool %s seconds at most", d.name, m, wait)
		}
	}
}

// While the BCH2 node catches up with the chain, the two ZMQ lines of each block are left out of
// the log and the node's progress is said every noticeProgressEvery; each block's lines come back
// once the node is level, or catchUpEvery after the last block. 1.0.15's release page gives the
// figures job_loop.go has, and names the two lines as the stratum logs them.
func TestReleaseNotesTellTheQuietCatchUp(t *testing.T) {
	src := string(mustRead(t, "cmd/stratum/job_loop.go")) + string(mustRead(t, "cmd/stratum/main.go"))
	every := regexp.MustCompile(`(?m)^\s*catchUpEvery\s*=\s*(\d+) \* time\.Second$`).FindStringSubmatch(src)
	if every == nil || !regexp.MustCompile(`(?m)^\s*noticeProgressEvery\s*=\s*time\.Minute$`).MatchString(src) {
		t.Fatal("DOCS-QUIET-CODE: cmd/stratum/job_loop.go no longer gives catchUpEvery in seconds, or no longer says the progress once a minute")
	}
	sec := flat(releaseSection(t, "1.0.15"))
	for _, c := range []struct{ code, want string }{
		{"DOCS-QUIET-LINES", `("ZMQ block notification received" and "ZMQ triggered job refresh")`},
		{"DOCS-QUIET-PROGRESS", "then gives its block and headers once a minute"},
		{"DOCS-QUIET-BACK", "or " + every[1] + " seconds after the last block"},
	} {
		if !strings.Contains(sec, c.want) {
			t.Errorf("%s: RELEASE_NOTES.md ## 1.0.15 does not say %q", c.code, c.want)
		}
	}
	for _, line := range []string{`"⚡ ZMQ block notification received"`, `"⚡ ZMQ triggered job refresh"`} {
		if !strings.Contains(src, line) {
			t.Errorf("DOCS-QUIET-LINES-CODE: the stratum no longer logs %s, which the release notes name", line)
		}
	}
}

// The release page gives the Linux README's own commands to check a download and to delete what an
// install or an upgrade leaves behind, so the two never differ. The check 1.0.12 gave stopped on
// BusyBox's sha256sum.
func TestReleaseNotesGiveTheLinuxReadmesCommands(t *testing.T) {
	sec := flat(releaseSection(t, "1.0.13"))
	check := startCommand(t, "sha256sum")
	if check == "" || !strings.Contains(sec, flat(check)) {
		t.Errorf("DOCS-NOTES-LINUX-SUM: RELEASE_NOTES.md ## 1.0.13 does not give the Linux README's check of a download, %q", check)
	}
	rm := ""
	if m := regexp.MustCompile("`(rm -r [^`]+)`").FindStringSubmatch(string(mustRead(t, "packaging/linux/README.md"))); m != nil {
		rm = m[1]
	}
	if rm == "" || !strings.Contains(sec, flat(rm)) {
		t.Errorf("DOCS-NOTES-LINUX-RM: RELEASE_NOTES.md ## 1.0.13 does not give the Linux README's command for what an install or an upgrade leaves behind, %q", rm)
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
	// on it; 1.0.13's release page and the READMEs do.
	for _, d := range docs {
		if !tellsTheRentalPort(d.name) {
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

// The rental port aims for one share every target_time seconds of the shipped template (which the
// Windows and Linux configs are held to), where MiningRigRentals asks for one every 10 to 60
// seconds at the rig's advertised hashrate. Every text that gives the rental port's share time
// gives that one, and the READMEs and 1.0.13's release page give it. The README said a large
// connection opened at the 1024 floor, the miners' port's; the rental port opens at 500,000.
// MiningRigRentals' range does not follow every listing's advertised hashrate, and a rig under
// about 36 TH/s sits above it at the floor, so no text says its warning goes away, and 1.0.13's
// release page says when it can stay.
func TestRentalPortDocsGiveTheShippedShareTime(t *testing.T) {
	var cfg struct {
		Rental struct {
			Vardiff struct {
				MinDiff    float64 `yaml:"min_diff"`
				TargetTime int     `yaml:"target_time"`
			} `yaml:"vardiff"`
		} `yaml:"stratum_rental"`
	}
	if err := yaml.Unmarshal(mustRead(t, "docker/stratum/config.template.yaml"), &cfg); err != nil {
		t.Fatal(err)
	}
	target := strconv.Itoa(cfg.Rental.Vardiff.TargetTime)
	if cfg.Rental.Vardiff.TargetTime <= 0 || cfg.Rental.Vardiff.MinDiff != 500000 {
		t.Fatalf("DOCS-RENTAL-CONFIG: the template's rental port has target_time %s and min_diff %g", target, cfg.Rental.Vardiff.MinDiff)
	}
	shareTime := regexp.MustCompile(`(?i)\bone share every (\d+) (?:s|seconds)\b`)
	marketplace := regexp.MustCompile(`(?i)NiceHash|MiningRigRentals|\bMRR\b`)
	rental := regexp.MustCompile(`(?i)\b3335\b|\brental|NiceHash|MiningRigRentals|\bMRR\b`)
	promise := regexp.MustCompile(`(?i)\bno longer (warns|flags)|\bnever warns|\bwarning\b[^.;]*\b(goes away|go away|is gone|disappears)`)
	largeAtFloor := regexp.MustCompile(`(?i)\b(large|rental|rented|3335)\b[^.;]*\bopens? at the 1,?024\b`)
	for _, d := range allDocs(t) {
		said := false
		for _, s := range sentences(d.text) {
			if rental.MatchString(s) {
				for _, m := range shareTime.FindAllStringSubmatch(s, -1) {
					if m[1] == target {
						said = true
					} else {
						t.Errorf("DOCS-RENTAL-TIME-WRONG: %s gives the rental port one share every %s s; it ships %s s: %q", d.name, m[1], target, s)
					}
				}
			}
			if marketplace.MatchString(s) && promise.MatchString(s) {
				t.Errorf("DOCS-RENTAL-PROMISE: %s says a marketplace's warning goes away, which a few listings and small rigs still get: %q", d.name, s)
			}
			if largeAtFloor.MatchString(s) {
				t.Errorf("DOCS-RENTAL-FLOOR: %s says a large or rented connection opens at 1024; the rental port opens at 500,000: %q", d.name, s)
			}
		}
		if !said && tellsTheRentalPort(d.name) {
			t.Errorf("DOCS-RENTAL-TIME: %s does not say the rental port aims for one share every %s s", d.name, target)
		}
	}
	if !strings.Contains(flat(string(mustRead(t, "README.md"))), "1024 on 3333 and 500,000 on 3335") {
		t.Error("DOCS-RENTAL-FLOOR-README: README.md's Difficulty does not say where each port opens: 1024 on 3333 and 500,000 on 3335")
	}
	sec := flat(releaseSection(t, "1.0.13"))
	for _, want := range []string{"A rig under about 36 TH/s stays above the range at the 500,000 floor",
		"a few listings give a range that does not follow the advertised hashrate"} {
		if !strings.Contains(sec, want) {
			t.Errorf("DOCS-RENTAL-CAVEAT: RELEASE_NOTES.md ## 1.0.13 does not say when MiningRigRentals' warning can stay: %q", want)
		}
	}
}

// The release page names the dashboard's texts as the dashboard shows them: the TIDES card's tiles,
// which count a payout at 2 confirmations, and the heading of the Blocks table in TIDES mode. "Paid
// to you (confirmed)" was the card's name, while the same page calls a solo block confirmed after
// 100. The page says which API times are in UTC now. A slow pool still holds up a new block's work
// for seconds, so no text says it no longer can. 1.0.15's page names the line the card now has
// under "The next pool block pays you".
func TestReleaseNotesNameTheDashboardAsItShows(t *testing.T) {
	sec := flat(releaseSection(t, "1.0.13"))
	solo := string(mustRead(t, "web/dist/solo.html"))
	js := string(mustRead(t, "web/dist/js/pool-solo-inline.js"))
	for _, c := range []struct{ code, version, label, file, src string }{
		{"DOCS-TIDES-PENDING", "1.0.13", "Pending (under 2 confirmations)", "web/dist/solo.html", solo},
		{"DOCS-TIDES-PAID", "1.0.13", "Paid to you (2+ confirmations)", "web/dist/solo.html", solo},
		{"DOCS-SOLO-BLOCKS", "1.0.13", "Your Solo Blocks", "web/dist/js/pool-solo-inline.js", js},
		{"DOCS-TIDES-NEXT", "1.0.15", "The next pool block pays you", "web/dist/solo.html", solo},
		{"DOCS-TIDES-NEXT-NOW", "1.0.15", "if found now", "web/dist/solo.html", solo},
		{"DOCS-TIDES-PAID", "1.0.15", "Paid to you (2+ confirmations)", "web/dist/solo.html", solo},
	} {
		if !strings.Contains(flat(releaseSection(t, c.version)), `"`+c.label+`"`) {
			t.Errorf("%s: RELEASE_NOTES.md ## %s does not name %q", c.code, c.version, c.label)
		}
		if !strings.Contains(c.src, c.label) {
			t.Errorf("%s-PAGE: %s no longer shows %q, which the release notes name", c.code, c.file, c.label)
		}
	}
	for _, d := range allDocs(t) {
		if strings.Contains(flat(d.text), "Paid to you (confirmed)") {
			t.Errorf("DOCS-TIDES-PAID-OLD: %s names the TIDES card's old tile, Paid to you (confirmed)", d.name)
		}
	}
	if !strings.Contains(sec, "Times in the API are in UTC on every platform, a miner's last share and connection times included") {
		t.Error("DOCS-API-UTC: RELEASE_NOTES.md ## 1.0.13 does not say that a miner's last share and connection times are in UTC too")
	}
	holdUp := regexp.MustCompile(`(?i)no longer hold(s)? up new work`)
	for _, d := range releaseTexts(t) {
		if m := holdUp.FindString(flat(d.text)); m != "" {
			t.Errorf("DOCS-POOL-HOLDUP: %s says a slow pool %q; a new block's work can still wait for it for seconds", d.name, m)
		}
	}
}

// The store's description and update notes keep their lists one item to a line and their
// paragraphs apart. umbrelOS shows both as plain text with every line break kept, not as Markdown.
// In a folded block, lines at the block's own indentation are joined into one: the store showed
// "Includes: - A built-in BCH2 full node ... - A built-in 1175 (ESF) node ...". One empty line
// after such a line folds into a single line break, so paragraphs set apart by one ran together on
// the app page; only after a more-indented line, the end of a list, does one empty line keep a
// blank line. The update screen is short.
func TestUmbrelStoreTextReadsAsWritten(t *testing.T) {
	m := readUmbrelManifest(t)
	if m.Version != "1.0.15" {
		t.Errorf("DOCS-UMBREL-VERSION: umbrel-app.yml is version %q, not 1.0.15", m.Version)
	}
	joined := regexp.MustCompile(`\S[ \t]+-[ \t]+[A-Z]`)
	item := regexp.MustCompile(`(?m)^[ \t]*- \S`)
	head := func(s string) string {
		if r := []rune(s); len(r) > 50 {
			return string(r[:50]) + "..."
		}
		return s
	}
	for _, d := range []docText{{"description", m.Description}, {"releaseNotes", m.ReleaseNotes}} {
		lines := strings.Split(d.text, "\n")
		for i, l := range lines {
			if joined.MatchString(l) {
				t.Errorf("DOCS-UMBREL-LIST: %s has list items run together on one line: %q", d.name, l)
			}
			if i == 0 {
				continue
			}
			// A line right under a line of text belongs to its paragraph only when it is a list
			// item; two empty lines show as two blank lines.
			if prev := lines[i-1]; prev != "" && l != "" && !item.MatchString(l) {
				t.Errorf("DOCS-UMBREL-PARAGRAPH: %s shows %q right under %q, with no blank line between", d.name, head(l), head(prev))
			} else if prev == "" && l == "" {
				t.Errorf("DOCS-UMBREL-BLANK: %s shows two blank lines in a row", d.name)
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
// More info, then Run anyway, at SmartScreen's warning; and where the steps are. From 1.0.15 the
// page says, in the README's words, to turn Smart App Control off only if it blocks Forge Solo: on
// a Windows 11 PC with it On, the 1.0.13 installer installed and Forge Solo ran. A page resolves a
// relative link against its own address, under /releases/tag/, where no file of the repository
// is, so its links are whole addresses.
func TestReleaseNotesTellWindowsUsersHowToInstall(t *testing.T) {
	for _, c := range []struct{ version, sac string }{
		{"1.0.13", "on Windows 11, Smart App Control must be Off"},
		{"1.0.15", "If Smart App Control on Windows 11 blocks the installer or Forge Solo, turn Smart App Control off"},
	} {
		sec := releaseSection(t, c.version)
		text := flat(sec)
		for _, want := range []string{
			c.sac,
			"choose More info, then Run anyway",
			"[Install on Windows](" + repoURL + "#install-on-windows)",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("DOCS-RELEASE-WIN-INSTALL: RELEASE_NOTES.md ## %s does not say %q", c.version, want)
			}
		}
	}
	for _, d := range releaseTexts(t) {
		for _, m := range regexp.MustCompile(`\]\(([^)\s]*)\)`).FindAllStringSubmatch(d.text, -1) {
			if !strings.HasPrefix(m[1], "https://") {
				t.Errorf("DOCS-RELEASE-LINK: %s links to %q, which the release page cannot open", d.name, m[1])
			}
		}
	}
}

// A TIDES job that committed to nothing was credited at the pool's 1,024 a share: 0.2% of the work
// of a rental's 500,000 shares. A miner at or near the 1024 floor was credited in full on such a
// job, and a big miner after a restart lost part of its first jobs at a rate of its own, so the
// 0.2% is told of rentals alone.
func TestReleaseNotesGiveTheConnectCreditOfRentals(t *testing.T) {
	anyMiner := regexp.MustCompile(`(?i)\b(any|every|all|each) miners?\b`)
	for _, d := range releaseTexts(t) {
		for _, s := range sentences(d.text) {
			if strings.Contains(s, "0.2%") && anyMiner.MatchString(s) {
				t.Errorf("DOCS-TIDES-ANY-MINER: %s says more than a rental was credited 0.2%% of its first jobs: %q", d.name, s)
			}
		}
	}
}

// A share refused over the rate limit is counted but not logged: a miner over the limit sends a
// hundred a second. The notes said every kind of refused share now leaves a line, and then that
// those over the limit are counted, which reads as logged too.
func TestReleaseNotesSayRateLimitedSharesAreNotLogged(t *testing.T) {
	every := regexp.MustCompile(`(?i)\b(every|all|each)( kind of)? refused shares?\b`)
	logged := regexp.MustCompile(`(?i)\b(line|logged)\b`)
	for _, d := range releaseTexts(t) {
		for _, s := range sentences(d.text) {
			if every.MatchString(s) && logged.MatchString(s) {
				t.Errorf("DOCS-REJECT-EVERY: %s says every refused share leaves a line; one over the rate limit does not: %q", d.name, s)
			}
		}
	}
	told := false
	for _, s := range sentences(releaseSection(t, "1.0.13")) {
		if !strings.Contains(s, "refused over the rate limit") {
			continue
		}
		told = true
		if !strings.Contains(s, "not logged") {
			t.Errorf("DOCS-REJECT-RATE: RELEASE_NOTES.md ## 1.0.13 tells of shares refused over the rate limit without saying they are not logged: %q", s)
		}
	}
	if !told {
		t.Error("DOCS-REJECT-RATE: RELEASE_NOTES.md ## 1.0.13 no longer tells of shares refused over the rate limit")
	}
}

// At one share every 25 s a rental sends about 2 shares a minute instead of 11, so its 5-minute
// hashrate, on the dashboard and on MiningRigRentals, swings about 30% either way instead of 13%.
// In TIDES mode a job commits to the highest difficulty its miners work at, on either port, so
// while a rental hashes every miner's shares, those on 3333 too, are forwarded 4 to 8 times less
// often. Only the difficulty of the miners on 3333 is unchanged, and the notes say that of their
// difficulty alone.
func TestReleaseNotesTellTheRentalPortsSideEffects(t *testing.T) {
	port3333 := regexp.MustCompile(`\b3333\b`)
	unchanged := regexp.MustCompile(`(?i)\bunchanged\b`)
	for _, d := range releaseTexts(t) {
		for _, s := range sentences(d.text) {
			if port3333.MatchString(s) && unchanged.MatchString(s) && !strings.Contains(strings.ToLower(s), "difficulty") {
				t.Errorf("DOCS-RENTAL-3333-SCOPE: %s says the miners on 3333 are unchanged; in TIDES mode a rental changes how often their shares are forwarded: %q", d.name, s)
			}
		}
	}
	sec := flat(releaseSection(t, "1.0.13"))
	for _, c := range []struct{ code, want string }{
		{"DOCS-RENTAL-NOISE", "a rental's 5-minute hashrate, on the dashboard and in MiningRigRentals' own figure, varies by about 30% either way instead of 13%"},
		{"DOCS-RENTAL-TIDES-COMMIT", "while a rental hashes it commits to the rental's level (2^26 to 2^27 instead of 2^24 for 4.5 PH/s)"},
		{"DOCS-RENTAL-TIDES-COMMIT", "your own on 3333 included, are forwarded 4 to 8 times less often"},
	} {
		if !strings.Contains(sec, c.want) {
			t.Errorf("%s: RELEASE_NOTES.md ## 1.0.13 does not say %q", c.code, c.want)
		}
	}
}

// A port's log has two budgets for the lines clients cause: a miner's login, refused shares,
// difficulty changes and disconnect have minerLogBurst lines at once and then minerLogRate a
// minute, and the lines about connections serverLogBudget a minute. Lines left out are counted in a
// line of their own at the cleanup round, every shareCleanupEvery. The release page gives the
// figures server.go has. No release text gives one figure for a whole port: the notes said a client
// could put at most 120 lines a minute per port in the log, and a miner's own lines go past that.
func TestReleaseNotesGiveTheLogBudgets(t *testing.T) {
	src := string(mustRead(t, "internal/stratum/server.go"))
	num := func(re string) string {
		t.Helper()
		m := regexp.MustCompile(re).FindStringSubmatch(src)
		if m == nil {
			t.Fatalf("DOCS-LOG-CODE: internal/stratum/server.go has nothing matching %s", re)
		}
		return m[1]
	}
	burst := num(`(?m)^\s*minerLogBurst\s*=\s*(\d+)$`)
	rate := num(`(?m)^\s*minerLogRate\s*=\s*(\d+)$`)
	conns := num(`(?m)^const serverLogBudget = (\d+)$`)
	every := num(`(?m)^var shareCleanupEvery = (\d+) \* time\.Second$`)
	sec := flat(releaseSection(t, "1.0.13"))
	for _, c := range []struct{ code, want string }{
		{"DOCS-LOG-MINERS", "have a budget of their own on each port, " + burst + " lines at once and then " + rate + " a minute"},
		{"DOCS-LOG-CONNS", "apart from the lines about connections (still " + conns + " a minute)"},
		{"DOCS-LOG-LEFT-OUT", "Lines left out are counted in a line of their own within " + every + " seconds"},
	} {
		if !strings.Contains(sec, c.want) {
			t.Errorf("%s: RELEASE_NOTES.md ## 1.0.13 does not say %q, as server.go has it", c.code, c.want)
		}
	}
	perPort := regexp.MustCompile(`(?i)\b(at most|up to|no more than) \d+ (log )?lines a minute (per|on each|on a|a) port\b`)
	for _, d := range releaseTexts(t) {
		if m := perPort.FindString(flat(d.text)); m != "" {
			t.Errorf("DOCS-LOG-PORT-ONE: %s says what a client puts in the log is %q; a miner's own lines have a budget of their own beside it", d.name, m)
		}
	}
}

// Vardiff times each share at the difficulty it was found at, over every share of the last
// VardiffSampleTime, or the latest VardiffSampleShares where those are more. A port whose config
// sets no variance_percent keeps VardiffVariancePercent, and the shipped template sets none for the
// rental port. The release page gives these as server.go and the template have them, and the
// figures of a steady miner on each port as the simulation gives them. Measured over its latest 30
// shares alone, a steady miner on 3333 changed difficulty about 20 times an hour and went up to
// 1.65 times its level; no release text gives those figures.
func TestReleaseNotesGiveTheVardiffWindow(t *testing.T) {
	src := string(mustRead(t, "internal/stratum/server.go"))
	num := func(re string) float64 {
		t.Helper()
		m := regexp.MustCompile(re).FindStringSubmatch(src)
		if m == nil {
			t.Fatalf("DOCS-VARDIFF-CODE: internal/stratum/server.go has nothing matching %s", re)
		}
		f, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			t.Fatalf("DOCS-VARDIFF-CODE: %s: %v", re, err)
		}
		return f
	}
	window := int(num(`(?m)^\s*VardiffSampleTime\s*=\s*(\d+) \* time\.Second$`))
	shares := strconv.Itoa(int(num(`(?m)^\s*VardiffSampleShares\s*=\s*(\d+)$`)))
	band := strconv.Itoa(int(num(`(?m)^\s*VardiffVariancePercent\s*=\s*([0-9.]+)\b`)*100 + 0.5))
	if window%60 != 0 {
		t.Fatalf("DOCS-VARDIFF-CODE: VardiffSampleTime is %d s, which the release notes cannot give in whole minutes", window)
	}
	var cfg struct {
		Rental struct {
			Vardiff map[string]any `yaml:"vardiff"`
		} `yaml:"stratum_rental"`
	}
	if err := yaml.Unmarshal(mustRead(t, "docker/stratum/config.template.yaml"), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Rental.Vardiff) == 0 {
		t.Fatal("DOCS-VARDIFF-RENTAL-SHIPPED: docker/stratum/config.template.yaml has no stratum_rental.vardiff")
	}
	if v, ok := cfg.Rental.Vardiff["variance_percent"]; ok {
		t.Errorf("DOCS-VARDIFF-RENTAL-SHIPPED: the template sets the rental port's variance_percent to %v; the release notes say no shipped config sets it", v)
	}
	sec := flat(releaseSection(t, "1.0.13"))
	for _, c := range []struct{ code, want string }{
		{"DOCS-VARDIFF-WINDOW", "Vardiff now times each share at the difficulty it was found at, over every share of the last " + strconv.Itoa(window/60) + " minutes"},
		{"DOCS-VARDIFF-SHARES", "minutes, or the latest " + shares + " where those are more"},
		{"DOCS-VARDIFF-STEADY-3333", "A steady miner's difficulty stays between about 0.75 and 1.35 times its level on 3333 and changes about 8 times an hour"},
		{"DOCS-VARDIFF-STEADY-3335", "on 3335 it stays between about 0.7 and 1.8 times its level and changes about 3 times an hour"},
		{"DOCS-VARDIFF-RENTAL-BAND", "the rental port, 3335, reads stratum_rental.vardiff.variance_percent as the main port reads its own; no shipped config sets it, so 3335 keeps its +/-" + band + "% band"},
	} {
		if !strings.Contains(sec, c.want) {
			t.Errorf("%s: RELEASE_NOTES.md ## 1.0.13 does not say %q, as the code has it", c.code, c.want)
		}
	}
	old := regexp.MustCompile(`(?i)\b20 times an hour|\b1\.65 times|not only the latest`)
	for _, d := range releaseTexts(t) {
		if m := old.FindString(flat(d.text)); m != "" {
			t.Errorf("DOCS-VARDIFF-OLD: %s says %q, a figure of vardiff over the latest %s shares alone", d.name, m, shares)
		}
	}
}

// Each worker's best share is kept in forgesolo.db. A worker with a share in the last
// WorkerRetention is always kept; of the others, a miner keeps its best one and the ones seen last,
// MaxKeptWorkers in all, and the MaxKeptMiners miners seen last keep theirs. The release page gives
// the figures internal/stats has, and names the dashboard's column as the page heads it.
func TestReleaseNotesTellWhichBestSharesAreKept(t *testing.T) {
	src := string(mustRead(t, "internal/stats/best_shares.go")) + string(mustRead(t, "internal/stats/constants.go"))
	num := func(re string) string {
		t.Helper()
		m := regexp.MustCompile(re).FindStringSubmatch(src)
		if m == nil {
			t.Fatalf("DOCS-ATH-CODE: internal/stats has nothing matching %s", re)
		}
		return m[1]
	}
	workers := num(`(?m)^\s*MaxKeptWorkers\s*=\s*(\d+)$`)
	miners := num(`(?m)^\s*MaxKeptMiners\s*=\s*(\d+)$`)
	if !regexp.MustCompile(`(?m)^\s*WorkerRetention\s*=\s*24 \* time\.Hour$`).MatchString(src) {
		t.Error("DOCS-ATH-DAY-CODE: internal/stats/constants.go no longer keeps a silent worker for 24 hours, while the release notes say one with a share in the last day is always kept")
	}
	column := "Best Diff (all time)"
	if !strings.Contains(string(mustRead(t, "web/dist/solo.html")), ">"+column+"</th>") {
		t.Errorf("DOCS-ATH-COLUMN-PAGE: web/dist/solo.html no longer heads a column %q, which the release notes name", column)
	}
	sec := flat(releaseSection(t, "1.0.13"))
	for _, c := range []struct{ code, want string }{
		{"DOCS-ATH-KEPT", "each worker's best share (Best Diff in the dashboard's Workers table, athDiff in the API) is kept in forgesolo.db, so a restart, an update or a reboot no longer resets it"},
		{"DOCS-ATH-COLUMN", `the column now reads "` + column + `"`},
		{"DOCS-ATH-DAY", "A worker with a share in the last day is always kept, also across a restart"},
		{"DOCS-ATH-WORKERS", "each payout address keeps its best one and the ones seen last, " + workers + " in all"},
		{"DOCS-ATH-MINERS", "and the " + miners + " payout addresses seen last keep theirs"},
		{"DOCS-ATH-API", "a miner's athDiff in /api/v1/miners/<address> is its best share of all time"},
		{"DOCS-ATH-ROLLBACK", "going back to 1.0.12 and forward again keeps the best shares 1.0.13 kept"},
	} {
		if !strings.Contains(sec, c.want) {
			t.Errorf("%s: RELEASE_NOTES.md ## 1.0.13 does not say %q", c.code, c.want)
		}
	}
}

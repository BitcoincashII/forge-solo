package forgesolo

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// readmes are the READMEs a user or a builder follows.
var readmes = []string{"README.md", "windows/README.md", "packaging/linux/README.md"}

// repoURL is this repository on GitHub. The release page links to the README by it.
const repoURL = "https://github.com/BitcoincashII/forge-solo"

// A docText is one text users read, and its name in a failure.
type docText struct{ name, text string }

// flat is s as a reader takes it in: without Markdown's emphasis and code marks, and with every
// run of white space one space, so that a phrase matches however the text is wrapped.
func flat(s string) string {
	s = strings.NewReplacer("**", "", "`", "").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

// sentences splits flat text into sentences, and table cells: what one claim can span.
var sentenceEnd = regexp.MustCompile(`[.;!?](\s|$)|\|`)

func sentences(s string) []string {
	return sentenceEnd.Split(flat(s), -1)
}

// The root README is where a user starts on every platform: it has a Windows install path (Smart
// App Control, SmartScreen, the one permission prompt, a Private network, the Settings password),
// says where the Settings password is on each platform, says on each platform what to delete when
// a node's chain data is damaged, and marks what is Umbrel's alone. Before, its setup sections
// were Umbrel's, it listed a postgres container, it never named the password Settings asks for,
// and only Umbrel users were told what to do about a damaged chain: on Windows the 1175 node's
// automatic -reindex cannot rebuild a mainnet chain, and the tray does not always name the folders.
func TestReadmeCoversEveryPlatform(t *testing.T) {
	readme := string(mustRead(t, "README.md"))
	text := flat(readme)
	for _, c := range []struct{ code, want string }{
		{"DOCS-README-WINDOWS", "## Install on Windows"},
		{"DOCS-README-SAC", "Smart App Control settings"},
		{"DOCS-README-SAC", "Windows 10 has no Smart App Control"},
		{"DOCS-README-SMARTSCREEN", "More info, then Run anyway"},
		{"DOCS-README-UAC", "Windows Command Processor"},
		{"DOCS-README-PRIVATE", "Set your network to Private"},
		{"DOCS-README-PASSWORD", "Copy Settings Password"},
		{"DOCS-README-PASSWORD", "Default credentials"},
		{"DOCS-README-PASSWORD", "DASHBOARD_PASSWORD"},
		{"DOCS-README-PORTS", "stratum+tcp://<PC-IP>:3333"},
	} {
		src := text
		if strings.HasPrefix(c.want, "## ") {
			src = readme
		}
		if !strings.Contains(src, c.want) {
			t.Errorf("%s: README.md no longer says %q", c.code, c.want)
		}
	}
	if regexp.MustCompile("(?m)^- `postgres`").MatchString(readme) {
		t.Error("DOCS-README-POSTGRES: README.md lists a postgres container; no database server runs from 1.0.13")
	}
	for _, h := range regexp.MustCompile(`(?m)^## (.*)$`).FindAllStringSubmatch(readme, -1) {
		if regexp.MustCompile(`(?i)^(Security|Ports other apps)`).MatchString(h[1]) && !strings.Contains(h[1], "Umbrel") {
			t.Errorf("DOCS-README-UMBREL-ONLY: README.md's section %q is about Umbrel alone and does not say so", h[1])
		}
	}
	_, chain, ok := strings.Cut(readme, "\n## If a node's chain data is damaged\n")
	chain, _, _ = strings.Cut(chain, "\n## ")
	chain = flat(chain)
	for _, want := range []string{
		"The 1175 node cannot rebuild a mainnet chain",
		"~/umbrel/app-data/bch2-apps-forge-solo/node/{blocks,chainstate}",
		`in %APPDATA%\ForgeSolo, delete elevenseventyfive\blocks and elevenseventyfive\chainstate for the 1175 node`,
		`bch2\blocks and bch2\chainstate for the BCH2 node`,
		"bch2/blocks and bch2/chainstate in the data directory",
	} {
		if !ok || !strings.Contains(chain, want) {
			t.Errorf("DOCS-README-CHAIN: README.md's section \"If a node's chain data is damaged\" does not say %q", want)
		}
	}
}

// The Settings password keeps out other accounts on a computer and other apps on an Umbrel. A
// program running under the user's own account can read secrets.env, so no text says programs
// do not have the password, or that they could change the payout address before it.
var passwordOverclaim = regexp.MustCompile(`(?i)\bprograms\b[^.;]*\b(do not|don't|cannot) (have|read|get)\b|other programs[^.;]*could change your payout address`)

func checkPasswordScope(t *testing.T, docs []docText) {
	t.Helper()
	for _, d := range docs {
		for _, s := range sentences(d.text) {
			if passwordOverclaim.MatchString(s) {
				t.Errorf("DOCS-PASSWORD-SCOPE: %s says the Settings password keeps out programs, which can read it under the user's own account: %q", d.name, s)
			}
		}
	}
}

func TestReadmesDoNotOverstateThePassword(t *testing.T) {
	var docs []docText
	for _, f := range readmes {
		docs = append(docs, docText{f, string(mustRead(t, f))})
	}
	checkPasswordScope(t, docs)
}

// The Windows README describes the build as it is: services on SQLite with the migrator beside
// them, PostgreSQL only for a move, the junction folder named from the account's SID, and Smart
// App Control as Microsoft documents it. Smart App Control checks every program and DLL that
// loads, so a trusted certificate on the installer alone would still leave the programs it installs
// blocked; text that said such a certificate lets Smart App Control run Forge Solo could steer the
// choice of certificate.
func TestWindowsReadmeDescribesTheSQLiteBuild(t *testing.T) {
	readme := string(mustRead(t, "windows/README.md"))
	text := flat(readme)
	builds := 0
	for _, l := range strings.Split(readme, "\n") {
		if strings.Contains(l, "go build") && regexp.MustCompile(`\./cmd/(stratum|api|forge-solo-migrate)\b`).MatchString(l) {
			builds++
			if !strings.Contains(l, "-tags sqlite") {
				t.Errorf("DOCS-WIN-BUILD-TAGS: windows/README.md builds a service without -tags sqlite, so it would look for a PostgreSQL server: %q", strings.TrimSpace(l))
			}
		}
	}
	if builds != 3 {
		t.Errorf("DOCS-WIN-BUILD-TAGS: windows/README.md builds %d of stratum.exe, api.exe and forge-solo-migrate.exe, want all 3", builds)
	}
	for _, s := range []struct {
		code string
		re   *regexp.Regexp
	}{
		{"DOCS-WIN-PG-RUNS", regexp.MustCompile(`(?i)orchestrates a bundled PostgreSQL|lacks the database password while the database exists|chainstate, and the database\)|database never started\. Its paths`)},
		{"DOCS-WIN-INITDB", regexp.MustCompile(`\.\./init-db\.sql`)},
		{"DOCS-WIN-STALE-HEADING", regexp.MustCompile(`(?i)Not yet done \(pre public release\)`)},
		{"DOCS-WIN-OVEV", regexp.MustCompile(`(?i)would remove the warning`)},
		{"DOCS-WIN-SAC-FILES", regexp.MustCompile(`(?i)let(s)? Smart App Control run Forge Solo`)},
		{"DOCS-WIN-LINKS", regexp.MustCompile(`(?i)links\\<account>`)},
	} {
		if m := s.re.FindString(text); m != "" {
			t.Errorf("%s: windows/README.md still says %q", s.code, m)
		}
	}
	for _, c := range []struct{ code, want string }{
		{"DOCS-WIN-SAC", "Windows treats a self-signed signature the same as none"},
		{"DOCS-WIN-SAC", "it blocks the installer and the programs it installs"},
		{"DOCS-WIN-SAC", "Windows 10 has none"},
		{"DOCS-WIN-OVEV", "SmartScreen would still warn until reputation builds"},
		{"DOCS-WIN-OVEV", "EV certificate no longer skips that"},
		{"DOCS-WIN-SAC-FILES", "Smart App Control runs the files signed with it"},
		{"DOCS-WIN-SAC-FILES", "Smart App Control checks every program and DLL that loads, not only the installer"},
		{"DOCS-WIN-SAC-FILES", "would need that signature: Forge Solo's own, the two nodes', and PostgreSQL's"},
		{"DOCS-WIN-LINKS", "named from the SHA-256 of the account's SID"},
		{"DOCS-WIN-PG-ONCE", "PostgreSQL runs only to move the data of Forge Solo 1.0.12 and earlier"},
		{"DOCS-WIN-PG-ONCE", "the launcher deletes {app}\\pgsql and pglog.txt"},
	} {
		if !strings.Contains(text, c.want) {
			t.Errorf("%s: windows/README.md no longer says %q", c.code, c.want)
		}
	}
}

// The Linux README has the kernel the node needs, the upgrade of a copy run without the service,
// and every file Forge Solo keeps in the data directory, the two lock files among them: deleting
// one while Forge Solo runs lets a second program take the database or the data directory.
func TestLinuxReadmeIsComplete(t *testing.T) {
	readme := string(mustRead(t, "packaging/linux/README.md"))
	text := flat(readme)
	if !strings.Contains(text, "kernel 3.17 or newer") || regexp.MustCompile(`3\.2 or newer`).MatchString(text) {
		t.Error("DOCS-LINUX-KERNEL: packaging/linux/README.md must say kernel 3.17 or newer: the node stops at once on an older one")
	}
	if !strings.Contains(text, "To upgrade a copy you run yourself") || !strings.Contains(text, "run ./forge-solo from the new release's folder") {
		t.Error("DOCS-LINUX-UPGRADE: packaging/linux/README.md no longer says how to upgrade a copy run without the service")
	}
	_, files, _ := strings.Cut(readme, "## Files")
	files, _, _ = strings.Cut(files, "\n## ")
	for _, f := range []string{"forgesolo.db.inuse", "forge-solo.lock", "forgesolo.db", "secrets.env"} {
		if !regexp.MustCompile("(?m)^\\| `" + regexp.QuoteMeta(f) + "`").MatchString(files) {
			t.Errorf("DOCS-LINUX-FILES: packaging/linux/README.md's Files table has no row for %s", f)
		}
	}
}

// checkPlainPunctuation fails the test for an em-dash in a doc, or " -- " used for one outside
// code.
func checkPlainPunctuation(t *testing.T, docs []docText) {
	t.Helper()
	for _, d := range docs {
		if strings.ContainsRune(d.text, '—') {
			t.Errorf("DOCS-EMDASH: %s has an em-dash", d.name)
		}
		fenced := false
		for _, l := range strings.Split(d.text, "\n") {
			if strings.HasPrefix(strings.TrimSpace(l), "```") {
				fenced = !fenced
				continue
			}
			if !fenced && strings.Contains(l, " -- ") {
				t.Errorf("DOCS-DOUBLE-DASH: %s has \" -- \" for a dash: %q", d.name, strings.TrimSpace(l))
			}
		}
	}
}

// The READMEs use plain punctuation, as the dashboard and the release notes do.
func TestReadmesUsePlainPunctuation(t *testing.T) {
	var docs []docText
	for _, f := range readmes {
		docs = append(docs, docText{f, string(mustRead(t, f))})
	}
	checkPlainPunctuation(t, docs)
}

// slug is the anchor GitHub gives a Markdown heading.
func slug(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(heading)) {
		switch {
		case r == ' ':
			b.WriteRune('-')
		case r == '-' || r == '_' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r > 0x7f:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// anchors are the anchors of a Markdown file's headings.
func anchors(t *testing.T, path string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	fenced := false
	for _, l := range strings.Split(string(mustRead(t, path)), "\n") {
		if strings.HasPrefix(l, "```") {
			fenced = !fenced
		}
		if h, ok := strings.CutPrefix(l, "#"); ok && !fenced {
			out[slug(strings.TrimLeft(h, "# "))] = true
		}
	}
	return out
}

// Every link to a heading, in the same file or another of the docs, leads somewhere; so does a
// link to this repository's README or files on GitHub. The Windows README linked "#build", which
// no heading had.
func TestDocsLinksLeadToHeadings(t *testing.T) {
	link := regexp.MustCompile(`\]\(([^)#\s]*)#([^)\s]+)\)`)
	for _, f := range append([]string{"RELEASE_NOTES.md"}, readmes...) {
		for _, m := range link.FindAllStringSubmatch(string(mustRead(t, f)), -1) {
			target := f
			switch {
			case m[1] == repoURL || m[1] == repoURL+"/":
				target = "README.md"
			case strings.HasPrefix(m[1], repoURL+"/blob/main/"):
				target = strings.TrimPrefix(m[1], repoURL+"/blob/main/")
			case strings.Contains(m[1], "://"):
				continue
			case m[1] != "":
				target = filepath.ToSlash(filepath.Join(filepath.Dir(f), m[1]))
			}
			if _, err := os.Stat(target); err != nil {
				t.Errorf("DOCS-ANCHOR: %s links to %s, which is not there", f, m[0])
				continue
			}
			if !anchors(t, target)[m[2]] {
				t.Errorf("DOCS-ANCHOR: %s links to %s, and %s has no such heading", f, m[0], target)
			}
		}
	}
}

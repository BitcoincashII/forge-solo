package forgesolo

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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

// The root README is where a user starts on every platform: it has a Windows install path
// (SmartScreen, what to do if Smart App Control blocks Forge Solo, the one permission prompt, a
// Private network, the Settings password), says where the Settings password is on each platform,
// says on each platform what to delete when a node's chain data is damaged, and marks what is
// Umbrel's alone. Before, its setup sections were Umbrel's, it listed a postgres container, it
// never named the password Settings asks for, and only Umbrel users were told what to do about a
// damaged chain: on Windows the 1175 node's automatic -reindex cannot rebuild a mainnet chain, and
// the tray does not name the folders. It then said Smart App Control must be Off, though on a
// Windows 11 PC with it On the 1.0.13 installer and Forge Solo ran.
func TestReadmeCoversEveryPlatform(t *testing.T) {
	readme := string(mustRead(t, "README.md"))
	text := flat(readme)
	for _, c := range []struct{ code, want string }{
		{"DOCS-README-WINDOWS", "## Install on Windows"},
		{"DOCS-README-SAC", "If Smart App Control on Windows 11 blocks the installer or Forge Solo, turn Smart App Control off"},
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

// The tray shows at most tipMax characters (windows/launcher/tips.go): it says what is wrong and
// where to look, and launcher.log says what to do, which folders to delete for a damaged chain
// among it. The docs said the tray named them. The launcher opens the dashboard at
// /solo?v=<version>, which its server answers with Clear-Site-Data, so that the browser drops the
// pages it kept from the version before; the Windows README said only that the address was new.
func TestWindowsDocsSayWhatTheLauncherDoes(t *testing.T) {
	folders := regexp.MustCompile(`(?i)folders to delete|which folders`)
	for _, d := range allDocs(t) {
		for _, s := range sentences(d.text) {
			if strings.Contains(s, "tray") && folders.MatchString(s) && !strings.Contains(s, "launcher.log") {
				t.Errorf("DOCS-WIN-CHAIN-TRAY: %s says the tray names the folders to delete; launcher.log does: %q", d.name, s)
			}
		}
	}
	tips := string(mustRead(t, "windows/launcher/tips.go"))
	if !strings.Contains(tips, `"'s chain is damaged: see launcher.log"`) {
		t.Error("DOCS-WIN-CHAIN-TRAY-CODE: tips.go no longer says a node's chain is damaged, see launcher.log")
	}
	_, chain, _ := strings.Cut(string(mustRead(t, "README.md")), "\n## If a node's chain data is damaged\n")
	chain, _, _ = strings.Cut(chain, "\n## ")
	if !strings.Contains(flat(chain), "Windows: the tray says the node's chain is damaged") {
		t.Error("DOCS-README-CHAIN-TRAY: README.md does not say what the tray shows when a node's chain data is damaged on Windows")
	}

	win := flat(string(mustRead(t, "windows/README.md")))
	for _, want := range []string{"Forge Solo opens the dashboard at /solo?v=<version>", `Clear-Site-Data: "cache"`} {
		if !strings.Contains(win, want) {
			t.Errorf("DOCS-WIN-DASH-VERSION: windows/README.md does not say %q", want)
		}
	}
	if !strings.Contains(string(mustRead(t, "windows/launcher/main.go")), `"/solo?v=" + url.QueryEscape(version)`) ||
		!strings.Contains(string(mustRead(t, "windows/launcher/web.go")), "w.Header().Set(\"Clear-Site-Data\", `\"cache\"`)") {
		t.Error("DOCS-WIN-DASH-CODE: the launcher no longer opens /solo?v=<version>, or its server no longer clears the cache for it")
	}
	// Every place the launcher opens the browser opens dashboardURL(), never the bare address.
	bare := regexp.MustCompile(`(?i)\bopens (the dashboard at )?http://127\.0\.0\.1:3080([^/]|$)`)
	for _, d := range []docText{
		{"README.md", string(mustRead(t, "README.md"))},
		{"windows/README.md", string(mustRead(t, "windows/README.md"))},
		{"RELEASE_NOTES.md ## 1.0.13", releaseSection(t, "1.0.13")},
	} {
		if m := bare.FindString(flat(d.text)); m != "" {
			t.Errorf("DOCS-WIN-OPENS-BARE: %s says Forge Solo %q; it opens /solo?v=<version>", d.name, m)
		}
	}

	m := regexp.MustCompile(`(?m)^const tipMax = (\d+)$`).FindStringSubmatch(tips)
	if m == nil {
		t.Fatal("DOCS-WIN-TRAY-MAX: tips.go has no tipMax")
	}
	if want := "at most " + m[1] + " characters"; !strings.Contains(win, want) {
		t.Errorf("DOCS-WIN-TRAY-MAX: windows/README.md does not say the tray's texts are %s, as tips.go makes them", want)
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

// codeLines are the commands in text's fenced code blocks: a line ending in a backslash goes on
// on the next one, and a comment (" # ") and the spaces around are left out.
func codeLines(text string) []string {
	var out []string
	fenced, cont := false, ""
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			fenced = !fenced
			continue
		}
		if !fenced {
			continue
		}
		l, _, _ = strings.Cut(cont+l, " # ")
		cont = ""
		if strings.HasSuffix(l, `\`) {
			cont = strings.TrimSuffix(l, `\`)
			continue
		}
		out = append(out, strings.TrimSpace(l))
	}
	return out
}

// The root README says how to build each part from source and run the tests, with the commands CI
// runs: the Go that go.mod names; each program with no build tag, and with -X main.version where it
// has a version to stamp; each Umbrel image with the Dockerfile, context and build argument
// docker-build.yml gives it; the Linux downloads for the architectures build-release.sh builds; and
// the unit and integration jobs' commands as test.yml runs them. It had no build instructions.
func TestReadmeBuildsAsCIDoes(t *testing.T) {
	readme := string(mustRead(t, "README.md"))
	at, rel := strings.Index(readme, "\n## Build from source\n"), strings.Index(readme, "\n## Releasing\n")
	if at < 0 || rel < 0 || at > rel {
		t.Fatalf("DOCS-BUILD-SECTION: README.md has no section ## Build from source before ## Releasing (at %d and %d)", at, rel)
	}
	_, sec, _ := strings.Cut(readme, "\n## Build from source\n")
	sec, _, _ = strings.Cut(sec, "\n## ")
	code := codeLines(sec)
	has := func(cmd string) bool { return slices.Contains(code, cmd) }

	m := regexp.MustCompile(`(?m)^toolchain (go\S+)$`).FindStringSubmatch(string(mustRead(t, "go.mod")))
	if m == nil || !strings.Contains(flat(sec), "toolchain "+m[1]+":") || !strings.Contains(sec, "GOTOOLCHAIN="+m[1]) {
		t.Errorf("DOCS-BUILD-GO: README.md's Build from source does not name the Go of go.mod's toolchain line (%v)", m)
	}

	goBuild := regexp.MustCompile(`\bgo build\b.*\./cmd/([\w-]+)$`)
	built := map[string]bool{}
	for _, l := range code {
		c := goBuild.FindStringSubmatch(l)
		if c == nil {
			continue
		}
		built[c[1]] = true
		if buildTags.MatchString(l) {
			t.Errorf("DOCS-BUILD-TAG: README.md builds %s with a build tag; there is one build: %q", c[1], l)
		}
		versioned := regexp.MustCompile(`(?m)^var version = `).Match(mustRead(t, "cmd/"+c[1]+"/main.go"))
		if stamps := strings.Contains(l, "-X main.version="); stamps != versioned {
			t.Errorf("DOCS-BUILD-VERSION: README.md stamps a version into %s: %v; cmd/%s/main.go has a version to stamp: %v: %q", c[1], stamps, c[1], versioned, l)
		}
	}
	for _, c := range []string{"stratum", "api", "forge-solo-migrate", "forge-solo-linux", "forge-gateway"} {
		if !built[c] {
			t.Errorf("DOCS-BUILD-PROGRAMS: README.md's Build from source does not build cmd/%s", c)
		}
	}

	images := 0
	for _, s := range loadWorkflow(t, ".github/workflows/docker-build.yml").Jobs["build"].Steps {
		if !strings.HasPrefix(s.Uses, "docker/build-push-action@") {
			continue
		}
		images++
		file := strings.TrimPrefix(fmt.Sprint(s.With["file"]), "./")
		ctx := strings.TrimPrefix(fmt.Sprint(s.With["context"]), "./")
		args, _ := s.With["build-args"].(string)
		found := false
		for _, l := range code {
			f := strings.Fields(l)
			if len(f) < 4 || f[0] != "docker" || f[1] != "build" || !strings.Contains(l, " -f "+file+" ") || f[len(f)-1] != ctx {
				continue
			}
			found = true
			if strings.Contains(l, "--build-arg VERSION=") != strings.Contains(args, "VERSION=") {
				t.Errorf("DOCS-BUILD-IMAGE-ARG: README.md builds %s with build arguments other than docker-build.yml's (%q): %q", file, args, l)
			}
		}
		if !found {
			t.Errorf("DOCS-BUILD-IMAGE: README.md does not build %s with the context %s, as docker-build.yml does", file, ctx)
		}
	}
	if images == 0 {
		t.Error("DOCS-BUILD-IMAGE: docker-build.yml builds no image with docker/build-push-action")
	}

	if a := regexp.MustCompile(`(?m)^ARCHES=\$\{\*:-([^}]+)\}$`).FindStringSubmatch(string(mustRead(t, "scripts/linux/build-release.sh"))); a == nil ||
		!has("for a in "+a[1]+"; do") {
		t.Errorf("DOCS-BUILD-LINUX-ARCHES: README.md does not take the nodes for the architectures build-release.sh builds (%v)", a)
	}

	w := loadWorkflow(t, ".github/workflows/test.yml")
	for _, step := range []string{"Vet", "Test", "Test as 32-bit (386)", "Test with race detector"} {
		if run := strings.TrimSpace(stepRun(t, w, "unit", step)); !has(run) {
			t.Errorf("DOCS-BUILD-UNIT: README.md's Build from source does not run %q, as the unit job does", run)
		}
	}
	if !strings.Contains(stepRun(t, w, "unit", "gofmt (whole tree)"), "$(gofmt -l .)") || !has("gofmt -l .") {
		t.Error("DOCS-BUILD-GOFMT: README.md's Build from source does not run gofmt -l ., as the unit job does")
	}
	if s := regexp.MustCompile(`go install honnef\.co/go/tools/cmd/staticcheck@\S+`).FindString(stepRun(t, w, "unit", "staticcheck")); s == "" ||
		!has(s+` && "$(go env GOPATH)/bin/staticcheck" ./...`) {
		t.Errorf("DOCS-BUILD-STATICCHECK: README.md's Build from source does not run the staticcheck the unit job installs (%q)", s)
	}
	its := 0
	for _, s := range w.Jobs["integration"].Steps {
		if run := strings.TrimSpace(s.Run); run != "" {
			its++
			if !has(run) {
				t.Errorf("DOCS-BUILD-IT: README.md's Build from source does not run %q, as the integration job does", run)
			}
		}
	}
	if its == 0 {
		t.Error("DOCS-BUILD-IT: the integration job runs nothing")
	}
}

// The Windows README describes the build as it is: the services and the migrator as the one build,
// with no tag, PostgreSQL only for a move, the junction folder named from the account's SID, and Smart
// App Control as Microsoft documents it. Smart App Control checks every program and DLL that
// loads, so a trusted certificate on the installer alone could still leave the programs it installs
// blocked; text that said such a certificate lets Smart App Control run Forge Solo could steer the
// choice of certificate. It also runs what Microsoft's cloud service predicts is safe: a Windows 11
// PC with it On ran the 1.0.13 installer and Forge Solo, so the README says it may block them, not
// that it does.
func TestWindowsReadmeDescribesTheSQLiteBuild(t *testing.T) {
	readme := string(mustRead(t, "windows/README.md"))
	text := flat(readme)
	builds := 0
	for _, l := range strings.Split(readme, "\n") {
		if strings.Contains(l, "go build") && regexp.MustCompile(`\./cmd/(stratum|api|forge-solo-migrate)\b`).MatchString(l) {
			builds++
			if strings.Contains(l, "-tags") {
				t.Errorf("DOCS-WIN-BUILD-TAG: windows/README.md builds a service with a build tag; there is one build: %q", strings.TrimSpace(l))
			}
		}
	}
	if builds != 3 {
		t.Errorf("DOCS-WIN-BUILD: windows/README.md builds %d of stratum.exe, api.exe and forge-solo-migrate.exe, want all 3", builds)
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
		{"DOCS-WIN-SAC", regexp.MustCompile(`(?i)Smart App Control blocks Forge Solo while it is On|while it is On, it blocks`)},
		{"DOCS-WIN-LINKS", regexp.MustCompile(`(?i)links\\<account>`)},
	} {
		if m := s.re.FindString(text); m != "" {
			t.Errorf("%s: windows/README.md still says %q", s.code, m)
		}
	}
	for _, c := range []struct{ code, want string }{
		{"DOCS-WIN-SAC", "Windows treats a self-signed signature the same as none"},
		{"DOCS-WIN-SAC", "runs a program that Microsoft's cloud service predicts is safe, or one signed by a certificate Windows trusts"},
		{"DOCS-WIN-SAC", "it may block the installer and the programs it installs"},
		{"DOCS-WIN-SAC", "If it blocks Forge Solo, turn it off in Windows Security"},
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

// While a viewer holds stratum.log or api.log, a move aside that failed is tried again only once
// the log has grown by another twentieth of its 20 MB limit (windows/launcher/log.go). The Windows
// README said the log was moved soon after the viewer was closed, which at the rate stratum.log
// grows outside a rental is days.
func TestWindowsReadmeSaysWhenAHeldLogIsMoved(t *testing.T) {
	win := flat(string(mustRead(t, "windows/README.md")))
	if m := regexp.MustCompile(`(?i)\bsoon after the viewer\b`).FindString(win); m != "" {
		t.Errorf("DOCS-WIN-LOG-SOON: windows/README.md says a held log is moved %q; it is moved at the next 1 MB after", m)
	}
	if want := "The move is tried again each time the log has grown by another 1 MB"; !strings.Contains(win, want) {
		t.Errorf("DOCS-WIN-LOG-RETRY: windows/README.md does not say %q", want)
	}
	code := string(mustRead(t, "windows/launcher/log.go"))
	for _, want := range []string{"const serviceLogLimit = 20 << 20", "l.retryAt = l.size + l.limit/20"} {
		if !strings.Contains(code, want) {
			t.Errorf("DOCS-WIN-LOG-CODE: windows/launcher/log.go no longer has %q, which windows/README.md's 20 MB and 1 MB follow", want)
		}
	}
}

package forgesolo

// The dashboard's pages and style sheet have no type checker and no browser in CI, so the rules
// that went wrong in a shipped build are pinned here as text.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func readWebFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("web/dist/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

type cssRule struct{ media, selectors, body string }

// cssRules splits a style sheet into its rules, one @media level deep, which is all the
// dashboard's style sheets use.
func cssRules(css string) []cssRule {
	css = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(css, "")
	var out []cssRule
	var media string
	for {
		open := strings.IndexByte(css, '{')
		if open < 0 {
			return out
		}
		head := strings.TrimSpace(css[:open])
		if i := strings.LastIndexByte(head, '}'); i >= 0 {
			// Closing braces before this rule end the @media block it was in.
			media = ""
			head = strings.TrimSpace(head[i+1:])
		}
		if strings.HasPrefix(head, "@media") {
			media = head
			css = css[open+1:]
			continue
		}
		end := strings.IndexByte(css[open:], '}')
		if end < 0 {
			return out
		}
		out = append(out, cssRule{media, head, css[open+1 : open+end]})
		css = css[open+end+1:]
	}
}

func hasSelector(r cssRule, sel string) bool {
	for _, s := range strings.Split(r.selectors, ",") {
		if strings.TrimSpace(s) == sel {
			return true
		}
	}
	return false
}

func declares(body, prop, value string) bool {
	re := regexp.MustCompile(`(?:^|;)\s*` + regexp.QuoteMeta(prop) + `\s*:\s*` + regexp.QuoteMeta(value) + `\s*(?:;|$|!)`)
	return re.MatchString(body)
}

// Below 901 px the header's links were hidden for a menu button that no page has, so a phone,
// a tablet or a half-screen window had no way from the dashboard to Settings, and lost the
// SOLO / TIDES badge. The links and the badge stay in the header and wrap under the logo.
func TestHeaderNavIsNeverHidden(t *testing.T) {
	for _, r := range cssRules(readWebFile(t, "css/common.css")) {
		if (hasSelector(r, "nav") || hasSelector(r, "header nav")) && declares(r.body, "display", "none") {
			t.Errorf("NAV-HIDDEN: %q hides the header links (%s), and no page has a menu to open them", r.selectors, r.media)
		}
	}
	for _, page := range []string{"solo.html", "settings.html", "tides.html"} {
		s := readWebFile(t, page)
		nav := regexp.MustCompile(`(?s)<nav\b.*?</nav>`).FindString(s)
		for _, want := range []string{`href="/solo"`, `href="/settings"`} {
			if !strings.Contains(nav, want) {
				t.Errorf("NAV-LINKS: %s's header nav has no %s", page, want)
			}
		}
		if page == "solo.html" && !strings.Contains(nav, `id="modeBadge"`) {
			t.Errorf("NAV-BADGE: the SOLO / TIDES badge is no longer in the dashboard's header nav")
		}
	}
}

// The dashboard's content sits in <div class="container dashboard">: .container gives it its
// side padding, and a padding shorthand on .dashboard, later on the page, took it away, so below
// 1200 px the cards touched the window's edges while the header kept its padding.
func TestDashboardKeepsTheContainersSidePadding(t *testing.T) {
	s := readWebFile(t, "solo.html")
	if !strings.Contains(s, `class="container dashboard"`) {
		t.Fatal("PAD-MARKUP: the dashboard's content is no longer in a .container")
	}
	style := regexp.MustCompile(`(?s)<style>(.*?)</style>`).FindStringSubmatch(s)
	if style == nil {
		t.Fatal("PAD-MARKUP: solo.html has no <style>")
	}
	side := regexp.MustCompile(`(?:^|;)\s*padding(?:-left|-right|-inline)?\s*:`)
	for _, r := range cssRules(style[1]) {
		if hasSelector(r, ".dashboard") && side.MatchString(r.body) {
			t.Errorf("PAD-SIDES: .dashboard sets {%s}, which overrides .container's side padding", r.body)
		}
	}
}

// The "lost its network connection" banner was switched to display:flex but stayed translated
// above the top of the window, for a .show class nothing ever added, so it was never seen.
func TestOfflineBannerComesIntoView(t *testing.T) {
	if !strings.Contains(readWebFile(t, "js/common.js"), "banner.style.display = this.isOnline ? 'none' : 'flex';") {
		t.Error("OFFLINE-TOGGLE: ConnectionStatus no longer shows the banner by its display")
	}
	found := false
	for _, r := range cssRules(readWebFile(t, "css/common.css")) {
		if !hasSelector(r, ".offline-banner") {
			continue
		}
		found = true
		if strings.Contains(r.body, "translate") || declares(r.body, "visibility", "hidden") || declares(r.body, "opacity", "0") {
			t.Errorf("OFFLINE-OFFSCREEN: .offline-banner {%s} keeps the banner out of sight when it is shown", strings.TrimSpace(r.body))
		}
		if !declares(r.body, "justify-content", "center") {
			t.Errorf("OFFLINE-CENTRED: the banner is a flex box, so its text is centred by justify-content")
		}
	}
	if !found {
		t.Error("OFFLINE-OFFSCREEN: common.css has no .offline-banner rule")
	}
}

// branchAfter is the body of the if/else-if branch that head opens, up to the next "} else".
func branchAfter(src, head string) string {
	i := strings.Index(src, head)
	if i < 0 {
		return ""
	}
	rest := src[i+len(head):]
	if j := strings.Index(rest, "} else"); j >= 0 {
		return rest[:j]
	}
	return rest
}

// The API tells a node that is starting (-28, with the step it is on) from one that is not
// answering. The dashboard read both as "Starting the BCH2 node… first launch can take a minute",
// for hours on a crashed node while nothing was mined, and showed "syncing…" in its tiles.
func TestDashboardTellsAStartingNodeFromOneNotAnswering(t *testing.T) {
	js := readWebFile(t, "js/pool-solo-inline.js")
	const startHead, downHead = "} else if (s.status === 'starting') {", "} else if (s.status !== 'synced') {"
	start, down := branchAfter(js, startHead), branchAfter(js, downHead)
	if !strings.Contains(start, "Starting the BCH2 node") || !strings.Contains(start, "escapeHtml(s.message") {
		t.Errorf("NODE-JS-STARTING: no branch for a starting node that says what it is doing: %q", start)
	}
	if !strings.Contains(down, "not answering") || !strings.Contains(down, "nodeDownAdvice()") || strings.Contains(down, "Starting") {
		t.Errorf("NODE-JS-DOWN: a node that is not answering is not said to be: %q", down)
	}
	// Any status other than synced is handled before the branches that assume a synced node.
	if i, j, k := strings.Index(js, startHead), strings.Index(js, downHead), strings.Index(js, "} else if (!minerAddress) {"); i < 0 || j < i || k < j {
		t.Errorf("NODE-JS-ORDER: the starting (%d) and not-answering (%d) branches must come before the synced ones (%d)", i, j, k)
	}
	advice := textBetween(js, "function nodeDownAdvice() {", "\n        }\n")
	for _, p := range []string{"'windows'", "'linux'", "'umbrel'", "launcher.log", "--reindex"} {
		if !strings.Contains(advice, p) {
			t.Errorf("NODE-JS-ADVICE: nodeDownAdvice() has nothing for %s", p)
		}
	}
	if !strings.Contains(js, "const t = nodeState === 'syncing' ? 'syncing…' : '--';") {
		t.Errorf("NODE-JS-TILES: the network tiles say syncing for a node that is not syncing")
	}
}

// textBetween is src from head up to the first end after it; "" when head is not there.
func textBetween(src, head, end string) string {
	i := strings.Index(src, head)
	if i < 0 {
		return ""
	}
	s := src[i:]
	if j := strings.Index(s, end); j >= 0 {
		return s[:j]
	}
	return s
}

// Settings read every answer as JSON. While the API was down or being restarted, the dashboard's
// server answered a save with an empty 502 (nginx: an HTML one), and the page showed "Error:
// SyntaxError: ... Unexpected end of JSON input" without saying whether anything was saved.
func TestSettingsSaysWhenForgeSoloDidNotAnswer(t *testing.T) {
	unchecked := regexp.MustCompile(`function\s*\(r\)\s*\{\s*return\s+r\.json\(\)\s*;?\s*\}`)
	for _, page := range []string{"settings.html", "tides.html"} {
		if m := unchecked.FindString(readWebFile(t, page)); m != "" {
			t.Errorf("SAVE-UNCHECKED-JSON: %s reads an answer as JSON without looking at it first: %s", page, m)
		}
	}
	s := readWebFile(t, "settings.html")
	// The page's reads take only the API's own JSON answer; a 503 with an error is no config.
	if !strings.Contains(textBetween(s, "function readJSON(url){", "\n   }"), "if(r.ok && d && typeof d==='object') return d;") {
		t.Error("SAVE-READ-CHECKED: readJSON takes an answer that is not a successful one")
	}
	if strings.Contains(s, "String(e)") {
		t.Error("SAVE-RAW-ERROR: Settings shows a JavaScript error as it is")
	}
	save := textBetween(s, "fetch('/api/v1/pool/config',{method:'POST'", "function notAnswered(")
	for _, want := range []string{"jsonOrNull(r)", "function(){ return {status:0, d:null}; }", "st.textContent=notAnswered(a.status);"} {
		if !strings.Contains(save, want) {
			t.Errorf("SAVE-NOT-ANSWERED: the save no longer has %q", want)
		}
	}
	na := textBetween(s, "function notAnswered(status){", "\n   }")
	if !strings.Contains(na, "if(status===0||status===502||status===503){") || !strings.Contains(na, "so nothing was saved") || !strings.Contains(na, "may or may not have gone through") {
		t.Errorf("SAVE-NOT-ANSWERED-TEXT: only a request that never reached the API is said to have saved nothing: %s", na)
	}
	// The API's own 5xx (its database is not answering) keeps what the user typed: no re-read.
	if b := branchAfter(s, "} else if(a.status>=500){"); b == "" || strings.Contains(b, "loadConfig") {
		t.Errorf("SAVE-5XX-KEEPS: a save the API could not make must leave the entries in the boxes: %q", b)
	}
}

// While the database is down the API answers 503 with its reason ("These figures cannot be shown
// right now: the database is not answering."), and mining-status says db_connected:false. The
// dashboard dropped both: red "Failed to load blocks/payouts" under a green "Mining" banner, and
// on a page opened during the outage "Can't reach Forge Solo" with 0.00 balances. Settings said
// "Can't reach Forge Solo" too.
func TestDatabaseOutageIsShownAsSuch(t *testing.T) {
	common := readWebFile(t, "js/common.js")
	if !strings.Contains(textBetween(common, "if (!response.ok) {", "throw err;"), "err.apiError = (body && typeof body.error === 'string') ? body.error : '';") {
		t.Error("DB-APIFETCH-REASON: apiFetch drops the API's reason from an error answer")
	}
	js := readWebFile(t, "js/pool-solo-inline.js")
	for _, fn := range []string{"async function fetchBlocks() {", "async function fetchPayouts() {"} {
		if !strings.Contains(textBetween(js, fn, "\n        }\n"), "(e && e.apiError) ? escapeHtml(e.apiError)") {
			t.Errorf("DB-TABLE-REASON: %s does not show the API's reason when the figures cannot be read", fn)
		}
	}
	if !strings.Contains(js, "if (ms && ms.db_connected === false) {") {
		t.Error("DB-BANNER: the status banner does not say when the database is not answering")
	}
	if strings.Count(js, "configError = (e && e.apiError) || '';") != 2 {
		t.Error("DB-CONFIG-ERROR: a settings read the API answered with its reason is not kept apart from an unreachable API")
	}
	if !strings.Contains(textBetween(js, "function noAddressNotice(forTable) {", "if (!configReachable) {"), "if (configError) {") {
		t.Error("DB-CONFIG-ERROR-NOTICE: with the settings unreadable, the page says Forge Solo cannot be reached")
	}
	page := readWebFile(t, "solo.html")
	for _, id := range []string{"matureBalance", "immatureBalance", "blocksFound", "totalEarned", "totalPaidAmount", "payoutCount"} {
		m := regexp.MustCompile(`id="` + id + `"[^>]*>([^<]*)<`).FindStringSubmatch(page)
		if m == nil || regexp.MustCompile(`\b0(\.0+)?\b`).MatchString(m[1]) {
			t.Errorf("DB-NO-ZEROS: #%s starts as %q; a figure not read yet is not zero", id, m)
		}
	}
	s := readWebFile(t, "settings.html")
	if !strings.Contains(textBetween(s, "function loadConfig(){", "\n   }"), "document.getElementById('apiDownWhy').textContent=apiError ||") {
		t.Error("DB-SETTINGS-REASON: Settings does not show the API's reason when the stored settings cannot be read")
	}
}

// Scripts and style sheets go out with no Cache-Control (only the pages are no-cache), so a
// browser keeps its copy for hours after an update unless the reference changes. Every page
// names each of them with the same ?v=, so a bump on one page is not missed on another.
func TestPagesLoadScriptsAndStylesByVersion(t *testing.T) {
	ref := regexp.MustCompile(`(?:src|href)="(/(?:js|css)/[^"?]+)(\?v=[^"]*)?"`)
	seen := map[string]string{}
	for _, page := range []string{"solo.html", "settings.html", "tides.html"} {
		for _, m := range ref.FindAllStringSubmatch(readWebFile(t, page), -1) {
			file, v := m[1], m[2]
			if file == "/js/chart.min.js" {
				continue // a vendored copy that does not change between releases
			}
			if v == "" {
				t.Errorf("ASSET-VERSION: %s loads %s with no ?v=, so an update can run with the old copy", page, file)
				continue
			}
			if prev, ok := seen[file]; ok && prev != v {
				t.Errorf("ASSET-VERSION-SAME: %s loads %s%s, another page %s%s", page, file, v, file, prev)
			}
			seen[file] = v
		}
	}
}

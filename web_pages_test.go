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

// The hashrate chart plots TH/s, with its legend hidden and no unit on its axis: a 500 MH/s miner
// read 0.0005 beside a tile saying 500.00 MH/s. Settings printed the network difficulty in full
// (2,345,678,901.234) where the dashboard says 2.35G.
func TestFiguresUseTheDashboardsUnits(t *testing.T) {
	chart := textBetween(readWebFile(t, "js/pool-solo-inline.js"), "hashrateChart = new Chart(ctx, {", "\n                });")
	if !strings.Contains(chart, "callback: v => formatHashrate(v * 1e12)") {
		t.Error("CHART-Y-UNITS: the chart's axis is not in the tiles' units")
	}
	if !strings.Contains(chart, "label: ctx => formatHashrate(ctx.parsed.y * 1e12)") {
		t.Error("CHART-TOOLTIP-UNITS: the chart's tooltip is not in the tiles' units")
	}
	s := readWebFile(t, "settings.html")
	if !strings.Contains(s, "getElementById('netDiff').textContent=formatDiff(") {
		t.Error("SETTINGS-NETDIFF: Settings does not show the network difficulty in the dashboard's short form")
	}
}

// The Windows installer opens the miner and rental ports (3333, 3335) to private networks only,
// and Windows treats a new network as public unless told otherwise, so on Windows the dashboard's
// connect card and Settings say the network must be Private. Umbrel and Linux have no such rule.
func TestWindowsSaysTheNetworkMustBePrivate(t *testing.T) {
	const words = "set its network profile to Private"
	solo := readWebFile(t, "solo.html")
	if note := regexp.MustCompile(`<div class="conn-note" id="connPrivateNote" hidden>([^\n]*)</div>`).FindStringSubmatch(solo); note == nil || !strings.Contains(note[1], words) {
		t.Error("WIN-PRIVATE-DASHBOARD: the connect card has no hidden note that the network must be Private")
	}
	if !strings.Contains(readWebFile(t, "js/pool-solo-inline.js"), "if (privateNote) privateNote.hidden = platform !== 'windows';") {
		t.Error("WIN-PRIVATE-DASHBOARD-SHOWN: the connect card's Private note is not shown on Windows alone")
	}
	s := readWebFile(t, "settings.html")
	if note := regexp.MustCompile(`<p class="note" id="winPrivateNote" style="display:none">([^\n]*)</p>`).FindStringSubmatch(s); note == nil || !strings.Contains(note[1], words) {
		t.Error("WIN-PRIVATE-SETTINGS: Settings has no hidden note that the network must be Private")
	}
	if !strings.Contains(textBetween(s, "function applyConfig(d){", "\n   }"), "getElementById('winPrivateNote').style.display = d.platform === 'windows' ? 'block' : 'none';") {
		t.Error("WIN-PRIVATE-SETTINGS-SHOWN: Settings' Private note is not shown on Windows alone")
	}
}

// On Linux, Settings said the password is "DASHBOARD_PASSWORD in secrets.env in the Forge Solo
// data directory", and there are two: the service's in /var/lib/forge-solo (read with sudo) and
// the one of a copy run by hand, in the user's home. It names the one the API reads, and both when
// the API does not say.
func TestLinuxSettingsNamesTheSecretsFile(t *testing.T) {
	s := readWebFile(t, "settings.html")
	if !strings.Contains(s, "var pt=d.platform==='linux' ? linuxPwText(d.secrets_path) : PW_TEXT[d.platform];") {
		t.Error("LINUX-PW-PATH: Settings does not use the secrets.env path the API gives on Linux")
	}
	fn := textBetween(s, "function linuxPwText(path){", "\n   }")
	for _, want := range []string{"/^\\/var\\/lib\\//.test(path) ? 'sudo cat ' : 'cat '", "sanitizeHTML(cmd)",
		"sudo cat /var/lib/forge-solo/secrets.env for the service, ~/.local/share/forge-solo/secrets.env for a copy you started yourself"} {
		if !strings.Contains(fn, want) {
			t.Errorf("LINUX-PW-TEXT: linuxPwText() lacks %q", want)
		}
	}
	if strings.Contains(s, "in secrets.env in the Forge Solo data directory") {
		t.Error("LINUX-PW-VAGUE: Settings still points at \"secrets.env in the Forge Solo data directory\"")
	}
}

// When another program holds 3335 (Forge Solo for Windows and Linux start without it), or Windows
// keeps it for itself, the connect card and Settings say so, and what to do, in the words the
// launchers use, from the mining status. Neither offers 3333 to NiceHash or MiningRigRentals
// instead: both did ("Rented hashpower uses port 3333 too"), while the launchers and the README said
// such orders cannot connect, and an order sent to 3333 starts at its floor of 1,024. The pages'
// logic is pinned whole: a test of fragments let a turned condition through.
func TestNoRentalPortSaysWhatToDo(t *testing.T) {
	const words = `function noRentalPort(ms) {
    if (!ms || ms.rental_port !== 0) return '';
    const taken = Number(ms.rental_port_taken) || 0;
    if (taken > 0) {
        return 'Another program uses port ' + taken + ', the rental port: rentals have no port of their own until you stop it and restart Forge Solo.';
    }
    const reserved = Number(ms.rental_port_reserved) || 0;
    if (reserved > 0) {
        return 'Windows keeps port ' + reserved + ', the rental port, for itself: rentals have no port of their own until Windows lets it go and you restart Forge Solo.';
    }
    return 'The rental port did not open: rentals have no port of their own. The mining service\'s log says why.';`
	if fn := textBetween(readWebFile(t, "js/common.js"), "function noRentalPort(ms) {", "\n}"); fn != words {
		t.Errorf("RENTAL-WORDS: noRentalPort() is not as it should be:\n%s\nwant:\n%s", fn, words)
	}
	card := textBetween(readWebFile(t, "js/pool-solo-inline.js"), "async function fetchConnectivity() {", "function renderRentalNote(")
	// Before the node has synced and an address is set, the status banner has not read the mining
	// status: the card reads it itself.
	if !strings.Contains(card, `            let ms = lastMiningStatus;
            if (!ms) {
                try {
                    ms = await apiFetch('/api/v1/mining-status');
                } catch (e) {
                    ms = null;
                }
            }
`) {
		t.Error("RENTAL-CARD-STATUS: the connect card does not read the mining status itself when the status banner has not")
	}
	if !strings.Contains(card, `            const rentalPort = (ms && Number(ms.rental_port)) || Number(c.rentalPort) || 3335;
            lastConn = c; lastRentalPort = rentalPort; lastNoRental = noRentalPort(ms);
            if (group) {
                group.hidden = false;
                const value = document.getElementById('connRental');
                if (lastNoRental) {
                    value.textContent = 'No port for rentals';
                    document.getElementById('connRentalNote').textContent = lastNoRental;
                } else if (c.publicIp) {
`) {
		t.Error("RENTAL-CARD: the connect card does not say why rentals have no port, and what to do, when they have none")
	}
	if strings.Contains(card, "Number(c.stratumPort)") {
		t.Error("RENTAL-CARD-NOT-3333: the connect card offers the miners' port to rentals")
	}
	s := readWebFile(t, "settings.html")
	if !strings.Contains(s, `   readJSON('/api/v1/mining-status').then(function(m){
     noRental=noRentalPort(m);
     if(noRental){ document.getElementById('noRentalNote').textContent=noRental; document.body.classList.add('no-rental'); showStratumUrls(); }
   }).catch(function(){});
`) {
		t.Error("RENTAL-SETTINGS-STATUS: Settings does not show why rentals have no port, and what to do, when they have none")
	}
	for _, want := range []string{`<p class="note rental-none" id="noRentalNote"></p>`,
		"document.getElementById('stratumUrlRental').textContent=noRental ? 'none (see below)' : 'stratum+tcp://'+host+':3335';",
		" .rental-none{display:none}\n .no-rental .rental{display:none}\n .no-rental .rental-none{display:block}\n"} {
		if !strings.Contains(s, want) {
			t.Errorf("RENTAL-SETTINGS: Settings lacks %q", want)
		}
	}
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, "getElementById('stratumUrlRental')") && strings.Contains(l, "3333") {
			t.Errorf("RENTAL-SETTINGS-NOT-3333: Settings offers 3333 to rentals: %s", strings.TrimSpace(l))
		}
	}
	if regexp.MustCompile(`rentalPort\s*=\s*3333`).MatchString(s) || strings.Contains(s, "<strong>3333</strong> too") {
		t.Error("RENTAL-SETTINGS-NOT-3333: Settings offers 3333 to rentals")
	}

	// The mining service's log and the launchers' say what to do in the dashboard's words. A Go
	// string split over lines is read as one.
	joined := regexp.MustCompile(`"\s*\+\s*\n\s*"`)
	taken := "rentals have no port of their own until you stop it and restart Forge Solo"
	kept := "rentals have no port of their own until Windows lets it go and you restart Forge Solo"
	for f, want := range map[string][]string{
		"cmd/stratum/main.go":              {taken, kept},
		"windows/launcher/public_ports.go": {taken, kept},
		"cmd/forge-solo-linux/main.go":     {taken},
	} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := joined.ReplaceAllString(string(b), "")
		for _, w := range want {
			if !strings.Contains(src, w) {
				t.Errorf("RENTAL-SAME-WORDS: %s does not say %q", f, w)
			}
		}
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

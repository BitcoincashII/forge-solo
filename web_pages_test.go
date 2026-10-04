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

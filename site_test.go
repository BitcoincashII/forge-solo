package forgesolo

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Forge Solo's website, https://forgesolo.bch2.org: site/README.md says what is in it and how it
// is served.
const (
	siteDir   = "site"
	siteNginx = "site/nginx/forgesolo.bch2.org.conf"
	siteURL   = "https://forgesolo.bch2.org/"
)

// sitePages are the site's HTML pages.
func sitePages(t *testing.T) []docText {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(siteDir, "*.html"))
	if err != nil || len(files) == 0 {
		t.Fatalf("SITE-PAGES: %s has no pages (%v)", siteDir, err)
	}
	var out []docText
	for _, f := range files {
		out = append(out, docText{filepath.ToSlash(f), string(mustRead(t, f))})
	}
	return out
}

var siteHref = regexp.MustCompile(`\bhref="([^"]*)"`)

// The site points to the README for the directions, by the anchors GitHub gives its headings,
// instead of repeating them. A heading renamed or a file moved fails here, not on the live site,
// where the link would open the top of the README, or GitHub's 404.
func TestSiteLinksLeadToHeadings(t *testing.T) {
	sections := 0
	for _, p := range sitePages(t) {
		for _, m := range siteHref.FindAllStringSubmatch(p.text, -1) {
			u := m[1]
			rest, ok := strings.CutPrefix(u, repoURL)
			if !ok || rest != "" && rest[0] != '/' && rest[0] != '#' {
				continue
			}
			file, anchor, hasAnchor := strings.Cut(rest, "#")
			var target string
			switch {
			case file == "" || file == "/":
				target = "README.md"
			case strings.HasPrefix(file, "/blob/main/"):
				target = strings.TrimPrefix(file, "/blob/main/")
			default:
				continue // the releases and the issues: GitHub's pages, not files of this repository
			}
			if _, err := os.Stat(target); err != nil {
				t.Errorf("SITE-LINK: %s links to %s, and the repository has no %s", p.name, u, target)
				continue
			}
			if hasAnchor {
				sections++
				if !anchors(t, target)[anchor] {
					t.Errorf("SITE-ANCHOR: %s links to %s, and %s has no such heading", p.name, u, target)
				}
			}
		}
	}
	if sections == 0 {
		t.Error("SITE-ANCHOR: the site links to no section of the README")
	}
}

// Every download is on GitHub's latest release, never on the site: the release page has every
// platform's file and their checksums, and a link to it cannot fall behind a release.
func TestSiteDownloadsAreOnGitHub(t *testing.T) {
	file := regexp.MustCompile(`(?i)\.(exe|msi|gz|tgz|xz|zip|dmg|pkg|deb|rpm|appimage)$`)
	latest := 0
	for _, p := range sitePages(t) {
		for _, m := range siteHref.FindAllStringSubmatch(p.text, -1) {
			u := m[1]
			if strings.Contains(u, "/releases") {
				if u != repoURL+"/releases/latest" {
					t.Errorf("SITE-RELEASE: %s links to %s; the site links to %s/releases/latest only", p.name, u, repoURL)
				} else {
					latest++
				}
			}
			if file.MatchString(u) || strings.Contains(u, "SHA256SUMS") {
				t.Errorf("SITE-DOWNLOAD: %s links to a file to download, %s", p.name, u)
			}
		}
	}
	if latest == 0 {
		t.Error("SITE-RELEASE: the site does not link to the latest release")
	}
}

// The site's nginx server sends a Content-Security-Policy that lets a page load its own styles,
// font and images and nothing else. The browser refuses a <script>, a <style> block, a style
// attribute, an event handler, and a stylesheet, font or image from another site, so each fails
// here first.
func TestSiteLivesWithItsContentSecurityPolicy(t *testing.T) {
	m := regexp.MustCompile(`(?m)^\s*add_header Content-Security-Policy "([^"]+)" always;$`).FindStringSubmatch(string(mustRead(t, siteNginx)))
	if m == nil {
		t.Fatalf("SITE-CSP: %s sends no Content-Security-Policy", siteNginx)
	}
	policy := map[string]string{}
	for _, d := range strings.Split(m[1], ";") {
		if f := strings.Fields(d); len(f) > 0 {
			policy[f[0]] = strings.Join(f[1:], " ")
		}
	}
	for dir, want := range map[string]string{"default-src": "'none'", "style-src": "'self'", "font-src": "'self'",
		"img-src": "'self'", "base-uri": "'none'", "form-action": "'none'", "frame-ancestors": "'none'"} {
		if policy[dir] != want {
			t.Errorf("SITE-CSP: the policy's %s is %q, want %q", dir, policy[dir], want)
		}
	}
	if _, ok := policy["script-src"]; ok {
		t.Error("SITE-CSP: the policy allows scripts; the site has none")
	}

	refused := regexp.MustCompile(`(?i)<script\b|<style\b|<iframe\b|<object\b|<embed\b|<form\b|\sstyle\s*=|\son[a-z]+\s*=`)
	tag := regexp.MustCompile(`(?i)<(link|img|source|audio|video)\b[^>]*>`)
	rel := regexp.MustCompile(`(?i)\brel="([^"]*)"`)
	src := regexp.MustCompile(`(?i)\b(?:href|src|srcset)="([^"]*)"`)
	elsewhere := regexp.MustCompile(`(?i)^(?:[a-z][a-z0-9+.-]*:|//)`)
	for _, p := range sitePages(t) {
		for _, s := range refused.FindAllString(p.text, -1) {
			t.Errorf("SITE-CSP-INLINE: %s has %q, which the policy refuses", p.name, strings.TrimSpace(s))
		}
		for _, el := range tag.FindAllString(p.text, -1) {
			if r := rel.FindStringSubmatch(el); r != nil && (r[1] == "canonical" || r[1] == "alternate") {
				continue // names an address, loads nothing
			}
			for _, s := range src.FindAllStringSubmatch(el, -1) {
				if elsewhere.MatchString(s[1]) {
					t.Errorf("SITE-CSP-ELSEWHERE: %s loads %s from another site: %s", p.name, s[1], el)
				}
			}
		}
	}
	css, _ := filepath.Glob(filepath.Join(siteDir, "css", "*.css"))
	if len(css) == 0 {
		t.Error("SITE-CSS: site/css has no stylesheet")
	}
	for _, f := range css {
		if s := regexp.MustCompile(`(?i)@import|url\(\s*["']?(?:[a-z][a-z0-9+.-]*:|//)`).FindString(string(mustRead(t, f))); s != "" {
			t.Errorf("SITE-CSP-ELSEWHERE: %s loads %q from another site", filepath.ToSlash(f), s)
		}
	}
}

// nginx sends a server's add_header lines only for a location without an add_header of its own.
// The site's HTTPS server sets its security headers once, and its locations set none: they cache
// with expires, which leaves them in place. pool.bch2.org's "location ~ \.html$" adds
// Cache-Control, so its pages go out without them. The port-80 server, the part of the file
// site/README.md installs before there is a certificate, names none, and answers the ACME
// challenge from the webroot certbot is given.
func TestSiteServerSendsItsHeadersEverywhere(t *testing.T) {
	conf := string(mustRead(t, siteNginx))
	plain, tls, ok := strings.Cut(conf, "\n# HTTPS\n")
	if !ok {
		t.Fatalf("SITE-NGINX-SPLIT: %s has no line \"# HTTPS\" before its HTTPS server, which site/README.md cuts the file at", siteNginx)
	}
	if strings.Contains(plain, "ssl_certificate") || strings.Contains(plain, "listen 443") {
		t.Errorf("SITE-NGINX-SPLIT: %s names the certificate above \"# HTTPS\": nginx -t fails there before certbot has made it", siteNginx)
	}
	for _, want := range []string{"location ^~ /.well-known/acme-challenge/ {\n        root /var/www/letsencrypt;", "return 301 https://$server_name$request_uri;"} {
		if !strings.Contains(plain, want) {
			t.Errorf("SITE-NGINX-80: the port-80 server of %s does not have %q", siteNginx, want)
		}
	}
	readme := flat(string(mustRead(t, "site/README.md")))
	for _, want := range []string{"sed '/^# HTTPS$/,$d'", "certbot certonly --webroot -w /var/www/letsencrypt -d forgesolo.bch2.org"} {
		if !strings.Contains(readme, want) {
			t.Errorf("SITE-NGINX-SETUP: site/README.md does not say %q", want)
		}
	}

	headers := map[string]bool{}
	depth := 0
	for _, line := range strings.Split(tls, "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "#") {
			continue
		}
		if strings.HasPrefix(l, "add_header ") {
			if depth != 1 {
				t.Errorf("SITE-NGINX-LOCATION: %s has %q in a location, which drops the server's security headers there", siteNginx, l)
			} else if !strings.HasSuffix(l, " always;") {
				t.Errorf("SITE-NGINX-ALWAYS: %q is not sent with an error page", l)
			} else {
				headers[strings.Fields(l)[1]] = true
			}
		}
		depth += strings.Count(l, "{") - strings.Count(l, "}")
	}
	for _, h := range []string{"Content-Security-Policy", "X-Frame-Options", "X-Content-Type-Options", "X-XSS-Protection",
		"Referrer-Policy", "Strict-Transport-Security"} {
		if !headers[h] {
			t.Errorf("SITE-NGINX-HEADERS: the HTTPS server of %s does not send %s", siteNginx, h)
		}
	}
	for _, want := range []string{"root /var/www/forgesolo;", "ssl_certificate     /etc/letsencrypt/live/forgesolo.bch2.org/fullchain.pem;", "error_page 404 /404.html;", "server_tokens off;"} {
		if !strings.Contains(tls, want) {
			t.Errorf("SITE-NGINX-443: the HTTPS server of %s does not have %q", siteNginx, want)
		}
	}
}

// deploy.sh copies site/ to the live root with --delete, apart from the repository's own files in
// it. Every other file in site/ is one a browser fetches: anything else would be published.
func TestSiteDeploySendsOnlyTheSite(t *testing.T) {
	sh := string(mustRead(t, "site/deploy.sh"))
	for _, want := range []string{`rsync -av --delete $DRY`, `[ -f "${SRC}index.html" ] || {`, "DST=/var/www/forgesolo/",
		"--exclude='/.well-known/'", "--exclude='*.bak*'", "--exclude='*.pre-*'"} {
		if !strings.Contains(sh, want) {
			t.Errorf("SITE-DEPLOY: site/deploy.sh does not have %s", want)
		}
	}
	excluded := map[string]bool{}
	for _, m := range regexp.MustCompile(`--exclude='/([^'*]+)'`).FindAllStringSubmatch(sh, -1) {
		excluded[m[1]] = true
	}
	served := map[string]bool{".html": true, ".css": true, ".svg": true, ".png": true, ".woff2": true, ".txt": true}
	pages := 0
	err := filepath.WalkDir(siteDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel := filepath.ToSlash(strings.TrimPrefix(p, siteDir+string(filepath.Separator)))
		top, _, nested := strings.Cut(rel, "/")
		if excluded[rel] || nested && excluded[top+"/"] {
			return nil
		}
		if !served[path.Ext(rel)] {
			t.Errorf("SITE-DEPLOY-FILE: site/%s is not a file the site serves, and deploy.sh would publish it", rel)
		}
		if path.Ext(rel) == ".html" {
			pages++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, own := range []string{"deploy.sh", "README.md", "nginx/"} {
		if !excluded[own] {
			t.Errorf("SITE-DEPLOY-OWN: deploy.sh would publish site/%s, which is the repository's", own)
		}
	}
	if pages == 0 {
		t.Error("SITE-DEPLOY: deploy.sh would publish no page")
	}
}

// Each text on a page carries a data-i18n key, as on pool.bch2.org, so that a translation can be
// added the way the pool adds one (site/README.md). A key names one English text wherever it is
// used. The texts use plain punctuation, as the READMEs do.
func TestSiteTextsAreReadyForTranslation(t *testing.T) {
	texts := map[string]string{}
	open := regexp.MustCompile(`<(p|h1|h2|h3|li)\b([^>]*)>`)
	for _, p := range sitePages(t) {
		for _, m := range open.FindAllStringSubmatchIndex(p.text, -1) {
			el, attrs := p.text[m[2]:m[3]], p.text[m[4]:m[5]]
			if strings.Contains(attrs, "data-i18n=") {
				continue
			}
			if el == "li" {
				// a list item made of its own headings and paragraphs, which carry the keys
				body, _, _ := strings.Cut(p.text[m[1]:], "</li>")
				if strings.Contains(body, "<p ") || strings.Contains(body, "<h3 ") {
					continue
				}
			}
			t.Errorf("SITE-I18N-KEY: %s has a <%s%s> without a data-i18n key", p.name, el, attrs)
		}
		for _, el := range []string{"p", "h1", "h2", "h3", "li", "a", "span"} {
			re := regexp.MustCompile(`<` + el + `\b[^>]*\bdata-i18n="([^"]+)"[^>]*>([\s\S]*?)</` + el + `>`)
			for _, m := range re.FindAllStringSubmatch(p.text, -1) {
				text := strings.Join(strings.Fields(m[2]), " ")
				if was, ok := texts[m[1]]; ok && was != text {
					t.Errorf("SITE-I18N-ONE: the key %s names two texts: %q and %q (%s)", m[1], was, text, p.name)
				}
				texts[m[1]] = text
			}
		}
		if s := regexp.MustCompile(`&mdash;|&#8212;|&#x2014;`).FindString(p.text); s != "" {
			t.Errorf("DOCS-EMDASH: %s has %s", p.name, s)
		}
	}
	if len(texts) == 0 {
		t.Error("SITE-I18N-KEY: no text on the site has a data-i18n key")
	}
	checkPlainPunctuation(t, append(sitePages(t), docText{"site/README.md", string(mustRead(t, "site/README.md"))}))
}

// The page's head names its canonical address, the one site/nginx serves, and what a link preview
// shows.
func TestSiteHead(t *testing.T) {
	page := string(mustRead(t, "site/index.html"))
	for _, want := range []string{`<html lang="en">`, `<link rel="canonical" href="` + siteURL + `">`, `<meta property="og:url" content="` + siteURL + `">`,
		`<meta property="og:image" content="` + siteURL + `img/og.png">`, `<meta name="twitter:card" content="summary_large_image">`,
		`<link rel="icon" type="image/svg+xml" href="/img/logo.svg">`, `<meta name="theme-color" content="#0a0a0a">`} {
		if !strings.Contains(page, want) {
			t.Errorf("SITE-HEAD: site/index.html does not have %s", want)
		}
	}
	if !strings.Contains(string(mustRead(t, siteNginx)), "server_name forgesolo.bch2.org;") {
		t.Errorf("SITE-HEAD: %s does not serve the page's canonical name", siteNginx)
	}
	for _, f := range []string{"img/og.png", "img/logo.svg", "img/apple-touch-icon.png", "fonts/inter-latin.woff2", "fonts/OFL.txt", "404.html", "robots.txt"} {
		if _, err := os.Stat(filepath.Join(siteDir, f)); err != nil {
			t.Errorf("SITE-FILES: site/%s is missing", f)
		}
	}
}

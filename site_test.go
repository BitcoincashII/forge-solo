package forgesolo

import (
	"encoding/binary"
	"html"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Forge Solo's website, https://forgesolo.bch2.org: site/README.md says what is in it and how it
// is served, site/BRAND.md how it looks.
const (
	siteDir   = "site"
	siteNginx = "site/nginx/forgesolo.bch2.org.conf"
	siteURL   = "https://forgesolo.bch2.org/"
)

// siteOwn are the repository's files in site/, which deploy.sh never sends. Everything else in
// site/ is what a browser gets.
var siteOwn = []string{"deploy.sh", "README.md", "BRAND.md", "nginx/"}

// sitePages are the site's HTML pages: site/*.html, and the index.html of a folder in it, which is
// the page at the folder's address (/faq/ is site/faq/index.html).
func sitePages(t *testing.T) []docText {
	t.Helper()
	var out []docText
	err := filepath.WalkDir(siteDir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Ext(p) == ".html" {
			out = append(out, docText{filepath.ToSlash(p), string(mustRead(t, p))})
		}
		return err
	})
	if err != nil || len(out) == 0 {
		t.Fatalf("SITE-PAGES: %s has no pages (%v)", siteDir, err)
	}
	return out
}

// siteAddress is the address a page is served at: site/index.html is /, site/faq/index.html is
// /faq/, site/404.html is /404.html.
func siteAddress(page string) string {
	rel := strings.TrimPrefix(page, siteDir+"/")
	if rel == "index.html" || strings.HasSuffix(rel, "/index.html") {
		return "/" + strings.TrimSuffix(rel, "index.html")
	}
	return "/" + rel
}

var (
	siteHref      = regexp.MustCompile(`\bhref="([^"]*)"`)
	siteElsewhere = regexp.MustCompile(`(?i)^(?:[a-z][a-z0-9+.-]*:|//)`)
)

// siteAttr is the value of the attribute name in an element's start tag, and whether it has one.
func siteAttr(tag, name string) (string, bool) {
	m := regexp.MustCompile(`\s` + regexp.QuoteMeta(name) + `="([^"]*)"`).FindStringSubmatch(tag)
	if m == nil {
		return "", false
	}
	return html.UnescapeString(m[1]), true
}

// siteImageSize is the width and height in pixels of a PNG or a WebP file (lossy VP8, lossless
// VP8L or extended VP8X), read from its header.
func siteImageSize(b []byte) (w, h int, ok bool) {
	switch {
	case len(b) >= 24 && string(b[1:4]) == "PNG":
		return int(binary.BigEndian.Uint32(b[16:20])), int(binary.BigEndian.Uint32(b[20:24])), true
	case len(b) >= 30 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		switch string(b[12:16]) {
		case "VP8X":
			return 1 + (int(b[24]) | int(b[25])<<8 | int(b[26])<<16), 1 + (int(b[27]) | int(b[28])<<8 | int(b[29])<<16), true
		case "VP8L":
			if b[20] == 0x2f {
				v := binary.LittleEndian.Uint32(b[21:25])
				return int(v&0x3fff) + 1, int(v>>14&0x3fff) + 1, true
			}
		case "VP8 ":
			if b[23] == 0x9d && b[24] == 0x01 && b[25] == 0x2a {
				return int(binary.LittleEndian.Uint16(b[26:28]) & 0x3fff), int(binary.LittleEndian.Uint16(b[28:30]) & 0x3fff), true
			}
		}
	}
	return 0, 0, false
}

// The site points to the README for the directions, by the anchors GitHub gives its headings,
// instead of repeating them. A heading renamed or a file moved fails here, not on the live site,
// where the link would open the top of the README, or GitHub's 404. Each platform's card links to
// its directions: the README's Umbrel and Windows sections, and packaging/linux/README.md.
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
	index := string(mustRead(t, "site/index.html"))
	for _, want := range []string{repoURL + "#install-on-umbrel", repoURL + "#install-on-windows", repoURL + "/blob/main/packaging/linux/README.md"} {
		if !strings.Contains(index, `href="`+want+`"`) {
			t.Errorf("SITE-DIRECTIONS: site/index.html does not link to %s", want)
		}
	}
}

// The Windows and Linux downloads are on GitHub's latest release, never on the site: the release
// page has their files and checksums, and a link to it cannot fall behind a release. (Umbrel
// installs Forge Solo from its app store.)
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
// fonts and images and nothing else. The browser refuses a <script>, a <style> block, a style
// attribute, an event handler, and a stylesheet, font or image from another site, so each fails
// here first.
func TestSiteLivesWithItsContentSecurityPolicy(t *testing.T) {
	const csp = `add_header Content-Security-Policy "`
	value := ""
	for _, s := range nginxStatements(string(mustRead(t, siteNginx))) {
		if rest, ok := strings.CutPrefix(s.text, csp); ok {
			value, _, _ = strings.Cut(rest, `"`)
		}
	}
	if value == "" {
		t.Fatalf("SITE-CSP: %s sends no Content-Security-Policy", siteNginx)
	}
	policy := map[string]string{}
	for _, d := range strings.Split(value, ";") {
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
	// what loads a file: the head's links, images and media, and SVG's use and image
	tag := regexp.MustCompile(`(?i)<(link|img|source|audio|video|track|input|use|image)\b[^>]*>`)
	rel := regexp.MustCompile(`(?i)\brel="([^"]*)"`)
	src := regexp.MustCompile(`(?i)\b(href|src|srcset)="([^"]*)"`)
	for _, p := range sitePages(t) {
		for _, s := range refused.FindAllString(p.text, -1) {
			t.Errorf("SITE-CSP-INLINE: %s has %q, which the policy refuses", p.name, strings.TrimSpace(s))
		}
		for _, el := range tag.FindAllString(p.text, -1) {
			if r := rel.FindStringSubmatch(el); r != nil && (r[1] == "canonical" || r[1] == "alternate") {
				continue // names an address, loads nothing
			}
			for _, s := range src.FindAllStringSubmatch(el, -1) {
				for _, u := range siteAddresses(s[1], s[2]) {
					if siteElsewhere.MatchString(u) {
						t.Errorf("SITE-CSP-ELSEWHERE: %s loads %s from another site: %s", p.name, u, el)
					}
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

// siteAddresses are the addresses in an attribute's value: one, or a srcset's list, "a.png 1x,
// b.png 2x", any of which the browser may load.
func siteAddresses(attr, value string) []string {
	if !strings.EqualFold(attr, "srcset") {
		return []string{value}
	}
	var out []string
	for _, c := range strings.Split(value, ",") {
		if f := strings.Fields(c); len(f) > 0 {
			out = append(out, f[0])
		}
	}
	return out
}

// siteFile is the file in site/ an address on the site leads to, as nginx's try_files and index
// find it: "/" and "/faq/" are their folder's index.html.
func siteFile(addr string) string {
	addr, _, _ = strings.Cut(addr, "#")
	addr, _, _ = strings.Cut(addr, "?")
	rel := strings.TrimPrefix(path.Clean("/"+addr), "/")
	if rel == "" || strings.HasSuffix(addr, "/") {
		rel = path.Join(rel, "index.html")
	}
	return rel
}

// Every address a page gives on the site itself, and every file the stylesheet loads, is a file in
// site/ that deploy.sh sends: a file renamed or dropped fails here, not as a missing font, image or
// page on the live site. A link to a part of a page leads to an id that page has. A page gives
// those addresses from the site's root, never relative to its own: nginx serves 404.html at
// whatever address was asked for, and a page in a folder (/faq/) is a level down, where "css/" is
// another folder. And every image and font in site/ is one a page or the stylesheet uses:
// deploy.sh would publish the rest for nothing.
func TestSiteUsesItsOwnFiles(t *testing.T) {
	ref := regexp.MustCompile(`(?i)\s(href|src|srcset|content)="([^"]*)"`)
	idAttr := regexp.MustCompile(`\sid="([^"]+)"`)
	pages := sitePages(t)
	ids := map[string]map[string]bool{}
	for _, p := range pages {
		rel := strings.TrimPrefix(p.name, siteDir+"/")
		ids[rel] = map[string]bool{}
		for _, m := range idAttr.FindAllStringSubmatch(p.text, -1) {
			ids[rel][m[1]] = true
		}
	}
	used := map[string]bool{}
	check := func(from, addr, rel string) {
		for _, own := range siteOwn {
			if rel == own || strings.HasSuffix(own, "/") && strings.HasPrefix(rel, own) {
				t.Errorf("SITE-FILE-OWN: %s gives %s, which deploy.sh does not send", from, addr)
				return
			}
		}
		if rel == "404.html" {
			t.Errorf("SITE-FILE-404: %s gives %s: nginx serves 404.html only for an address it has no file for", from, addr)
			return
		}
		if st, err := os.Stat(filepath.Join(siteDir, filepath.FromSlash(rel))); err != nil || st.IsDir() || strings.HasPrefix(rel, "../") {
			t.Errorf("SITE-FILE-MISSING: %s gives %s, and site/%s is not there", from, addr, rel)
			return
		}
		used[rel] = true
		if _, frag, ok := strings.Cut(addr, "#"); ok && ids[rel] != nil && !ids[rel][frag] {
			t.Errorf("SITE-FILE-PART: %s links to %s, and site/%s has no id %q", from, addr, rel, frag)
		}
	}
	for _, p := range pages {
		self := strings.TrimPrefix(p.name, siteDir+"/")
		for _, m := range ref.FindAllStringSubmatch(p.text, -1) {
			attr := strings.ToLower(m[1])
			for _, a := range siteAddresses(attr, m[2]) {
				switch {
				case strings.HasPrefix(a, siteURL):
					check(p.name, a, siteFile("/"+strings.TrimPrefix(a, siteURL)))
				case attr == "content":
					// a meta tag's words, not an address
				case strings.HasPrefix(a, "#"):
					if !ids[self][a[1:]] {
						t.Errorf("SITE-FILE-PART: %s links to %s, and has no such id", p.name, a)
					}
				case siteElsewhere.MatchString(a):
					// another site: the tests above
				case strings.HasPrefix(a, "/"):
					check(p.name, a, siteFile(a))
				default:
					t.Errorf("SITE-FILE-RELATIVE: %s gives %q relative to its own address; give it from the site's root", p.name, a)
				}
			}
		}
	}
	cssURL := regexp.MustCompile(`url\(\s*["']?([^"')]+)["']?\s*\)`)
	css, _ := filepath.Glob(filepath.Join(siteDir, "css", "*.css"))
	for _, f := range css {
		name := filepath.ToSlash(f)
		for _, m := range cssURL.FindAllStringSubmatch(string(mustRead(t, f)), -1) {
			u := m[1]
			if strings.HasPrefix(u, "#") || siteElsewhere.MatchString(u) {
				continue
			}
			if strings.HasPrefix(u, "/") {
				check(name, u, siteFile(u))
				continue
			}
			file, _, _ := strings.Cut(u, "?")
			check(name, u, path.Join("css", file))
		}
	}
	if len(used) == 0 {
		t.Fatal("SITE-FILE: no page uses a file of the site")
	}
	for _, dir := range []string{"img", "fonts"} {
		entries, err := os.ReadDir(filepath.Join(siteDir, dir))
		if err != nil {
			t.Errorf("SITE-FILE: %v", err)
		}
		for _, e := range entries {
			rel := dir + "/" + e.Name()
			if !used[rel] && !(dir == "fonts" && strings.HasPrefix(e.Name(), "OFL-")) {
				t.Errorf("SITE-FILE-UNUSED: no page or stylesheet uses site/%s, and deploy.sh would publish it", rel)
			}
		}
	}
}

// nginxStmt is one directive of an nginx file, without its ";" or "{", and how many blocks deep it
// is: a server's own directives are at 1, a location's at 2.
type nginxStmt struct {
	text  string
	depth int
}

// nginxStatements splits an nginx file into its directives however they are laid out on lines, so
// "location = /x { add_header A b; }" is two, the second at depth 2. Comments are left out, and a
// quoted string (the policy has ";" in it) stays in its directive. As in nginx, "#" and a quote
// begin a comment or a string only at the start of a word.
func nginxStatements(conf string) []nginxStmt {
	var out []nginxStmt
	var cur strings.Builder
	depth, quote, comment := 0, byte(0), false
	emit := func() {
		if s := strings.Join(strings.Fields(cur.String()), " "); s != "" {
			out = append(out, nginxStmt{s, depth})
		}
		cur.Reset()
	}
	for i := 0; i < len(conf); i++ {
		c := conf[i]
		wordStart := i == 0 || strings.IndexByte(" \t\r\n;{}", conf[i-1]) >= 0
		switch {
		case comment:
			if c == '\n' {
				comment = false
				cur.WriteByte(' ')
			}
		case quote != 0:
			cur.WriteByte(c)
			if c == '\\' && i+1 < len(conf) {
				i++
				cur.WriteByte(conf[i])
			} else if c == quote {
				quote = 0
			}
		case wordStart && (c == '"' || c == '\''):
			quote = c
			cur.WriteByte(c)
		case wordStart && c == '#':
			comment = true
		case c == ';':
			emit()
		case c == '{':
			emit()
			depth++
		case c == '}':
			emit()
			depth--
		default:
			cur.WriteByte(c)
		}
	}
	emit()
	return out
}

// nginx sends a server's add_header lines only for a location without an add_header of its own.
// The site's HTTPS server sets its security headers once, and its locations set none: they cache
// with expires, which leaves them in place. pool.bch2.org's "location ~ \.html$" adds
// Cache-Control, so its pages go out without them. The port-80 server, the part of the file
// site/README.md installs before there is a certificate, names none, and answers the ACME
// challenge from the webroot certbot is given. Neither server is the box's default for its port:
// a name the box has no server for would get this site.
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

	for _, s := range nginxStatements(conf) {
		f := strings.Fields(s.text)
		if f[0] != "listen" {
			continue
		}
		for _, p := range f[1:] {
			if p == "default_server" || p == "default" {
				t.Errorf("SITE-NGINX-DEFAULT: %s has %q: every name the box has no server for would get this site", siteNginx, s.text)
			}
		}
	}

	headers := map[string]bool{}
	for _, s := range nginxStatements(tls) {
		f := strings.Fields(s.text)
		switch {
		case f[0] != "add_header":
		case s.depth != 1:
			t.Errorf("SITE-NGINX-LOCATION: %s has %q in a location, which drops the server's security headers there", siteNginx, s.text)
		case len(f) < 4 || f[len(f)-1] != "always":
			t.Errorf("SITE-NGINX-ALWAYS: %q is not sent with an error page", s.text)
		default:
			headers[f[1]] = true
		}
	}
	for _, h := range []string{"Content-Security-Policy", "X-Frame-Options", "X-Content-Type-Options", "X-XSS-Protection",
		"Referrer-Policy", "Strict-Transport-Security"} {
		if !headers[h] {
			t.Errorf("SITE-NGINX-HEADERS: the HTTPS server of %s does not send %s", siteNginx, h)
		}
	}
	for _, want := range []string{"root /var/www/forgesolo;", "ssl_certificate     /etc/letsencrypt/live/forgesolo.bch2.org/fullchain.pem;", "error_page 404 /404.html;", "error_page 403 =404 /404.html;", "server_tokens off;"} {
		if !strings.Contains(tls, want) {
			t.Errorf("SITE-NGINX-443: the HTTPS server of %s does not have %q", siteNginx, want)
		}
	}
}

// deploy.sh copies site/ to the live root with --delete, apart from the repository's own files in
// it. Every other file in site/ is one a browser fetches: anything else would be published. On the
// box it refuses a site/ with a change that is not committed or a file git does not track.
func TestSiteDeploySendsOnlyTheSite(t *testing.T) {
	sh := string(mustRead(t, "site/deploy.sh"))
	for _, want := range []string{`rsync -av --delete $DRY`, `[ -f "${SRC}index.html" ] || {`, "DST=/var/www/forgesolo/",
		`dirty=$(git -C "$SRC" status --porcelain --untracked-files=all --ignored -- . 2>&1)`, `if [ -n "$dirty" ]; then`,
		"--exclude='/.well-known/'", "--exclude='*.bak*'", "--exclude='*.pre-*'"} {
		if !strings.Contains(sh, want) {
			t.Errorf("SITE-DEPLOY: site/deploy.sh does not have %s", want)
		}
	}
	excluded := map[string]bool{}
	for _, m := range regexp.MustCompile(`--exclude='/([^'*]+)'`).FindAllStringSubmatch(sh, -1) {
		excluded[m[1]] = true
	}
	served := map[string]bool{".html": true, ".css": true, ".svg": true, ".png": true, ".webp": true, ".woff2": true, ".txt": true}
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
	for _, own := range siteOwn {
		if !excluded[own] {
			t.Errorf("SITE-DEPLOY-OWN: deploy.sh would publish site/%s, which is the repository's", own)
		}
	}
	if pages == 0 {
		t.Error("SITE-DEPLOY: deploy.sh would publish no page")
	}
}

// Each text on a page carries a data-i18n key, as on pool.bch2.org, so that a translation can be
// added the way the pool adds one (site/README.md): the pool's loader puts a key's text in its
// element, in place of what is there. So every text in a page's body, and its title, is inside an
// element with a key, and no element with a key is inside another one, whose translation would
// replace it. A key names one text wherever it is used. The texts use plain punctuation, as the
// READMEs do.
func TestSiteTextsAreReadyForTranslation(t *testing.T) {
	token := regexp.MustCompile(`<!--[\s\S]*?-->|<![^>]*>|</?([A-Za-z][A-Za-z0-9:-]*)((?:[^>"']|"[^"]*"|'[^']*')*)>|[^<]+`)
	keyAttr := regexp.MustCompile(`\sdata-i18n="([^"]*)"`)
	void := map[string]bool{"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true, "img": true,
		"input": true, "link": true, "meta": true, "source": true, "track": true, "wbr": true}
	type open struct {
		name, key string
		content   int // where the element's content starts in the page
	}
	texts := map[string]string{}
	for _, p := range sitePages(t) {
		var stack []open
		keyed := func() string {
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i].key != "" {
					return stack[i].key
				}
			}
			return ""
		}
		within := func(name string) bool {
			for _, o := range stack {
				if o.name == name {
					return true
				}
			}
			return false
		}
		for _, m := range token.FindAllStringSubmatchIndex(p.text, -1) {
			tok := p.text[m[0]:m[1]]
			switch {
			case strings.HasPrefix(tok, "<!"):
				// a comment or the doctype
			case m[2] < 0:
				if strings.TrimSpace(html.UnescapeString(tok)) != "" && keyed() == "" && (within("body") || within("title")) {
					t.Errorf("SITE-I18N-KEY: %s has a text with no data-i18n key: %q", p.name, strings.Join(strings.Fields(tok), " "))
				}
			case strings.HasPrefix(tok, "</"):
				name := strings.ToLower(p.text[m[2]:m[3]])
				for i := len(stack) - 1; i >= 0; i-- {
					if stack[i].name != name {
						continue
					}
					if k := stack[i].key; k != "" {
						text := strings.Join(strings.Fields(p.text[stack[i].content:m[0]]), " ")
						if was, ok := texts[k]; ok && was != text {
							t.Errorf("SITE-I18N-ONE: the key %s names two texts: %q and %q (%s)", k, was, text, p.name)
						}
						texts[k] = text
					}
					stack = stack[:i]
					break
				}
			default:
				name, attrs := strings.ToLower(p.text[m[2]:m[3]]), p.text[m[4]:m[5]]
				key := ""
				if km := keyAttr.FindStringSubmatch(attrs); km != nil {
					if key = km[1]; key == "" {
						t.Errorf("SITE-I18N-KEY: %s has an empty data-i18n key: %s", p.name, tok)
					}
				}
				if key != "" && keyed() != "" {
					t.Errorf("SITE-I18N-NESTED: in %s, the key %s is inside the key %s, whose translation would replace it", p.name, key, keyed())
				}
				if void[name] || strings.HasSuffix(attrs, "/") {
					continue
				}
				stack = append(stack, open{name, key, m[1]})
			}
		}
		if s := regexp.MustCompile(`&mdash;|&#8212;|&#x2014;`).FindString(p.text); s != "" {
			t.Errorf("DOCS-EMDASH: %s has %s", p.name, s)
		}
	}
	if len(texts) == 0 {
		t.Error("SITE-I18N-KEY: no text on the site has a data-i18n key")
	}
	checkPlainPunctuation(t, append(sitePages(t), docText{"site/README.md", string(mustRead(t, "site/README.md"))},
		docText{"site/BRAND.md", string(mustRead(t, "site/BRAND.md"))}))
}

// The pages' heads: on every page the language, the icon and the stylesheet, the same version of
// it everywhere (nginx lets a browser keep it for a week, so a page with another ?v= would get
// another copy); on every page but 404.html its canonical address, the one site/nginx serves it
// at, and what a link preview shows, with the page's own title and description; 404.html is kept
// out of search engines. The pictures a link preview and a phone's home screen show have the sizes
// the head gives, every image and font is small, and every font has its license beside it.
func TestSiteHead(t *testing.T) {
	stylesheet := regexp.MustCompile(`<link rel="stylesheet" href="(/css/site\.css\?v=\d+)">`)
	version, first := "", ""
	for _, p := range sitePages(t) {
		for _, want := range []string{`<html lang="en">`, `<meta charset="utf-8">`, `<meta name="viewport" content="width=device-width, initial-scale=1">`,
			`<link rel="icon" type="image/svg+xml" href="/img/favicon.svg">`, `<meta name="theme-color" content="#f6efe4">`} {
			if !strings.Contains(p.text, want) {
				t.Errorf("SITE-HEAD: %s does not have %s", p.name, want)
			}
		}
		switch m := stylesheet.FindStringSubmatch(p.text); {
		case m == nil:
			t.Errorf("SITE-HEAD: %s does not link /css/site.css?v=<n>", p.name)
		case version == "":
			version, first = m[1], p.name
		case m[1] != version:
			t.Errorf("SITE-HEAD-CSS: %s links %s and %s links %s: every page links the same", first, version, p.name, m[1])
		}
	}
	meta := func(text, attr, name string) string {
		m := regexp.MustCompile(`<meta ` + attr + `="` + regexp.QuoteMeta(name) + `" content="([^"]*)">`).FindStringSubmatch(text)
		if m == nil {
			return ""
		}
		return html.UnescapeString(m[1])
	}
	title := regexp.MustCompile(`<title[^>]*>([^<]*)</title>`)
	previews := 0
	for _, p := range sitePages(t) {
		if p.name == "site/404.html" {
			continue
		}
		previews++
		u := siteURL + strings.TrimPrefix(siteAddress(p.name), "/")
		for _, want := range []string{`<link rel="canonical" href="` + u + `">`, `<meta property="og:url" content="` + u + `">`,
			`<meta property="og:type" content="website">`, `<meta property="og:site_name" content="Bitcoin Cash II">`,
			`<meta property="og:image" content="` + siteURL + `img/og.png">`, `<meta property="og:image:width" content="1200">`,
			`<meta property="og:image:height" content="630">`, `<meta name="twitter:card" content="summary_large_image">`,
			`<meta name="twitter:site" content="@BitcoincashII">`, `<link rel="apple-touch-icon" href="/img/apple-touch-icon.png">`} {
			if !strings.Contains(p.text, want) {
				t.Errorf("SITE-HEAD: %s does not have %s", p.name, want)
			}
		}
		var name string
		if m := title.FindStringSubmatch(p.text); m != nil {
			name = html.UnescapeString(m[1])
		}
		og := meta(p.text, "property", "og:description")
		if name == "" || meta(p.text, "name", "description") == "" || og == "" || meta(p.text, "property", "og:image:alt") == "" {
			t.Errorf("SITE-HEAD-PREVIEW: %s has no title, description, og:description or og:image:alt", p.name)
		}
		// a link preview shows the page's own title, on every service
		for _, c := range []struct{ tag, got, want string }{
			{"og:title", meta(p.text, "property", "og:title"), name},
			{"twitter:title", meta(p.text, "name", "twitter:title"), name},
			{"twitter:description", meta(p.text, "name", "twitter:description"), og},
		} {
			if c.got != c.want {
				t.Errorf("SITE-HEAD-PREVIEW: %s's %s is %q, want %q", p.name, c.tag, c.got, c.want)
			}
		}
	}
	if previews < 2 {
		t.Errorf("SITE-HEAD: the site has %d pages besides 404.html; want the home page and the questions page", previews)
	}
	if !strings.Contains(string(mustRead(t, "site/404.html")), `<meta name="robots" content="noindex">`) {
		t.Error("SITE-HEAD: site/404.html does not keep itself out of search engines")
	}
	if !strings.Contains(string(mustRead(t, siteNginx)), "server_name forgesolo.bch2.org;") {
		t.Errorf("SITE-HEAD: %s does not serve the page's canonical name", siteNginx)
	}
	for f, size := range map[string][2]int{"img/og.png": {1200, 630}, "img/apple-touch-icon.png": {180, 180}} {
		b := mustRead(t, filepath.Join(siteDir, f))
		if len(b) < 24 || string(b[1:4]) != "PNG" {
			t.Errorf("SITE-IMAGE: site/%s is not a PNG", f)
			continue
		}
		if w, h, _ := siteImageSize(b); w != size[0] || h != size[1] {
			t.Errorf("SITE-IMAGE: site/%s is %d x %d, want %d x %d", f, w, h, size[0], size[1])
		}
	}
	for _, f := range []string{"404.html", "robots.txt"} {
		if _, err := os.Stat(filepath.Join(siteDir, f)); err != nil {
			t.Errorf("SITE-FILES: site/%s is missing", f)
		}
	}

	// fonts/<face>-<weight>.woff2 has its license in fonts/OFL-<Face>.txt
	fonts, _ := filepath.Glob(filepath.Join(siteDir, "fonts", "*.woff2"))
	licenses, _ := filepath.Glob(filepath.Join(siteDir, "fonts", "OFL-*.txt"))
	if len(fonts) == 0 {
		t.Error("SITE-FONT: site/fonts has no font")
	}
	for _, f := range fonts {
		base := strings.TrimSuffix(filepath.Base(f), ".woff2")
		face := strings.ReplaceAll(base[:max(strings.LastIndex(base, "-"), 0)], "-", "")
		found := false
		for _, l := range licenses {
			name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(l), "OFL-"), ".txt")
			if face != "" && strings.EqualFold(name, face) && strings.Contains(string(mustRead(t, l)), "SIL OPEN FONT LICENSE") {
				found = true
			}
		}
		if !found {
			t.Errorf("SITE-FONT-LICENSE: site/fonts has no OFL-<face>.txt with the SIL Open Font License for %s", filepath.ToSlash(f))
		}
	}
	for _, dir := range []string{"img", "fonts"} {
		entries, _ := os.ReadDir(filepath.Join(siteDir, dir))
		for _, e := range entries {
			if info, err := e.Info(); err == nil && info.Size() > 150<<10 {
				t.Errorf("SITE-SIZE: site/%s/%s is %d KB; the site's images and fonts are 150 KB or less", dir, e.Name(), info.Size()>>10)
			}
		}
	}
}

// The home page shows real screenshots of Forge Solo's Settings page and dashboard, as WebP. Each
// <img> gives its file's width and height, so the page does not jump while it loads, and a srcset
// of the same picture at its real widths with sizes, so a phone fetches the smaller file. Every
// image has an alt; a described one has its description keyed in data-i18n-alt, as the texts are
// in data-i18n, and a key names one description.
func TestSiteImages(t *testing.T) {
	img := regexp.MustCompile(`(?i)<img\b[^>]*>`)
	alts := map[string]string{}
	shots := 0
	size := func(addr string) (int, int, bool) {
		b, err := os.ReadFile(filepath.Join(siteDir, filepath.FromSlash(siteFile(addr))))
		if err != nil {
			return 0, 0, true // a missing file is SITE-FILE-MISSING's to report
		}
		return siteImageSize(b)
	}
	for _, p := range sitePages(t) {
		for _, tag := range img.FindAllString(p.text, -1) {
			src, _ := siteAttr(tag, "src")
			alt, hasAlt := siteAttr(tag, "alt")
			switch key, _ := siteAttr(tag, "data-i18n-alt"); {
			case !hasAlt:
				t.Errorf("SITE-IMG-ALT: %s has an image with no alt: %s", p.name, tag)
			case alt != "" && key == "":
				t.Errorf("SITE-I18N-ALT: %s has an image whose alt has no data-i18n-alt key: %s", p.name, src)
			case alt != "":
				if was, ok := alts[key]; ok && was != alt {
					t.Errorf("SITE-I18N-ONE: the alt key %s names two descriptions: %q and %q (%s)", key, was, alt, p.name)
				}
				alts[key] = alt
			}
			if path.Ext(src) == ".svg" {
				continue // drawn to any size
			}
			w, h, ok := size(src)
			if !ok {
				t.Errorf("SITE-IMG: %s shows %s, which is not a PNG or WebP file", p.name, src)
				continue
			}
			if ws, _ := siteAttr(tag, "width"); w != 0 && ws != strconv.Itoa(w) {
				t.Errorf("SITE-IMG-SIZE: %s gives %s width=%q; the file is %d px wide", p.name, src, ws, w)
			}
			if hs, _ := siteAttr(tag, "height"); h != 0 && hs != strconv.Itoa(h) {
				t.Errorf("SITE-IMG-SIZE: %s gives %s height=%q; the file is %d px high", p.name, src, hs, h)
			}
			if path.Ext(src) != ".webp" {
				continue
			}
			shots++
			srcset, _ := siteAttr(tag, "srcset")
			if _, ok := siteAttr(tag, "sizes"); !ok || srcset == "" {
				t.Errorf("SITE-IMG-SRCSET: %s shows %s without a srcset and sizes", p.name, src)
				continue
			}
			self := false
			for _, c := range strings.Split(srcset, ",") {
				f := strings.Fields(c)
				if len(f) != 2 || !strings.HasSuffix(f[1], "w") {
					t.Errorf("SITE-IMG-SRCSET: %s's srcset for %s has %q; give each file with its width, \"x.webp 800w\"", p.name, src, c)
					continue
				}
				self = self || f[0] == src
				cw, ch, ok := size(f[0])
				if !ok || cw == 0 {
					continue
				}
				if f[1] != strconv.Itoa(cw)+"w" {
					t.Errorf("SITE-IMG-SRCSET: %s gives %s as %s; the file is %d px wide", p.name, f[0], f[1], cw)
				}
				// the same picture: scaled to the img's width, its height is within a pixel
				if d := cw*h - ch*w; w != 0 && (d > cw || -d > cw) {
					t.Errorf("SITE-IMG-SRCSET: %s's srcset for %s has %s, which is %d x %d, not the same picture as %d x %d", p.name, src, f[0], cw, ch, w, h)
				}
			}
			if !self {
				t.Errorf("SITE-IMG-SRCSET: %s's srcset for %s does not list %s itself", p.name, src, src)
			}
		}
	}
	if shots == 0 {
		t.Error("SITE-IMG: no page shows a screenshot of the app")
	}
}

// The pages with the site's header, the home page and the questions page, share it: the same links
// in the same order, the questions page among them, and on each page the link to the page itself
// marked as the current one. A page that draws the mark draws the home page's.
func TestSitePagesShareTheirHeader(t *testing.T) {
	nav := regexp.MustCompile(`(?s)<nav class="nav"[^>]*>(.*?)</nav>`)
	link := regexp.MustCompile(`<a\s[^>]*>`)
	mark := regexp.MustCompile(`(?s)<symbol id="mark".*?</symbol>`)
	homeMark := mark.FindString(string(mustRead(t, "site/index.html")))
	if homeMark == "" {
		t.Error("SITE-MARK: site/index.html does not draw the mark (<symbol id=\"mark\">)")
	}
	var want []string
	first := ""
	for _, p := range sitePages(t) {
		if m := mark.FindString(p.text); m != "" && m != homeMark {
			t.Errorf("SITE-MARK: %s draws the mark differently from site/index.html", p.name)
		}
		n := nav.FindStringSubmatch(p.text)
		if n == nil {
			continue
		}
		self := siteAddress(p.name)
		var got []string
		for _, a := range link.FindAllString(n[1], -1) {
			u, _ := siteAttr(a, "href")
			if strings.HasPrefix(u, "#") {
				u = self + u
			}
			got = append(got, u)
			if cur, _ := siteAttr(a, "aria-current"); (u == self) != (cur == "page") {
				t.Errorf("SITE-NAV-CURRENT: in %s's header, the link to %s has aria-current=%q; only the link to the page itself has \"page\"", p.name, u, cur)
			}
		}
		if !slices.Contains(got, "/faq/") {
			t.Errorf("SITE-NAV: %s's header does not link to the questions page, /faq/", p.name)
		}
		if want == nil {
			want, first = got, p.name
		} else if !slices.Equal(got, want) {
			t.Errorf("SITE-NAV: %s's header links %v and %s's links %v: every page has the same header", first, want, p.name, got)
		}
	}
	if want == nil {
		t.Error("SITE-NAV: no page has the site's header")
	}
}

// The questions page, /faq/: its list of questions is the questions on it, in the same order; each
// answer links to where its details are (the README, mostly), and the page re-explains no more;
// each word of the glossary has a #glossary-<word> anchor; and a link drawn as a word to look up
// (class "term", on any page) leads to one of them. TestSiteUsesItsOwnFiles checks every anchor is there.
func TestSiteQuestionsPage(t *testing.T) {
	const page = "site/faq/index.html"
	faq := string(mustRead(t, page))
	toc := regexp.MustCompile(`(?s)<ul class="toc-list"[^>]*>(.*?)</ul>`).FindStringSubmatch(faq)
	if toc == nil {
		t.Fatalf("SITE-FAQ-LIST: %s has no list of its questions (<ul class=\"toc-list\">)", page)
	}
	var listed, asked []string
	for _, m := range regexp.MustCompile(`href="#([^"]+)"`).FindAllStringSubmatch(toc[1], -1) {
		listed = append(listed, m[1])
	}
	starts := regexp.MustCompile(`<div class="qa" id="([^"]+)">`).FindAllStringSubmatchIndex(faq, -1)
	for i, m := range starts {
		id, end := faq[m[2]:m[3]], len(faq)
		if i+1 < len(starts) {
			end = starts[i+1][0]
		}
		answer := faq[m[1]:end]
		if j := strings.Index(answer, "</section>"); j >= 0 {
			answer = answer[:j]
		}
		asked = append(asked, id)
		if !strings.Contains(answer, `href="https://`) {
			t.Errorf("SITE-FAQ-DETAILS: the answer #%s in %s links to nowhere its details are", id, page)
		}
	}
	if len(asked) == 0 {
		t.Fatalf("SITE-FAQ: %s has no questions (<div class=\"qa\" id=...>)", page)
	}
	if !slices.Equal(listed, asked) {
		t.Errorf("SITE-FAQ-LIST: %s lists the questions %v, and asks %v", page, listed, asked)
	}
	words := regexp.MustCompile(`<div class="word" id="([^"]+)">`).FindAllStringSubmatch(faq, -1)
	if len(words) == 0 {
		t.Errorf("SITE-FAQ-WORDS: %s has no glossary (<div class=\"word\" id=...>)", page)
	}
	for _, w := range words {
		if !strings.HasPrefix(w[1], "glossary-") {
			t.Errorf("SITE-FAQ-WORDS: the word #%s in %s does not have a glossary-<word> anchor", w[1], page)
		}
	}
	term := regexp.MustCompile(`<a\s[^>]*\bclass="term"[^>]*>`)
	terms := 0
	for _, p := range sitePages(t) {
		for _, a := range term.FindAllString(p.text, -1) {
			terms++
			u, _ := siteAttr(a, "href")
			if strings.HasPrefix(u, "#") {
				u = siteAddress(p.name) + u
			}
			if !strings.HasPrefix(u, "/faq/#glossary-") {
				t.Errorf("SITE-TERM: %s draws %s as a word to look up; it leads to %s, not to a word of the glossary", p.name, a, u)
			}
		}
	}
	if terms == 0 {
		t.Error("SITE-TERM: no page links a word to the glossary")
	}
}

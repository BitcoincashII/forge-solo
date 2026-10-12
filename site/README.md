# Forge Solo's website

https://forgesolo.bch2.org, Forge Solo's home for people new to mining: what Forge Solo is, what you
need, how Solo and TIDES pay, where to get it for each platform, how to connect a miner and what the
app looks like; and, at /faq/, short answers to newcomers' questions and the words they will meet.
Static files, served by nginx on the pool box (144.202.73.66).

| Path | What |
|---|---|
| `index.html` | the home page |
| `faq/index.html` | the questions page, /faq/: twelve questions and a glossary, each answer linking to where its details are (the README, mostly) |
| `404.html` | the answer to any other address |
| `css/site.css` | the styles, and the drawings' colours |
| `fonts/*.woff2`, `fonts/OFL-*.txt` | Zilla Slab Bold, Source Sans 3 and Source Code Pro Medium, served from the site, and their licenses |
| `img/favicon.svg` | the mark on a dark square: the browser tab's icon |
| `img/mark.svg` | the mark on paper, in the 404 page's header |
| `img/apple-touch-icon.png`, `img/og.png` | the icon for a phone's home screen, and the picture a link preview shows |
| `img/bch2-coin.svg` | the Bitcoin Cash II coin, byte for byte `bch2_logo.svg` from [BitcoincashII/BCH2-Graphics](https://github.com/BitcoincashII/BCH2-Graphics) at commit d8f44f8; that repository has no licence file, and the coin is the BCH2 project's own mark, used here as it is (BRAND.md says where) |
| `img/forge-solo-*.webp` | screenshots of Forge Solo 1.0.15's Settings page and dashboard (Solo and TIDES), each also at twice the size (`@2x`), on the home page, where each links to its `@2x` file |
| `robots.txt` | lets every crawler in |
| `deploy.sh` | copies the files above to the box's web root |
| `BRAND.md` | how the site looks: the colours, the type, the mark, the drawings and the words |
| `nginx/forgesolo.bch2.org.conf` | the box's nginx server for the site (a draft until the setup below is done) |

The pages say what Forge Solo is and point to the README on GitHub for the directions, by the
anchors of its headings, rather than repeating them. The Windows and Linux downloads are on
[the latest release](https://github.com/BitcoincashII/forge-solo/releases/latest), never on the
site; Umbrel installs Forge Solo from its app store. The pages have no script, and load nothing
from another site: the nginx server's Content-Security-Policy allows only their own styles, fonts
and images. They link their files from the site's root (`/css/site.css?v=6`), since nginx serves
`404.html` at whatever address was asked for and the questions page is a folder down. A
screenshot's link opens its `@2x` file on the browser's own image page, whose inline styles the
policy refuses: Chrome's console says so, and the image still shows, fitted to the window.
[BRAND.md](BRAND.md) is the look, and why.

`site_test.go` fails when a heading the site links to is renamed, when the site links to a download
or a release other than the latest, when a page carries a script or a style the policy refuses, or
loads a file that is not in site/, when a link leads to an anchor a page does not have, when a text
has no `data-i18n` key or an image's description no `data-i18n-alt` key, when a page's link preview
does not carry its own title and address, when the pages' headers differ, when a page with the
header does not end with the way to get the app, when an image's size or `srcset` does not match
its files, when a screenshot does not link to its full-size file, when the questions page's lists
and its questions or glossary words differ, when a
location in the nginx server drops its security headers, when the server would be the box's
default for its port, and when site/ holds a file that is not the site's and that deploy.sh would
publish.

When a release changes what the page says, change the page with it, and the version in its footer.
A changed stylesheet gets a new `?v=` on every page, as on pool.bch2.org: nginx lets browsers keep it
for a week.

## Translations

The pages are in English. Each text on them has a `data-i18n` key, as on pool.bch2.org, so that a
language can be added the way the pool adds one: a `lang/fs-<code>.js` with the texts by key
(`var PT = { _code: "de", fs_hero_summary: "..." }`), and a loader like the pool's
`js/pool-i18n.js`, which picks the language from the `lang` cookie bch2.org sets for all of
`.bch2.org`, then the browser's, and puts each text in its element. The screenshots' descriptions
(their `alt`) have their keys in `data-i18n-alt`, which the pool's loader does not read yet: the
site's loader sets each image's `alt` from it. The policy then needs
`script-src 'self'`, and the loader's language menu needs its styles in `css/site.css`: the pool's
loader adds them in a `<style>` element, which the policy refuses. The fonts here have Latin letters
only: a language with other letters needs their subsets too.

## Look at it locally

```sh
python3 -m http.server -d site 8000
```

and open http://127.0.0.1:8000. That server sends no Content-Security-Policy.

## One-time setup on the pool box

As root on 144.202.73.66. Nothing here changes pool.bch2.org or the box's other sites.

```sh
# 1. The name points straight at this box, not through Cloudflare.
getent hosts forgesolo.bch2.org                      # 144.202.73.66

# 2. A checkout of this repository, and the site's files in /var/www/forgesolo.
git clone https://github.com/BitcoincashII/forge-solo /root/forge-solo
/root/forge-solo/site/deploy.sh -n
/root/forge-solo/site/deploy.sh

# 3. The port-80 server alone (the file above its "# HTTPS" line), to answer the certificate's challenge.
sed '/^# HTTPS$/,$d' /root/forge-solo/site/nginx/forgesolo.bch2.org.conf > /etc/nginx/sites-available/forgesolo.bch2.org
ln -s /etc/nginx/sites-available/forgesolo.bch2.org /etc/nginx/sites-enabled/forgesolo.bch2.org
nginx -t && systemctl reload nginx
curl -sI http://forgesolo.bch2.org/ | head -n 3       # 301 to https://forgesolo.bch2.org/

# 4. The certificate, the way the box's certificates are made: certbot's webroot authenticator,
#    with the webroot forge.bch2.org and hashforge.bch2.org use, and nginx reloaded on renewal.
certbot certonly --webroot -w /var/www/letsencrypt -d forgesolo.bch2.org --deploy-hook 'systemctl reload nginx'
certbot certificates --cert-name forgesolo.bch2.org  # Domains: forgesolo.bch2.org, Key Type: ECDSA
grep -E '^(authenticator|webroot_path|renew_hook)' /etc/letsencrypt/renewal/forgesolo.bch2.org.conf

# 5. The whole server, with HTTPS.
cp /root/forge-solo/site/nginx/forgesolo.bch2.org.conf /etc/nginx/sites-available/forgesolo.bch2.org
nginx -t && systemctl reload nginx
curl -sI https://forgesolo.bch2.org/ | grep -i -E '^(HTTP|content-security-policy|strict-transport-security)'
certbot renew --dry-run --cert-name forgesolo.bch2.org
```

certbot's timer renews the certificate from then on, as it does the others. To take the site down:
`rm /etc/nginx/sites-enabled/forgesolo.bch2.org && nginx -t && systemctl reload nginx`, then
`certbot delete --cert-name forgesolo.bch2.org` and `rm -r /var/www/forgesolo`.

## Deploy

```sh
cd /root/forge-solo && git pull --ff-only
site/deploy.sh -n      # what would change
site/deploy.sh
```

deploy.sh copies site/ to /var/www/forgesolo and deletes what site/ no longer has. It refuses a
checkout without `index.html`, and one whose site/ has a change that is not committed or a file git
does not track, which it would publish too. It never sends itself, this README, `BRAND.md` or
`nginx/`, and leaves `.well-known/` and backup copies (`*.bak*`, `*.pre-*`) alone. A change to
`nginx/forgesolo.bch2.org.conf` is not deployed: copy it to
`/etc/nginx/sites-available/forgesolo.bch2.org`, then `nginx -t && systemctl reload nginx`.

# Forge Solo's website

https://forgesolo.bch2.org, one static page, served by nginx on the pool box (144.202.73.66).

| Path | What |
|---|---|
| `index.html` | the page |
| `404.html` | the answer to any other address |
| `css/site.css` | the styles: pool.bch2.org's colours and type |
| `fonts/inter-latin.woff2`, `fonts/OFL.txt` | Inter, the pool's font, served from the site, and its license |
| `img/logo.svg` | the BCH2 logo, as on the dashboard (also the favicon) |
| `img/apple-touch-icon.png`, `img/og.png` | the logo for a phone's home screen, and the picture a link preview shows |
| `robots.txt` | lets every crawler in |
| `deploy.sh` | copies the files above to the box's web root |
| `nginx/forgesolo.bch2.org.conf` | the box's nginx server for the site (a draft until the setup below is done) |

The page says what Forge Solo is and points to the README on GitHub for the directions, by the
anchors of its headings, rather than repeating them. The Windows and Linux downloads are on
[the latest release](https://github.com/BitcoincashII/forge-solo/releases/latest), never on the
site; Umbrel installs Forge Solo from its app store. The page has no script, and loads nothing
from another site: the nginx server's Content-Security-Policy allows only its own styles, font
and images.

`site_test.go` fails when a heading the site links to is renamed, when the site links to a download
or a release other than the latest, when a page carries a script or a style the policy refuses, when
a location in the nginx server drops its security headers, when the server would be the box's
default for its port, and when site/ holds a file that is not the site's and that deploy.sh would
publish.

When a release changes what the page says, change the page with it, and the version in its footer.

## Translations

The page is in English. Each text on it has a `data-i18n` key, as on pool.bch2.org, so that a
language can be added the way the pool adds one: a `lang/fs-<code>.js` with the texts by key
(`var PT = { _code: "de", fs_hero_lede: "..." }`), and a loader like the pool's `js/pool-i18n.js`,
which picks the language from the `lang` cookie bch2.org sets for all of `.bch2.org`, then the
browser's, and puts each text in its element. The policy then needs `script-src 'self'`, and the
loader's language menu needs its styles in `css/site.css`: the pool's loader adds them in a
`<style>` element, which the policy refuses. Inter's file here has Latin letters only: a language
with other letters needs its subset too.

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
does not track, which it would publish too. It never sends itself, this README or `nginx/`, and
leaves `.well-known/` and backup copies (`*.bak*`, `*.pre-*`) alone. A change to
`nginx/forgesolo.bch2.org.conf` is not deployed: copy it to
`/etc/nginx/sites-available/forgesolo.bch2.org`, then `nginx -t && systemctl reload nginx`.

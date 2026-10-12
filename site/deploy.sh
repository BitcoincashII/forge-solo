#!/bin/bash
# Deploy Forge Solo's website, the files beside this script, to nginx's root for forgesolo.bch2.org.
# Run it as root on the pool box, from a checkout of github.com/BitcoincashII/forge-solo
# (site/README.md). Pass -n to see what would change without changing anything.
#
# --delete makes the live site match site/, but leaves alone what is not the site's own: .well-known/
# and the backup copies kept beside the files (*.bak*, *.pre-*). Those are neither sent nor deleted.
# This script, site/README.md and the nginx draft are the repository's, not the site's: never sent.
set -euo pipefail

SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/"
DST=/var/www/forgesolo/

case "${1:-}" in
    -n) DRY=--dry-run ;;
    "") DRY= ;;
    *) echo "usage: $0 [-n]"; exit 2 ;;
esac

# An empty or missing source with --delete would empty the live site.
[ -f "${SRC}index.html" ] || { echo "ABORT: ${SRC}index.html is missing; nothing was changed."; exit 1; }

# Only what is committed: a file changed, added or left in the checkout's site/ (an editor's backup,
# say) would be published too, and taken off the live site again by the next deploy from a clean one.
if ! dirty=$(git -C "$SRC" status --porcelain --untracked-files=all --ignored -- . 2>&1); then
    echo "ABORT: ${SRC} is not in a git checkout ($dirty); nothing was changed."
    exit 1
fi
if [ -n "$dirty" ]; then
    echo "ABORT: ${SRC} has changes that are not committed; nothing was changed:"
    echo "$dirty"
    exit 1
fi

# --chmod: nginx reads the files as www-data, whatever umask the checkout was made with.
rsync -av --delete $DRY \
    --chmod=D755,F644 \
    --exclude='/.well-known/' \
    --exclude='*.bak*' \
    --exclude='*.pre-*' \
    --exclude='/deploy.sh' \
    --exclude='/README.md' \
    --exclude='/nginx/' \
    "$SRC" "$DST"

if [ -n "$DRY" ]; then
    echo "Dry run: nothing was changed."
else
    echo "Forge Solo's website deployed to $DST"
fi

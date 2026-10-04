#!/usr/bin/env bash
# Fail unless every image docker-compose.yml pins can be pulled without logging in.
#
# GitHub makes a package private when it is first published, even in a public repository, and an
# Umbrel cannot log in to pull: every install of the release would fail, and every update would stop
# the app and then fail to pull, leaving it stopped. Run this after the tag's images are pushed and
# before the store is synced (README, "Releasing"); the Tests run the re-pin starts runs it too.
#
#   scripts/check-images-public.sh [COMPOSE_FILE]
#
# For each image it asks the registry for the pinned digest's manifest the way docker pull does,
# without credentials: unauthenticated first, then with the anonymous token the registry offers.
# Anything but 200 fails, and so does an image without a digest or a registry it cannot reach.
set -uo pipefail
compose=${1:-$(dirname "${BASH_SOURCE[0]}")/../docker-compose.yml}

# Every manifest type a pinned digest can name: a multi-platform index or a single image.
accept='application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json'

images=$(sed -n 's/^[[:space:]]*image:[[:space:]]*\([^[:space:]]*\).*/\1/p' "$compose")
if [ -z "$images" ]; then
  echo "no images in $compose" >&2
  exit 1
fi

# param NAME: the value of NAME="..." in the registry's WWW-Authenticate challenge.
param() { sed -n "s/.*[ ,]$1=\"\([^\"]*\)\".*/\1/p" <<<" $challenge"; }

failed=0
for ref in $images; do
  ref=${ref//[\"\']/}
  if [[ ! $ref =~ ^([^/]+)/([^@]+)@(sha256:[0-9a-f]{64})$ ]]; then
    echo "FAIL $ref: not a registry/name[:tag]@sha256:digest reference" >&2
    failed=1
    continue
  fi
  host=${BASH_REMATCH[1]} repo=${BASH_REMATCH[2]} digest=${BASH_REMATCH[3]}
  if [[ $host != *.* && $host != *:* && $host != localhost ]]; then
    echo "FAIL $ref: names no registry host, and this check reads only images that do" >&2
    failed=1
    continue
  fi
  [[ ${repo##*/} == *:* ]] && repo=${repo%:*} # the tag
  scheme=https
  [[ $host == 127.0.0.1:* || $host == localhost:* ]] && scheme=http # a registry on this machine
  url=$scheme://$host/v2/$repo/manifests/$digest

  if ! headers=$(curl -sS -I --max-time 30 -H "Accept: $accept" "$url" | tr -d '\r'); then
    echo "FAIL $ref: $host cannot be reached" >&2
    failed=1
    continue
  fi
  code=$(sed -n '1s/^HTTP\/[0-9.]* \([0-9]*\).*/\1/p' <<<"$headers")
  if [ "$code" = 401 ]; then
    challenge=$(sed -n 's/^[Ww][Ww][Ww]-[Aa]uthenticate:[[:space:]]*[Bb]earer[[:space:]]*//p' <<<"$headers")
    realm=$(param realm)
    if [ -z "$realm" ]; then
      echo "FAIL $ref: $host asks for a login and offers no anonymous token" >&2
      failed=1
      continue
    fi
    token=$(curl -sS --fail --max-time 30 -G "$realm" --data-urlencode "service=$(param service)" \
      --data-urlencode "scope=$(param scope)" 2>/dev/null | sed -n 's/.*"token"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
    if [ -z "$token" ]; then
      echo "FAIL $ref: needs a login. $host gives no anonymous pull token: the package is private, or does not exist" >&2
      failed=1
      continue
    fi
    code=$(curl -sS -I -o /dev/null -w '%{http_code}' --max-time 30 -H "Accept: $accept" \
      -H "Authorization: Bearer $token" "$url") || code=unreachable
  fi
  case $code in
    200) echo "ok   $ref" ;;
    404) echo "FAIL $ref: $host has no such digest" >&2; failed=1 ;;
    *) echo "FAIL $ref: $host answers $code" >&2; failed=1 ;;
  esac
done
exit $failed

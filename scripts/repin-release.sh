#!/usr/bin/env bash
# Re-pin docker-compose.yml and packaging_test.go to the digests CI just published.
#
# This is the step that used to be done by hand and silently was not: the 1.0.8 release bumped
# umbrel-app.yml and left the compose pinned at 1.0.0, so for eight releases this repo would
# have installed the ORIGINAL images. Docker resolves name:tag@digest by DIGEST, so bumping the
# tag alone ships the previous release under a new version number.
#
#   scripts/repin-release.sh <version> <node> <node1175> <api> <stratum> <web>
#
# Digests are the sha256 values (with or without the "sha256:" prefix) CI published for <version>.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

[ $# -eq 6 ] || { echo "usage: $0 <version> <node> <node1175> <api> <stratum> <web>" >&2; exit 2; }
VERSION="$1"; shift
declare -A D=( [node]="$1" [node1175]="$2" [api]="$3" [stratum]="$4" [web]="$5" )
for k in "${!D[@]}"; do
  D[$k]="${D[$k]#sha256:}"
  [[ "${D[$k]}" =~ ^[0-9a-f]{64}$ ]] || { echo "digest for $k is not a sha256: ${D[$k]}" >&2; exit 1; }
done

# The manifest is the source of truth for the version; CI must not invent one.
manifest_version=$(sed -n 's/^version: *"\{0,1\}\([^"]*\)"\{0,1\}.*/\1/p' umbrel-app.yml | head -1)
if [ "$manifest_version" != "$VERSION" ]; then
  echo "umbrel-app.yml says $manifest_version but re-pinning $VERSION -- bump the manifest first" >&2
  exit 1
fi

for k in node node1175 api stratum web; do
  # name:ANYTAG@sha256:ANY  ->  name:VERSION@sha256:NEW   (keeps the rest of the line intact)
  sed -i -E "s#(ghcr\.io/bitcoincashii/forge-solo-${k}):[^@]+@sha256:[0-9a-f]{64}#\1:${VERSION}@sha256:${D[$k]}#" docker-compose.yml
  sed -i -E "s#(\"forge-solo-${k}\": *)\"[0-9a-f]{64}\"#\1\"${D[$k]}\"#" packaging_test.go
done
sed -i -E "s#(const releaseDigestsForVersion = )\"[^\"]*\"#\1\"${VERSION}\"#" packaging_test.go

echo "re-pinned to ${VERSION}:"
grep -oE 'forge-solo-[a-z0-9]+:[^@]+@sha256:[0-9a-f]{12}' docker-compose.yml | sed 's/^/  /'

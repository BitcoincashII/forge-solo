#!/usr/bin/env bash
# publish-release.sh <version>: the release page of the tag v<version>, as release.yml's publish
# job makes it once the installer is signed and its signature verified. Run in the checkout, with
# the signed ForgeSolo-Setup-<version>.exe there:
#
#   1. SHA256SUMS-windows and the page's text are made from the installer as it is. The page has
#      the version's section of RELEASE_NOTES.md, then the downloads with the installer's SHA-256
#      (the line of SHA256SUMS-windows), how to check a download on Windows and on Linux, and what
#      the installer was built from. Nothing is published unless all of it is there, and the
#      installer is hashed again just before the upload.
#   2. Once gh says there is no release for the tag yet, draft or not, the draft is created with the
#      installer and SHA256SUMS-windows, then read back: those two files, both uploaded, the
#      installer with its size, and with its SHA-256 where GitHub gives one. A draft this run
#      created that is not so is deleted again (its tag stays), so a re-run starts clean. A release
#      that was there before is never touched.
#
# Environment: SIGNING_CERT_SHA256, BCH2_VERSION, ESF_VERSION and PG_VERSION (release.yml's), and
# GITHUB_REPOSITORY, GITHUB_SHA and GITHUB_REF_NAME (the tag) and GH_TOKEN, which GitHub gives.
# release_page_test.go runs it as the workflow does, with a fake installer and a fake gh.
set -euo pipefail

fail() {
  echo "::error::$*" >&2
  exit 1
}

V=${1:-}
case "$V" in '' | *[!0-9A-Za-z.-]*) fail "not a version: '$V'" ;; esac
TAG="v${V}"
SETUP="ForgeSolo-Setup-${V}.exe"
SUMS=SHA256SUMS-windows

[[ ${GITHUB_REF_NAME:-} == "$TAG" ]] || fail "the tag is '${GITHUB_REF_NAME:-}', not ${TAG}"
[[ ${SIGNING_CERT_SHA256:-} =~ ^([0-9A-F]{2}:){31}[0-9A-F]{2}$ ]] || fail "SIGNING_CERT_SHA256 is not a SHA-256 fingerprint"
[[ ${GITHUB_REPOSITORY:-} =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || fail "GITHUB_REPOSITORY is not owner/name"
[[ ${GITHUB_SHA:-} =~ ^[0-9a-f]{40}$ ]] || fail "GITHUB_SHA is not a commit"
for n in BCH2_VERSION ESF_VERSION PG_VERSION; do
  [[ ${!n:-} =~ ^[0-9][0-9.]*$ ]] || fail "$n is not a version"
done

# The installer as it will be uploaded.
[[ -s $SETUP ]] || fail "$SETUP is not here"
[[ "$(head -c 2 "$SETUP")" == MZ ]] || fail "$SETUP is not a Windows program"
HASH=$(sha256sum "$SETUP" | cut -d' ' -f1)
[[ $HASH =~ ^[0-9a-f]{64}$ ]] || fail "sha256sum gave no SHA-256 for $SETUP"
SIZE=$(wc -c <"$SETUP" | tr -d ' ')
printf '%s  %s\n' "$HASH" "$SETUP" >"$SUMS"
sha256sum -c "$SUMS" >/dev/null || fail "$SUMS does not check against $SETUP"
SUMS_HASH=$(sha256sum "$SUMS" | cut -d' ' -f1)

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
NOTES="$WORK/notes.md"

# What changed goes first: the section of RELEASE_NOTES.md headed "## <version>". A release without
# one stops here rather than publishing a page that says nothing.
awk -v h="## ${V}" '$0 == h {on = 1; next} /^## / {on = 0} on' RELEASE_NOTES.md >"$WORK/changes.md"
grep -q '[^[:space:]]' "$WORK/changes.md" || fail "RELEASE_NOTES.md has no section headed '## ${V}'"

# The rest of the page, with {{NAME}} for each value. Every value was checked above: none holds a
# character sed would read in a replacement.
cat >"$WORK/downloads.md" <<'EOF'

---

## Downloads

- **Umbrel:** update Forge Solo from the App Store on your Umbrel.
- **Windows:** `{{SETUP}}`, signed by the certificate with SHA-256 fingerprint `{{CERT}}`.
  The installer's SHA-256 is `{{HASH}}`, as in `SHA256SUMS-windows`.
- **Linux:** `forge-solo-{{V}}-linux-<arch>.tar.gz`, where `<arch>` is what `uname -m` prints; checksums in `SHA256SUMS-linux`.

## Check your download

- **Windows:** in PowerShell, in the folder you downloaded it to, run `Get-FileHash .\{{SETUP}}`.
  The Hash it shows must be the installer's SHA-256 above (PowerShell shows it in capital letters).
  Or right-click the installer, choose **Properties**, then **Digital Signatures**: the signer is
  **BCH2 Software**.
- **Linux:** in the folder with your download and `SHA256SUMS-linux`, run
  `grep x86_64 SHA256SUMS-linux | sha256sum -c -`, with what `uname -m` prints in place of `x86_64`:
  it says OK.

The Windows installer was built and signed by CI from a clean runner:

| | |
|---|---|
| Services built from | `{{REPO}}@{{SHA}}` |
| BCH2 node | {{BCH2}} |
| 1175 node | {{ESF}} |
| PostgreSQL | {{PG}}: only to move an earlier version's database |

Every bundled component is fetched during the build and sha256-verified. `SHA256SUMS-windows` covers the installer.
EOF
sed -e "s|{{SETUP}}|${SETUP}|g" -e "s|{{CERT}}|${SIGNING_CERT_SHA256}|g" -e "s|{{HASH}}|${HASH}|g" \
  -e "s|{{V}}|${V}|g" -e "s|{{REPO}}|${GITHUB_REPOSITORY}|g" -e "s|{{SHA}}|${GITHUB_SHA}|g" \
  -e "s|{{BCH2}}|${BCH2_VERSION}|g" -e "s|{{ESF}}|${ESF_VERSION}|g" -e "s|{{PG}}|${PG_VERSION}|g" \
  "$WORK/downloads.md" >"$WORK/filled.md"
# Only the part filled in here: the version's notes may well hold a "{{" of their own.
if grep -q '{{' "$WORK/filled.md"; then
  fail "the page's text has a value left out: $(grep -o '{{[A-Z0-9]*}}' "$WORK/filled.md" | sort -u | tr '\n' ' ')"
fi
cat "$WORK/changes.md" "$WORK/filled.md" >"$NOTES"
for want in "The installer's SHA-256 is \`${HASH}\`" "Get-FileHash .\\${SETUP}" "${SIGNING_CERT_SHA256}" "**BCH2 Software**"; do
  grep -qF -- "$want" "$NOTES" || fail "the page's text does not say: $want"
done
echo "The release page, as it will be published:"
cat "$NOTES"
echo "---- end of the page"
echo "$SETUP: $SIZE bytes, SHA-256 $HASH"

# The installer must still be the one hashed above.
[[ "$(sha256sum "$SETUP" | cut -d' ' -f1)" == "$HASH" ]] || fail "$SETUP changed while the page was made"

# Go on only when gh says there is no release for the tag, draft or not. Any other answer (GitHub
# down for a moment, say) stops here with nothing created: going on then could make a second draft
# beside one a cancelled run left, and the read-back and the clean-up below find a draft by its tag.
if seen=$(gh release view "$TAG" --json tagName 2>&1); then
  fail "a release for ${TAG} is there already: it is left as it is. Delete it first if it is a broken draft."
elif [[ $seen != *"release not found"* ]]; then
  fail "could not tell whether a release for ${TAG} is there (gh: ${seen}). Nothing was created: run the job again."
fi

# A draft this run created and something went wrong with: delete it again, so that nothing half made
# stays behind and the job can simply be run again.
drop_draft() {
  echo "::error::$*" >&2
  if [[ "$(gh release view "$TAG" --json isDraft --jq .isDraft 2>/dev/null || true)" == true ]]; then
    if gh release delete "$TAG" --yes; then
      echo "The draft this run created was deleted (the tag stays): run the job again." >&2
    else
      echo "::error::the draft this run created could not be deleted: delete it (gh release delete ${TAG} --yes) before running the job again" >&2
    fi
  fi
  exit 1
}

gh release create "$TAG" "$SETUP" "$SUMS" --verify-tag --draft \
  --title "Forge Solo ${V}" --notes-file "$NOTES" ||
  drop_draft "the release could not be created with ${SETUP} and ${SUMS} on it"

# Read back what GitHub has.
id=$(gh release view "$TAG" --json databaseId --jq .databaseId) || drop_draft "the new draft could not be read back"
[[ $id =~ ^[0-9]+$ ]] || drop_draft "the new draft has no id ('${id}')"
gh api "repos/${GITHUB_REPOSITORY}/releases/${id}" >"$WORK/release.json" || drop_draft "the new draft could not be read back"
jq -e --arg setup "$SETUP" --arg sums "$SUMS" --argjson size "$SIZE" \
  --arg digest "sha256:${HASH}" --arg sums_digest "sha256:${SUMS_HASH}" '
  .draft == true
  and ([.assets[].name] | sort) == ([$setup, $sums] | sort)
  and all(.assets[]; .state == "uploaded")
  and any(.assets[]; .name == $setup and .size == $size and (.digest == null or .digest == $digest))
  and any(.assets[]; .name == $sums and (.digest == null or .digest == $sums_digest))' \
  "$WORK/release.json" >/dev/null ||
  drop_draft "the new draft is not as uploaded: $(jq -c '{draft, assets: [.assets[] | {name, size, state, digest}]}' "$WORK/release.json" 2>/dev/null || echo unreadable)"
echo "Draft ${TAG} created: ${SETUP} (${SIZE} bytes, SHA-256 ${HASH}; GitHub's digest: $(jq -r --arg setup "$SETUP" '.assets[] | select(.name == $setup) | .digest // "not given"' "$WORK/release.json")) and ${SUMS}."

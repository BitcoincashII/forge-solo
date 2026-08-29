#!/usr/bin/env python3
"""Fail if this repo's umbrel-app.yml disagrees with the published store listing.

Publishing a release means hand-copying umbrel-app.yml into
BitcoincashII/umbrel-app-store at bch2-apps-forge-solo/umbrel-app.yml. Nothing
enforced that, and a missed copy is invisible: the store kept serving the old
file while this repo looked correct.

That is not hypothetical. The dashboard port moved 3080 -> 31175 in v1.0.5 to fix
an install failure ("Bind for 0.0.0.0:3080 failed: port is already allocated");
the fix went into the store and was never mirrored here, so this repo's manifest
was wrong across four releases. Separately, the store went on advertising
"Automatic solo payouts to your address" -- a feature this app does not have --
after the claim had been corrected here.

Compares PARSED values, not bytes, so comments and line wrapping may differ.
"""
import os
import sys
import difflib
import urllib.request

try:
    import yaml
except ImportError:
    print("PyYAML is required: pip install pyyaml", file=sys.stderr)
    sys.exit(2)

# The contents API, not raw.githubusercontent.com. Raw is served through a CDN that can
# keep returning the previous file for minutes after a push, which would make this report
# drift that had just been fixed -- a check that cries wolf gets ignored, and then it is
# worse than no check.
STORE_DIR = (
    "https://api.github.com/repos/BitcoincashII/umbrel-app-store/contents/"
    "bch2-apps-forge-solo/{name}?ref=main"
)

# Every file the store serves for this app, not just the manifest.
#
# Checking the manifest alone is how the store's compose sat on 1.0.8 images while its
# manifest advertised 1.0.9: the version field matched what the check looked at, so the drift
# was invisible, and a fresh install got neither version. The compose is the file that decides
# which images a user actually runs.
LOCAL = "umbrel-app.yml"
STORE_FILES = ["umbrel-app.yml", "docker-compose.yml", "exports.sh", "init-db.sql"]


def main() -> int:
    with open(LOCAL) as fh:
        local = yaml.safe_load(fh)

    def fetch(name: str) -> bytes:
        req = urllib.request.Request(
            STORE_DIR.format(name=name),
            headers={
                "Accept": "application/vnd.github.raw",
                "User-Agent": "forge-solo-manifest-drift-check",
            },
        )
        # GITHUB_TOKEN lifts the unauthenticated rate limit; the repo is public so the check
        # still works without one, which keeps it runnable locally.
        token = os.environ.get("GITHUB_TOKEN")
        if token:
            req.add_header("Authorization", f"Bearer {token}")
        with urllib.request.urlopen(req, timeout=30) as resp:
            return resp.read()

    # Byte-compare every file the store serves; the manifest additionally gets a field-level
    # diff below, because "which field changed" is the useful answer for that one.
    raw = {}
    try:
        for name in STORE_FILES:
            raw[name] = fetch(name)
    except Exception as exc:  # noqa: BLE001 - any failure here is worth reporting verbatim
        # A network failure must not turn into a false "they match". Report and fail
        # soft: the check is a drift alarm, not a correctness gate on the code.
        print(f"::warning::could not fetch the store copy ({exc}); drift NOT checked")
        return 0

    file_drift = []
    for name in STORE_FILES:
        try:
            with open(name, "rb") as fh:
                mine = fh.read()
        except FileNotFoundError:
            file_drift.append((name, "missing from this repo"))
            continue
        if mine != raw[name]:
            a = mine.decode("utf-8", "replace").splitlines()
            b = raw[name].decode("utf-8", "replace").splitlines()
            # Skip the ---/+++ headers, or a one-line change reports as three.
            n = sum(
                1
                for line in difflib.unified_diff(b, a, lineterm="")
                if line[:1] in "+-" and not line.startswith(("---", "+++"))
            )
            file_drift.append((name, f"{n} differing lines"))

    store = yaml.safe_load(raw[LOCAL])

    keys = sorted(set(local) | set(store))
    drift = [k for k in keys if local.get(k) != store.get(k)]

    if not drift and not file_drift:
        print(f"store copy matches this repo: all {len(keys)} manifest fields "
              f"and all {len(STORE_FILES)} files byte-identical")
        return 0

    if file_drift:
        print("::error::the published store copy has drifted from this repo")
        for name, how in file_drift:
            print(f"  {name}: {how}")

    if drift:
        print("\n  umbrel-app.yml field differences:")
    for k in drift:
        print(f"\n  {k}:")
        print(f"    this repo : {local.get(k, '<missing>')!r}")
        print(f"    the store : {store.get(k, '<missing>')!r}")
    print(
        "\nThe store copy is what users actually install. Sync the drifted files to\n"
        "BitcoincashII/umbrel-app-store under bch2-apps-forge-solo/.\n"
        "\nCHECK WHICH SIDE IS RIGHT FIRST. The drift has run BOTH ways: the store once\n"
        "held a dependency-free secret generator this repo lacked, and copying this repo\n"
        "over it blindly would have reintroduced a crash on every fresh install."
    )
    return 1


if __name__ == "__main__":
    sys.exit(main())

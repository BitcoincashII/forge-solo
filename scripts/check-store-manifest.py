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
STORE_API = (
    "https://api.github.com/repos/BitcoincashII/umbrel-app-store/contents/"
    "bch2-apps-forge-solo/umbrel-app.yml?ref=main"
)
LOCAL = "umbrel-app.yml"


def main() -> int:
    with open(LOCAL) as fh:
        local = yaml.safe_load(fh)

    req = urllib.request.Request(
        STORE_API,
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

    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            store = yaml.safe_load(resp.read())
    except Exception as exc:  # noqa: BLE001 - any failure here is worth reporting verbatim
        # A network failure must not turn into a false "they match". Report and fail
        # soft: the check is a drift alarm, not a correctness gate on the code.
        print(f"::warning::could not fetch the store manifest ({exc}); drift NOT checked")
        return 0

    keys = sorted(set(local) | set(store))
    drift = [k for k in keys if local.get(k) != store.get(k)]

    if not drift:
        print(f"store manifest matches this repo on all {len(keys)} fields")
        return 0

    print("::error::umbrel-app.yml has drifted from the published store listing")
    for k in drift:
        print(f"\n  {k}:")
        print(f"    this repo : {local.get(k, '<missing>')!r}")
        print(f"    the store : {store.get(k, '<missing>')!r}")
    print(
        "\nThe store copy is what users actually install. Copy umbrel-app.yml to\n"
        "BitcoincashII/umbrel-app-store at bch2-apps-forge-solo/umbrel-app.yml,\n"
        "or fix this repo if the store is the correct one."
    )
    return 1


if __name__ == "__main__":
    sys.exit(main())

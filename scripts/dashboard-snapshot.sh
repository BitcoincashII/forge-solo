#!/usr/bin/env bash
# What the dashboard shows of an install's data, as one sorted JSON file: the payout settings, the
# TIDES Gateway ID, each miner's settings, blocks, payouts and 1175 blocks, and the health answer
# with the state of the move from PostgreSQL. What changes from one minute to the next (hashrates,
# uptimes, the node's height, live TIDES figures) and what differs by platform (where the password
# is kept, the platform's name) is left out, so the same data gives the same file on Umbrel, on
# Windows (scripts/windows/dashboard-snapshot.ps1 writes it byte for byte the same) and on Linux,
# before an update and after it.
#
#   scripts/dashboard-snapshot.sh http://127.0.0.1:3080 after.json             # Windows' dashboard
#   scripts/dashboard-snapshot.sh http://<api>:8080 before.json --save DIR     # keep the answers too
#   scripts/dashboard-snapshot.sh --from DIR out.json                          # from kept answers
#
# The miners are those of testdata/migrate/seed-1012.sql unless MINERS names others, as
# "label=address" pairs separated by spaces.
set -euo pipefail
exec python3 - "$@" <<'PY'
import json, os, re, sys, urllib.error, urllib.request

MINERS = os.environ.get("MINERS") or (
    "A=bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2 "
    "B=bitcoincashii:qzet9v4jk2et9v4jk2et9v4jk2et9v4jkg0xty3z5s "
    "C=bitcoincashii:qrpu8s7rc0pu8s7rc0pu8s7rc0pu8s7rcv2v0f4la4")

# Each answer: its name in the file, what kind of answer it is, and its API path.
ENDPOINTS = [("health", "health", "/api/v1/health"), ("pool-config", "pool-config", "/api/v1/pool/config"),
             ("stats", "stats", "/api/v1/stats"), ("mining-status", "mining-status", "/api/v1/mining-status")]
for pair in MINERS.split():
    label, addr = pair.split("=", 1)
    for kind, path in [("miner", ""), ("settings", "/settings"), ("solo-blocks", "/solo-blocks"),
                       ("solo-payouts", "/solo-payouts"), ("payouts", "/payouts"), ("blocks", "/blocks")]:
        ENDPOINTS.append((label + "-" + kind, kind, "/api/v1/miners/" + addr + path))

# What is left out, by kind of answer: flattened keys, one pattern per reason.
VOLATILE = {
    "health": [r"settings_loaded"],                          # how many miners the api has read yet
    "pool-config": [r"platform|password_length|secrets_path",  # differ by platform and install
                    r"merge_mining_available"],                # Linux runs no 1175 node
    "stats": [r"hashrate|hashrateRaw|workers|miners|luck",     # live mining
              r"currentHeight|networkDifficulty|networkHashrate|bestBlockHash",  # the node
              r"uptime", r"rentals\..*"],
    "mining-status": [r"(?!payout_mode$|tides\.gateway$).*"],  # live state: all but the mode and the Gateway ID
    "miner": [r"hashrate5m|hashrate60m|workers|onlineWorkers|validShares|roundShares|invalidShares",
              r"bestDiff|athDiff|totalWork|lastShare",         # live mining
              r"currentHeight|balance|matureBalance|immatureBalance|balanceKnown"],  # the node's height
    "solo-blocks": [r"blocks\.\d+\.(confirmations|matures_in|mature)"],  # the node's height
}

# A time with a fraction of a second is written to the second: forgesolo.db keeps whole seconds,
# PostgreSQL kept the fraction.
FRACTION = re.compile(r"^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)\.\d+(Z|[+-]\d\d:\d\d)$")

EMPTY_OBJECT, EMPTY_LIST = object(), object()

def flatten(prefix, v, out):
    if isinstance(v, dict):
        if not v:
            out[prefix] = EMPTY_OBJECT
        for k, x in v.items():
            flatten(prefix + "." + k if prefix else k, x, out)
    elif isinstance(v, list):
        if not v:
            out[prefix] = EMPTY_LIST
        for i, x in enumerate(v):
            flatten(prefix + "." + str(i), x, out)
    else:
        out[prefix] = v

# A value as the file writes it: numbers as integers when they are whole, else to 8 decimals without
# trailing zeros; text as JSON, ASCII only, times to the second.
def value(v):
    if v is None:
        return "null"
    if v is EMPTY_OBJECT:
        return "{}"
    if v is EMPTY_LIST:
        return "[]"
    if isinstance(v, bool):
        return "true" if v else "false"
    if isinstance(v, (int, float)):
        if float(v).is_integer() and abs(v) < 1e15:
            return str(int(v))
        return ("%.8f" % v).rstrip("0").rstrip(".")
    return json.dumps(FRACTION.sub(r"\1\2", v), ensure_ascii=True)

def main(args):
    src = base = save = None
    if len(args) == 3 and args[0] == "--from":
        src, out = args[1], args[2]
    elif len(args) == 2 or (len(args) == 4 and args[2] == "--save"):
        base, out = args[0].rstrip("/"), args[1]
        save = args[3] if len(args) == 4 else None
    else:
        sys.exit("usage: dashboard-snapshot.sh BASE_URL OUT [--save DIR] | --from DIR OUT")
    if save:
        os.makedirs(save, exist_ok=True)
    flat = {}
    for name, kind, path in ENDPOINTS:
        # A kept answer is the status line "HTTP <code>" and the body.
        if src:
            with open(os.path.join(src, name + ".json"), "rb") as f:
                head, _, body = f.read().partition(b"\n")
            status = int(head.split()[1])
        else:
            try:
                with urllib.request.urlopen(base + path, timeout=30) as r:
                    status, body = r.status, r.read()
            except urllib.error.HTTPError as e:
                status, body = e.code, e.read()
            if save:
                with open(os.path.join(save, name + ".json"), "wb") as f:
                    f.write(b"HTTP %d\n" % status + body)
        flat[name + ".http"] = status
        try:
            doc = json.loads(body.decode("utf-8"))
        except ValueError:
            flat[name + ".body"] = "not JSON"
            continue
        one = {}
        flatten("", doc, one)
        for k, v in one.items():
            if not any(re.fullmatch(p, k) for p in VOLATILE.get(kind, [])):
                flat[name + "." + k] = v
    lines = ["%s: %s" % (json.dumps(k, ensure_ascii=True), value(flat[k])) for k in sorted(flat)]
    with open(out, "w", encoding="ascii", newline="\n") as f:
        f.write("{\n" + ",\n".join(lines) + "\n}\n")

main(sys.argv[1:])
PY

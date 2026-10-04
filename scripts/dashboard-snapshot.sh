#!/usr/bin/env bash
# What the dashboard shows of an install's data, as one sorted JSON file: the payout settings, the
# TIDES Gateway ID, each miner's settings, blocks, payouts and 1175 blocks, and the health answer
# with the state of the move from PostgreSQL. What changes from one minute to the next (hashrates,
# uptimes, the node's height, live TIDES figures) and what differs by platform (where the password
# is kept, the platform's name, the server's time zone) is left out, so the same data gives the same
# file on Umbrel, on Windows (scripts/windows/dashboard-snapshot.ps1 writes it byte for byte the
# same) and on Linux, before an update and after it.
#
#   scripts/dashboard-snapshot.sh http://127.0.0.1:3080 after.json             # Windows' dashboard
#   scripts/dashboard-snapshot.sh http://<api>:8080 before.json --save DIR     # keep the answers too
#   scripts/dashboard-snapshot.sh --from DIR out.json                          # from kept answers
#   scripts/dashboard-snapshot.sh --compare before.json after.json
#
# --compare lists the lines that differ: 0 when none does, 1 when one does, 2 when a file cannot be
# read. When one of the two was taken on 1.0.12 (only 1.0.13 answers password_required), what 1.0.13
# shows differently of the same data by design is left out.
#
# The miners are those of testdata/migrate/seed-1012.sql unless MINERS names others, as
# "label=address" pairs separated by spaces; a label is letters, digits and _.
set -euo pipefail
exec python3 - "$@" <<'PY'
import datetime, json, os, re, sys, urllib.error, urllib.request

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

# What 1.0.13's API shows differently from 1.0.12's of the same data, one pattern a change, on the
# lines of a snapshot.
KNOWN_1012 = [
    r'^"[A-Za-z0-9_]+-payouts\.',                              # a miner's payouts: 1.0.12's failed on PostgreSQL and listed none
    r'^"[A-Za-z0-9_]+-solo-(blocks|payouts)\.total',           # the totals: of every block in 1.0.13, of the latest 100 in 1.0.12
    r'^"[A-Za-z0-9_]+-solo-(blocks|payouts)\.(blocks|payouts)": (null|\[\])$',  # none: an empty list in 1.0.13, null in 1.0.12
    r'^"pool-config\.password_required":',                     # new in 1.0.13: a save needs the app's password
]

# A time is written in UTC and to the second, as forgesolo.db keeps it: PostgreSQL kept the
# fraction, and wrote the server's time zone (on Windows the PC's). One without a zone keeps none.
# Half a second or more counts as the next second, as the move rounds it (a time in the last second
# of the year 9999, which has no next, keeps its own).
TIME = re.compile(r"([0-9]{4})-([0-9]{2})-([0-9]{2})T([0-9]{2}):([0-9]{2}):([0-9]{2})(?:\.([0-9]+))?"
                  r"([Zz]|([+-])([0-9]{2})(?::?([0-9]{2}))?)?")

def text(s):
    m = TIME.fullmatch(s)
    if not m:
        return s
    try:
        t = datetime.datetime(*(int(x) for x in m.group(1, 2, 3, 4, 5, 6)))
        if m.group(9):
            off = int(m.group(10)) * 60 + int(m.group(11) or 0)
            t -= datetime.timedelta(minutes=off if m.group(9) == "+" else -off)
    except (ValueError, OverflowError):
        return s
    if (m.group(7) or "0")[0] >= "5":
        try:
            t += datetime.timedelta(seconds=1)
        except OverflowError:
            pass
    return "%04d-%02d-%02dT%02d:%02d:%02d%s" % (t.year, t.month, t.day, t.hour, t.minute, t.second,
                                              "Z" if m.group(8) else "")

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
# trailing zeros; text as JSON, ASCII only, times in UTC to the second.
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
    return json.dumps(text(v), ensure_ascii=True)

def snapshot(base, out, save, src):
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

LINE = re.compile(r'("(?:[^"\\]|\\.)*"): (.*?),?')

# A snapshot's lines by key, each as '"key": value'.
def lines_of(path):
    with open(path, encoding="ascii", errors="replace") as f:
        got = {}
        for l in f.read().split("\n"):
            m = LINE.fullmatch(l)
            if m:
                got[m.group(1)] = m.group(1) + ": " + m.group(2)
    if not got:
        raise ValueError("%s is not a snapshot" % path)
    return got

def compare(before, after):
    pw = '"pool-config.password_required"'
    known = KNOWN_1012 if (pw in before) != (pw in after) else []
    left = differ = 0
    out = []
    for k in sorted(set(before) | set(after)):
        b, a = before.get(k), after.get(k)
        if b == a:
            continue
        if any(re.search(p, l) for p in known for l in (b, a) if l is not None):
            left += 1
            continue
        differ += 1
        out += ["- " + b] if b is not None else []
        out += ["+ " + a] if a is not None else []
    if known:
        print("one of the two was taken on 1.0.12: %d lines that 1.0.13 shows differently by design are left out" % left)
    print("\n".join(out + ["%d lines differ" % differ if differ else "the same"]))
    return 1 if differ else 0

def main(args):
    if len(args) == 3 and args[0] == "--compare":
        try:
            before, after = lines_of(args[1]), lines_of(args[2])
        except (OSError, ValueError) as e:
            print("cannot read a snapshot: %s" % e, file=sys.stderr)
            sys.exit(2)
        sys.exit(compare(before, after))
    if len(args) == 3 and args[0] == "--from":
        return snapshot(None, args[2], None, args[1])
    if (len(args) == 2 or (len(args) == 4 and args[2] == "--save")) and args[0].startswith(("http://", "https://")):
        return snapshot(args[0].rstrip("/"), args[1], args[3] if len(args) == 4 else None, None)
    print("usage: dashboard-snapshot.sh BASE_URL OUT [--save DIR] | --from DIR OUT | --compare BEFORE AFTER", file=sys.stderr)
    sys.exit(2)

main(sys.argv[1:])
PY

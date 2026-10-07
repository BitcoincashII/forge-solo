#!/usr/bin/env bash
# The move from PostgreSQL to SQLite, end to end: real clusters, the images this tree builds, and
# the compose as umbreld runs it. An install's data as Forge Solo 1.0.12 leaves it on Umbrel (made by
# 1.0.12's own code on its TimescaleDB, then testdata/migrate/seed-1012.sql, then hard-killed with its
# last change only in the WAL) is moved by the migrate service, and every row, 54 figures, the
# dashboard and the old cluster are compared with what they were. Then everything around it:
#
#    1. the move: 54 figures, every row (PostgreSQL's to_json against SQLite's json_object), the WAL
#       change, sqlite_sequence; the api and the stratum start on it
#    2. the old cluster: pg_control says shut down, 1.0.12 opens it with the same figures and
#       fingerprint (the files PostgreSQL's own start and stop changed are listed, not failed)
#    3. a second start finds nothing to do and leaves forgesolo.db as it was
#    4. SIGTERM as the old data's server starts, and SIGKILL half way through the copy, leave no
#       forgesolo.db and no "done", and the next start moves
#    5. PG_VERSION 15, and pg_control garbage: failed, migrate exits 0, no forgesolo.db, the api in
#       maintenance, the stratum not listening; "start without the old data" with the password, then
#       an empty database; bringing the data back in, then a merge
#    6. the stale postmaster.pid (PID 1) a hard-killed 1.0.12 container leaves
#    7. a cluster whose TimeZone is Pacific/Chatham moves to the same rows
#    8. the databases of 1.0.0 and 1.0.9 (testdata/migrate) move with no figure changed
#    9. back to 1.0.12: it opens its data with the figures it had
#   10. forward again, twice: a merge, both periods kept, the newer payout address, one
#       before-merge copy, the workers' best shares 1.0.13 kept
#   11. per height: a 1175 block distributed and paid in 1.0.13 but not in 1.0.12, after which the
#       stratum's sweep never refuses to redistribute; a block that replaced another at its height
#   12. an update while 1.0.12's database runs: it shuts down before the move starts
#   13. the marker removed while the app runs: deferred, the api and the stratum keep running, and a
#       settings save through the api and a block are kept by the merge at the next start
#   14. pg_control deleted, and PG_VERSION overwritten, after a move: degraded, the app runs
#   15. the Windows shape: plain PostgreSQL 16.15 in America/Chicago, prepare and commit with the
#       server's address in FORGE_MIGRATE_PG; the rows equal testdata/migrate/seed-1012.db.json,
#       which the Windows CI job compares its own move with; and the lib/pq tests on a real server
#   16. after a fresh start, a move, a merge and a deferral, db/ is 10001:10001 0700 and every file
#       in it 10001:10001 0600
#   snapshot: the dashboard (scripts/dashboard-snapshot.sh) after the move reads byte for byte as
#       the kept testdata/migrate/dashboard-snapshot.json, and the same as from 1.0.12 (built from
#       the v1.0.12 tag) before the move, its cluster in America/Chicago, as a Windows PC's is, and
#       in Pacific/Chatham, apart from what 1.0.13 shows differently by design. Kept answers
#       (testdata/migrate/dashboard*) read as their committed snapshots, which the Windows CI job
#       checks dashboard-snapshot.ps1 against.
#
#   ./scripts/it-pg-to-sqlite.sh                    # all of it; 1 when a check fails or cannot run
#   IT_CASES="5 snapshot" ./scripts/...             # the move, the old cluster and these: 3, never 0
#   KEEP=1                    leave the containers and $IT_WORK for a look
#   IT_FAIL_FAST=1            stop at the first check that fails
#   IT_REUSE_IMAGES=1         use the api and stratum images of an earlier run when they are there
#   IT_TAG, IT_PROJECT, IT_PORT_BASE, IT_WORK   the images' tag, the compose projects' prefix, the
#                             first of the local ports, the work folder: two runs side by side
#   CAPTURE_FIXTURES=1        write the pg_control files it captures into internal/pgmigrate/testdata
#   UPDATE_GOLDEN=1           write testdata/migrate/seed-1012.db.json from case 15, the answers after
#                             the move into testdata/migrate/dashboard, 1.0.12's into dashboard-1012,
#                             and the kept answers' snapshots (dashboard-zones is edited by hand)
#
# Needs docker with compose, Go, python3, curl, and the v1.0.12 tag (a checkout with fetch-depth 0).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib-it.sh
ROOT=$PWD

need_tools docker go python3 curl git
docker info >/dev/null 2>&1 || { echo "✗ docker does not answer"; exit 1; }
docker compose version >/dev/null 2>&1 || { echo "✗ docker compose is needed and missing"; exit 1; }
git rev-parse -q --verify 'v1.0.12^{commit}' >/dev/null || { echo "✗ the v1.0.12 tag is missing (fetch-depth: 0)"; exit 1; }
# The images pinned by digest run as this machine's platform: Docker keeps one platform per digest,
# and another one pulled before would run emulated, or not at all.
PLATFORM=$(docker version --format '{{.Server.Os}}/{{.Server.Arch}}')

P=${IT_PROJECT:-forgeitpg}
PORT_BASE=${IT_PORT_BASE:-15700}
VERSION=$(sed -n 's/^version: *"\{0,1\}\([^"]*\)"\{0,1\}$/\1/p' umbrel-app.yml)
PG16=$(sed -n 's/^FROM \(postgres:16[^ ]*\) AS pg$/\1/p' docker/migrate/Dockerfile)
ALPINE=$(sed -n 's/^FROM \(alpine:[^ ]*\)$/\1/p' docker/migrate/Dockerfile)
if [ -z "$VERSION" ] || [ -z "$PG16" ] || [ -z "$ALPINE" ]; then echo "✗ cannot read the version or the image pins"; exit 1; fi
# platform_ref IMAGE: IMAGE by this platform's own manifest, when its pin names an index of several.
# Docker keeps one platform's image per digest: one another platform's pull left there first would
# not be replaced ("cannot overwrite digest").
platform_ref() {
  local d
  d=$(docker buildx imagetools inspect --raw "$1" | python3 -c '
import json, sys
os_, arch = sys.argv[1].split("/")
m = json.load(sys.stdin)
print(next((x["digest"] for x in m.get("manifests", []) if x.get("platform", {}).get("os") == os_ and x["platform"].get("architecture") == arch), ""))' "$PLATFORM") \
    || { echo "✗ cannot read the manifest of $1" >&2; exit 1; }
  if [ -n "$d" ]; then echo "${1%@*}@$d"; else echo "$1"; fi
}
PG16=$(platform_ref "$PG16")
ALPINE=$(platform_ref "$ALPINE")
TAG=${IT_TAG:-$VERSION} # the tag of the images built here
MIGRATE_IMG=forge-it-migrate:$TAG API_IMG=forge-it-api:$TAG STRATUM_IMG=forge-it-stratum:$TAG
API_1012_IMG=forge-it-api:v1.0.12 STRATUM_1012_IMG=forge-it-stratum:v1.0.12 # built from the v1.0.12 tag
PW=it-settings-password-0123456789   # the app's settings password (APP_PASSWORD)
DBPW=it-db-password                   # 1.0.12's database password (APP_DB_PASSWORD)
MINER_A=bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2
MINER_D=bitcoincashii:qr2df4x56n2df4x56n2df4x56n2df4x56sj8dc24hj
ESF=esf1quhj7te09uhj7te09uhj7te09uhj7te09dnlk6x
HASH_B=$(printf 'b%.0s' $(seq 64))    # the block that replaces 100149 in 1.0.13

WORK=${IT_WORK:-$(mktemp -d /tmp/forge-it-pgsqlite.XXXXXX)}
mkdir -p "$WORK"
chmod 755 "$WORK"
OUT=$WORK/out BIN=$WORK/bin
mkdir -p "$OUT/control" "$BIN"
chmod 777 "$OUT"

CASES=${IT_CASES:-all}
want() { [ "$CASES" = all ] || [[ " $CASES " == *" $1 "* ]]; }

PASSED=0 FAILED=0
ok() { PASSED=$((PASSED + 1)); echo "  ✓ $*"; }
bad() {
  FAILED=$((FAILED + 1))
  echo "  ✗ $*"
  if [ "${IT_FAIL_FAST:-0}" = 1 ]; then echo "── IT_FAIL_FAST: stopped at the first failure"; exit 1; fi
}
# check WHAT COMMAND...: one check, passed when the command succeeds.
check() {
  local what=$1
  shift
  if "$@" >"$OUT/check.log" 2>&1; then ok "$what"; else bad "$what"; head -30 "$OUT/check.log" | sed 's/^/      /'; fi
}
die() { echo "✗ $*"; exit 1; }
# has PATTERN: whether what comes in matches, read to its end (grep -q stops reading, and with
# pipefail the writer's broken pipe would fail the check).
has() { grep -- "$1" >/dev/null; }

INSTANCES=()
cleanup() {
  local rc=$?
  [ "${KEEP:-0}" = 1 ] && { echo "── KEEP=1: the containers and $WORK are left"; exit $rc; }
  for n in "${INSTANCES[@]}"; do
    for f in new old app1012; do
      [ -f "$WORK/$n/$f.json" ] && docker compose -p "$P-$n" -f "$WORK/$n/$f.json" down -t 5 --remove-orphans >/dev/null 2>&1
    done
  done
  docker ps -aq --filter "name=^$P-" | xargs -r docker rm -f >/dev/null 2>&1
  docker network ls -q --filter "name=^$P-" | xargs -r docker network rm >/dev/null 2>&1
  docker run --platform "$PLATFORM" --rm -v "$(dirname "$WORK"):/w" "$ALPINE" rm -rf "/w/$(basename "$WORK")" >/dev/null 2>&1
  exit $rc
}
trap cleanup EXIT

# asroot runs a shell command as root, with $WORK where it is here: the clusters belong to uid 70
# and the databases to uid 10001.
asroot() { docker run --platform "$PLATFORM" --rm --network none -v "$WORK:$WORK" "$ALPINE" sh -c "$1"; }

# ── instances: one APP_DATA_DIR each, and the compose files that run it ──────────────────────────
# inst NAME N: $WORK/NAME/app is its APP_DATA_DIR; its old database listens on 127.0.0.1:PORT_BASE+2N
# and its api on the next port. The compose files: new (this tree's postgres, migrate, api and
# stratum services, with the images built here), old (1.0.12's postgres service), and app1012
# (1.0.12's postgres, api and stratum services, the last two built from the v1.0.12 tag). Neither
# node runs; TIDES mode reaches no pool.
inst() {
  local n=$1 i=$2 d=$WORK/$1
  mkdir -p "$d/app"
  INSTANCES+=("$n")
  cat > "$d/env" <<EOF
APP_DATA_DIR=$d/app
APP_PASSWORD=$PW
APP_DB_PASSWORD=$DBPW
APP_NODE_RPC_PASSWORD=it-node
APP_1175_RPC_PASSWORD=it-node1175
APP_INTERNAL_API_TOKEN=it-internal-token
IT_PG_PORT=$((PORT_BASE + 2 * i))
IT_API_PORT=$((PORT_BASE + 2 * i + 1))
EOF
  git show v1.0.12:init-db.sql > "$d/app/init-db.sql"
  git show v1.0.12:docker-compose.yml | sed '/^version:/d' > "$d/old.yml" # a key compose ignores
  (
    set -a
    # shellcheck source=/dev/null
    . "$d/env"
    set +a
    docker compose -f docker-compose.yml config --no-consistency --format json > "$d/new.src.json"
    docker compose -f "$d/old.yml" config --no-consistency --format json > "$d/old.src.json"
    IT_MIGRATE_IMAGE=$MIGRATE_IMG IT_API_IMAGE=$API_IMG IT_STRATUM_IMAGE=$STRATUM_IMG \
      IT_API_1012_IMAGE=$API_1012_IMG IT_STRATUM_1012_IMAGE=$STRATUM_1012_IMG python3 - "$d" <<'PY'
import json, os, sys
d = sys.argv[1]
E = os.environ
load = lambda f: json.load(open("%s/%s.src.json" % (d, f)))["services"]
alias = lambda s: {"aliases": ["bch2-apps-forge-solo_%s_1" % s]}

def app(name):
    s = load("new")[name]
    s.pop("ports", None)
    s["image"] = E["IT_%s_IMAGE" % name.upper()]
    s["networks"] = {"forge": alias(name)}
    s["environment"]["DATUM_POOL_URL"] = "http://127.0.0.1:9"
    s["depends_on"] = {k: v for k, v in s.get("depends_on", {}).items() if k in ("migrate", "api")}
    if name == "api":
        s["ports"] = [{"target": 8080, "published": E["IT_API_PORT"], "host_ip": "127.0.0.1", "protocol": "tcp"}]
    return s

def app1012(name):
    s = load("old")[name]
    s.pop("ports", None)
    s["image"] = E["IT_%s_1012_IMAGE" % name.upper()]
    s["networks"] = {"forge": alias(name)}
    s["environment"]["DATUM_POOL_URL"] = "http://127.0.0.1:9"
    s["depends_on"] = {k: v for k, v in s.get("depends_on", {}).items() if k in ("postgres", "api")}
    if name == "api":
        s["ports"] = [{"target": 8080, "published": E["IT_API_PORT"], "host_ip": "127.0.0.1", "protocol": "tcp"}]
    return s

new = load("new")
mine = {s: new[s] for s in ("postgres", "migrate") if s in new}
for s in mine.values():
    s["image"] = E["IT_MIGRATE_IMAGE"]
pg = load("old")["postgres"]
pg.pop("container_name", None)
pg["ports"] = [{"target": 5432, "published": E["IT_PG_PORT"], "host_ip": "127.0.0.1", "protocol": "tcp"}]
pg["networks"] = {"default": {}, "forge": alias("postgres")}
files = {
    "new": dict(mine, api=app("api"), stratum=app("stratum")),
    "old": {"postgres": pg},
    "app1012": {"postgres": pg, "api": app1012("api"), "stratum": app1012("stratum")},
}
for f, svcs in files.items():
    json.dump({"services": svcs, "networks": {"default": {}, "forge": {}}}, open("%s/%s.json" % (d, f), "w"), indent=1)
PY
  )
}
inst_port() { sed -n "s/^IT_$2_PORT=//p" "$WORK/$1/env"; }
dc() { local n=$1 f=$2; shift 2; docker compose -p "$P-$n" -f "$WORK/$n/$f.json" "$@"; }
cid() { docker ps -aq --filter "label=com.docker.compose.project=$P-$1" --filter "label=com.docker.compose.service=$2" | head -1; }
dsn() { echo "postgres://forge:$DBPW@127.0.0.1:$(inst_port "$1" PG)/forgesolo?sslmode=disable"; }
api() { echo "http://127.0.0.1:$(inst_port "$1" API)"; }
psqlc() { local n=$1; shift; docker exec -i "$(cid "$n" postgres)" psql -U forge -d forgesolo -v ON_ERROR_STOP=1 -qtA "$@"; }
db() { echo "$WORK/$1/app/db"; }
there() { asroot "test -e '$1'"; }
absent() { ! there "$1"; }
state_is() { asroot "cat $(db "$1")/migration-status.json 2>/dev/null" | has "\"state\": \"$2\""; }
state_not() { ! state_is "$1" "$2"; }
exit_of() { docker inspect -f '{{.State.ExitCode}}' "$(cid "$1" "$2")"; }
migrate_log() { docker logs "$(cid "$1" migrate)" 2>&1; }
migrate_said() { migrate_log "$1" | has "$2" || { migrate_log "$1" | tail -5; return 1; }; }
# migrate_ran INSTANCE STATE [WORDS]: migrate exited 0, the status says STATE, its log says WORDS.
migrate_ran() {
  local code
  code=$(exit_of "$1" migrate)
  [ "$code" = 0 ] || { echo "migrate exited $code"; migrate_log "$1" | tail -5; return 1; }
  state_is "$1" "$2" || { asroot "cat $(db "$1")/migration-status.json"; return 1; }
  [ -z "${3:-}" ] || migrate_said "$1" "$3"
}
logs_say() { docker logs "$(cid "$1" "$2")" 2>&1 | has "$3"; }
health_says() { curl -fsS "$(api "$1")/api/v1/health" | has "$2" || { curl -sS "$(api "$1")/api/v1/health"; return 1; }; }
equal() { [ "$1" = "$2" ] || { echo "got [$1], want [$2]"; return 1; }; }

# A copy of the cluster as the hard kill left it, for the cases that start from there.
inst_from_crashed() {
  inst "$1" "$2"
  asroot "cp -a $WORK/crashed $WORK/$1/app/postgres"
}

wait_pg() { # instance [init]
  local c
  c=$(cid "$1" postgres)
  if [ "${2:-}" = init ]; then
    wait_for "1.0.12's database made" 300 container_log_has "$c" "PostgreSQL init process complete" || return 1
  fi
  wait_for "1.0.12's database" 300 docker exec "$c" psql -U forge -d forgesolo -c "SELECT 1" || return 1
}

wait_http() { # url seconds
  local i
  for i in $(seq 1 "$2"); do
    curl -fsS -o /dev/null --max-time 5 "$1" 2>/dev/null && return 0
    sleep 1
  done
  echo "no answer from $1 within $2 s"
  return 1
}

# The app is up when the api answers and reaches the stratum's figures.
wait_app() { wait_http "$(api "$1")/api/v1/health" 120 && wait_http "$(api "$1")/api/v1/miners/$MINER_A/solo-blocks" 120; }

# up INSTANCE FILE [SERVICE]: compose up -d, its output kept. It waits for the move before it starts
# the api and the stratum, so it is bounded as a move is.
up() {
  local n=$1 f=$2
  shift 2
  timeout "${IT_MOVE_LIMIT:-600}" docker compose -p "$P-$n" -f "$WORK/$n/$f.json" up -d "$@" >"$OUT/$n.up.log" 2>&1 \
    || { cat "$OUT/$n.up.log"; return 1; }
}
# move INSTANCE: compose runs the move alone (the postgres service, then migrate), to its end. A move
# that has not ended within MOVE_LIMIT seconds hangs the app's start: a failure.
MOVE_LIMIT=${IT_MOVE_LIMIT:-600}
move() {
  up "$1" new migrate || return 1
  timeout "$MOVE_LIMIT" docker wait "$(cid "$1" migrate)" >/dev/null && return 0
  echo "the move did not end within $MOVE_LIMIT s: $(migrate_log "$1" | tail -3)"
  docker kill "$(cid "$1" migrate)" >/dev/null 2>&1
  return 1
}

# it.test, built from internal/pgmigrate: on this machine (ithost), or in the database's folder as
# the app's user, 10001 (itapp), one test at a time.
ithost() { local t=$1; shift; env "$@" "$BIN/it.test" -test.run "^$t\$" -test.count=1 -test.v > "$OUT/$t.log" 2>&1 || { cat "$OUT/$t.log"; return 1; }; }
itapp() {
  local n=$1 t=$2 e=() kv
  shift 2
  for kv in IT_DB=/data/forgesolo.db "$@"; do e+=(-e "$kv"); done
  docker run --platform "$PLATFORM" --rm --user 10001:10001 --network none -v "$(db "$n"):/data" -v "$BIN:/b:ro" -v "$OUT:/out" \
    "${e[@]}" "$ALPINE" /b/it.test -test.run "^$t\$" -test.count=1 -test.v > "$OUT/$n.$t.log" 2>&1 \
    || { cat "$OUT/$n.$t.log"; return 1; }
}
# itq INSTANCE SQL: what a query on forgesolo.db answers, one line per row.
itq() { itapp "$1" TestITQuery "IT_SQL=$2" && sed -n 's/^row: //p' "$OUT/$1.TestITQuery.log"; }
query_is() { equal "$(itq "$1" "$2")" "$3"; }

figures_equal() { # the before file, the after file
  if diff "$1" "$2" > "$OUT/figures.diff"; then echo "$(wc -l < "$1") figures equal"; else cat "$OUT/figures.diff"; return 1; fi
}
# figures_after INSTANCE [BEFORE]: forgesolo.db's figures are BEFORE's (1.0.12's, by default).
figures_after() { itapp "$1" TestITFigures "IT_OUT=/out/$1.figures.after" && figures_equal "${2:-$OUT/main.figures.before}" "$OUT/$1.figures.after"; }

# Every table numbered by AUTOINCREMENT has its sqlite_sequence row at or above its largest id.
sequences_ok() {
  local t
  for t in miners blocks payouts payouts_1175; do
    query_is "$1" "SELECT (SELECT seq FROM sqlite_sequence WHERE name = '$t') >= (SELECT max(id) FROM $t)" 1 || { echo "sqlite_sequence of $t"; return 1; }
  done
}

# heights INSTANCE H...: which of these heights forgesolo.db has a block at, in order.
heights() {
  local n=$1
  shift
  itq "$n" "SELECT group_concat(height, ' ') FROM (SELECT height FROM blocks WHERE height IN ($(echo "$@" | tr ' ' ',')) ORDER BY height)"
}

owned_by_app() { # instance, what just ran
  local d wrong
  d=$(db "$1")
  wrong=$(asroot "cd $d && stat -c '%u:%g %a %n' . && for f in * .[!.]*; do [ -e \"\$f\" ] && stat -c '%u:%g %a %n' \"\$f\"; done" \
    | awk '($3 == "." && $1 $2 != "10001:10001700") || ($3 != "." && $1 $2 != "10001:10001600")')
  [ -z "$wrong" ] || { echo "after $2: $wrong"; return 1; }
}

# The logical fingerprint of a 1.0.12 database: every table's rows, and the stored shares.
FP_SQL="SET TIME ZONE 'UTC';
SELECT 'fp.' || t || '=' || md5(r) FROM (
  SELECT 'pool_config' t, string_agg(x::text, '|' ORDER BY x::text) r FROM pool_config x UNION ALL
  SELECT 'datum_identity', string_agg(x::text, '|' ORDER BY x::text) FROM datum_identity x UNION ALL
  SELECT 'miners', string_agg(x::text, '|' ORDER BY x::text) FROM miners x UNION ALL
  SELECT 'blocks', string_agg(x::text, '|' ORDER BY x::text) FROM blocks x UNION ALL
  SELECT 'payouts', string_agg(x::text, '|' ORDER BY x::text) FROM payouts x UNION ALL
  SELECT 'blocks_1175', string_agg(x::text, '|' ORDER BY x::text) FROM blocks_1175 x UNION ALL
  SELECT 'payouts_1175', string_agg(x::text, '|' ORDER BY x::text) FROM payouts_1175 x) f
UNION ALL SELECT 'shares=' || count(*) FROM shares
UNION ALL SELECT 'chunks=' || count(*) FROM timescaledb_information.chunks WHERE hypertable_name = 'shares';"

manifest() { asroot "cd $WORK/$1/app/postgres && find . -type f -exec sha256sum {} + | sort -k2"; }

# The old cluster after a move: pg_control says shut down, and 1.0.12's database image opens a copy
# of it with the figures and the fingerprint it had. The original is not started: that would change
# pg_control, and the next start would merge.
gate() { # instance
  local n=$1 c=$P-gate st rc=0
  st=$(docker run --platform "$PLATFORM" --rm --network none --user 70:70 -v "$WORK/$n/app/postgres:/pg" --entrypoint pg_controldata "$TS" /pg | grep 'cluster state')
  echo "$st" | has 'shut down$' || { echo "pg_control: $st"; return 1; }
  asroot "rm -rf $WORK/gate && cp -a $WORK/$n/app/postgres $WORK/gate"
  docker run --platform "$PLATFORM" -d --name "$c" -p "127.0.0.1:$(inst_port "$n" PG):5432" -v "$WORK/gate:/var/lib/postgresql/data" "$TS" >/dev/null
  if wait_for "1.0.12 on a copy" 300 docker exec "$c" psql -U forge -d forgesolo -c "SELECT 1" >/dev/null; then
    ithost TestITFigures IT_PG="$(dsn "$n")" IT_OUT="$OUT/$n.figures.gate" || rc=1
    docker exec -i "$c" psql -U forge -d forgesolo -qtA <<< "$FP_SQL" > "$OUT/$n.fp.gate" || rc=1
  else
    docker logs "$c" 2>&1 | tail
    rc=1
  fi
  docker rm -f "$c" >/dev/null || true
  asroot "rm -rf $WORK/gate"
  [ $rc = 0 ] && figures_equal "$OUT/main.figures.before" "$OUT/$n.figures.gate" && diff "$OUT/main.fp.before" "$OUT/$n.fp.gate"
}

# ── build ────────────────────────────────────────────────────────────────────────────────────────
echo "── building the images and helpers of this tree ($VERSION)"
build_image() { # tag dockerfile [reusable] [context]
  if [ -n "${3:-}" ] && [ "${IT_REUSE_IMAGES:-0}" = 1 ] && docker image inspect "$1" >/dev/null 2>&1; then echo "   $1: kept"; return 0; fi
  docker build -q --build-arg "VERSION=$VERSION" -f "$2" -t "$1" "${4:-.}" > "$OUT/build.log" 2>&1 || { tail -30 "$OUT/build.log"; die "the build of $1 failed"; }
  echo "   $1"
}
build_image "$MIGRATE_IMG" docker/migrate/Dockerfile
build_image "$API_IMG" docker/api/Dockerfile reusable
build_image "$STRATUM_IMG" docker/stratum/Dockerfile reusable
CGO_ENABLED=0 go test -c -tags it -o "$BIN/it.test" ./internal/pgmigrate/ || die "it.test does not build"
CGO_ENABLED=0 go build -o "$BIN/forge-solo-migrate" ./cmd/forge-solo-migrate || die "forge-solo-migrate does not build"

# ── 1.0.12 with data ─────────────────────────────────────────────────────────────────────────────
echo "── 1.0.12's database, made and filled by 1.0.12's own code and the seed"
inst main 0
TS=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["services"]["postgres"]["image"])' "$WORK/main/old.json")
up main old postgres || die "1.0.12's database did not start"
wait_pg main init || die "1.0.12's database is not ready"
mkdir -p "$WORK/v1012"
git archive v1.0.12 | tar -x -C "$WORK/v1012"
frozen_is_1012s() { (cd internal/pgmigrate && IT_V1012="$WORK/v1012" go test -count=1 -tags it -run '^TestITFrozenSchemaIs1012s$' .); }
check "the frozen schema the move's spec follows is what 1.0.12's own InitDB ran" frozen_is_1012s
cp internal/stats/seed1012_test.go "$WORK/v1012/internal/stats/"
if ! (cd "$WORK/v1012" && IT_PG="$(dsn main)" go test -count=1 -tags seed1012 -run '^TestITSeed1012$' -v ./internal/stats/) > "$OUT/seed1012.log" 2>&1 \
  || ! grep -q -- '--- PASS: TestITSeed1012' "$OUT/seed1012.log"; then
  cat "$OUT/seed1012.log"
  die "1.0.12's code did not make its database"
fi
docker cp testdata/migrate/seed-1012.sql "$(cid main postgres):/tmp/seed.sql"
psqlc main -f /tmp/seed.sql >/dev/null || die "seed-1012.sql did not load"
# The last settings change before the update, which only the WAL will hold.
psqlc main -c "UPDATE pool_config SET coinbase_tag = '/forge ü wal/', updated_at = now() WHERE id = 1" >/dev/null
ithost TestITFigures IT_PG="$(dsn main)" IT_OUT="$OUT/main.figures.before" || die "the figures before"
ithost TestITPostgresRows IT_PG="$(dsn main)" IT_OUT="$OUT/main.rows.json" || die "the rows before"
psqlc main <<< "$FP_SQL" > "$OUT/main.fp.before"
grep -q '^chunks=1100$' "$OUT/main.fp.before" || die "the seed made $(grep chunks "$OUT/main.fp.before") share chunks, not 1100"
asroot "cp $WORK/main/app/postgres/global/pg_control $OUT/control/pg_control-inproduction && chmod 644 $OUT/control/pg_control-inproduction"
docker kill "$(cid main postgres)" >/dev/null
manifest main > "$OUT/main.manifest.before"
PIDLINE=$(asroot "head -1 $WORK/main/app/postgres/postmaster.pid" || true)
asroot "cp -a $WORK/main/app/postgres $WORK/crashed"
echo "   seeded: $(grep -E '^count\.(blocks|payouts|blocks_1175)=' "$OUT/main.figures.before" | tr '\n' ' ')"

# ── the dashboard before: 1.0.12 on two copies of the data, each cluster in another zone ───────
# The cluster 1.0.12 made on a Windows PC is in the PC's zone: PostgreSQL answers times in its own
# zone, which the snapshot must read as the same instants.
# snapshot_before NAME N ZONE OFFSET: 1.0.12's dashboard on a copy of the data whose cluster is in
# ZONE, as $OUT/dashboard.NAME.json, its answers in $OUT/answers.NAME; its payout times carry
# OFFSET. 1.0.12's api has a miner's solo blocks and payouts from the stratum, which serves them once
# it has waited for the node (none runs here) and started.
stratum_answers() { curl -fsS "$(api "$1")/api/v1/mining-status" | has '"payout_mode"'; }
snapshot_before() {
  local n=$1 i=$2 zone=$3 offset=$4
  inst_from_crashed "$n" "$i"
  asroot "echo \"timezone = '$zone'\" >> $WORK/$n/app/postgres/postgresql.conf"
  up "$n" app1012 || die "1.0.12 in $zone did not start"
  check "snapshot: 1.0.12 in $zone serves the dashboard" wait_app "$n"
  check "snapshot: the stratum of 1.0.12 in $zone answers the api" wait_for "the stratum of 1.0.12 in $zone" 120 stratum_answers "$n"
  scripts/dashboard-snapshot.sh "$(api "$n")" "$OUT/dashboard.$n.json" --save "$OUT/answers.$n" || die "the snapshot of 1.0.12 in $zone"
  check "snapshot: 1.0.12 in $zone answers times in $zone" grep -q -- "\"paidAt\":\"[^\"]*$offset\"" "$OUT/answers.$n/A-solo-payouts.json"
  dc "$n" app1012 down -t 30 >/dev/null 2>&1 || true
}
if want snapshot; then
  echo "── the dashboard before the move: 1.0.12, its cluster in America/Chicago and in Pacific/Chatham"
  build_image "$API_1012_IMG" "$WORK/v1012/docker/api/Dockerfile" reusable "$WORK/v1012"
  build_image "$STRATUM_1012_IMG" "$WORK/v1012/docker/stratum/Dockerfile" reusable "$WORK/v1012"
  snapshot_before app1012 11 America/Chicago -05:00
  snapshot_before app1012chatham 1 Pacific/Chatham +12:45
  check "snapshot: 1.0.12 reads the same instants whatever its cluster's zone" diff "$OUT/dashboard.app1012.json" "$OUT/dashboard.app1012chatham.json"
fi

# ── 1. the move, and 6: the stale postmaster.pid ────────────────────────────────────────────────
echo "── 1 and 6: the move, from a cluster hard-killed with postmaster.pid saying ${PIDLINE:-nothing}"
check "6: the hard-killed cluster left postmaster.pid with PID 1" equal "$PIDLINE" 1
move main || die "the move did not run"
check "1: migrate exits 0, the status says done" migrate_ran main "done" "done: move from postgres; blocks 152, payouts 155, blocks_1175 4, payouts_1175 4, miners 2"
check "1: the postgres service ran noop and exited 0" equal "$(exit_of main postgres)" 0
check "1: postgres-migrated.json is there" there "$(db main)/postgres-migrated.json"
check "1: the 54 figures are the same" figures_after main
check "1: every row is the same (to_json against json_object)" itapp main TestITCompareRows IT_ROWS=/out/main.rows.json
check "1: the change only the WAL held is there" grep -q '^pool.tag="/forge ü wal/"$' "$OUT/main.figures.after"
check "1: sqlite_sequence is at least every table's largest id" sequences_ok main
check "16: after a move, db/ and its files belong to 10001:10001" owned_by_app main "a move"
itapp main TestITSnapshot IT_OUT=/out/main.db.json || bad "1: the moved rows could not be read"

# ── 3. the next start ────────────────────────────────────────────────────────────────────────────
if want 3; then
  echo "── 3: the next start finds nothing to do"
  h1=$(asroot "sha256sum $(db main)/forgesolo.db")
  move main || bad "3: migrate did not run"
  check "3: migrate exits 0, plan none, the status says none" migrate_ran main none "plan: none"
  check "3: forgesolo.db is byte for byte as it was" equal "$(asroot "sha256sum $(db main)/forgesolo.db")" "$h1"
fi

# ── 2. the old cluster ───────────────────────────────────────────────────────────────────────────
echo "── 2: the old cluster"
asroot "cp $WORK/main/app/postgres/global/pg_control $OUT/control/pg_control-shutdown && chmod 644 $OUT/control/pg_control-shutdown"
check "2: shut down, and 1.0.12 opens it with the same figures and fingerprint" gate main
manifest main > "$OUT/main.manifest.after"
echo "   files PostgreSQL's own start and stop changed:"
diff "$OUT/main.manifest.before" "$OUT/main.manifest.after" | sed -n 's/^> [0-9a-f]*  *\.\//      /p' | head -40 || true
control_fixtures() { (cd internal/pgmigrate && IT_CONTROL_DIR="$OUT/control" go test -count=1 -tags it -run '^TestITControl$' .); }
check "2: the pg_control files captured here read as the committed fixtures" control_fixtures
if [ "${CAPTURE_FIXTURES:-0}" = 1 ]; then
  cp "$OUT/control/pg_control-shutdown" "$OUT/control/pg_control-inproduction" internal/pgmigrate/testdata/
  echo "   wrote internal/pgmigrate/testdata/pg_control-shutdown and pg_control-inproduction"
fi

# ── the app on the moved data, and the dashboard after ─────────────────────────────────────────
echo "── the api and the stratum on forgesolo.db"
up main new || die "the app did not start"
check "1: the api and the stratum start on it" wait_app main
check "1: the api says it runs on SQLite" logs_say main api "Connected to SQLite database"
check "1: the stratum says it runs on SQLite" logs_say main stratum "Connected to SQLite database"
# compared BEFORE AFTER [1.0.12]: dashboard-snapshot.sh --compare finds them the same; with 1.0.12,
# it left out what 1.0.13 shows differently by design.
compared() {
  scripts/dashboard-snapshot.sh --compare "$1" "$2" > "$OUT/compare.log" || { cat "$OUT/compare.log"; return 1; }
  cat "$OUT/compare.log"
  [ -z "${3:-}" ] || has "one of the two was taken on 1.0.12" < "$OUT/compare.log"
}
if want snapshot; then
  check "snapshot: the stratum answers the api" wait_for "the stratum" 120 stratum_answers main
  scripts/dashboard-snapshot.sh "$(api main)" "$OUT/dashboard.after.json" --save "$OUT/answers.after" || bad "snapshot: after the move"
  check "snapshot: it shows the seed's blocks and 1175 blocks" grep -q '"A-solo-blocks.blocks.0.height"' "$OUT/dashboard.after.json"
  check "snapshot: the dashboard after the move reads as the kept snapshot, testdata/migrate/dashboard-snapshot.json" \
    diff testdata/migrate/dashboard-snapshot.json "$OUT/dashboard.after.json"
  check "snapshot: 1.0.12's dashboard reads the same, apart from what 1.0.13 shows differently by design" \
    compared "$OUT/dashboard.app1012.json" "$OUT/dashboard.after.json" 1.0.12
  check "snapshot: so does 1.0.12's on the cluster in Pacific/Chatham" \
    compared "$OUT/dashboard.app1012chatham.json" "$OUT/dashboard.after.json" 1.0.12
  if [ "${UPDATE_GOLDEN:-0}" = 1 ]; then
    rm -rf testdata/migrate/dashboard && cp -r "$OUT/answers.after" testdata/migrate/dashboard
    rm -rf testdata/migrate/dashboard-1012 && cp -r "$OUT/answers.app1012" testdata/migrate/dashboard-1012
  fi
fi
# Kept answers read as their committed snapshots, which the Windows CI job's dashboard-snapshot.ps1
# must write byte for byte the same: the answers of a run after a move (dashboard); the same with
# every time written in other zones, some with a fraction of a second, which reads as the nearest
# second as the move rounds it, half a second as the next (dashboard-zones); 1.0.12's answers on a
# cluster in America/Chicago (dashboard-1012); and a list of times, and of text that only looks
# like one (dashboard-times).
# recorded ANSWERS SNAPSHOT: testdata/migrate/ANSWERS read as testdata/migrate/SNAPSHOT.
recorded() {
  scripts/dashboard-snapshot.sh --from "testdata/migrate/$1" "$OUT/recorded.$1.json" || return 1
  if [ "${UPDATE_GOLDEN:-0}" = 1 ] && [ "$1" != dashboard-zones ]; then cp "$OUT/recorded.$1.json" "testdata/migrate/$2"; fi
  diff "testdata/migrate/$2" "$OUT/recorded.$1.json"
}
check "snapshot: kept answers read as testdata/migrate/dashboard-snapshot.json" recorded dashboard dashboard-snapshot.json
check "snapshot: the same answers, their times in other zones, read the same" recorded dashboard-zones dashboard-snapshot.json
check "snapshot: 1.0.12's kept answers read as dashboard-1012-snapshot.json" recorded dashboard-1012 dashboard-1012-snapshot.json
check "snapshot: times read in UTC to the second, other text as it is" recorded dashboard-times dashboard-times-snapshot.json
check "snapshot: the kept 1.0.12 and 1.0.13 snapshots compare as the same" \
  compared testdata/migrate/dashboard-1012-snapshot.json testdata/migrate/dashboard-snapshot.json 1.0.12
# A payout time a second later, and a miner's payouts shown differently between two 1.0.13 snapshots,
# are differences (1); a file that is not a snapshot is an error (2), never the same or a difference.
compare_says() { # code BEFORE AFTER
  local rc=0
  scripts/dashboard-snapshot.sh --compare "$2" "$3" > "$OUT/compare.log" 2>&1 || rc=$?
  cat "$OUT/compare.log"
  equal "$rc" "$1"
}
sed 's/^\("A-solo-payouts\.payouts\.1\.paidAt": "[^"]*\)00Z"/\101Z"/' testdata/migrate/dashboard-snapshot.json > "$OUT/later.json"
sed 's/^\("A-payouts\.total": \)2/\13/' testdata/migrate/dashboard-snapshot.json > "$OUT/payouts.json"
check "snapshot: a payout time a second later is a difference, after 1.0.12 too" \
  compare_says 1 testdata/migrate/dashboard-1012-snapshot.json "$OUT/later.json"
check "snapshot: between two 1.0.13 snapshots, every line counts" compare_says 1 testdata/migrate/dashboard-snapshot.json "$OUT/payouts.json"
check "snapshot: a file that is not a snapshot is an error" compare_says 2 testdata/migrate/dashboard/health.json testdata/migrate/dashboard-snapshot.json
check "snapshot: a snapshot that is not there is an error" compare_says 2 testdata/migrate/dashboard-snapshot.json "$OUT/none.json"

# ── 13. the marker removed while the app runs ──────────────────────────────────────────────────
save_settings() {
  curl -fsS -X POST -H "Content-Type: application/json" -H "X-Forge-Password: $PW" \
    --data "{\"pool_address\":\"$MINER_A\",\"payout_address_1175\":\"$ESF\",\"coinbase_tag\":\"/saved in 1.0.13/\"}" \
    "$(api main)/api/v1/pool/config" | has '"success":true'
}
if want 13; then
  echo "── 13: the move deferred while the app runs"
  ids="$(cid main api) $(cid main stratum)"
  asroot "rm $(db main)/postgres-migrated.json"
  up main new || bad "13: compose up"
  check "13: migrate exits 0 and says deferred" migrate_ran main deferred "status deferred"
  check "13: the api and the stratum keep running, in the same containers" equal "$(cid main api) $(cid main stratum)" "$ids"
  check "13: the dashboard says the move finishes at the next start" health_says main '"migration":{[^}]*"state":"deferred"'
  check "16: after a deferral, db/ and its files belong to 10001:10001" owned_by_app main "a deferral"
  check "13: a settings save through the api" save_settings
  check "13: a block recorded meanwhile" itapp main TestITWrite IT_ACTION=block IT_HEIGHT=100300
  dc main new stop -t 30 >/dev/null 2>&1 || true
  move main || bad "13: migrate did not run"
  check "13: the next start merges" migrate_ran main "done" "done: merge"
  check "13: the settings saved through the api are kept" query_is main "SELECT coinbase_tag FROM pool_config" "/saved in 1.0.13/"
  check "13: the block is kept" query_is main "SELECT count(*) FROM blocks WHERE height = 100300" 1
  check "13: the marker is back" there "$(db main)/postgres-migrated.json"
  check "16: after a merge, db/ and its files belong to 10001:10001" owned_by_app main "a merge"
fi

# ── 11, 9, 10: per height, back to 1.0.12 and forward, twice ───────────────────────────────────
if want 9 || want 10 || want 11; then
  echo "── 11: what 1.0.13 records at heights 1.0.12 has too"
  dc main new stop -t 30 >/dev/null 2>&1 || true
  check "11: 1175 block 5002 distributed and paid, block 100149 replaced and a best share kept, in 1.0.13" \
    itapp main TestITWrite IT_ACTION=later IT_HASH="$HASH_B"
  figures_back() { ithost TestITFigures IT_PG="$(dsn main)" IT_OUT="$OUT/main.figures.back" && figures_equal "$OUT/main.figures.before" "$OUT/main.figures.back"; }
  for cycle in 1 2; do
    echo "── 9 and 10: back to 1.0.12 and forward again ($cycle)"
    # Going back replaces the whole app: 1.0.13's api and stratum stop.
    dc main new stop -t 30 >/dev/null 2>&1 || true
    up main old postgres || die "1.0.12 does not start again"
    wait_pg main || die "1.0.12 is not ready"
    if [ $cycle = 1 ]; then
      check "9: 1.0.12 opens its data with the figures it had" figures_back
      psqlc main -c "UPDATE pool_config SET pool_address = '$MINER_D', updated_at = now() WHERE id = 1" >/dev/null
    fi
    h=$((100200 + cycle))
    psqlc main -c "INSERT INTO blocks (height, hash, miner_address, reward, status, is_solo) VALUES ($h, md5('r$h') || md5('$h'), '$MINER_D', 3.125, 'pending', true)" \
      -c "INSERT INTO payouts (miner_address, block_height, amount, confirmed, txid, status, paid_at) VALUES ('$MINER_D', $h, 3.125, true, 'coinbase-direct', 'paid', now())" >/dev/null
    dc main old stop -t 60 postgres >/dev/null 2>&1 || true
    up main new || bad "10: compose up"
    STRATUM_SINCE=$(date -u +%Y-%m-%dT%H:%M:%SZ) STRATUM_AT=$(date +%s)
    check "10: the start merges ($cycle)" migrate_ran main "done" "done: merge"
    expect="99000 100000 100150 100201"
    [ $cycle = 2 ] && expect="$expect 100202"
    if want 13; then expect="$expect 100300"; fi
    check "10: blocks of both periods are there ($cycle)" equal "$(heights main 99000 100000 100150 100201 100202 100300)" "$expect"
    check "10: the payout address set later, in 1.0.12, is in effect ($cycle)" query_is main "SELECT pool_address FROM pool_config" "$MINER_D"
    check "10: one before-merge copy is kept ($cycle)" equal "$(asroot "ls $(db main) | grep -c '^forgesolo.db.before-merge-'")" 1
    check "10: the best share kept in 1.0.13 is kept ($cycle)" query_is main \
      "SELECT worker_name || '=' || CAST(difficulty AS INTEGER) FROM best_shares WHERE miner_address = '$MINER_A'" "s19=5500000000"
  done
  # The stratum reads it at its start, and the api gives it as the miner's best with no worker back.
  best_shown() { curl -fsS "$(api main)/api/v1/miners/$MINER_A" | has '"athDiff":5500000000[,}]'; }
  check "10: the miner's best share is the one kept in 1.0.13" wait_for "the kept best share" 120 best_shown
  shown() { wait_app main && curl -fsS "$(api main)/api/v1/miners/$MINER_A/solo-blocks" | has "$HASH_B"; }
  check "11: the block that replaced 100149 in 1.0.13 is the one shown" shown
  check "11: 1175 block 5002 is 1.0.13's, whole: distributed, confirmed, its credit paid" \
    query_is main "SELECT b.distributed || b.status || p.status FROM blocks_1175 b JOIN payouts_1175 p ON p.block_height = b.height WHERE b.height = 5002" 1confirmedpaid
fi

# ── 4 and 14: interrupted, then damaged after the move ─────────────────────────────────────────
if want 4 || want 14; then
  echo "── 4: the move stopped half way"
  inst_from_crashed intr 2
  # 200,000 payouts more, so that the copy's checks take seconds after its rows are written; then
  # hard-killed again, as the others.
  up intr old postgres || die "4: 1.0.12 does not start"
  wait_pg intr || die "4: 1.0.12 is not ready"
  psqlc intr -c "INSERT INTO payouts (miner_address, block_height, amount, confirmed, txid, status, created_at, paid_at)
    SELECT 'bitcoincashii:qzet9v4jk2et9v4jk2et9v4jk2et9v4jkg0xty3z5s', 300000 + g, 0.001, true, 'coinbase-direct', 'paid', now(), now()
    FROM generate_series(1, 200000) g" >/dev/null
  ithost TestITFigures IT_PG="$(dsn intr)" IT_OUT="$OUT/intr.figures.before" || bad "4: the figures before"
  docker kill "$(cid intr postgres)" >/dev/null
  # SIGTERM as the old data's server starts; SIGKILL half way through the copy, once its rows are
  # written (to the copy, not to forgesolo.db) and while the copy is being checked.
  for sig in TERM KILL; do
    up intr new migrate || bad "4: compose up"
    c=$(cid intr migrate)
    at="the old data's server started"
    [ $sig = KILL ] && at="payouts: 200155 rows"
    for _ in $(seq 1 1200); do docker logs "$c" 2>&1 | has "$at" && break; sleep 0.05; done
    # Paused at once, so the signal comes before the copy is checked and put in place.
    docker pause "$c" >/dev/null 2>&1 || bad "4: the move ended before $sig could stop it half way"
    docker kill -s "$sig" "$c" >/dev/null 2>&1 || true
    docker unpause "$c" >/dev/null 2>&1 || true
    code=$(docker wait "$c")
    if [ $sig = TERM ]; then check "4: SIGTERM ends the move with exit code 3" equal "$code" 3
    else check "4: SIGKILL kills it" equal "$code" 137; fi
    check "4: no forgesolo.db after $sig" absent "$(db intr)/forgesolo.db"
    check "4: the status does not say done after $sig" state_not intr "done"
  done
  check "4: what SIGKILL cut short was the copy, forgesolo.db.migrating" there "$(db intr)/forgesolo.db.migrating"
  move intr || bad "4: migrate did not run"
  check "4: the next start moves the data" migrate_ran intr "done"
  check "4: with the same figures" figures_after intr "$OUT/intr.figures.before"

  echo "── 14: the old folder damaged after the move"
  asroot "mv $WORK/intr/app/postgres/global/pg_control $WORK/intr/pg_control.kept"
  up intr new || bad "14: compose up"
  check "14: pg_control deleted: degraded" migrate_ran intr degraded "pg_control is missing"
  check "14: the app runs" wait_app intr
  check "14: the dashboard says the old folder is damaged" health_says intr '"state":"degraded"'
  check "14: the stratum listens" docker exec "$(cid intr stratum)" nc -z -w 2 localhost 3333
  asroot "mv $WORK/intr/pg_control.kept $WORK/intr/app/postgres/global/pg_control && echo garbage > $WORK/intr/app/postgres/PG_VERSION"
  up intr new || bad "14: compose up"
  check "14: PG_VERSION overwritten: degraded" migrate_ran intr degraded "PG_VERSION is damaged"
  check "14: the app runs" wait_app intr
  dc intr new down -t 10 >/dev/null 2>&1 || true
fi

# ── 5: refused, failed, and the choices the dashboard offers ───────────────────────────────────
maintenance() { # instance
  wait_http "$(api "$1")/api/v1/health" 120 || return 1
  health_says "$1" '"status":"maintenance"' || return 1
  equal "$(curl -s -o /dev/null -w '%{http_code}' "$(api "$1")/api/v1/miners/$MINER_A/solo-blocks")" 503
}
not_mining() { # instance: the stratum does not listen, and says why
  sleep 3
  if docker exec "$(cid "$1" stratum)" nc -z -w 2 localhost 3333; then echo "the stratum listens on 3333"; return 1; fi
  logs_say "$1" stratum 'not mining: moving the data to the new database failed'
}
old_data() { # instance, skip, [password]: the HTTP status of POST /api/v1/old-data
  local h=(-H 'Content-Type: application/json')
  [ -n "${3:-}" ] && h+=(-H "X-Forge-Password: $3")
  curl -s -o /dev/null -w '%{http_code}' -X POST "${h[@]}" --data "{\"skip\":$2}" "$(api "$1")/api/v1/old-data"
}
skip_written() { equal "$(old_data badctl true "$PW")" 200 && there "$(db badctl)/SKIP-POSTGRES-MIGRATION"; }
skip_removed() { equal "$(old_data badctl false "$PW")" 200 && absent "$(db badctl)/SKIP-POSTGRES-MIGRATION"; }
if want 5; then
  echo "── 5: a cluster that is not PostgreSQL 16"
  inst_from_crashed pgv15 3
  asroot "echo 15 > $WORK/pgv15/app/postgres/PG_VERSION"
  up pgv15 new || bad "5: compose up"
  check "5: PG_VERSION 15: failed, and migrate exits 0" migrate_ran pgv15 failed "not PostgreSQL 16"
  check "5: no forgesolo.db" absent "$(db pgv15)/forgesolo.db"
  check "5: the api is in maintenance" maintenance pgv15
  check "5: the stratum does not mine" not_mining pgv15
  dc pgv15 new down -t 10 >/dev/null 2>&1 || true

  echo "── 5: pg_control garbage before the first move"
  inst_from_crashed badctl 4
  asroot "dd if=/dev/urandom of=$WORK/badctl/app/postgres/global/pg_control bs=8192 count=1 conv=notrunc 2>/dev/null"
  up badctl new || bad "5: compose up"
  check "5: pg_control garbage: failed, and migrate exits 0" migrate_ran badctl failed "pg_control is damaged"
  check "5: no forgesolo.db" absent "$(db badctl)/forgesolo.db"
  check "5: the api is in maintenance" maintenance badctl
  check "5: the stratum does not mine" not_mining badctl
  check "5: starting without the old data needs the password" equal "$(old_data badctl true)" 401
  check "5: with it, SKIP-POSTGRES-MIGRATION is written" skip_written
  dc badctl new stop -t 30 >/dev/null 2>&1 || true
  up badctl new || bad "5: compose up"
  check "5: after a restart: skipped" migrate_ran badctl skipped
  check "5: the app runs on a new, empty database" wait_app badctl
  check "5: an empty one" query_is badctl "SELECT count(*) FROM blocks" 0
  check "5: the dashboard says the old data was left out" health_says badctl '"state":"skipped"'
  check "5: bringing the old data back in" skip_removed
  asroot "cp -a $WORK/crashed/global/pg_control $WORK/badctl/app/postgres/global/pg_control"
  dc badctl new stop -t 30 >/dev/null 2>&1 || true
  up badctl new || bad "5: compose up"
  check "5: after a restart: merged" migrate_ran badctl "done" "done: merge"
  check "5: with the figures of 1.0.12" figures_after badctl
  dc badctl new down -t 10 >/dev/null 2>&1 || true
fi

# ── 7: another server time zone ─────────────────────────────────────────────────────────────────
if want 7; then
  echo "── 7: a cluster in Pacific/Chatham"
  inst_from_crashed chatham 5
  asroot "echo \"timezone = 'Pacific/Chatham'\" >> $WORK/chatham/app/postgres/postgresql.conf"
  move chatham || bad "7: migrate did not run"
  check "7: the move" migrate_ran chatham "done"
  check "7: the same rows as the move in UTC" itapp chatham TestITSnapshot IT_WANT=/out/main.db.json
  dc chatham new down -t 10 >/dev/null 2>&1 || true
fi

# ── 8: the databases of 1.0.0 and 1.0.9 ──────────────────────────────────────────────────────────
has_rows() { equal "$(grep -c -E '^count\.(blocks|payouts|blocks_1175|payouts_1175)=[1-9]' "$1")" 4; }
if want 8; then
  i=6
  for v in v1.0.0 v1.0.9; do
    echo "── 8: a database of $v"
    n=dump${v//./}
    inst "$n" $i
    i=$((i + 1))
    c=$P-$n-old
    docker run --platform "$PLATFORM" -d --name "$c" -p "127.0.0.1:$(inst_port "$n" PG):5432" -e POSTGRES_USER=forge -e "POSTGRES_PASSWORD=$DBPW" \
      -e POSTGRES_DB=forgesolo -v "$WORK/$n/app/postgres:/var/lib/postgresql/data" "$TS" >/dev/null
    wait_for "$v's database" 300 container_log_has "$c" "PostgreSQL init process complete" >/dev/null || bad "8: $v's database"
    wait_for "$v's database" 120 docker exec "$c" psql -U forge -d forgesolo -c "SELECT 1" >/dev/null || bad "8: $v's database"
    check "8: $v's database loads" docker exec -i "$c" psql -U forge -d forgesolo -v ON_ERROR_STOP=1 -q -f - < "testdata/migrate/$v.sql"
    ithost TestITFigures IT_PG="$(dsn "$n")" IT_OUT="$OUT/$n.figures.before" || bad "8: the figures of $v"
    check "8: $v's database has blocks, payouts and 1175 rows" has_rows "$OUT/$n.figures.before"
    docker stop -t 60 "$c" >/dev/null && docker rm "$c" >/dev/null
    move "$n" || bad "8: migrate did not run"
    check "8: $v's database moves" migrate_ran "$n" "done"
    check "8: with no figure changed" figures_after "$n" "$OUT/$n.figures.before"
    dc "$n" new down -t 10 >/dev/null 2>&1 || true
  done
fi

# ── 12: an update while 1.0.12's database runs ─────────────────────────────────────────────────
shut_down_first() {
  local t_down t_move
  t_down=$(sed -n 's/^\([^ ]*\) .*database system is shut down.*/\1/p' "$OUT/live.oldpg.log" | tail -1)
  t_move=$(docker logs -t "$(cid live migrate)" 2>&1 | head -1 | cut -d' ' -f1)
  echo "1.0.12's database shut down at ${t_down:-no time}; the move began at $t_move"
  [ -n "$t_down" ] && [ "$(date -d "$t_down" +%s%N)" -lt "$(date -d "$t_move" +%s%N)" ]
}
if want 12; then
  echo "── 12: the update made while 1.0.12's database runs"
  inst_from_crashed live 8
  up live old postgres || bad "12: 1.0.12 up"
  wait_pg live || bad "12: 1.0.12 is not ready"
  docker logs -f -t "$(cid live postgres)" > "$OUT/live.oldpg.log" 2>&1 &
  follow=$!
  up live new || bad "12: compose up"
  docker wait "$(cid live migrate)" >/dev/null
  kill $follow 2>/dev/null || true
  wait $follow 2>/dev/null || true
  check "12: 1.0.12's database shut down before the move began" shut_down_first
  check "12: the move" migrate_ran live "done"
  check "12: shut down, and 1.0.12 opens it with the same figures and fingerprint" gate live
  dc live new down -t 10 >/dev/null 2>&1 || true
fi

# ── 15: the Windows shape ────────────────────────────────────────────────────────────────────────
if want 15; then
  echo "── 15: plain PostgreSQL 16.15 in America/Chicago, the launcher's steps"
  inst win 9
  d=$WORK/win net=$P-win c=$P-win-pg
  docker network create "$net" >/dev/null
  docker run --platform "$PLATFORM" -d --name "$c" --network "$net" -e TZ=America/Chicago -e POSTGRES_USER=forge -e POSTGRES_PASSWORD=Secret-Pw-1 \
    -e POSTGRES_DB=forgesolo -v "$d/pg:/var/lib/postgresql/data" "$PG16" >/dev/null
  wait_for "PostgreSQL 16.15" 300 container_log_has "$c" "PostgreSQL init process complete" >/dev/null || bad "15: PostgreSQL 16.15"
  wait_for "PostgreSQL 16.15" 120 docker exec "$c" psql -U forge -d forgesolo -c "SELECT 1" >/dev/null || bad "15: PostgreSQL 16.15"
  docker exec -i "$c" psql -U forge -d forgesolo -q < internal/pgmigrate/testdata/pg-1.0.12-schema.sql >/dev/null 2>&1
  docker exec -i "$c" psql -U forge -d forgesolo -v ON_ERROR_STOP=1 -q < testdata/migrate/seed-1012.sql >/dev/null || bad "15: the seed did not load"
  check "15: the server's time zone is America/Chicago" equal "$(docker exec "$c" psql -U forge -d forgesolo -qtAc 'SHOW timezone')" America/Chicago
  docker stop -t 60 "$c" >/dev/null && docker rm "$c" >/dev/null
  mig() { docker run --platform "$PLATFORM" --rm --network "$net" -e FORGE_MIGRATE_PG -v "$d:$d" -v "$BIN:/b:ro" "$ALPINE" /b/forge-solo-migrate "$@"; }
  check "15: plan says move" equal "$(mig plan --db "$d/db/forgesolo.db" --pgdata "$d/pg")" move
  docker run --platform "$PLATFORM" -d --name "$c" --network "$net" -v "$d/pg:/var/lib/postgresql/data" "$PG16" \
    postgres -c default_transaction_read_only=on -c autovacuum=off >/dev/null
  wait_for "the read-only server" 120 docker exec "$c" psql -U forge -d forgesolo -c "SELECT 1" >/dev/null || bad "15: the read-only server"
  export FORGE_MIGRATE_PG="host=$c port=5432 user=forge password=Secret-Pw-1 dbname=forgesolo sslmode=disable"
  check "15: prepare, with the server's address in FORGE_MIGRATE_PG" mig prepare --db "$d/db/forgesolo.db"
  refuses_writes() {
    local said
    said=$(docker exec "$c" psql -U forge -d forgesolo -c "INSERT INTO miners (address) VALUES ('x')" 2>&1) && { echo "it took it"; return 1; }
    echo "$said" | has "read-only transaction"
  }
  check "15: the server takes no write" refuses_writes
  docker stop -t 60 "$c" >/dev/null && docker rm "$c" >/dev/null
  unset FORGE_MIGRATE_PG
  check "15: commit" mig commit --db "$d/db/forgesolo.db" --pgdata "$d/pg"
  win_done() { asroot "cat $d/db/migration-status.json" | has '"state": "done"'; }
  check "15: the status says done" win_done
  check "15: plan says none" equal "$(mig plan --db "$d/db/forgesolo.db" --pgdata "$d/pg")" none
  golden=(-e IT_WANT=/golden/seed-1012.db.json)
  [ "${UPDATE_GOLDEN:-0}" = 1 ] && golden=(-e IT_OUT=/golden/seed-1012.db.json)
  check "15: the rows are testdata/migrate/seed-1012.db.json, which the Windows CI job compares with" \
    docker run --platform "$PLATFORM" --rm --network none -v "$d/db:/data" -v "$BIN:/b:ro" -v "$ROOT/testdata/migrate:/golden" -e IT_DB=/data/forgesolo.db \
    "${golden[@]}" "$ALPINE" /b/it.test -test.run '^TestITSnapshot$' -test.count=1
  docker network rm "$net" >/dev/null

  echo "── 15: the lib/pq tests on a real PostgreSQL 16.15"
  c=$P-pgit
  docker run --platform "$PLATFORM" -d --name "$c" -p "127.0.0.1:$(inst_port win API):5432" -e POSTGRES_USER=forge -e POSTGRES_PASSWORD=Secret-Pw-1 "$PG16" >/dev/null
  wait_for "PostgreSQL 16.15" 300 container_log_has "$c" "PostgreSQL init process complete" >/dev/null || bad "15: PostgreSQL 16.15"
  wait_for "PostgreSQL 16.15" 120 docker exec "$c" psql -U forge -c "SELECT 1" >/dev/null || bad "15: PostgreSQL 16.15"
  libpq() {
    FORGE_MIGRATE_IT_PG="postgres://forge:Secret-Pw-1@127.0.0.1:$(inst_port win API)/postgres?sslmode=disable" \
      run_must_pass TestPostgresSourceReadsAsTheModel -tags it ./internal/pgmigrate/ -run '^TestPostgres'
  }
  check "15: the lib/pq source reads a real PostgreSQL 16 as the model the unit tests use" libpq
  docker rm -f "$c" >/dev/null
fi

# ── 16: a fresh install ──────────────────────────────────────────────────────────────────────────
if want 16; then
  echo "── 16: a fresh install"
  inst fresh 10
  move fresh || bad "16: migrate did not run"
  check "16: nothing to move: migrate exits 0, the status says none" migrate_ran fresh none "plan: none"
  check "16: after a fresh start, db/ and its files belong to 10001:10001" owned_by_app fresh "a fresh start"
fi

# ── 11: the stratum's sweep after the merges ───────────────────────────────────────────────────
if [ -n "${STRATUM_AT:-}" ]; then
  left=$((STRATUM_AT + 3 * 120 + 20 - $(date +%s)))
  if [ $left -gt 0 ]; then echo "── 11: waiting ${left}s for three sweeps of the stratum's 1175 ledger"; sleep $left; fi
  stratum_since() { docker logs --since "$STRATUM_SINCE" "$(cid main stratum)" 2>&1; }
  never_refused() { if stratum_since | grep "refusing to redistribute"; then return 1; fi; }
  ledger_ran() { stratum_since | has "1175 payout processor started"; }
  check "11: the stratum's 1175 ledger ran" ledger_ran
  check "11: three sweeps, and none refused to redistribute" never_refused
fi

echo
echo "── $PASSED checks passed, $FAILED failed"
[ "$FAILED" = 0 ] || exit 1
[ "$CASES" = all ] || { echo "IT_CASES=$CASES: not every case ran"; exit 3; }
echo "✓ the move from PostgreSQL to SQLite works end to end"

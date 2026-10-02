#!/usr/bin/env bash
# The database update an Umbrel makes when the app updates: a data directory made by the previous
# release's database image, opened by the image this tree builds (docker/postgres).
#
#   ./scripts/it-postgres-upgrade.sh                                         # amd64
#   PLATFORM=linux/arm64 UPGRADE_HOURS=200 ./scripts/it-postgres-upgrade.sh  # arm64, emulated
#
# 1. The previous image (1.0.12: timescale/timescaledb:2.17.2-pg16) creates the database as the
#    app's compose does -- the same POSTGRES_* settings and init-db.sql -- and the app's own code
#    fills it: its settings, its TIDES key, its blocks, and hourly chunks of stored shares.
# 2. This tree's image starts on the same data directory, with max_locks_per_transaction=128 as a
#    small board has it, and the app's code checks it: PostgreSQL 16.15, TimescaleDB still 2.17.2,
#    every setting and block there, the old shares cleared, new chunks made; and the server's log
#    has no error.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib-it.sh

PLATFORM="${PLATFORM:-linux/amd64}"
OLD_IMAGE="timescale/timescaledb@sha256:4e459e217f00cbb09920c34d245501e63427e6767a495de57ce76823ff280f12"
NEW_IMAGE="forge-solo-postgres:upgrade-it"
NAME="forge-it-pg-upgrade"
VOLUME="forge-it-pg-upgrade-data"
PORT="${PG_PORT:-15433}"

cleanup() {
  [[ "${KEEP:-0}" == "1" ]] && return
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  docker volume rm -f "$VOLUME" >/dev/null 2>&1 || true
}
trap cleanup EXIT
docker rm -f "$NAME" >/dev/null 2>&1 || true
docker volume rm -f "$VOLUME" >/dev/null 2>&1 || true

# An image index pinned by digest names every platform at once, and the Docker store keeps one of
# them per digest: run the platform's own manifest.
platform_ref() {
  local digest
  digest=$(docker buildx imagetools inspect --raw "$1" | python3 -c '
import json, sys
m = json.load(sys.stdin)
print([x["digest"] for x in m["manifests"] if x.get("platform", {}).get("architecture") == sys.argv[1]][0])' "${PLATFORM#linux/}")
  echo "${1%@*}@${digest}"
}

start() { # image, then the server's command line
  local img="$1"; shift
  docker run -d --name "$NAME" --platform "$PLATFORM" -p "127.0.0.1:${PORT}:5432" \
    -e POSTGRES_USER=forge -e POSTGRES_PASSWORD=forgepass -e POSTGRES_DB=forgesolo \
    -v "$VOLUME:/var/lib/postgresql/data" -v "$PWD/init-db.sql:/docker-entrypoint-initdb.d/init.sql:ro" \
    "$img" "$@" >/dev/null
}

export TIDES_PG_DB="postgres://forge:forgepass@127.0.0.1:${PORT}/forgesolo?sslmode=disable"
export UPGRADE_HOURS="${UPGRADE_HOURS:-1100}"

echo "── the previous release's database ($PLATFORM)"
start "$(platform_ref "$OLD_IMAGE")"
wait_for "postgres init" 600 container_log_has "$NAME" "PostgreSQL init process complete"
wait_for "postgres queries" 300 docker exec "$NAME" psql -U forge -d forgesolo -c "SELECT 1"
PG_UPGRADE_PHASE=seed run_must_pass TestPostgresUpgradeSeed ./internal/stats/ -run TestPostgresUpgradeSeed
docker stop -t 120 "$NAME" >/dev/null
docker rm "$NAME" >/dev/null

echo "── this tree's database image, on the same data directory"
docker build -q --platform "$PLATFORM" -t "$NEW_IMAGE" docker/postgres >/dev/null
mapfile -t PG_CMD < <(docker inspect "$NEW_IMAGE" --format '{{range .Config.Cmd}}{{println .}}{{end}}' | sed '/^$/d')
start "$NEW_IMAGE" "${PG_CMD[@]}" -c max_locks_per_transaction=128
wait_for "postgres queries" 300 docker exec "$NAME" psql -U forge -d forgesolo -c "SELECT 1"
PG_UPGRADE_PHASE=check run_must_pass TestPostgresUpgradeCheck ./internal/stats/ -run TestPostgresUpgradeCheck

if docker logs "$NAME" 2>&1 | grep -E 'FATAL|PANIC|ERROR|could not load|incompatible'; then
  echo "✗ the new server logged the errors above"
  exit 1
fi
echo "✓ a database from the previous release opens and works on this tree's image ($PLATFORM)"

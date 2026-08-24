#!/usr/bin/env bash
# Runs the integration tests that need a real postgres, against the SAME image
# docker-compose.yml pins, and tears it down afterwards.
#
# These cover the 1175 merge-mining payout ledger's fund-safety invariants (confirmation
# gate, orphan-void, no double-credit). They were gated on an environment variable whose
# provisioning script was never committed, and CI ran no tests at all, so none of them had
# executed since this repo was created.
#
#   ./scripts/it-postgres.sh
#
# Set KEEP=1 to leave the container running for debugging.

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib-it.sh

PG_IMAGE="timescale/timescaledb:2.17.2-pg16@sha256:4e459e217f00cbb09920c34d245501e63427e6767a495de57ce76823ff280f12"
NAME="${PG_CONTAINER:-forge-it-postgres}"
PORT="${PG_PORT:-15432}"

cleanup() { [[ "${KEEP:-0}" == "1" ]] || docker rm -f "$NAME" >/dev/null 2>&1 || true; }

if [[ "${USE_RUNNING_PG:-0}" != "1" ]]; then
  trap cleanup EXIT
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  echo "── starting postgres ($NAME on :$PORT)"
  docker run -d --name "$NAME" \
    -e POSTGRES_USER=forge -e POSTGRES_PASSWORD=forgepass -e POSTGRES_DB=forgesolo \
    -p "${PORT}:5432" "$PG_IMAGE" >/dev/null
  # Wait for the entrypoint's init to finish before trusting pg_isready. The official
  # image runs a temporary server on a unix socket to run initdb, then restarts it to
  # listen on TCP; pg_isready passes against that temporary server, so connecting
  # straight after it races the restart and the first real query dies with
  # "connection reset by peer". These markers are the entrypoint's own contract.
  wait_for "postgres init" 120 container_log_has "$NAME" "PostgreSQL init process complete"
  wait_for "postgres" 60 docker exec "$NAME" pg_isready -U forge -d forgesolo
  wait_for "postgres queries" 60 docker exec "$NAME" psql -U forge -d forgesolo -c "SELECT 1"
fi

export MMTEST_DB="postgres://forge:forgepass@127.0.0.1:${PORT}/forgesolo?sslmode=disable"

run_must_pass TestPayout1175Accounting ./internal/stats/ -run TestPayout1175Accounting

echo "✓ postgres integration suite passed"

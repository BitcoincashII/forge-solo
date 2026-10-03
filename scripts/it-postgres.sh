#!/usr/bin/env bash
# Runs the integration tests that need a real postgres, against the image docker/postgres builds
# (the one docker-compose.yml ships since 1.0.13), and tears it down afterwards. The server runs
# with max_locks_per_transaction=128, what TimescaleDB's tuning gives a board under 8 GB: the
# share clear must work within that (TestPostgresClearsThousandsOfShareChunks).
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

PG_IMAGE="forge-solo-postgres:it"
NAME="${PG_CONTAINER:-forge-it-postgres}"
PORT="${PG_PORT:-15432}"

cleanup() { [[ "${KEEP:-0}" == "1" ]] || docker rm -f "$NAME" >/dev/null 2>&1 || true; }

if [[ "${USE_RUNNING_PG:-0}" != "1" ]]; then
  trap cleanup EXIT
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  echo "── building the shipped database image (docker/postgres)"
  docker build -q -t "$PG_IMAGE" docker/postgres >/dev/null
  # The image's own settings (its CMD), plus a small board's lock limit.
  mapfile -t PG_CMD < <(docker inspect "$PG_IMAGE" --format '{{range .Config.Cmd}}{{println .}}{{end}}' | sed '/^$/d')
  echo "── starting postgres ($NAME on :$PORT)"
  docker run -d --name "$NAME" \
    -e POSTGRES_USER=forge -e POSTGRES_PASSWORD=forgepass -e POSTGRES_DB=forgesolo \
    -p "${PORT}:5432" "$PG_IMAGE" "${PG_CMD[@]}" -c max_locks_per_transaction=128 >/dev/null
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
export TIDES_PG_DB="$MMTEST_DB"
run_must_pass TestPostgresTidesConfig ./internal/stats/ -run TestPostgresTidesConfig
run_must_pass TestPostgresStoredOldDefaultTagReadsAsNoneChosen ./internal/stats/ -run TestPostgresStoredOldDefaultTagReadsAsNoneChosen
run_must_pass TestPostgresSoloSharesAreNotStoredAndOldOnesAreCleared ./internal/stats/ -run TestPostgresSoloSharesAreNotStoredAndOldOnesAreCleared
run_must_pass TestPostgresClearsThousandsOfShareChunks ./internal/stats/ -run TestPostgresClearsThousandsOfShareChunks
run_must_pass TestPostgresDashboardTotalsCoverEveryBlock ./internal/stats/ -run TestPostgresDashboardTotalsCoverEveryBlock

echo "✓ postgres integration suite passed"

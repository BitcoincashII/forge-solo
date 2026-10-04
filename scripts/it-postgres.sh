#!/usr/bin/env bash
# Runs the integration tests that need a real postgres, against the image docker/postgres builds
# (the one docker-compose.yml ships since 1.0.13), and tears it down afterwards. The server runs
# with max_locks_per_transaction=128, what TimescaleDB's tuning gives a board under 8 GB: the
# share clear must work within that (TestPostgresClearsThousandsOfShareChunks). It first checks
# what the image sets in every database it makes: no telemetry to Timescale and no JIT.
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

# expect_sql stops the run unless the query, run in the test container, prints exactly want.
expect_sql() { # what, query, want
  local got
  got=$(docker exec "$NAME" psql -U forge -d forgesolo -tAc "$2" 2>&1) || true
  if [[ "$got" != "$3" ]]; then
    echo "✗ $1: got '$got', want '$3'"
    exit 1
  fi
  echo "✓ $1"
}

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

  # Telemetry off from the command line covers the databases made before 1.0.13 too, whose
  # postgresql.conf says basic. A fresh one also has it off in its postgresql.conf and its telemetry
  # job unscheduled. JIT is off from the command line, as the old tuning set it in every database.
  expect_sql "telemetry off, from the command line" \
    "SELECT setting || ' (' || source || ')' FROM pg_settings WHERE name = 'timescaledb.telemetry_level'" "off (command line)"
  expect_sql "telemetry off in a fresh database's postgresql.conf" \
    "SELECT setting FROM pg_file_settings WHERE name = 'timescaledb.telemetry_level' ORDER BY seqno DESC LIMIT 1" "off"
  expect_sql "telemetry job unscheduled in a fresh database" \
    "SELECT scheduled FROM timescaledb_information.jobs WHERE job_id = 1" "f"
  expect_sql "JIT off, from the command line" \
    "SELECT setting || ' (' || source || ')' FROM pg_settings WHERE name = 'jit'" "off (command line)"
fi

export MMTEST_DB="postgres://forge:forgepass@127.0.0.1:${PORT}/forgesolo?sslmode=disable"

run_must_pass TestPayout1175Accounting ./internal/stats/ -run TestPayout1175Accounting
export TIDES_PG_DB="$MMTEST_DB"
run_must_pass TestPostgresTidesConfig ./internal/stats/ -run TestPostgresTidesConfig
run_must_pass TestPostgresStoredOldDefaultTagReadsAsNoneChosen ./internal/stats/ -run TestPostgresStoredOldDefaultTagReadsAsNoneChosen
run_must_pass TestPostgresSoloSharesAreNotStoredAndOldOnesAreCleared ./internal/stats/ -run TestPostgresSoloSharesAreNotStoredAndOldOnesAreCleared
run_must_pass TestPostgresClearsThousandsOfShareChunks ./internal/stats/ -run TestPostgresClearsThousandsOfShareChunks
run_must_pass TestPostgresDashboardTotalsCoverEveryBlock ./internal/stats/ -run TestPostgresDashboardTotalsCoverEveryBlock
run_must_pass TestPostgresFoundTimesAreKept ./internal/stats/ -run TestPostgresFoundTimesAreKept
run_must_pass TestPostgresA1175BlockIsPutBack ./internal/stats/ -run TestPostgresA1175BlockIsPutBack

echo "✓ postgres integration suite passed"

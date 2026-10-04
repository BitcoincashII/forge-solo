#!/usr/bin/env bash
# Runs the integration tests that need a real postgres, against the database 1.0.12 shipped
# (TimescaleDB 2.17.2 on PostgreSQL 16, the data the move to SQLite reads), and tears it down
# afterwards. The server runs with max_locks_per_transaction=128, what TimescaleDB's tuning gives
# a board under 8 GB, and a lock table no larger than a small board's (40 connections, 12
# workers, 2 autovacuum workers): the share clear must work within that, and one statement over
# thousands of chunks must not (TestPostgresClearsThousandsOfShareChunks).
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

PG_IMAGE="timescale/timescaledb@sha256:4e459e217f00cbb09920c34d245501e63427e6767a495de57ce76823ff280f12" # 2.17.2-pg16
NAME="${PG_CONTAINER:-forge-it-postgres}"
PORT="${PG_PORT:-15432}"

cleanup() { [[ "${KEEP:-0}" == "1" ]] || docker rm -f "$NAME" >/dev/null 2>&1 || true; }

if [[ "${USE_RUNNING_PG:-0}" != "1" ]]; then
  trap cleanup EXIT
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  echo "── starting postgres ($NAME on :$PORT)"
  docker run -d --name "$NAME" \
    -e POSTGRES_USER=forge -e POSTGRES_PASSWORD=forgepass -e POSTGRES_DB=forgesolo \
    -p "${PORT}:5432" "$PG_IMAGE" -c max_locks_per_transaction=128 \
    -c max_connections=40 -c max_worker_processes=12 -c autovacuum_max_workers=2 >/dev/null
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
run_must_pass TestPostgresFoundTimesAreKept ./internal/stats/ -run TestPostgresFoundTimesAreKept
run_must_pass TestPostgresA1175BlockIsPutBack ./internal/stats/ -run TestPostgresA1175BlockIsPutBack
run_must_pass TestPostgresSchemaIsCreatedOneProcessAtATime ./internal/stats/ -run TestPostgresSchemaIsCreatedOneProcessAtATime
run_must_pass TestPostgresMinerPayoutsList ./internal/stats/ -run TestPostgresMinerPayoutsList

echo "✓ postgres integration suite passed"

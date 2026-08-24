#!/usr/bin/env bash
# Shared helper for the integration-test runners.
#
# The point of these runners is that the gated tests actually EXECUTE. Both of them call
# t.Skip() when their environment variable is unset, and a skipped Go test still lets the
# package print "ok" -- so a runner that merely sets the variable and shells out to
# `go test` would report success even if the gate silently failed to open. run_must_pass
# therefore requires an explicit "--- PASS: <name>" in the output and treats a skip as a
# failure, which is the whole reason these tests can be trusted after this change.

set -euo pipefail

run_must_pass() {
  local name="$1"; shift
  local out
  echo "── running ${name}"
  if ! out=$(go test -count=1 -v "$@" 2>&1); then
    echo "$out"
    echo "✗ ${name}: test command failed"
    return 1
  fi
  echo "$out" | grep -E "^(=== RUN|--- |ok|FAIL)" || true
  if echo "$out" | grep -q -- "--- SKIP: ${name}"; then
    echo "✗ ${name} SKIPPED — its environment gate did not open, so nothing was verified"
    return 1
  fi
  if ! echo "$out" | grep -q -- "--- PASS: ${name}"; then
    echo "✗ ${name} did not report PASS"
    return 1
  fi
  echo "✓ ${name}"
}

# container_log_has is a named predicate so the readiness waits below need no nested
# bash -c quoting.
container_log_has() {
  docker logs "$1" 2>&1 | grep -q "$2"
}

wait_for() {
  local what="$1" tries="$2"; shift 2
  local i
  for ((i = 1; i <= tries; i++)); do
    if "$@" >/dev/null 2>&1; then
      echo "   ${what} ready after ${i}s"
      return 0
    fi
    sleep 1
  done
  echo "✗ timed out waiting for ${what} after ${tries}s"
  return 1
}

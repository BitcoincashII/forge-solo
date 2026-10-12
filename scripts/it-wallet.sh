#!/usr/bin/env bash
# Runs the dashboard's Wallet card against live nodes: a BCH2 node and a 1175 node, each on a
# regtest chain of its own and with its wallet off, as Forge Solo runs them, asked through the
# api's own handler (cmd/api/wallet_it_test.go).
#
#   ./scripts/it-wallet.sh
#
# The nodes are those of the images docker-compose.yml pins, by digest, so the binaries the Umbrel
# app ships are the ones tested; the 1175 node is also the one the Windows installer ships. Set
# BCH2_BIN_DIR (bitcoincashIId) and ESF_BIN_DIR (elevenseventyfived) to run local binaries instead.

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib-it.sh

need_tools go python3 curl
if [[ -z "${BCH2_BIN_DIR:-}" || -z "${ESF_BIN_DIR:-}" ]]; then
  need_tools docker
fi

WORKDIR="$(mktemp -d)"
PREFIX="forge-it-wallet-$$"
PASSWORD="$(python3 -c 'import secrets; print(secrets.token_hex(16))')"

# A free port on 127.0.0.1 for each node's RPC.
free_port() {
  python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
}
BCH2_PORT=$(free_port)
ESF_PORT=$(free_port)

# rpc_answers is a named predicate for wait_for: the node at port answers RPC with a result.
rpc_answers() {
  curl -fsS --max-time 5 --user "forge:$PASSWORD" -H 'Content-Type: application/json' \
    --data '{"jsonrpc":"1.0","id":"it","method":"getblockcount","params":[]}' "http://127.0.0.1:$1/" | grep -q '"error":null'
}

rpc_stop() {
  curl -fsS --max-time 10 --user "forge:$PASSWORD" -H 'Content-Type: application/json' \
    --data '{"jsonrpc":"1.0","id":"it","method":"stop","params":[]}' "http://127.0.0.1:$1/" >/dev/null 2>&1 || true
}

cleanup() {
  rpc_stop "$BCH2_PORT"
  rpc_stop "$ESF_PORT"
  docker stop -t 30 "$PREFIX-bch2" "$PREFIX-1175" >/dev/null 2>&1 || true
  # A node run from a local binary is waited for until it has stopped, before its data goes.
  for _ in $(seq 1 30); do
    pgrep -f -- "-datadir=$WORKDIR/" >/dev/null || break
    sleep 1
  done
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

# The node flags both runs share: regtest, RPC on 127.0.0.1 only, no peers, and no wallet.
COMMON=(-regtest -server=1 -disablewallet -listen=0 -dnsseed=0 -printtoconsole=0
  -rpcbind=127.0.0.1 -rpcallowip=127.0.0.1 -rpcuser=forge "-rpcpassword=$PASSWORD")
# 1175's regtest keys are not written as Bitcoin's, so its coinbases pay a script that needs no key
# (raw(51)), which only a node that relays nonstandard transactions spends.
ESF_ONLY=(-acceptnonstdtxn=1)

if [[ -n "${BCH2_BIN_DIR:-}" ]]; then
  [[ -x "$BCH2_BIN_DIR/bitcoincashIId" ]] || { echo "✗ BCH2_BIN_DIR=$BCH2_BIN_DIR has no bitcoincashIId"; exit 1; }
  mkdir -p "$WORKDIR/bch2"
  echo "── starting the BCH2 node from $BCH2_BIN_DIR on :$BCH2_PORT"
  "$BCH2_BIN_DIR/bitcoincashIId" -datadir="$WORKDIR/bch2" -daemon "${COMMON[@]}" -rpcport="$BCH2_PORT" >/dev/null
else
  IMG=$(grep -oE 'ghcr\.io/bitcoincashii/forge-solo-node:[^ @]+@sha256:[0-9a-f]{64}' docker-compose.yml)
  echo "── starting the BCH2 node of $IMG on :$BCH2_PORT"
  docker run -d --rm --quiet --name "$PREFIX-bch2" --network host --entrypoint bitcoincashIId "$IMG" \
    "${COMMON[@]}" -rpcport="$BCH2_PORT" >/dev/null
fi
if [[ -n "${ESF_BIN_DIR:-}" ]]; then
  [[ -x "$ESF_BIN_DIR/elevenseventyfived" ]] || { echo "✗ ESF_BIN_DIR=$ESF_BIN_DIR has no elevenseventyfived"; exit 1; }
  mkdir -p "$WORKDIR/esf"
  echo "── starting the 1175 node from $ESF_BIN_DIR on :$ESF_PORT"
  "$ESF_BIN_DIR/elevenseventyfived" -datadir="$WORKDIR/esf" -daemon "${COMMON[@]}" "${ESF_ONLY[@]}" -rpcport="$ESF_PORT" >/dev/null
else
  IMG=$(grep -oE 'ghcr\.io/bitcoincashii/forge-solo-node1175:[^ @]+@sha256:[0-9a-f]{64}' docker-compose.yml)
  echo "── starting the 1175 node of $IMG on :$ESF_PORT"
  docker run -d --rm --quiet --name "$PREFIX-1175" --network host --entrypoint elevenseventyfived "$IMG" \
    "${COMMON[@]}" "${ESF_ONLY[@]}" -rpcport="$ESF_PORT" >/dev/null
fi
wait_for "BCH2 node" 120 rpc_answers "$BCH2_PORT"
wait_for "1175 node" 120 rpc_answers "$ESF_PORT"

export WALLET_IT_BCH2_URL="http://127.0.0.1:$BCH2_PORT" WALLET_IT_BCH2_USER=forge WALLET_IT_BCH2_PASSWORD="$PASSWORD"
export WALLET_IT_1175_URL="http://127.0.0.1:$ESF_PORT" WALLET_IT_1175_USER=forge WALLET_IT_1175_PASSWORD="$PASSWORD"

run_must_pass TestWalletAgainstRegtestNodes_Live ./cmd/api/ -run '^TestWalletAgainstRegtestNodes_Live$'
echo "✓ the Wallet card read both regtest nodes"

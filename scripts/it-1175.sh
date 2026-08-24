#!/usr/bin/env bash
# Runs the end-to-end merge-mining proof: real pool code (CreateJob's coinbase commitment
# and multi-tx merkle branch, plus the same AuxPoW assembler the stratum share path uses)
# against a live 1175 regtest node, confirming a solved parent block is accepted by
# submitauxblock and advances the aux chain.
#
#   ./scripts/it-1175.sh
#
# The node binary is taken from ESF_BIN_DIR if set, otherwise the pinned upstream release
# is downloaded and sha256-verified fail-closed, exactly as docker/node1175/Dockerfile does.
# Set KEEP=1 to leave the node running for debugging.

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib-it.sh

ESF_VERSION="${ESF_VERSION:-29.1.0}"
ESF_SHA256_AMD64=444c75a4cd884e2c9b86b64e2ad6d5210004400486a309517d1b94269780be67
ESF_SHA256_ARM64=021c94846cc82ee815b0dea244df1448a1b0588eadea7c04ef39f509364e17cf
RPC_PORT="${ESF_RPC_PORT:-18777}"
WORKDIR="$(mktemp -d)"

# AuxPoW is inactive until this height on regtest, and getauxblock fails until it is
# reached -- which reads exactly like a broken node if you do not know to generate first.
AUXPOW_ACTIVATION_HEIGHT=200

cleanup() {
  if [[ "${KEEP:-0}" == "1" ]]; then
    echo "── KEEP=1: node left running, datadir ${WORKDIR}"
    return
  fi
  "$ESF_CLI" -regtest -datadir="$WORKDIR/node" -rpcuser=esf -rpcpassword=esfpass -rpcport="$RPC_PORT" stop >/dev/null 2>&1 || true
  sleep 2
  rm -rf "$WORKDIR"
}

if [[ -n "${ESF_BIN_DIR:-}" ]]; then
  ESF_D="$ESF_BIN_DIR/elevenseventyfived"
  ESF_CLI="$ESF_BIN_DIR/elevenseventyfive-cli"
  [[ -x "$ESF_D" && -x "$ESF_CLI" ]] || { echo "✗ ESF_BIN_DIR=$ESF_BIN_DIR does not contain the 1175 binaries"; exit 1; }
else
  case "$(uname -m)" in
    x86_64)          ARCH_SUFFIX=linux-x86_64;  SHA="$ESF_SHA256_AMD64" ;;
    aarch64|arm64)   ARCH_SUFFIX=linux-aarch64; SHA="$ESF_SHA256_ARM64" ;;
    *) echo "✗ no pinned 1175 release for $(uname -m); set ESF_BIN_DIR"; exit 1 ;;
  esac
  echo "── fetching 1175 v${ESF_VERSION} (${ARCH_SUFFIX})"
  curl -fsSL "https://github.com/1175Dev/1175/releases/download/v${ESF_VERSION}/elevenseventyfive-${ESF_VERSION}-${ARCH_SUFFIX}.tar.gz" -o "$WORKDIR/esf.tar.gz"
  # Fail-closed: an unverified mining binary never runs.
  echo "${SHA}  ${WORKDIR}/esf.tar.gz" | sha256sum -c -
  tar -xzf "$WORKDIR/esf.tar.gz" -C "$WORKDIR"
  ESF_D="$WORKDIR/elevenseventyfive-${ESF_VERSION}/bin/elevenseventyfived"
  ESF_CLI="$WORKDIR/elevenseventyfive-${ESF_VERSION}/bin/elevenseventyfive-cli"
fi

trap cleanup EXIT
mkdir -p "$WORKDIR/node"
CLI=("$ESF_CLI" -regtest -datadir="$WORKDIR/node" -rpcuser=esf -rpcpassword=esfpass -rpcport="$RPC_PORT")

echo "── starting 1175 regtest node on :$RPC_PORT"
"$ESF_D" -regtest -datadir="$WORKDIR/node" -daemon -server=1 \
  -rpcuser=esf -rpcpassword=esfpass -rpcport="$RPC_PORT" -listen=0 >/dev/null
wait_for "1175 node" 60 "${CLI[@]}" getblockchaininfo

"${CLI[@]}" createwallet esfwallet >/dev/null
PAYOUT=$("${CLI[@]}" -rpcwallet=esfwallet getnewaddress)
echo "── generating past AuxPoW activation (height ${AUXPOW_ACTIVATION_HEIGHT})"
"${CLI[@]}" -rpcwallet=esfwallet generatetoaddress $((AUXPOW_ACTIVATION_HEIGHT + 5)) "$PAYOUT" >/dev/null
echo "   aux chain at height $("${CLI[@]}" getblockcount), payout ${PAYOUT}"

export MM_REGTEST_RPC="http://127.0.0.1:${RPC_PORT}"
export MM_REGTEST_USER=esf
export MM_REGTEST_PASS=esfpass
export MM_REGTEST_PAYOUT="$PAYOUT"

run_must_pass TestMergeMineMultiTxParent_Live ./internal/mining/ -run TestMergeMineMultiTxParent_Live
echo "✓ merge-mining end-to-end proof passed (aux chain now at $("${CLI[@]}" getblockcount))"

#!/bin/sh
set -e

# Advertise the address our peers report seeing us on.
#
# Inside a container the node's only interface holds an RFC1918 address. That is not
# routable, so it is never recorded as a local address, so the node has nothing to
# announce -- and a node that announces no address is one no peer ever learns how to
# dial. Inbound connections then stay at zero no matter how the router is forwarded,
# which looks exactly like a broken port forward and cannot be fixed by the user.
#
# getpeerinfo.addrlocal carries the same fact one hop out: the address a peer observed
# our connection arriving from. Learn it while running, persist it, and advertise it
# from the next start.
# shellcheck source=docker/node1175/peeraddr.sh
. /peeraddr.sh

DATADIR="/data/.elevenseventyfive"
IPFILE="$DATADIR/external-ip"
CLI="/usr/local/bin/elevenseventyfive-cli"
P2P_PORT="25360"

if [ -z "${EXTERNAL_IP:-}" ]; then
    EXTERNAL_IP="$(saved_external_ip "$IPFILE")"
fi
if [ -n "${EXTERNAL_IP:-}" ]; then
    # Set by hand, EXTERNAL_IP may be IPv6, which the node takes only in brackets before a port.
    case "$EXTERNAL_IP" in
        \[*) ;;
        *:*) EXTERNAL_IP="[$EXTERNAL_IP]" ;;
    esac
    set -- "$@" "-externalip=${EXTERNAL_IP}:${P2P_PORT}"
    echo "[entrypoint] advertising ${EXTERNAL_IP}:${P2P_PORT} (1175)" >&2
else
    echo "[entrypoint] no external address known yet (1175); learning from peers" >&2
fi

# Peer-supplied data, so no single peer decides it: take the value at least two OUTBOUND peers
# (ones this node chose) agree on, and only a public IPv4 address: one the outside world could dial,
# and one the node is sure to accept at its next start.
learn_external_ip() {
    while true; do
        sleep 300
        peers="$($CLI -datadir="$DATADIR" -rpcuser="${RPC_USER:-}" \
                 -rpcpassword="${RPC_PASSWORD:-}" getpeerinfo 2>/dev/null)" || continue
        [ -n "$peers" ] || continue
        best="$(printf '%s' "$peers" | outbound_addrlocals | sort | uniq -c | sort -rn | head -1)"
        count="$(printf '%s' "$best" | awk '{print $1}')"
        ip="$(printf '%s' "$best" | awk '{print $2}')"
        [ -n "$ip" ] || continue
        [ "${count:-0}" -ge 2 ] || continue
        public_ipv4 "$ip" || continue
        if [ "$(cat "$IPFILE" 2>/dev/null || true)" != "$ip" ]; then
            printf '%s\n' "$ip" > "$IPFILE" 2>/dev/null \
                && echo "[entrypoint] learned external address $ip ($count peers agree);" \
                        "advertised from next restart" >&2
        fi
    done
}
learn_external_ip &

# shellcheck source=docker/node1175/damaged-chain.sh
. /damaged-chain.sh
if rebuild_once "$DATADIR" "1175 node" node1175; then
    set -- "$@" -reindex
fi

exec /usr/local/bin/elevenseventyfived "$@"

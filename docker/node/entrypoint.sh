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
DATADIR="/data/.bch2"
IPFILE="$DATADIR/external-ip"
CLI="/usr/local/bin/bitcoincashII-cli"
P2P_PORT="8339"

if [ -z "${EXTERNAL_IP:-}" ] && [ -r "$IPFILE" ]; then
    EXTERNAL_IP="$(tr -d '[:space:]' < "$IPFILE" 2>/dev/null || true)"
fi
if [ -n "${EXTERNAL_IP:-}" ]; then
    set -- "$@" "-externalip=${EXTERNAL_IP}:${P2P_PORT}"
    echo "[entrypoint] advertising ${EXTERNAL_IP}:${P2P_PORT} (BCH2)" >&2
else
    echo "[entrypoint] no external address known yet (BCH2); learning from peers" >&2
fi

# Peer-supplied data, so no single peer decides it: take the value at least two peers
# agree on, and never accept one the outside world could not dial anyway.
learn_external_ip() {
    while true; do
        sleep 300
        peers="$($CLI -datadir="$DATADIR" -rpcuser="${RPC_USER:-}" \
                 -rpcpassword="${RPC_PASSWORD:-}" getpeerinfo 2>/dev/null)" || continue
        [ -n "$peers" ] || continue
        best="$(printf '%s' "$peers" \
            | grep -o '"addrlocal"[[:space:]]*:[[:space:]]*"[^"]*"' \
            | sed 's/.*"\([^"]*\)"$/\1/' \
            | sed 's/:[0-9]*$//; s/^\[//; s/\]$//' \
            | sort | uniq -c | sort -rn | head -1)"
        count="$(printf '%s' "$best" | awk '{print $1}')"
        ip="$(printf '%s' "$best" | awk '{print $2}')"
        [ -n "$ip" ] || continue
        [ "${count:-0}" -ge 2 ] || continue
        case "$ip" in
            10.*|127.*|0.*|192.168.*|169.254.*) continue ;;
            172.1[6-9].*|172.2[0-9].*|172.3[01].*) continue ;;
            100.6[4-9].*|100.[7-9][0-9].*|100.1[01][0-9].*|100.12[0-7].*) continue ;;
        esac
        if [ "$(cat "$IPFILE" 2>/dev/null || true)" != "$ip" ]; then
            printf '%s\n' "$ip" > "$IPFILE" 2>/dev/null \
                && echo "[entrypoint] learned external address $ip ($count peers agree);" \
                        "advertised from next restart" >&2
        fi
    done
}
learn_external_ip &

exec /usr/local/bin/bitcoincashIId "$@"

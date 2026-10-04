#!/bin/sh
set -e

DATADIR="/data/.bch2"

# The node announces its own address: with -discover (the default when listening) it
# advertises the address its peers see it on, and follows that address when the ISP changes
# it. Earlier releases pinned a learned address with -externalip, which switches discovery
# off and keeps announcing the old address after a change until the next restart. Drop the
# address they saved.
rm -f "$DATADIR/external-ip"

# shellcheck source=docker/node/damaged-chain.sh
. /damaged-chain.sh
if rebuild_once "$DATADIR" "BCH2 node" node; then
    set -- "$@" -reindex
fi

exec /usr/local/bin/bitcoincashIId "$@"

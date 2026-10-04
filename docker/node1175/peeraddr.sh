# shellcheck shell=sh
# outbound_addrlocals reads getpeerinfo's JSON on stdin and prints, one per line, the address each
# OUTBOUND peer saw this node's connection arrive from (addrlocal, without its port). Inbound peers
# are left out: anyone can connect in and claim anything, so two inbound connections could otherwise
# choose the address this node advertises.
#
# Only the "inbound" key decides: each peer also carries "connection_type": "inbound", and matching
# that line too set every inbound peer back to outbound (testdata/1175-v29.1.0-getpeerinfo.json is
# real output of the node this image runs).
outbound_addrlocals() {
    awk '
        /^  [{]/ { a = ""; inb = 0 }
        /"addrlocal"[ \t]*:/ { s = $0; sub(/.*"addrlocal"[ \t]*:[ \t]*"/, "", s); sub(/".*/, "", s); a = s }
        /"inbound"[ \t]*:/ { inb = ($0 ~ /:[ \t]*true/) }
        /^  [}]/ { if (a != "" && !inb) print a; a = ""; inb = 0 }
    ' | sed 's/:[0-9]*$//; s/^\[//; s/\]$//'
}

# public_ipv4 reports whether $1 is an IPv4 address other machines on the internet could dial: four
# decimal numbers 0-255 without leading zeros, and not a private, loopback, link-local, shared,
# documentation, benchmark, multicast or reserved address. The node is given it as -externalip, and
# a value it cannot parse (an IPv6 address without brackets, say) stops it from starting at all.
public_ipv4() {
    printf '%s\n' "$1" | awk -F. '
        NF != 4 { bad = 1 }
        { for (i = 1; i <= NF; i++) if ($i !~ /^[0-9]+$/ || length($i) > 3 || ($i ~ /^0./) || $i + 0 > 255) bad = 1 }
        $1 == 0 || $1 == 10 || $1 == 127 || $1 >= 224 { bad = 1 }
        $1 == 169 && $2 == 254 { bad = 1 }
        $1 == 172 && $2 >= 16 && $2 <= 31 { bad = 1 }
        $1 == 192 && $2 == 168 { bad = 1 }
        $1 == 192 && $2 == 0 && ($3 == 0 || $3 == 2) { bad = 1 }
        $1 == 100 && $2 >= 64 && $2 <= 127 { bad = 1 }
        $1 == 198 && ($2 == 18 || $2 == 19) { bad = 1 }
        $1 == 198 && $2 == 51 && $3 == 100 { bad = 1 }
        $1 == 203 && $2 == 0 && $3 == 113 { bad = 1 }
        END { exit (NR == 1 && !bad) ? 0 : 1 }
    '
}

# saved_external_ip prints the address saved in file $1 if it is one public_ipv4 accepts. Anything
# else -- an IPv6 address saved before 1.0.13, a partial write -- is deleted rather than passed to the
# node, which would refuse to start on it, start after start, until someone deleted it by hand.
saved_external_ip() {
    [ -r "$1" ] || return 0
    ip="$(tr -d '[:space:]' < "$1" 2>/dev/null || true)"
    if public_ipv4 "$ip"; then
        printf '%s\n' "$ip"
    else
        rm -f "$1"
        echo "[entrypoint] dropped the saved address '$ip' (not a public IPv4 address)" >&2
    fi
}

# outbound_addrlocals reads getpeerinfo's JSON on stdin and prints, one per line, the address each
# OUTBOUND peer saw this node's connection arrive from (addrlocal, without its port). Inbound peers
# are left out: anyone can connect in and claim anything, so two inbound connections could otherwise
# choose the address this node advertises.
outbound_addrlocals() {
    awk '
        /^  [{]/ { a = ""; inb = 0 }
        /"addrlocal"/ { s = $0; sub(/.*"addrlocal"[ \t]*:[ \t]*"/, "", s); sub(/".*/, "", s); a = s }
        /"inbound"/ { inb = ($0 ~ /true/) }
        /^  [}]/ { if (a != "" && !inb) print a; a = ""; inb = 0 }
    ' | sed 's/:[0-9]*$//; s/^\[//; s/\]$//'
}

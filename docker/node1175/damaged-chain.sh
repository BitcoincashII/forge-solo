# shellcheck shell=sh
# Sourced by the entrypoints of both node images. docker/node and docker/node1175 carry the same
# copy, since each image is built from its own folder.
#
# Chain data damaged by a power cut, a full disk or a crash mid-write stops the node at every start
# ("Corrupted block database detected", "Please restart with -reindex"), and Docker starts it again,
# for ever. Started once with -reindex, the node rebuilds it from the blocks on disk. Forge Solo for
# Windows does the same.

# said_damaged LOG FROM succeeds if the node's last run wrote to LOG that its chain data is damaged.
# FROM is LOG's length when that run started. A LOG shorter than that was trimmed by the node as it
# started (it keeps the last 10 MB); the run's lines then follow the empty lines the node writes
# each time it opens LOG.
said_damaged() {
    [ -f "$1" ] || return 1
    from=$2
    [ "$(($(wc -c < "$1")))" -ge "$from" ] || from=0
    tail -c +"$((from + 1))" "$1" | LC_ALL=C awk '
        /^$/ { if (++empty == 4) damaged = 0; next }
        { empty = 0 }
        /Corrupted block database detected|restart with -reindex/ { damaged = 1 }
        END { exit !damaged }'
}

# rebuild_once DATADIR NAME FOLDER succeeds when the node should start with -reindex: its last run
# said its chain data is damaged, and no rebuild has been tried in this container. A rebuild that
# did not help is not tried again until the app is restarted (a new container); the folders to
# delete are logged instead. NAME is the node's name in the log, FOLDER its folder in the app's data.
rebuild_once() {
    log=$1/debug.log
    start=$1/debug-log-start
    tried=$1/reindex-tried
    from=$(cat "$start" 2>/dev/null) || from=0
    case $from in '' | *[!0-9]*) from=0 ;; esac
    now=0
    [ -f "$log" ] && now=$(($(wc -c < "$log")))
    echo "$now" > "$start" 2>/dev/null || true
    if ! said_damaged "$log" "$from"; then
        rm -f "$tried"
        return 1
    fi
    if [ "$(cat "$tried" 2>/dev/null)" = "$(uname -n)" ]; then
        echo "[entrypoint] the $2 still finds its chain data damaged after rebuilding it: stop Forge Solo," \
             "delete the blocks and chainstate folders in app-data/bch2-apps-forge-solo/$3, then start it again" >&2
        return 1
    fi
    uname -n > "$tried" 2>/dev/null || return 1
    echo "[entrypoint] the $2 says its chain data is damaged: starting it once with -reindex, which" \
         "rebuilds it from the blocks on disk (this takes a few minutes)" >&2
}

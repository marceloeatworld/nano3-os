#!/bin/sh
# Run nano3d for a bounded session, then restore the stock btcminer.
# Stops btcminer, runs /tmp/nano3d with the given arguments until it exits (or the
# watchdog fires after MAX seconds), then restarts btcminer. Run as root on the miner.
# Usage: sh nano3d-session.sh MAX nano3d-args...   (give nano3d a -seconds below MAX)
MAX=${1:?usage: nano3d-session.sh MAX nano3d-args...}
shift

restart_btcminer() {
    cd /mnt/heater/app && setsid ./btcminer </dev/null >/dev/console 2>&1 &
    sleep 3
    echo "btcminer restarted = $(pidof btcminer)"
}

# pidof can return several PIDs; re-query by name so every instance is handled.
OLD=$(pidof btcminer)
[ -n "$OLD" ] && kill $OLD
i=0
while pidof btcminer >/dev/null && [ $i -lt 10 ]; do sleep 1; i=$((i+1)); done
if pidof btcminer >/dev/null; then
    echo "btcminer $(pidof btcminer) did not stop in 10s; forcing"
    kill -9 $(pidof btcminer); sleep 1
fi
if pidof btcminer >/dev/null; then
    echo "btcminer $(pidof btcminer) still alive; aborting, UART still owned"
    exit 1
fi
echo "btcminer stopped (was $OLD)"

# Same cleanup as bridge-session.sh: clear the traps first (BusyBox sh runs the EXIT
# trap again after "exit" in a signal trap, which would start btcminer twice).
ND=""; WD=""
cleanup() {
    trap - INT TERM HUP EXIT
    kill "$WD" 2>/dev/null; kill "$ND" 2>/dev/null
    restart_btcminer
    cat /tmp/nano3d.log 2>/dev/null
    exit
}
trap cleanup INT TERM HUP EXIT

/tmp/nano3d "$@" </dev/null >/tmp/nano3d.log 2>&1 &
ND=$!
( sleep "$MAX"; kill "$ND" 2>/dev/null ) </dev/null >/dev/null 2>&1 &
WD=$!
wait "$ND"
echo "nano3d exit code $?"
# EXIT trap kills the watchdog, restarts btcminer, and prints the nano3d log.

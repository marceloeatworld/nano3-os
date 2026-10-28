#!/bin/sh
# Run the UART bridge for a bounded session, then restore btcminer.
# Stops btcminer, runs /tmp/uartbridge until the client disconnects (or the
# watchdog fires), then restarts btcminer. Run as root on the miner.
# Usage: sh bridge-session.sh [max_seconds]   (default 150)
MAX=${1:-150}

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

# From here on, always restart btcminer and never leave the bridge running,
# whether we exit normally, on the watchdog, or on a signal (HUP covers a
# dropped interactive SSH session). cleanup clears the traps first: in
# BusyBox sh, "exit" inside a signal trap runs the EXIT trap again, which
# would start a second btcminer on the same UART.
BR=""; WD=""
cleanup() {
    trap - INT TERM HUP EXIT
    kill "$WD" 2>/dev/null; kill "$BR" 2>/dev/null
    restart_btcminer
    cat /tmp/bridge.log 2>/dev/null
    exit
}
trap cleanup INT TERM HUP EXIT

/tmp/uartbridge 9113 /dev/ttyS1 >/tmp/bridge.log 2>&1 &
BR=$!
sleep 1
if ! kill -0 "$BR" 2>/dev/null; then
    echo "uartbridge failed to start (port busy or UART locked):"
    exit 1   # trap restores btcminer and prints the bridge log
fi

# No inherited stdout/stderr: cleanup kills this subshell but not its sleep, and
# a sleep holding the SSH channel would keep the caller's ssh open until MAX.
( sleep "$MAX"; kill "$BR" 2>/dev/null ) </dev/null >/dev/null 2>&1 &
WD=$!
wait "$BR"
# EXIT trap kills the watchdog, restarts btcminer, and prints the bridge log.

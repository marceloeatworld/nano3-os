#!/bin/sh
# Replace nano3d in the live slot by hand (the dashboard's Update button does the same
# from a GitHub release): the start script's loop restarts it within 10 s.
# Usage: update-nano3d.sh [signal]   (default TERM; KILL skips the exiting nano3d's clean-up)
set -e
[ -s /tmp/nano3d.new ]
# No pid is fine: nano3d may be between two starts of the loop, or unable to start at all.
PIDS=$(pidof nano3d) || true
if [ -n "$PIDS" ]; then kill -${1:-TERM} $PIDS; fi
i=0
while pidof nano3d >/dev/null && [ $i -lt 15 ]; do sleep 1; i=$((i + 1)); done
# A TERM can wait behind the Wi-Fi bring-up (up to 2 minutes): force it, and never delete
# the binary under a running nano3d (its blocks stay allocated and the copy runs out of space).
if pidof nano3d >/dev/null; then kill -KILL $(pidof nano3d); sleep 2; fi
if pidof nano3d >/dev/null; then echo "nano3d still running, nothing replaced" >&2; exit 1; fi
rm -f /mnt/heater/app/nano3d
cp /tmp/nano3d.new /mnt/heater/app/nano3d
chmod 755 /mnt/heater/app/nano3d
sync
rm -f /tmp/nano3d.new /tmp/nano3d.new.sha256
logger -t nano3-os "nano3d updated: $(sha256sum /mnt/heater/app/nano3d | cut -c1-16)"

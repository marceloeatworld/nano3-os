#!/bin/sh
# Restart btcminer under strace to capture the K230 <-> K210 bring-up (DETECT/SYNC, termios, K210 reset GPIO).
# Run as root on the miner, with /tmp/strace in place. Mining stops for the restart; btcminer keeps
# running afterwards (strace kills only its own `sh` child and detaches from btcminer).
# Usage: sh bringup-capture.sh [seconds]   (default 90; output /tmp/cap3.txt)
SECS=${1:-90}
# Check before stopping btcminer: nothing below restarts it if strace cannot run.
[ -x /tmp/strace ] || { echo "/tmp/strace missing or not executable; btcminer left running"; exit 1; }
OLD=$(pidof btcminer)
[ -n "$OLD" ] && kill $OLD
i=0
while pidof btcminer >/dev/null && [ $i -lt 10 ]; do sleep 1; i=$((i+1)); done
# A second btcminer on the same UART would garble both streams.
if pidof btcminer >/dev/null; then echo "btcminer $(pidof btcminer) still alive after 10s; aborting"; exit 1; fi
echo "old btcminer ($OLD) stopped after ${i}s"
cd /mnt/heater/app || exit 1
# Lessons from the first run (2026-10-27 11:13):
# - With "-o FILE PROG" strace defaults to -I3 (fatal signals always blocked), so kill -INT did nothing. -I1 fixes it.
# - That was not the whole story (2026-10-27 20:29): sh starts background jobs with SIGINT and SIGQUIT
#   ignored (SigIgn 0x6 on the strace process), so kill -INT can never stop it. Use SIGTERM.
# - --seccomp-bpf implies --kill-on-exit: any strace exit kills btcminer. Do not use it here.
# setsid: strace and btcminer get their own session, so a Ctrl-C or SSH hangup aimed at
# this script never reaches them. (BusyBox setsid execs in place for a background job,
# so $! is still strace's PID.)
# strace's own output goes to a file: the "sh -c" wrapper inherits it and stays alive as
# btcminer's parent, so the caller's stdout here would keep its ssh open until btcminer exits.
setsid /tmp/strace -q -f -I1 -tt -xx -s 4096 -e trace=openat,ioctl,read,write -e signal=none \
  -P /dev/ttyS1 -P /sys/class/gpio/gpio61/value -P /sys/class/gpio/gpio61/direction -P /sys/class/gpio/export \
  -o /tmp/cap3.txt sh -c './btcminer </dev/null >/dev/console 2>&1 & wait' </dev/null >/tmp/strace.err 2>&1 &
S=$!
# On a signal, end the capture early instead of leaving strace attached and
# filling the RAM-backed /tmp. Background sleep + wait lets the trap run at once.
trap 'kill -TERM $S 2>/dev/null; kill $SL 2>/dev/null' INT TERM HUP
sleep "$SECS" &
SL=$!
wait $SL
kill -TERM $S 2>/dev/null
wait $S
sleep 1
NEW=$(pidof btcminer)
echo "btcminer now: ${NEW:-NOT RUNNING}"
[ -n "$NEW" ] && grep TracerPid /proc/$NEW/status
wc -l /tmp/cap3.txt

#!/bin/sh
# nano3-os start script. Installed as heater/app/btcminer in app slot A (mtd10): the
# stock /etc/init.d/rcS runs "cd /mnt/heater/app && ./btcminer &" as root.
#
# The board falls back to the stock app in slot B only when nano3d itself is at fault:
# the Internet answers but nano3d has not reached its main loop (MM up, pool
# connected: /api/status answers) within 15 minutes, or there is no Internet at all
# for 60 minutes (the Wi-Fi bring-up may be what is broken). The next boot is never
# pointed at slot B in advance, so a power cut during the first minutes of a boot
# keeps nano3-os. The guard in the root filesystem (/etc/init.d/S51nano3os, from
# tools/nano3-os-guard.sh) brings the board back to slot A afterwards.
#
# nano3d's log goes to syslog (/tmp/messages, rotated by BusyBox syslogd). Boots and
# fallbacks are appended to /data/nano3-os/boots.log.
D=/data/nano3-os
cd /mnt/heater/app || exit 1
# note appends a line to boots.log, kept at 200 lines; nothing is written when /data
# is not mounted (the rootfs must not grow a stray directory).
note() {
    mountpoint -q /data || return
    mkdir -p $D
    [ "$(wc -l < $D/boots.log 2>/dev/null || echo 0)" -gt 200 ] && tail -n 100 $D/boots.log > $D/boots.log.tmp && mv $D/boots.log.tmp $D/boots.log
    echo "$(date '+%Y-%m-%d %H:%M:%S') $1" >> $D/boots.log
}

online() { ping -c 1 -W 3 1.1.1.1 >/dev/null 2>&1 || ping -c 1 -W 3 8.8.8.8 >/dev/null 2>&1; }
fallback() {
    # The guard reads the marker to come back to slot A; without it, slot B would
    # look chosen by hand, so no marker means no fallback (SSH stays available).
    if ! mountpoint -q /data || ! mkdir -p $D || ! echo "$1" > $D/fallback; then
        logger -t nano3-os "cannot write $D/fallback: staying on slot A ($1)"
        return
    fi
    note "fallback to slot B: $1"
    logger -t nano3-os "falling back to the stock app (slot B): $1"
    sync
    fw_setenv app_partition B && reboot
    logger -t nano3-os "fw_setenv failed: staying on slot A"
}

(
    # No RTC on the board: the clock reads 1970 until ntpd has synced, about 20 s after
    # the Wi-Fi is up. Log the boot once the date is real, or after 2 minutes anyway.
    i=0
    while [ "$(date +%Y)" -lt 2020 ] && [ $i -lt 120 ]; do
        sleep 1
        i=$((i + 1))
    done
    note "boot on slot A ($(cut -d. -f1 /proc/uptime) s after power-up)"
    idle=0
    down=0
    while :; do
        sleep 60
        # /api/status answers only once nano3d runs its main loop (503 before that).
        if wget -q -O - http://127.0.0.1/api/status 2>/dev/null | grep -q '"pool"'; then
            rm -f $D/fallback 2>/dev/null
            [ "$(fw_printenv -n app_partition 2>/dev/null)" = A ] || fw_setenv app_partition A
            logger -t nano3-os "nano3d is mining: the next boot stays on slot A"
            exit 0
        fi
        if online; then
            idle=$((idle + 1))
            down=0
            [ $idle -ge 15 ] && fallback "nano3d not mining after 15 minutes online"
        else
            down=$((down + 1))
            [ $down -ge 60 ] && fallback "no Internet for 60 minutes"
        fi
    done
) </dev/null >/dev/null 2>&1 &

# swap_in installs a binary that the dashboard's update (nano3d exit code 5), or an
# operator, left in /tmp with its checksum next to it. Slot A cannot hold two binaries,
# so this runs only once nano3d has exited, and the old file goes before the new one
# comes. A bad checksum discards the staged file and keeps the running binary.
swap_in() {
    [ -s /tmp/nano3d.new ] && [ -s /tmp/nano3d.new.sha256 ] || return 0
    if (cd /tmp && sha256sum -c nano3d.new.sha256 >/dev/null 2>&1); then
        if rm -f nano3d && cp /tmp/nano3d.new nano3d && chmod 755 nano3d && sync; then
            logger -t nano3-os "nano3d updated: $(sha256sum nano3d | cut -c1-16)"
        else
            logger -t nano3-os "nano3d update failed: copy error, slot A may be full"
        fi
    else
        logger -t nano3-os "staged nano3d rejected: checksum mismatch"
    fi
    rm -f /tmp/nano3d.new /tmp/nano3d.new.sha256
}

while true; do
    ./nano3d -wifi </dev/null 2>&1 | logger -t nano3d
    swap_in
    sleep 10
done

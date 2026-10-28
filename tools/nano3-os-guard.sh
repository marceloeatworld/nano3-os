#!/bin/sh
# nano3-os guard. Installed in the root filesystem as /etc/init.d/S51nano3os, so the
# stock rcS runs it ("start") on every boot whichever app slot is active, right after
# sshd and before the app. It keeps the board reachable and brings it back to nano3-os:
#  - sshd is started again if it dies (host keys generated first if missing);
#  - ntpd is restarted when the clock is still at 1970 with the network up;
#  - Wi-Fi is brought up from the stock config when wlan0 has had no address for
#    3 minutes, whatever the app did or failed to do;
#  - on the stock slot B after a fallback (/data/nano3-os/fallback, written by
#    tools/nano3-os-start.sh), the next boot is pointed back at slot A once the
#    Internet has answered for 10 minutes (a network fallback) or after an hour
#    (a nano3d fault: a wrong retry costs 15 minutes of stock mining), and the board
#    reboots.
# A slot B chosen by hand (no fallback file) is left alone.
[ "$1" = start ] || exit 0
D=/data/nano3-os
T=nano3-os-guard

# wifi_up connects wlan0 as btcminer's netdev_connect does, from [netcfg] in
# systemcfg.ini: auth 1..3 is an open network, anything else WPA-PSK.
wifi_up() {
    # [netcfg] only, keys and values trimmed, as nano3d's own INI reader does.
    net=$(sed -n '/^[[:space:]]*\[netcfg\]/,/^[[:space:]]*\[/p' /data/usrcon/systemcfg.ini | tr -d '\r')
    key() { printf '%s\n' "$net" | sed -n "s/^[[:space:]]*$1[[:space:]]*=[[:space:]]*//p" | head -1 | sed 's/[[:space:]]*$//'; }
    ssid=$(key ssid)
    psk=$(key password)
    auth=$(key auth)
    [ -n "$ssid" ] || return
    case "${auth:-7}" in
        1 | 2 | 3) km="key_mgmt=NONE" ;;
        *) km="psk=\"$psk\"" ;;
    esac
    printf 'ctrl_interface=/var/run/wpa_supplicant\nap_scan=1\nnetwork={\n\tssid="%s"\n\t%s\n}\n' "$ssid" "$km" > /tmp/$T.conf
    chmod 600 /tmp/$T.conf
    killall udhcpc wpa_supplicant 2>/dev/null
    ifconfig wlan0 up
    wpa_supplicant -B -D nl80211 -i wlan0 -c /tmp/$T.conf
    udhcpc -i wlan0 -b
    logger -t $T "Wi-Fi brought up from the stock config"
}

online() { ping -c 1 -W 3 1.1.1.1 >/dev/null 2>&1 || ping -c 1 -W 3 8.8.8.8 >/dev/null 2>&1; }
# linkup: an address and an associated supplicant (wpa_cli fails at once when the
# supplicant is dead; a live but disconnected one reports another wpa_state).
linkup() {
    ip -4 addr show wlan0 2>/dev/null | grep -q inet && wpa_cli -i wlan0 status 2>/dev/null | grep -q wpa_state=COMPLETED
}

(
    noip=0
    up=0
    stale=0
    while :; do
        sleep 60
        if ! pidof sshd >/dev/null; then
            [ -s /etc/ssh/ssh_host_ed25519_key ] || ssh-keygen -A
            /usr/sbin/sshd && logger -t $T "sshd started again"
        fi
        if linkup; then
            noip=0
        else
            noip=$((noip + 1))
        fi
        # With a supplicant alive the app is still trying (it retries every 2 minutes
        # or so): give it 10 minutes before taking over, 3 when nothing runs at all.
        limit=3
        pidof wpa_supplicant >/dev/null && limit=10
        if [ $noip -ge $limit ] && mountpoint -q /data; then
            noip=0
            # The same lock nano3d takes for its own set-up (internal/board/wifi.go):
            # never two set-ups at once. Re-check once it is ours.
            exec 9>/tmp/nano3-os-wifi.lock
            if flock -n 9; then # nano3d holds it: its own set-up is under way, come back later
                linkup || wifi_up
                flock -u 9
            fi
            exec 9>&-
        fi
        # No RTC: a clock still at 1970 after 5 minutes online means ntpd is stuck
        # (a fresh sync takes well under a minute).
        if [ "$(date +%Y)" -lt 2020 ] && [ $noip -eq 0 ] && online; then
            stale=$((stale + 1))
        else
            stale=0
        fi
        if [ $stale -ge 5 ]; then
            stale=0
            /etc/init.d/S49ntp restart >/dev/null 2>&1 && logger -t $T "ntpd restarted: the clock was not set"
        fi
        # On slot B (mtd11 on /mnt) after a fallback: point the next boot back at A.
        if [ "$(cat /sys/class/ubi/ubi1/mtd_num 2>/dev/null)" = 11 ] && mountpoint -q /data && [ -s $D/fallback ]; then
            if online; then up=$((up + 1)); else up=0; fi
            back=
            case "$(cat $D/fallback)" in
                "no Internet"*) [ $up -ge 10 ] && back=y ;;
                *) [ $up -ge 10 ] && [ "$(cut -d. -f1 /proc/uptime)" -ge 3600 ] && back=y ;;
            esac
            if [ -n "$back" ]; then
                # The marker goes only once the next boot really points at A: without
                # it, slot B would count as chosen by hand.
                if fw_setenv app_partition A && [ "$(fw_printenv -n app_partition 2>/dev/null)" = A ]; then
                    echo "$(date '+%Y-%m-%d %H:%M:%S') back to slot A after: $(cat $D/fallback)" >> $D/boots.log
                    logger -t $T "back to nano3-os (slot A)"
                    rm -f $D/fallback
                    sync
                    reboot
                else
                    logger -t $T "fw_setenv failed: staying on slot B"
                    up=0
                fi
            fi
        fi
    done
) </dev/null >/dev/null 2>&1 &

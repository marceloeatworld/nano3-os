# Avalon Nano 3 - flashing custom firmware

How to put our own build on this unit, and what not to use. Facts below come from the flash dump, `rootfs/`, and a read of the TNA web flasher on 2026-10-27.
Route 1 was run on this unit on 2026-10-28: nano3-os now boots from app slot A. Route 2 is untested on the hardware.

## Summary

| Route | Status for this Nano 3 |
|---|---|
| TNA web flasher (lnbits.molonlabe.holdings/tnaflasher) | Do not use. No Nano 3 build, cannot flash our own image, hardcodes the Nano 3s 256 MiB layout |
| SSH, app slot A (`mtd10`) | Recommended for a custom miner app, used on this unit since 2026-10-28. Stock slot B stays as rollback |
| USB BOOT mode + KBURN (`k230-flash`) | Possible for full images, but the BOOT button on the Nano 3 is unverified |

## Before you start

1. **SSH as root on the miner.** The stock firmware does not start `sshd`. On this unit SSH and the
   `admin` / `admin` login came from the public "nanojb" jailbreak, which uses the web UI timezone
   injection described in `SECURITY.md`. Any other way to a root shell works as well. `admin` has full
   `sudo`; the steps below assume `sudo -s`.
2. **Change the `admin` password** (`passwd admin` as root): the jailbreak's password is public and the
   web UI gives root to anyone on the LAN anyway (`SECURITY.md`).
3. **Back up slot A** before step 2 writes it. On the miner as root (`mtd10` is 16 MiB, `/tmp` has room):
   `cat /dev/mtd10 > /tmp/mtd10_app_ubi_a.bin && chmod 644 /tmp/mtd10_app_ubi_a.bin && sha256sum /tmp/mtd10_app_ubi_a.bin`.
   From the PC: `scp admin@<miner>:/tmp/mtd10_app_ubi_a.bin .` and compare its `sha256sum` with the
   miner's. Slot B (the live stock app) is never written.
4. **Get the `nano3d` binary.** Either download `nano3d-riscv64` and `nano3d-riscv64.sha256` from the
   latest GitHub release (built by GitHub Actions from the tagged source, `.github/workflows/build.yml`)
   and check it with `sha256sum -c nano3d-riscv64.sha256`, or build it with Go 1.26:
   `GOOS=linux GOARCH=riscv64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/nano3d-riscv64 ./cmd/nano3d`.
5. **Copy the files to the miner's `/tmp`** (tmpfs, wiped at reboot). From the PC:
   `scp nano3d-riscv64 admin@<miner>:/tmp/nano3d` (`bin/nano3d-riscv64` for a local build) and
   `scp tools/nano3d-session.sh tools/nano3-os-start.sh tools/nano3-os-guard.sh admin@<miner>:/tmp/`,
   then on the miner `chmod 755 /tmp/nano3d` (a downloaded release has no execute bit).
6. **Wi-Fi.** The `-wifi` bring-up reads the SSID and key from the stock `systemcfg.ini`, so the miner
   must have been joined to the Wi-Fi once with the stock app, with DHCP (a static IP makes nano3d exit)
   and an open or WPA-PSK network (no EAP).

## Boot slots (from the dump)

Active u-boot env copy is `mtd5` (flags 6; `mtd4` has flags 5). Both copies agree:

| Variable | Value | Used by |
|---|---|---|
| `app_partition` | `B` | `/etc/init.d/rcS`: `A` attaches `mtd10`, anything else `mtd11`; mounted as `/dev/ubi1_0` on `/mnt` |
| `rootfs_partition` | `B` | u-boot |
| `linux_partition` | `B` | u-boot |
| `uboot_partition`, `uboot_spl_partition` | `B` | u-boot |

Every slot runs from B. Slot A is inactive.

| Partition | UBI volume 0 | LEBs | Notes |
|---|---|---|---|
| `mtd10` app_ubi_a | `ubi_app_part_a` | 116 | holds data (20 PEBs used), not compared with B; backup in `flash/mtd10_app_ubi_a.bin` |
| `mtd11` app_ubi_b | `ubi_app_part_a` | 116 | live app (same volume name in both slots) |

`rcS` runs `cd /mnt/heater/app && ./btcminer &` as root, after `sshd` is already up. The custom entry point must therefore be a file named `btcminer` in `heater/app/`.

## Constraint: the app brings up the network

`btcminer` starts Wi-Fi itself (strings in the binary):

```
wpa_supplicant -B -D nl80211 -i wlan0 -c ../confiles/wpa_supplicant.conf
udhcpc -i wlan0 -b
hostapd -B ../confiles/hostapd.conf
```

`S40network` does not start Wi-Fi, and the serial getty on `ttyS0` is commented out in `/etc/inittab`.
So an app slot that does not start Wi-Fi leaves the unit with no network and no console: no SSH, no rollback over the LAN.
Any custom app must start `wpa_supplicant` and `udhcpc` itself, or run the stock `btcminer` network setup.

## Route 1: SSH, app slot A (recommended)

Run on the miner as root (`sudo -s`). Builds are copied to `/tmp` (tmpfs) first.

### Step 1: test from RAM, no flash write

`tools/nano3d-session.sh` stops `btcminer`, runs the build from `/tmp`, and restarts stock `btcminer` afterwards.
Only go to step 2 once the build mines and keeps Wi-Fi up (`-wifi`).

Start the session detached, or it dies with the SSH terminal: `sudo sh -c 'setsid sh /tmp/nano3d-session.sh 200 -wifi -seconds 150 </dev/null >/tmp/session.out 2>&1 & sleep 2'`.
With `-wifi` the SSH connection stalls for a few seconds while nano3d restarts the Wi-Fi, then comes back on the same address.
Without the `sleep 2`, the hang-up from the closing terminal can reach the job before `setsid` runs (seen live).

### Step 2: write slot A

Start from a copy of the live stock app so `confiles/` (Wi-Fi config) and `www/` are present, then add the build.
Back up `mtd10` first (`cat /dev/mtd10` over SSH, compare the SHA-256 on both sides).

```
fw_printenv app_partition                 # must print B before touching mtd10
ubiformat /dev/mtd10 -y                   # keeps erase counters
ubiattach /dev/ubi_ctrl -m 10 -d 3        # ubi0 rootfs, ubi1 app B, ubi2 data are taken
ubimkvol /dev/ubi3 -N ubi_app_part_a -m
mkdir -p /tmp/slota && mount -t ubifs /dev/ubi3_0 /tmp/slota
cp -a /mnt/. /tmp/slota/
mv /tmp/slota/heater/app/btcminer /tmp/slota/heater/app/btcminer.stock
cp /tmp/nano3d /tmp/slota/heater/app/nano3d
cp /tmp/nano3-os-start.sh /tmp/slota/heater/app/btcminer     # tools/nano3-os-start.sh
chmod 755 /tmp/slota/heater/app/nano3d /tmp/slota/heater/app/btcminer
sync && umount /tmp/slota && ubidetach -m 10
```

Remount once (`ubiattach` + `mount` as above) and check the files before step 3. A slot that fails to mount runs nothing, keeps no network, and boots that way again on every power cycle.

### Step 3: boot slot A

The start script `tools/nano3-os-start.sh` (installed as `heater/app/btcminer`) does this on every boot:
- It runs `nano3d -wifi` in a loop, 10 s apart, with the log in syslog (`/tmp/messages`), and
  appends the boot to `/data/nano3-os/boots.log`.
- Once `nano3d` runs its main loop (its `/api/status` answers), it makes sure the next boot stays on slot A.
- It falls back to the stock app in slot B only when nano3d itself is at fault: the Internet answers
  (ping to 1.1.1.1 or 8.8.8.8) but nano3d is not mining after 15 minutes, or there is no Internet at
  all for 60 minutes. It writes the reason to `/data/nano3-os/fallback` and reboots. The next boot is
  never pointed at slot B in advance, so a power cut during the first minutes of a boot keeps nano3-os.

The guard `tools/nano3-os-guard.sh`, installed in the root filesystem as `/etc/init.d/S51nano3os`, runs on
every boot whichever slot is active (the root filesystem is shared; sshd starts from the same place):
- it starts `sshd` again if it dies, generating host keys first if they are missing;
- it brings Wi-Fi up from `systemcfg.ini` when `wlan0` has had no address for 3 minutes, so SSH works
  even when the app fails before its own Wi-Fi bring-up;
- on slot B after a fallback, it points the next boot back at slot A once the Internet has answered for
  10 minutes (network fallback) or after an hour (nano3d fault), and reboots. A slot B chosen by hand
  (no fallback file) is left alone.

Install the guard: `cp /tmp/nano3-os-guard.sh /etc/init.d/S51nano3os && chmod 755 /etc/init.d/S51nano3os`
(as root; the file name must not end in `.sh`, or rcS sources it instead of running it).

`-wifi` makes nano3d bring up Wi-Fi as btcminer does (`wpa_cli add_network` with the SSID from `systemcfg.ini`), again on every restart.
nano3d reconnects to the pool by itself and goes back to the first pool once it answers again (probed every 5 minutes); it exits when the MM asks for a reset (exit 3), when a pool change is saved on the web page (exit 4), or when no pool answers for 2 minutes at start.
On exit it sets the fan to 100 %; the MM keeps its level, so the next start continues at full clock.

Then on the miner: `fw_setenv app_partition A && reboot`. Its web page answers about 20 s later.

### Step 4: permanent

Nothing to do: the next boot stays on slot A unless the start script falls back, and the guard returns
to slot A by itself. `fw_setenv app_partition A && reboot` brings nano3-os back by hand at any time;
`/data/nano3-os/boots.log` lists the boots, fallbacks and returns.

### Update a running install

Slot A has about 3.6 MB free, less than one binary, so the running `nano3d` is stopped and deleted before
the new one is copied. `tools/update-nano3d.sh` does that; the start script's loop restarts `nano3d` within
10 s. Install the script once on the miner, in the data partition so it survives a reboot. From the PC
`scp tools/update-nano3d.sh admin@<miner>:/tmp/`, then on the miner
`sudo mkdir -p /data/nano3-os && sudo cp /tmp/update-nano3d.sh /data/nano3-os/ && sudo chmod 755 /data/nano3-os/update-nano3d.sh`.

Then for each update, from the PC: `scp nano3d-riscv64 admin@<miner>:/tmp/nano3d.new` (`bin/nano3d-riscv64` for a
local build), and on the miner,
detached so it survives the SSH terminal:
`sudo sh -c 'setsid sh /data/nano3-os/update-nano3d.sh </dev/null >/tmp/update.out 2>&1 & sleep 2'`.
The web page answers again about 15 s later; `logger` writes the new binary's hash to syslog (`/tmp/messages`).

### Rollback

`fw_setenv app_partition B && reboot`. Stock app B was never written.

## Route 2: USB BOOT mode + KBURN

The K230 boot ROM enumerates as USB `29F1:0230` and speaks KBURN. Canaan's open-source host tool is `k230-flash` on PyPI (1.4.0, source github.com/kendryte/k230_flash_py).

Before using it:

1. Confirm the Nano 3 has a BOOT/recovery button and a data-capable USB port. The TNA steps (button next to the USB-C port, USB-A to USB-A cable) are written for the Nano 3s and Q.
2. Build images for this unit's layout: 128 MiB SPI-NAND, partitions in `PHASE1-RECON.md`. Nano 3s images assume 256 MiB.
3. Keep `flash/mtd*.bin` as the restore source. 11 of 13 partitions match the device byte for byte (see `README.md`).

## TNA web flasher: why not

Read on 2026-10-27 (page, `k230-flasher.js` v4.8.5, `kdimg-parser.js`, `/api/v1/devices`). Copies in `reference/tnaflasher/`.

- It only writes its own TNA-OS image, downloaded from its server with a one-time token (`/api/v1/firmware/<device>/<version>?token=`). There is no option to load a local `.kdimg`.
- Canaan builds offered: `v0.4.2-Q` and `v0.4.2-Nano3s` only (0 sats, 2% dev fee). No Nano 3.
- `k230-flasher.js` is written for the Nano 3s. When the image has no data partition, it erases a fixed region at `0x0c800000`, size `0x03800000`, which ends at 256 MiB. This Nano 3 has 128 MiB.
- Nano 3s firmware does not match this hashboard path: here the 10 x A3198S are reached through the K210 on `ttyS1` (`analysis/PROTOCOL.md`).
- The KBURN details in `k230-flasher.js` (BROM loader at `0x80360000`, full-range UBI erase, per-partition SHA-256) are a useful reference for route 2.

## Open questions

- Is the hardware watchdog armed when nothing opens `/dev/watchdog`? `btcminer` feeds it; a custom app may need to as well.
- Does the Nano 3 expose USB BOOT mode at all?

# Avalon Nano 3 reverse engineering - work journal

Chronological log of what was done, how, and what came out of it. Source material for a later writeup.
All times are local (2026-10-27). Device: owner's Avalon Nano 3 at <MINER_IP>.

## 10:13 - Access

- SSH as `admin` (password `admin`) works. Ghostty's `ssh-terminfo` feature caused a double password prompt (it opens a first connection to install terminfo, which fails on the BusyBox rootfs).
- For tooling, one OpenSSH ControlMaster connection is opened and reused: `ssh -fNM -S /run/user/<uid>/nano3.sock admin@<MINER_IP>`.
- `admin` is uid 1001, not root, but `sudo -l` shows `(ALL) ALL`.

## 10:17 - Phase 1: read-only recon

- Ran `recon/recon.sh` over SSH (`ssh ... 'sh -s' < recon.sh`): id, kernel, device tree model, cmdline, cpuinfo, MTD table, mounts, processes, sockets, devices, dmesg.
- Key results: Kendryte K230, 1 Linux hart, 128 MiB SPI-NAND with A/B partitions, Buildroot 2021.02, miner `/mnt/heater/app/btcminer` running as root, ports 22/80/4028/9090.
- Surprise vs the Nano 3s report: no inter-core channel is visible. `/dev/ttyS1` was the recently used device node.
- Details: `PHASE1-RECON.md`.

## 10:26 - Full dump

- Every file readable by `admin`: `tar` streamed over SSH into `rootfs.tar` (8 root-only files refused, listed in `tar-errors.txt`).
- All 13 MTD partitions: `sudo cat /dev/mtdN` streamed over SSH into `flash/`.
- Verified with SHA-256 on both sides: 11/13 identical; `mtd9` (live rootfs) and `mtd12` (data) changed during the dump, as expected for read-write mounts.

## 10:30 - Phase 2 step 1: first look at btcminer

- ELF64 RISC-V, dynamically linked against glibc, NOT stripped (3,726 function symbols), Canaan cgminer fork, source path `k230_sdk/app_heater/cgminer/driver-avalon12.c`.
- Function `avalon12_reset_k210` and strings "Cannot connect to k210", "k210 crc16 verfity ok" revealed a separate Kendryte K210 MCU ("MM") between Linux and the ASICs.
- `/data/upgrade_aup` is the K210 firmware: "AUP format" v2 header in clear, payload encrypted (entropy 7.99 bit/byte).
- Tooling: REA/Ghidra decompiler would not launch on this host; nixpkgs rizin has no RISC-V plugin; `llvm-objdump` from nixpkgs with T-Head extensions works.
- `uart_init`: `/dev/ttyS1`, 115200 8N1 raw. `data_toast_send`/`data_toast_recv_thread`: "CN" magic, CRC16/XMODEM, 12-byte header, payload <= 128.

## 10:40 - Phase 2 step 2: full protocol from static analysis

- Split into three parallel analyses (job path, receive path, control path), each writing its own file with CONFIRMED/INFERRED tags and instruction addresses.
- Spot checks of two claims (poll request 0x33 header, job frame 0x20 byte order) against the disassembly: both correct.
- Merged into `analysis/PROTOCOL.md`: 31 packet types, bring-up, job burst, polling, settings, firmware upgrade, open questions.
- One self-correction: DETECT header bytes 8..9 were first misread; `idx`/`cnt` at offsets 6/8 is the confirmed convention.

## 11:00 - Phase 2 step 3: live validation (passive)

- Built a static riscv64 strace 7.2 with the cached Nix cross toolchain (`tools/strace-static.nix`). First build came out dynamic: a plain `LDFLAGS` attribute does not reach the linker; `env.NIX_CFLAGS_LINK = "-static -no-pie -static-libgcc"` does.
- Copied to `/tmp/strace` (tmpfs, RAM only, flash untouched). sha256 `cff10a01...f0f5` on both sides.
- `btcminer` (pid 165, 19 threads) holds `/dev/ttyS1` as fd 10 and also feeds `/dev/watchdog`, so captures were kept short and checked for a clean detach (`TracerPid: 0`, no reboot).
- Capture command (BusyBox has no `timeout`):
  `strace -q -f -tt -xx -s 4096 -e trace=read,write -e signal=none -P /dev/ttyS1 -o /tmp/capN.txt -p <pid> & sleep N; kill -INT $!`
- Captures: `captures/cap1-passive-20s.*` and `captures/cap2-passive-180s.*` (raw strace + decoded).
- Decoder `tools/decode_capture.py`: reassembles the RX stream (the UART delivers 9-12 bytes per read), splits "CN" frames, checks CRC. Two parser bugs found and fixed on real data: strace prints `<... read resumed>, "..."` with a comma, and for split writes the data is on the `<unfinished ...>` line.

Results (all frames in both directions: CRC OK, zero bytes outside frames):
- Frame format, CRC-16/XMODEM, idx/cnt convention: confirmed.
- Every 0x33 poll gets exactly one reply frame. The MM cycles 0x71, 0x51, 0x52, 0x54, 0x53, 0x55 and slips in 0x50 when it has nonces (1600 polls in 180 s = 8.9/s, 1600 replies).
- 0x52 and 0x54 walk ASIC idx 0..9 with cnt=10: 10 ASICs. 0x51 Core string = "A3198S": the Nano 3 uses 10 x A3198S (not the A3197S of the 3s report).
- Live values: 4.13 TH/s (GHSmm 4129.44), ASIC temps 89-93 C (target 90), per-ASIC voltage readings 311-315, PLLs 100/574/594/614 MHz, work level 2 of max_level 3.
- 0x71 = per-level table: (60, 80, 300, 100, 293, 313, 333), (90, 85, 310, 100, 447, 467, 487), (125, 90, 330, 100, 574, 594, 614). Level 2 values equal the live PLL set and TarT, so fields are (power?, target temp, voltage, PLL0..PLL3).
- 0x55 is 1 byte `00` (no PSU telemetry on this model).
- Job bursts: 6 in 180 s, exact order as predicted. A new block at 11:07:30 produced the 32-byte 0x20 with `restart=1`, an empty-block template (coinbase 129 B, 0 merkles), then the full template 0.34 s later.
- Nonce proof (`tools/verify_nonces.py`, output `captures/nonce-verification.txt`): rebuilt the header from captured job frames for every 0x50 record. 25 of 25 checkable nonces hash to 44..51 leading zero bits; 44 bits = difficulty 4096 = the 0x25 target, so the MM filters at that target. Confirmed layout: header_bin words byte-swapped on the wire, nonce2 written little endian at n2_off, version index n -> word n of 0x27, ntime offset added to ntime, nonce bytes as-is. The other 7 records belong to jobs sent before the capture started.

## 11:13 - Phase 2 step 3: bring-up capture (btcminer restart)

- `tools/bringup-capture.sh`: stop btcminer, restart it from `/mnt/heater/app` under `strace -f`, also tracing `openat`/`ioctl` and the GPIO 61 sysfs files.
- Problem: strace did not stop on `kill -INT`. With `-o FILE PROG` strace defaults to `-I3` (fatal signals blocked). And `--seccomp-bpf` implies `--kill-on-exit`, so ending strace killed btcminer. Recovery: SIGKILL strace, btcminer died with it as expected, restarted it detached (`setsid ./btcminer`, PID 2089, parent init). Script fixed for next time (`-I1`, no `--seccomp-bpf`).
- Mining checked afterwards over the cgminer API (port 4028): pool 0 alive, 4.09 TH/s average after 151 s, diff-1 shares climbing; stratum difficulty 262144 means about one accepted share per 4.6 min.
- Capture `captures/cap3-bringup.*`: 2108 polls, 7 jobs, full bring-up, every CRC valid.

Results:
- `openat("/dev/ttyS1", O_RDWR|O_NOCTTY) = 10`, `TCFLSH TCIFLUSH`, `TCSETS {c_cflag=B115200|CS8|CREAD|CLOCAL}`: the static `uart_init` decode was exact.
- GPIO 61: `direction` <- "high", `value` <- "1". No reset pulse at start; the K210 answered DETECT in 24 ms.
- Bring-up in 46 ms: 0x10 -> 0x11 (88 bytes) -> 0x12 -> 0x13 -> 0x41 (level 2) -> 0x31; first 0x33 poll 110 ms later.
- DETECT_ACK: `BTC\0`, HDNA `0201000000000000` (ASCII), HVERSION `24071801_b906c52_6223725` (same build as `/data/upgrade_aup`), `1366`, 0x10, HWTYPE `PMMv1_X1`, SWTYPE `MM318_X2`, max_level 3. SYNC_ACK `01 03 01 01` (mm_status 1).
- 0x55 POWER sometimes 13 bytes: valid=1, then 2738, 4, 124, 328 (plausibly 27.38 V input, 124 W, 328 core voltage units). Otherwise 1 byte `00`.
- 0x52 PLLCNT sums to 177 on every frame of every ASIC while the split moves over time: the MM shifts cores of each A3198S between the 574/594/614 MHz domains.
- Nonce proof over all three captures: 68 of 68 checkable nonces valid (14 jobs, 76 records), minimum exactly 44 zero bits.
- Cleanup: `/tmp/strace`, the capture files and the script were deleted from the miner; nothing was ever written to flash.

## 12:10 - Phase 3 step 1: independent host-side driver (read-only)

Goal: prove the protocol from our own code, not just by decoding btcminer's traffic. `tools/nano3ctl.c`: a small static riscv64 tool that opens /dev/ttyS1, builds CN frames (crc16/xmodem), and sends only queries (DETECT 0x10, POLL 0x33). It sends no SET command, so it cannot change the miner. Built with `tools/nano3ctl.nix` (same cached cross toolchain as strace; needs `glibc.static` as a buildInput or the link fails with `cannot find -lc`).

Procedure each run: stop btcminer, run the tool, restart btcminer (`cd /mnt/heater/app && setsid ./btcminer </dev/null >/dev/console 2>&1 &`). Mining pauses ~15 s.

- Run 1 and 2: DETECT got no reply, and poll-only also got zero bytes. Not the K210: strace of nano3ctl (`captures/nano3ctl-strace.txt`) showed `TCSETS2 {c_cflag=B0|...}`. My bug: `open_port` called `cfsetospeed(B115200)` and then overwrote `c_cflag` wholesale, wiping the baud bits back to B0, so the correct frames (`43 4e 1d 1d 33 00 ...`) went out at zero baud. The port uses the termios2 ioctls (TCGETS2/TCSETS2); the baud lives in c_cflag. Fix: set c_cflag first, then cfsetispeed/cfsetospeed.
- Run 3 (`captures/nano3ctl-run3.txt`): works. DETECT -> DETECT_ACK with HDNA `0201000000000000`, HVERSION `24071801_b906c52_6223725`, HWTYPE `PMMv1_X1`, SWTYPE `MM318_X2`, max_level 3 (identical to the strace bring-up capture). Polls returned FREQ_REPORT, POWER, LEVELPARAM, STATUS (HashStatus 1, GHSmm 4120.48, TA 10, Core A3198S, TMax 25, worklevel 2, TarT 90), ASIC_COUNTERS and ASIC_PVT (per-ASIC temp 92-94 C, volt 307-309).
- So DETECT does work on an already-synced K210; the first-run failure was entirely the baud bug.
- btcminer restarted clean, 0 hardware errors, mining normally. `/tmp/nano3ctl` and `/tmp/strace` removed. Binary kept at `tools/bin/nano3ctl-riscv64-static`.

This is the key Phase 3 milestone: our own program, built only from the reverse-engineered spec, enumerates the K210 and reads its live telemetry. The protocol is now proven by an independent implementation, not only by reading the vendor binary.

## 12:40 - Phase 3 step 2: job feed + nonce harvest (independent driver)

Architecture chosen for fast iteration: a tiny UART-over-TCP bridge on the miner (`tools/uartbridge.c`, static riscv64) plus all protocol/pool logic in Python on the PC (`tools/nano3miner.py`). The bridge only relays bytes; it sends no commands itself. `tools/bridge-session.sh` stops btcminer, runs the bridge for a bounded window with a watchdog, then restarts btcminer.

The driver (stage 2a, verify-only) connects to the user's pool (<POOL_HOST>, worker ...<worker>), takes one mining.notify, builds the job burst (0x20..0x27) from the reverse-engineered spec, polls (0x33), collects 0x50 nonce reports for that job, rebuilds each block header, and checks the hash locally. It sends NO SET command, so it changes no hardware setting; the K210 keeps btcminer's settings.

Bugs found and fixed on the way:
- bridge exited after one client: my TCP probe consumed the single accept(); made the bridge loop on accept.
- pool IO: a socket makefile with a sub-ms timeout wedges ("cannot read from timed out object"); replaced with a raw-socket line buffer and explicit per-read timeouts.
- coinbase corruption: this pool uses n2size=8, and `rebuild_header` did `cb[off:off+8] = pack("<I",n2)[:8]` which assigns 4 bytes to an 8-byte bytearray slice and SHRINKS the array, moving coinb2 and giving random hashes (best 5 zero-bits). Fixed to write exactly the sent_n2size (min(n2size,4)) bytes at the offset, leaving length unchanged.

Result (`captures/nano3miner-verify2.txt`): our job was accepted by the K210, which returned 16 nonce records for it; all 16 rebuild to valid headers at >= 44 zero-bits (up to 47). That meets the capped on-chip target (difficulty 4096 is ~44 zero-bits) and the difficulty-1 target, which is what proves the job encoder and nonce reconstruction. It does NOT meet the live pool vardiff of 262144, which needs ~50 zero-bits, so none of these would be accepted as pool shares. The log's `meet_pool_target=16` was produced by an older driver that compared against the capped 4096 target; the stage-2b submit runs confirm the gap (all shares rejected "Difficulty too low"). The current driver compares against the live pool target. This proves the full job encoder and nonce reconstruction from our own code on the real hardware, with no setting changed.

This also answers an open question: with pool n2size=8 the MM writes the low 4 bytes of nonce2 (LE) at nonce2_off and leaves the rest zero (confirmed because reconstruction under that assumption hashes valid).

Stage 2b (submit) is not done yet. `captures/nano3miner-submit1.txt`: submits reached the pool in time but were rejected `[23, 'bad-version']`. Two separate issues to resolve before an accepted share:
1. Difficulty: the driver caps the on-chip target (0x25) at difficulty 4096 (the `avalonnano_drv` table `max_diff` 4096.0 at 0x236bf0+0xe0, and `avalonnano_sswork_update` clamps before building the target; whether the K210 could accept a harder target is untested). btcminer then re-filters the reported shares to the pool's live vardiff (262144 here) before submitting. The driver must submit only nonces meeting the true pool difficulty, not 4096. At 262144 and ~4.1 TH/s that is about one share every 4.5 minutes, and the job must still be fresh.
2. Version rolling: Ocean rejects the submitted nVersion with bad-version. Need the exact Ocean submit convention (whether version-rolling was actually negotiated, and the version param format); best taken from btcminer's own stratum submit code rather than live, since shares are rare.

Miner restored each run; btcminer mining normally afterwards. No intentional firmware flashing: tools live in /tmp and were removed. This is not a proof of zero persistent writes, since /data is mounted read-write and stock logging writes /data/log/run.log during normal operation.

## 12:55 - Flashing options for custom firmware (offline, nothing written)

- Checked the Tech Nerd Army web flasher (lnbits.molonlabe.holdings/tnaflasher): it only writes its own TNA-OS image fetched with a one-time token, offers Canaan builds for the Q and the Nano 3s only, and `k230-flasher.js` hardcodes a data-partition erase at 0x0c800000 (+56 MiB, ends at 256 MiB). Not usable on this 128 MiB Nano 3. Copies saved in `reference/tnaflasher/`.
- Decoded both u-boot env copies from the dump: `mtd5` is the active one (flags 6), and every slot (`app`, `rootfs`, `linux`, `uboot`, `uboot_spl`) is set to B. Slot A is inactive.
- Both app slots hold UBI volume 0 `ubi_app_part_a` (116 LEBs). `rcS` mounts the selected slot as `/dev/ubi1_0` and runs `/mnt/heater/app/btcminer`.
- Found that `btcminer` itself starts Wi-Fi (`wpa_supplicant ... ../confiles/wpa_supplicant.conf`, `udhcpc -i wlan0 -b`) and the `ttyS0` getty is disabled. A custom app that does not start Wi-Fi would cut SSH with no console fallback.
- Wrote `FLASHING.md`: test from RAM first, then write slot A (`mtd10`) from a copy of the stock app, boot once with a start script that resets `app_partition` to B, make it permanent only after it is proven. USB KBURN (`k230-flash` 1.4.0) kept as the full-image route once the BOOT button is confirmed.

## 2026-10-28

### 12:29 - Stock miner found stopped since the night

- `btcminer` had not run since 22:51 on 2026-10-27: the last session's restart left no trace in `run.log`, and no Wi-Fi bring-up followed. That session ran in the foreground over SSH; the cause was not found. The fan sat at 71 %, nano3d's last duty, for 13.5 h. A power cycle restored stock mining.
- Fix: whatever ends nano3d, it now leaves the fan at 100 % and sends the MM to level 0. Checked live with a wrapper that reads the PWM right after the exit: `duty_cycle 40000` (100 %).

### 12:51 - Detached sessions over sudo

- `ssh -t ... "sudo sh -c 'setsid sh session.sh ... >/tmp/session.out 2>&1 &'"` ran (sudo logged it) but left no `session.out`: the job died before its redirection, from the hang-up of the closing terminal. Adding `& sleep 2` inside the sudo shell fixes it; every session since runs that way.
- Session results, stock pools: 4.13 TH/s, 26/26 nonces valid, 5/5 shares accepted, then btcminer back at 4.36 TH/s.

### 13:04 - Wi-Fi bring-up, pool reconnect

- `-wifi` live: nano3d killed the stock network daemons, ran wpa_supplicant, `wpa_cli add_network` from `systemcfg.ini` and udhcpc, and was connected 6 s later. The watchdog was free this time and was armed (89 s).
- nano3d no longer exits when the pool goes away: it reconnects in the background while polling, fan, safety and watchdog go on, and restarts the MM on the new connection's job. A pool silent for 5 minutes counts as lost. Tested with a relay on the miner that cut the first connection after 60 s: reconnected 0.5 s later with a new extranonce1, 7/7 shares accepted afterwards.
- Seen twice: the hottest ASIC read 99 and 101 C at the start of a session. The stock btcminer of the previous restore had run 40 s at its 25 % start-up fan. The safety caught it (level 0, fan 100 %). Sessions are now spaced a few minutes apart.

### 13:14 - Front LCD and button

- The LCD is `/dev/fb0`, `fb_st7789v`, 240x240, 16 bpp RGB565 little endian, stride 480, rotation done by the driver. A capture of the stock screen (LVGL) decodes upright with that layout.
- nano3d draws whole frames with the Go fonts (`golang.org/x/image`): hashrate, ASIC temperatures, fan, shares, pool, IP; start-up steps and "stopped" as one-line screens. Checked live by capturing the framebuffer during a run.
- The front button is the `gpio_keys` input device (`/dev/input/event1`); a press cycles the work level in force through 0..2, as stock. Not pressed on the hardware yet.
- The LCD backlight is not a Linux PWM: btcminer keeps it in the u-boot env (`fw_setenv lcd-bright`), so it is on from boot.

### 13:20 - Web page and JSON API

- nano3d serves `/` (status), `/api/status` (JSON, with all 10 ASIC temperatures), and behind basic auth (root/root, as the stock UI) `/settings` and `/api/settings`. Levels and night mode apply within 2 s; a pool change is saved and nano3d exits with code 4 so the caller restarts it. Settings go to nano3d's own `/data/nano3-os/config.json`; the stock configuration is never written.
- Found live: the pool-change exit came before the HTTP answer (browser saw a closed connection). nano3d now waits 1 s first. A page opened as `http://root:root@host/settings` could not load its data, because fetch() rejects URLs with credentials; the pages now fetch from `location.origin`.

### 13:30 - Installed in app slot A

- Backup of `mtd10` taken over SSH, SHA-256 identical on both sides (`7203471365a23606fa21c0f24f9a001219b8851019325c37f714b8407a9cf313`).
- Slot A written from a copy of the live stock app, with `nano3d` and `tools/nano3-os-start.sh` as `heater/app/btcminer`; checked from a fresh read-only mount.
- `fw_setenv app_partition A`, reboot. nano3d's web page answered 22 s after the reboot command. Valid nonces from 100 s, 4.19 TH/s and 21/21 shares accepted at 147 s, ASICs 90/93 C, fan 71 %.
- The K210 is now released (GPIO 61 high) before the Wi-Fi and pool set-up, as btcminer does, so on a cold boot it has that time to start.

### 13:51 - Power telemetry, pool view, and a restart that overheated

- nano3d now reads what the MM already reports: the 0x55 power report (valid about every 4 s; 27.38 V, 4 A, 124 W at level 2), the expected hashrate (GHSspd) and error rate (DH, 2.5 %) from 0x51, the PLL clocks from 0x53, per-ASIC voltage from 0x54, and the nominal power of each level from 0x71 (60, 90, 125 W). The web page shows power, J/TH (25 to 30), kWh/day, an optional cost, rule-of-thumb checks and 24 h charts; the LCD has a power row.
- The owner's Public Pool runs on StartOS. nano3d reads its `/api/client/<address>` once a minute for the pool's view of the worker, with TLS verified against the StartOS local root CA (the server certificate carries the LAN IP as a subject name). Without the CA the request fails, as it should.
- Updating nano3d in the live slot: slot A has 3.6 MB free and the binary is 7 MB, so the update stops nano3d, deletes the file, copies the new one, and the start loop restarts it.
- Found live: the MM changes level slowly, about 40 MHz every 5 s, while 0x51 reports the new level at once. After an update, the exiting nano3d had set level 0 (12:29 fix); the MM ramped down, then back up for the new nano3d, whose fan PID had followed the cooling chips down to 33 %. The hottest ASIC reached 96 C and the safety tripped (level 0, fan 100 %). The exit now only sets the fan to 100 %: the MM keeps its level, so a restart continues at full clock.

### 14:05 - Pools, price, level planner

- Pools set from the web API: the owner's Public Pool first, `solo.ckpool.org:3333` as fallback (the two stock Ocean entries removed). ckpool answers a stratum subscribe and resolves on the miner. The stock `cgminer.ini` still lists the old pools; it only matters on a fallback to slot B.
- Electricity price: the Portuguese regulated simple tariff (ERSE, 6.9 kVA) is 0.1711 EUR/kWh before VAT from 1 October 2026 (comparison sites, not checked on ERSE's own page); with 23 % VAT 0.21 EUR/kWh, now nano3d's default. At level 2 that is 3.0 kWh and 0.63 EUR a day.
- Level planner: the page lists each level's nominal power, estimated hashrate (2.12, 3.16, 4.02 TH/s against 2.23, 3.24, 4.13 measured), J/TH, kWh/day and cost. Levels 0 and 1 give about 10 % more hashes per watt than level 2.
- The MM's clock ramp also showed up live: a change of level takes about a minute to reach the new clocks.
- 14:23: the 24 h unattended run started (polled from the PC every 5 minutes).

### 14:50 - Three-model audit, coinbase tag

- The Go code was reviewed by three models (Claude Fable 5.1, GPT, Gemini 4 Argon) plus go vet, staticcheck, gosec, govulncheck and the race detector. Fixed: a pool difficulty under 2^-32 made the target builder panic (reproduced, now capped); `POST /api/settings` took a cross-site `text/plain` form with the cached login (now JSON with a same-site `Origin` only); a share submit error ended nano3d instead of reconnecting; the PLL readings were published as a slice of the main loop's array (a data race); a zero temperature counted as a fresh reading for the over-heat safety; `-level` and `-night-level` accepted the MM's max level itself (3), which the stock clamps below; the Public Pool fetch leaked a transport a minute; NaN or Inf from the MM or the pool broke the JSON; handshake, job size, message flood, unanswered submits and a pool without jobs are now bounded. Rejected as design: the stock `root` / `root` login over plain HTTP on the LAN.
- Coinbase tag: Public Pool gives an 8-byte extranonce2 (live: `n2size=8`) and the K210 writes only its first 4 bytes, so nano3d puts up to 4 ASCII characters in the other 4, in the coinbase it sends and in every submit. Set `HODL` from the web API at 14:58: 9 shares accepted in the next 2 minutes, 0 rejected, nonce check still clean. A found block would show `HODL` in its coinbase.
- The 24 h run restarted with this install (14:55).

### 15:20 - Boot hardening: guard in the root filesystem

- Where SSH lives: `/etc/init.d/S50sshd.sh` in the root filesystem (mtd9, `rootfs_partition=B`), shared by both app slots, so a fallback to the stock app keeps SSH. The other root copy (mtd8, March 2024 SDK) also starts sshd and telnet. What SSH did not survive: an app that fails before its own Wi-Fi bring-up (nano3d brings Wi-Fi up like btcminer), and the start script's own rule that every boot first points the next boot at slot B.
- New start script: the next boot is no longer pointed at B in advance. Fallback to stock only when the Internet answers (ping 1.1.1.1 / 8.8.8.8) but nano3d is not in its main loop after 15 minutes, or with no Internet at all for 60 minutes; the reason goes to `/data/nano3-os/fallback`, every boot to `/data/nano3-os/boots.log`.
- New guard `/etc/init.d/S51nano3os` (`tools/nano3-os-guard.sh`), run by rcS on every boot on either slot: restarts sshd if it dies (host keys generated if missing), brings Wi-Fi up from `systemcfg.ini` after 3 minutes without an address, and on slot B after a fallback points the next boot back at A once the Internet has answered for 10 minutes (24 hours after a nano3d fault), then reboots.
- Installed live at 15:17 (guard started by hand, pid 1238), then a reboot test.
- Reboot test 15:21: status page down 3 s after the reboot command, mining again 24 s later, sshd answering. The Public Pool server was down at that moment (port 3333 closed), so nano3d took `solo.ckpool.org`, and it would have stayed there: nano3d had no fail-back to the first pool. Added: while on a fallback pool the first pool is probed every 5 minutes off the main loop, and the miner switches back as soon as it answers with a job (as cgminer does). After a power cut the StartOS box is back minutes after the miner, so this matters every time.

### 15:52 - The three work levels measured

- Each level held 8 minutes (6 to settle, 2 sampled every 10 s, 12 samples), input power from the MM's 0x55 report, fan from the PID at its settled duty, room conditions as in the afternoon runs:

| Level | Hashrate | Input power | J/TH | Fan | ASIC mean / max | Core clocks (PLL1..3) |
|---|---|---|---|---|---|---|
| 0 | 2.25 TH/s | 61 W | 27.1 | 39 % | 80 / 82 C | 293 / 313 / 333 MHz |
| 1 | 3.27 TH/s | 91 W | 27.8 | 54 % | 85 / 87 C | 447 / 467 / 487 MHz |
| 2 | 4.13 TH/s | 125 W | 30.3 | 73 % | 90 / 93 C | 574 / 594 / 614 MHz |

- Levels 0 and 1 give 10 to 12 % more hashes per joule than level 2. The fan, which is what the night mode is about, runs at 39 % on level 0 against 73 % on level 2; the MM's temperature targets are 80, 85 and 90 C for the three levels, so the fan settles where the chips meet the target.

### 16:58 - Fan fixed at 85 % on level 2

- Same method as the level table (6 minutes to settle, 12 samples): 4.16 TH/s, 125 W, 30.1 J/TH, ASICs 84 / 86 C (mean / max) against 90 / 93 C with the PID at 73 %. So 12 % more fan buys 6 C on the chips and changes nothing else. Back on automatic the PID continued from the duty in force and settled at 74 % and 89 / 92 C within 2 minutes.
- The day fan is now a web setting (automatic or a fixed duty); the night window always runs the PID.

### 17:40 - Second audit round (Fable, GPT, Gemini) on the whole system

- 18 fixes from 27 + 8 findings, each checked against the code: until NTP sets the clock the night settings apply (quiet first, instead of the day level); the PID is re-seeded before the level logic, so a level rise from a low fixed duty still starts at 72 %; without a temperature reading the fan keeps its duty instead of the PID's cold 25 %; a coinbase tag change waits for the next job (resending the running job broke its nonces); no history sample before NTP (a 1970 point flattened the 24 h charts); the learned hashrate is written on its own, at a 5 % change, so a test session's `-level` never lands in the settings file; the warm-up counts from the MM bring-up; the pool's user is read from the connection; extranonce1 is bounded; the share target is computed once per job; the blur class survives each refresh; the guard waits 10 minutes before taking over a Wi-Fi the app is still retrying, parses only `[netcfg]`, restarts ntpd after 5 stale minutes, returns to slot A after an hour (not a day) and only once `fw_setenv` confirmed A; the start script never writes under an unmounted `/data`, keeps `boots.log` at 200 lines and reboots only after `fw_setenv` succeeded.
- UI, kept light: night and warm-up shown next to the level, rejected shares as a percentage, hot ASIC tiles in colour, refresh when a phone brings the page back, "Use this browser's time zone", the fan target greyed out under a fixed duty, Save disabled while saving, saved values the lists lack are kept.
- Rejected again: BIP310 masked bits (Public Pool XORs, verified in its source), fractional difficulty, per-ASIC sensor freshness, a reply channel for the save result.

### 17:58 - Dashboard look and layout

- The status page restyled in the Grafana manner: near-black canvas, bordered panels with muted titles, big tabular numbers, a status dot in the header (green when the safety is ok and the pool connected), sparklines with a gradient fill in one colour per series, threshold colours on the hottest ASIC (yellow at 94 C, red at 95 C) and on the ASIC tiles. Still one static file, no framework, light scheme kept.
- Layout fixed instead of "as many columns as fit", which left orphan panels: 4 columns on a desktop (hero over two, pool over three), 2 on tablets and phones (shares and pool full width on a phone), charts five across above 1200 px and stacked below, the level table's secondary columns dropped on a phone. Checked in a browser at 1900, 820 and 390 px. Chart time labels moved out of the SVG, which stretched them.
- The pool's view (hashrate seen, best share, last seen) now sits in the Pool panel.

### 18:10 - The day fan setting is a floor, not a fixed duty

- A fixed duty under what the level needs made the safety cycle (95 C trip, 100 % fan and level 0, back to the low duty after 5 minutes, again). The setting is now a minimum: the PID runs as always and the fan never goes under it. The over-heat safety still forces 100 % above either. The target field stays active with a floor set.

### 18:30 - LCD in the dashboard style

- The front LCD redrawn like the web page: header with a status dot and the level, big hashrate with expected, J/TH and uptime under it, three tiles (power, ASIC mean / max coloured by threshold, fan), shares and the pool's view, best share, the 24 h hashrate as an area chart, the LAN IP. The pool address is no longer drawn at all, so a photo of the miner shows nothing private. `cmd/lcdshot` renders the same frame from `/api/status` as a PNG; the design was iterated with it, then checked on the real framebuffer.

### 18:50 - Ready for a public repository

- Checked every tracked file and the whole local history for personal data: no wallet, pool user, SSID,
  Wi-Fi key or password anywhere. The two LAN addresses that were (the miner's, as the `lcdshot` default,
  and a local Public Pool as a placeholder in the settings page) are replaced; `-miner` is now required.
  The published repository starts from one commit: the development history, which held those two
  addresses, stays local.
- Second opinions (GPT, Gemini) on the published state, fixed: the updater forces a nano3d that ignores
  TERM for 15 s and never deletes the binary under a running process; the slot A backup is taken from a
  root shell and fetched with `scp` (`sudo` has no terminal in a one-shot `ssh` command); the RAM test
  runs with `-wifi`, as step 2 demands; explicit `scp` commands, `chmod 755` for a downloaded binary,
  the guard copied from `/tmp`, `mkdir -p` and a fresh upload before the updater's install; the README
  promises the fallback for a boot that fails, which is what the start script watches.
- Gaps for a stranger closed: a "Before you start" section in `FLASHING.md` (SSH, password, backup of slot
  A, where to get the binary), an "Update a running install" section with `tools/update-nano3d.sh` (which
  so far lived only on the miner), a quick start at the top of the README, and a GitHub Actions workflow
  that runs vet and the tests, builds the riscv64 binary on every push, and attaches it with its SHA-256
  to a release on a `v*` tag.

### 22:40 - Published, release v0.1.0

- Repository public at github.com/marceloeatworld/nano3-os, one commit. GitHub Actions on Ubuntu 26.04
  (checkout, setup-go and upload-artifact v7) runs vet and the tests, builds the riscv64 binary and, on a
  `v*` tag, publishes it with its SHA-256 as a release. The release binary was installed on the miner
  through the update script and mined (7 then 2 shares accepted, 0 rejected, two restarts of 15 s).
- The Nix Go toolchain and the upstream one give different bytes for the same source and flags; the
  checksum next to the release binary is the reference.

### 23:00 - Updates from the dashboard

- `internal/update`: the latest release from the GitHub API, a semver comparison with the tag the binary
  was built for (`-X main.release`, set by the workflow on a tag; a build from the tree is never offered
  an update), and a download of `nano3d-riscv64` into `/tmp` that keeps the file only when its SHA-256 is
  the one in the release's `.sha256`. The rootfs has no CA bundle, so the Mozilla roots come embedded
  (`golang.org/x/crypto/x509roots/fallback`, 200 kB).
- nano3d asks GitHub once a day (after NTP: TLS needs the clock; the owner can turn it off in the settings,
  turning it on asks at once), shows the result in `/api/status` and on the dashboard as an Update card
  with the release notes and a button (`POST /api/update`, settings login). The download runs off the main
  loop with its progress on the page; a verified binary makes nano3d exit with code 5, and the start
  script's loop checks the hash again and swaps the file into slot A before the restart: slot A cannot
  hold two binaries, so the swap happens only once nano3d has exited, and never under a running process.
- Tested from the PC against the real v0.1.0 release (API, checksum parsing, download, BusyBox-style
  `sha256sum -c` on the staged pair), then live on the miner.

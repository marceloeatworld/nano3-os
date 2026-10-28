# Avalon Nano 3 - security findings (stock firmware, 2026-10-27)

Findings from the owner's own device, for the owner's research and responsible disclosure. Stated as
conclusions with the location in the binary, not as exploit recipes. Tags: VERIFIED = re-read in the
disassembly during this work; FROM-CROSSCHECK = reported by the Ghidra cross-check and not independently
re-read here (see `analysis/crosscheck-control.md`).

Binary: `rootfs/mnt/heater/app/btcminer`, sha256 `e6c11630a187d677f55178fa1dc7f2f1a52805856c538fae70cfbf0038ca6751`.
SDK string `unkonwn-20240718-200655`; K210 MM firmware build `24071801_b906c52_6223725`.

## 1. Accounts and exposed services (VERIFIED; corrected 2026-10-27)

An earlier version of this finding called SSH with `admin` / `admin` a stock default. It is not: on this
unit SSH and that password were set up by the public "nanojb" jailbreak (finding 2), applied through the web
UI before this work began. Stock state, from the inactive rootfs slot A in the flash dump and the file times
on the live system:

| Item | Detail |
|---|---|
| Accounts | Stock has `admin` (`/bin/sh`) with `(ALL) ALL` in `/etc/sudoers` (file dated 2024-07-18, unchanged). Its stock password is set but is not `admin`, `nano` or `root` (checked against the stock hash in slot A). |
| SSH | The July 2024 build ships `/usr/sbin/sshd` but does not start it: before the jailbreak it had no `sshd` user and no SSH init script (`/etc/passwd-` backup, file times). The older February 2024 build in slot A still contains `S50sshd` and `S50telnet` init scripts; whether they ran there was not tested. |
| Web UI | root / root (per the owner), port 80, served by `btcminer` (mongoose). Gives root through finding 2. |
| cgminer API | port 4028 on `0.0.0.0`, no auth. `ascset` write commands are accepted (see finding 4). |
| swupdate | port 9090 on `0.0.0.0`, firmware upload endpoint. |

Impact: the cgminer API needs no password at all, and web UI access gives root (finding 2). SSH is not a
stock entry point.
Mitigation for the owner: change the web UI password, firewall ports 80/4028/9090 (and 22 if SSH was enabled)
to trusted hosts. After a jailbreak, change the `admin` password: the jailbreak's password is public.

Evidence caveats: the listening addresses above are from the web UI / owner report and the service start
lines in `/etc/init.d/rcS`, not from a port scan. Both `netstat` calls in `recon/recon.sh` used `-p`, which
BusyBox netstat rejects, so `recon/recon.txt` has no listening-socket output.

## 2. Command injection in the web UI timezone setting (CONFIRMED LIVE)

`btcminer` applies the timezone with the format string `ln -sf /usr/share/zoneinfo/%s /etc/localtime`
(string at file offset 0x1e1fb8; handler `device_timezone_config_process`, CGI `/timezoneconf.cgi`) and runs
the result through a shell without validating the value. A timezone value that contains shell syntax
therefore runs commands as root.

This is the vector of the public "nanojb" jailbreak, and this unit shows it: `run.log` records the timezone
being set, at 10:09:41, to a value that downloads a shell script from a third-party host and runs it. Within
one second the root filesystem changed: an `sshd` user, an SSH host key, `/etc/init.d/S50sshd.sh` (starts
`/usr/sbin/sshd`), a rewritten `/etc/shadow`, and a stray `app/zoneinfo` symlink left by the broken `ln`
command. A search of the whole filesystem for files changed that day found nothing else: no root SSH keys,
no cron jobs, no `sudoers.d` entries. Later starts loaded a plain `Etc/UTC`, so the command ran once, when
the value was set.

Evidence: the format string, the log line, and the file times. The call path into the shell was not re-read
in the disassembly.

Impact: anyone who can use the web UI gets root. Whether `/timezoneconf.cgi` checks the web login was not
tested.

## 3. World-readable OTA key (VERIFIED, Phase 1)

`/etc/ota_aes_key_iv` (99 bytes) and `/etc/ota_public.pem` are mode 0755, readable by any user, and are in the
dump. swupdate is started with `-k /etc/ota_public.pem -K /etc/ota_aes_key_iv` (`/etc/init.d/rcS`). The AES
key/IV that protects K230-side update images is therefore not secret on the device. Not yet tested whether it
decrypts the swupdate images; the K210 MM image (`/data/upgrade_aup`) uses a different, still-opaque scheme.

## 4. Heap overflow in the API upgrade path (VERIFIED)

`set_avalon_upgrade` (0x30400), reached from the cgminer API / web `upgrade` command via
`set_avalon_device_upgrade` (0x306d0). The 32-byte block header carries a total size (+0x14), an offset
(+0x18) and a u16 chunk length (+0x1c). The code checks only:
- `offset <= total` (0x30438),
- `offset <= offset_next` (0x30466) - `offset_next` (0x23a434) is the running expected offset from the previous chunk, not the buffer size,
- `strlen(hex) - 0x40 >= chunk_len` (0x3044a) - and this compares a hex-character count against a byte count.

It then runs `hex2bin(buf + offset, hex + 0x40, chunk_len)` (0x30472..0x30478), writing up to `chunk_len`
bytes at `buf + offset` inside a `malloc(total)` buffer. There is no `offset + chunk_len <= total` check, so a
block with `offset` at or near `total` writes past the end of the heap buffer. `upgrade_aup` likewise trusts
the AUP header's `hw_count`, `sw_count` and `payload_len` without a buffer-length argument.

Impact: a local-network client can corrupt the heap of the root-run `btcminer`. The `upgrade` command is an
`ascset`-style command on the same `device_cmds` dispatch as `frequency`/`factory` (protocol-control.md
"API `upgrade`"). Per finding 1 the cgminer API on 4028 is no-auth and accepts `ascset` writes, so on that path
no password is required; the web UI path (port 80) is additionally gated by the web login. Unverified:
whether cgminer's `api-allow` restricts privileged `ascset` writes to specific hosts on this build; confirm
before treating the no-auth path as reachable from an arbitrary LAN host.

## 5. SIGUSR1 starts a firmware flash (VERIFIED)

`avalonnano_trig_upgrade` (0x30800) is installed as the SIGUSR1 handler: `init_signal_handlers` calls
`sigaction(10, {sa_handler = 0x30800})` (0x15840..0x15854). `kill -USR1 <btcminer pid>` therefore begins a
K210 firmware flash from `/data/upgrade_aup`. The handler calls `upgrade_aup` (rewriting `/opt/upgrade_aup`)
before confirming the K210 is connected, and calls `malloc`, `fopen` and `zlog` from signal context (not
async-signal-safe). Any local process able to signal the miner (same uid, or root) can trigger a flash.

## 6. File-descriptor leak on failed upgrade (FROM-CROSSCHECK)

`upgrade_send_data` (0x2fad0) returns without `close()` when a data chunk fails its 5 retries
(0x2fc88..0x2fc9a, 0x2fdd2..0x2fe0c). `miner_upgrade` retries up to 5 times, so up to 5 descriptors leak per
upgrade attempt. The `read()` return value is also ignored. Low severity (local, needs repeated failures).

## 7. Hardcoded AUP error strings (FROM-CROSSCHECK, cosmetic)

The "AUP is NOT for this hardware/software" errors print the hardcoded strings `MM4v1_X3` and `MM318_X2`
(0x1552b0, 0x155340) instead of the device's real HWTYPE/SWTYPE. No security impact; noted because it can
mislead a reader comparing an AUP file against a device.

## 8. Outbound telemetry

Not re-examined in this pass. The public Nano 3s report documents stock outbound telemetry; whether this Nano 3
build phones home was not tested here. The live captures (`captures/`) show only the configured pool
(`<stock-default-pool-ip>:3303`) and `ntpd`, but the captures were short and only covered `/dev/ttyS1` and local API.

## Notes

- Findings 2 and 4-7 are in code that runs as root (`btcminer` is launched from `/etc/init.d/rcS` as root).
- This work exploited none of these. Findings 4 and 5 were confirmed by reading the disassembly; a working
  proof was not built and is out of scope for this writeup. Finding 2 is known from its use: the unit had
  been jailbroken through it at 10:09, four minutes before this work's first log entry.
- Full per-claim evidence: `analysis/crosscheck-control.md`.

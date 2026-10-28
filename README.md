# Canaan Avalon Nano 3 - A3198S ASIC
## *Reverse Engineering Report*

### Independent reverse-engineering of the Canaan A3198S and the Nano 3 K230-to-K210 mining protocol

**Date:** October 27, 2026

**Conducted by:** Marcelo (`marceloeatworld`)

**ASIC:** Canaan A3198S (Avalon-Nano class), 177 cores per chip (strongly indicated by live core counts)

**Host platform:** Canaan Avalon Nano 3 (10x A3198S; Kendryte K230 single Linux hart + a separate Kendryte K210 "MM" controller)

**Stock firmware:** SDK build `unkonwn-20240718-200655`; K210 MM firmware build `24071801_b906c52_6223725`

**Miner binary:** `btcminer`, sha256 `e6c11630a187d677f55178fa1dc7f2f1a52805856c538fae70cfbf0038ca6751`

**Relationship:** companion to the A3197S / Avalon Nano 3s report by Nikos ([CryptoIceMLH/Nano3S](https://github.com/CryptoIceMLH/Nano3S)). Different hardware, different architecture; see section 8.

### nano3-os

This repository also holds **nano3-os**, an open replacement for the stock
`btcminer`, built on the protocol documented below. Its daemon `nano3d` (Go, one
static riscv64 binary) brings up the K210, mines to any Stratum V1 pool with
version rolling, checks every nonce the K210 reports and submits shares. Like the
stock app it drives the fan, the hardware watchdog, the Wi-Fi, the front LCD and the
front button, and it serves a web page. It never sends a frequency, voltage or
firmware-upgrade command and never writes the stock configuration.

#### Quick start

1. Get a root shell on the miner over SSH and change the `admin` password: "Before you start" in `FLASHING.md`.
2. Download `nano3d-riscv64` from the latest release (built by GitHub Actions) and check its `.sha256`,
   or build it with Go 1.26: `GOOS=linux GOARCH=riscv64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/nano3d-riscv64 ./cmd/nano3d`.
3. Run it from RAM first, no flash write: `FLASHING.md` step 1. The stock app comes back when the session ends.
4. Write app slot A and boot it: `FLASHING.md` steps 2 and 3. The stock app stays in slot B and the board
   falls back to it on its own if nano3d fails to come up after a boot.
5. Done. Open `http://<miner>/`. New releases show up there with an Update button.

**Disclaimer.** nano3-os is an independent project, not affiliated with Canaan. It drives
mining hardware with code worked out by reverse engineering. You flash and run it at your
own risk: the author takes no responsibility for damaged or bricked units, lost hashrate,
lost coins, voided warranties or any other loss. The MIT License (`LICENSE`) says the same
in legal terms: no warranty of any kind.

| Milestone | Status |
|---|---|
| 1. Daemon: K210 bring-up, pool client, job encoder, nonce check, submit | Done. Live on the miner: 64/64 nonces valid, 14/14 shares accepted |
| 2. Board control: fan PID, watchdog, Wi-Fi bring-up, over-heat safety, night mode, pool reconnect | Done. Live 2026-10-28, including a pool connection cut mid-run |
| 3. Front LCD status and front button (cycles the work level) | LCD done, live 2026-10-28. Button written, not yet pressed on the hardware |
| 4. Web page and JSON API on port 80 | Done. Live 2026-10-28 |
| 5. Install in app slot A, falling back to the stock app on its own (`FLASHING.md`) | Done. Mining from slot A since 2026-10-28 |
| 6. Boot hardening: guard in the root filesystem (SSH, Wi-Fi, return to slot A), fail-back to the first pool | Done. Reboot test 2026-10-28: mining 24 s after the reboot command |
| 7. Code audit by three models plus vet, staticcheck, gosec, govulncheck, race detector | Done 2026-10-28, fixes applied (`JOURNAL.md`) |
| 8. 24 h unattended run | Running since 2026-10-28 evening |
| 9. Updates from the dashboard: GitHub release found, downloaded, SHA-256 checked, swapped in by the start script | Done. Live 2026-10-28 |

- Day to day: nothing to do. The miner runs level 2 by day and the level set for the
  night window by itself (on this unit level 0 from 22:00 to 08:00: 61 W, fan near
  39 %, 2.25 TH/s), reconnects to the pool, goes back to the first pool when it answers
  again, and survives a power cut: it boots, brings Wi-Fi up, syncs the clock and mines
  again in about half a minute, with the settings kept. The three work levels measured
  on this unit: 2.25 TH/s at 61 W (level 0), 3.27 TH/s at 91 W (level 1), 4.13 TH/s at
  125 W (level 2); levels 0 and 1 give 10 to 12 % more hashes per watt.
- LCD: the front display shows the same dashboard in small: hashrate, expected
  hashrate, J/TH and uptime, power, ASIC temperatures and fan tiles, shares, the
  pool's view and best share, the 24 h hashrate chart and the miner's LAN address.
  It never shows the pool address, so a photo of it gives nothing away.
  `go run ./cmd/lcdshot -miner http://<miner> -o lcd.png` renders the frame from the
  miner's status as a PNG, a screenshot of the LCD without a camera.
- Build: `GOOS=linux GOARCH=riscv64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/nano3d-riscv64 ./cmd/nano3d`
- Test: `go test ./...`. Job bursts are checked byte for byte against the reference
  driver `tools/nano3miner.py` (`tools/gen_job_vectors.py`), and the nonce rebuild
  against the Bitcoin genesis block.
- Try it on a miner: copy `bin/nano3d-riscv64` to `/tmp/nano3d` and
  `tools/nano3d-session.sh` to `/tmp`, then as root
  `sh /tmp/nano3d-session.sh 200 -pool <host>:<port> -user <address>.<worker> -seconds 150`.
  The session stops `btcminer`, runs `nano3d`, and always restarts `btcminer`.
  Without `-pool`, `nano3d` mines to the stock pools from `/data/usrcon/cgminer.ini`.
- Web page: `http://<miner>/` is a dashboard in the Grafana style, one static file with
  no framework, dark or light with the system: hashrate, input power, J/TH, kWh/day
  and cost, temperatures (hot chips in colour), fan, chip health, shares, the pool and
  the pool's view, the level planner with the hashrate measured at each level, 24 h
  charts and per-ASIC readings. It reflows for tablets and phones, warnings wait out
  the first 10 minutes after a start, and a "Blur private" button hides the pool, the
  address and the IP for a screenshot. `http://<miner>/api/status` returns the status
  as JSON, `/api/history` the charts' data.
  With a Public Pool web address (and, for HTTPS on StartOS or Umbrel, its local root CA)
  in the settings, it also shows the pool's view of the worker. Once a day it asks GitHub
  for the latest release and, when there is a newer one, shows an Update button: nano3d
  downloads the binary, checks its SHA-256 against the one the release publishes and
  restarts on it (`FLASHING.md`, "Update a running install"); the check can be turned off. `http://<miner>/settings` (user `root`, password `root`, as the stock web
  login) changes the work level, the night mode (time zone picked from a list), the day
  fan (automatic at the MM's or a cooler target, with an optional minimum duty under it;
  the night window always runs the MM's automatic fan alone), the pools, the electricity price and
  an optional coinbase tag (up to 4 ASCII characters, written into the part of an
  8-byte extranonce2 that the K210 never touches, so a block this miner finds carries
  them). Settings are saved in nano3d's own file `/data/nano3-os/config.json`, which
  overrides the stock configuration; command-line flags override both. A pool change
  restarts `nano3d` (exit code 4). `POST /api/settings` takes JSON only, with
  `Content-Type: application/json`, and refuses a foreign `Origin`.
- Security: the page is plain HTTP on the LAN with the stock `root` / `root` login
  (`web_password` in the settings file changes it). Reading the status needs no login.
  Do not expose port 80 to the Internet.
- Install: `FLASHING.md`, route 1. The slot A start script is `tools/nano3-os-start.sh`;
  `tools/nano3-os-guard.sh` goes in the root filesystem and keeps SSH and Wi-Fi up and brings
  the board back to nano3-os after a fallback to the stock app.

### Disclosure

This is an independent reverse-engineering study of hardware the author
owns, performed on a single unit on the author's own bench and local
network, for research and to develop alternative mining software. All
findings come from a device owned by the author. No vendor systems were
accessed, probed, or attacked at any point.

The work used three sources only: a read-only filesystem and flash dump
taken over SSH from the owner's unit (SSH access came from the public
"nanojb" jailbreak; see section 5), static analysis of the stock
`btcminer` binary, and passive live capture of the one UART that carries
the mining protocol. The device kept mining to the owner's own pool
throughout.

Unlike the A3197S report in this repository, this companion includes the
full protocol tables and the driver source. The author's goal is an
openly documented, independently drivable Nano 3.

---

## Executive Summary

To the author's knowledge, this is the first public documentation of the
Canaan Avalon Nano 3 (A3198S) host-to-controller mining protocol.

The Nano 3 is not built like the Nano 3s. The 3s report describes a
dual-core K230D in which a second RISC-V core drives the ASIC chain and
talks to Linux through shared memory. The Nano 3 is different: Linux sees
a single K230 hart, and the ASIC chain is owned by a separate Kendryte
K210 microcontroller (the "MM"). Linux talks to the K210 over a plain
UART (`/dev/ttyS1`, 115200 8N1). There is no second RISC-V core, no
remoteproc, and no shared-memory ring on this device.

That makes the protocol tractable from a single serial line. This study
recovered the complete framed K230-to-K210 protocol: the `CN` frame and
its CRC-16/XMODEM, every packet type in both directions, the bring-up
handshake, the per-job work burst (parameters, rolled versions, target,
coinbase, Merkle branches, header template), the polling cycle, and the
nonce-report format including its retry rule.

The protocol was verified three ways. It was read out of the stock
`btcminer` disassembly, cross-checked against an independent Ghidra pass,
and then confirmed on the wire against more than 7,000 live frames in both
directions with every CRC valid and no byte falling outside a frame. 68 of
68 checkable nonces captured live re-hashed to valid shares from their
captured job frames.

Finally it was proven end to end by an independent driver that runs on a
PC: it speaks Stratum to a pool, builds the work burst itself, sends it to
the K210 through a thin UART-over-TCP bridge on the miner, reads the nonce
reports back, reconstructs and submits shares, and had those shares
accepted by a real pool with zero rejects and zero CRC errors. The pool's
own accepted-share accounting is the correctness authority behind every
claim of "works" in this report.

---

## 1. The A3198S ASIC

### 1.1 What it is

| Parameter | Detail |
|-----------|--------|
| Vendor | Canaan (Avalon) |
| Class | Avalon-Nano-class SHA-256 mining ASIC |
| Model string | `A3198S` (reported in the stock status frame, type 0x51) |
| Cores per chip | 177 (strongly indicated; see 1.3) |
| Chips on this unit | 10 |
| Algorithm | SHA-256 with version-rolling (AsicBoost) |
| Documentation | None public; protocol undocumented prior to this work |

A scope note up front: this study did not probe the chip-to-chip ASIC bus
with a logic analyzer. On the Nano 3 the A3198S chain is owned entirely by
the K210 controller, which this study treats as a black box. The evidence
here is the K230-to-K210 protocol and the stock `btcminer` binary, not the
chip-level silicon bus. What the A3198S does internally is inferred from
the controller protocol and the binary, not measured at the chip pins.

### 1.2 Version-rolling and the nonce report

The A3198S is a conventional SHA-256 miner underneath the controller. The
host supplies four rolled block-version words per job (packet 0x27); the
chip searches them across its engines. Each result the controller returns
(packet 0x50, 16 bytes per record) carries the nonce, the extranonce2 the
result belongs to, a job tag, the ASIC index that found it, an ntime
offset, and the index of which of the four rolled versions was used (the
low nibble of the last byte). Reconstructing the exact header that each
result was found against, including the rolled version and the rolled
ntime, is the single correctness detail that the whole submit path turns
on. See `analysis/PROTOCOL.md` section 3 for the full record layout and
the two-snapshot retry rule.

### 1.3 Core count and clock domains

The per-ASIC counter frame (packet 0x52) reports four PLL core counts per
chip. In every frame of every chip those four counts sum to exactly
**177**, while the split between domains changes over time (one chip was
observed moving from 0/99/75/3 to 0/176/1/0; another reached 0/177/0/0).
This strongly indicates 177 cores per A3198S, which the controller
distributes at run time between the PLL1, PLL2 and PLL3 frequency domains.
The PLL0 domain (100 MHz) never holds cores.

### 1.4 On-chip difficulty target and telemetry

The chip gates which results it reports on a 256-bit difficulty target
that the host writes (packet 0x25). The stock driver caps that target at
difficulty 4096. Each chip also publishes per-chip PVT telemetry: a die
temperature and a core voltage (packet 0x54), plus per-chip and aggregate
work counters. The live unit ran at roughly 89 to 93 C against a 90 C
target.

---

## 2. Host Platform - Avalon Nano 3

### 2.1 System overview

| Component | Specification |
|-----------|--------------|
| SoC | Kendryte K230 (device-tree model `kendryte,k230`) |
| CPU visible to Linux | 1 hart, `rv64imafdcvxthead` (XuanTie T-Head), sv39 |
| ASIC controller | A separate Kendryte K210 microcontroller ("MM") on `/dev/ttyS1` |
| ASIC | 10x A3198S |
| DRAM | ~126 MiB mapped (129016 KiB), ~103 MiB available to Linux |
| Flash | 128 MiB SPI-NAND (Winbond; 2048 B page, 64 B OOB, 128 KiB erase) |
| Linux kernel | 5.10.4 (XuanTie T-Head); SBI v0.3 (TIME/IPI/RFENCE/HSM) |
| Userland | Buildroot 2021.02-git, BusyBox v1.33.0 |
| Display | ST7789V SPI LCD, 240x240 |
| Connectivity | RTL8723 USB/SDIO Wi-Fi |

### 2.2 The controller split (K230 Linux to K210 MM)

The defining fact of the platform, and the main difference from the Nano
3s, is that the ASIC chain is not driven by a second core of the K230. It
is driven by a distinct Kendryte K210 microcontroller. Linux reaches it
over one UART:

```
        +-------------------------------------------+
        |  K230  (Linux, single hart)               |
        |   btcminer: pool client, work scheduling, |
        |   nonce verification, power/sensors, fans, |
        |   web UI / cgminer API                     |
        +---------------------+---------------------+
                              |  UART /dev/ttyS1
                              |  115200 8N1, "CN" framed, CRC-16/XMODEM
        +---------------------+---------------------+
        |  K210  "MM" microcontroller               |
        |   owns the A3198S chain: bring-up,        |
        |   per-chip config, work dispatch,         |
        |   nonce collection, telemetry             |
        +---------------------+---------------------+
                              |  ASIC chain bus (not probed here)
                      +-------+--------+
                      |  10 x A3198S    |
                      +-----------------+
```

There is no `remoteproc`, `rpmsg`, or IPC device visible from Linux. A
single GPIO line (gpio61) is driven high at `btcminer` start and holds the
K210 running; `btcminer` does not reset the K210 on start (the K210
answered a detect in 24 ms). Everything the mining stack does to the chips
goes through the one UART, which is exactly why the protocol on that UART
is enough to drive the hardware independently.

### 2.3 Flash layout

128 MiB SPI-NAND with redundant A/B slots for the bootloaders, kernel,
root filesystem and application area. The live root is the "b" rootfs
slot; the application area (`/mnt/heater`, holding `btcminer` and the web
UI) is the slot chosen by the u-boot `app_partition` environment variable.
Full partition table in `PHASE1-RECON.md`.

### 2.4 Boot and services

`btcminer` is launched as root from `/etc/init.d/rcS` and serves the web
UI (port 80) and a cgminer-compatible API (port 4028). `swupdate` listens
on port 9090 for firmware upload. The stock credentials and the exposed
services are covered as security findings in section 5 and `SECURITY.md`.

---

## 3. The K230-to-K210 Protocol

This is the core of the study. The summary below is enough to recognise
the protocol on the wire; the field-level tables, every packet type, and
the instruction addresses that back each claim are in
`analysis/PROTOCOL.md` and its three companions (`protocol-job.md`,
`protocol-rx.md`, `protocol-control.md`), with two independent Ghidra
cross-checks in `analysis/crosscheck-*.md`.

### 3.1 Transport and frame

| Item | Value |
|------|-------|
| Port | `/dev/ttyS1` (K230 UART1) |
| Settings | 115200 baud, 8N1, raw, no flow control |
| Writer | one UART write of the whole frame under a global mutex; frames never interleave |
| Reader | a thread resynchronises on the `CN` magic, length-checks and CRC-checks each frame, and queues whole frames |
| Request/response | send, then wait up to 200 ms for a reply, up to 5 attempts |

```
off  size  field
0    2     magic "CN" (0x43 0x4E)
2    2     CRC-16/XMODEM, little endian, over bytes [4, 12+len)
4    1     type
5    1     opt (always 0)
6    2     idx, u16 LE (frame index within a multi-frame group)
8    2     cnt, u16 LE (frames in the group)
10   2     len, u16 LE (payload length, max 0x80)
12   len   payload
```

The CRC is CRC-16/XMODEM (polynomial 0x1021, init 0, MSB first, no final
xor). The same routine hashes the Stratum job-id string into the 16-bit
job tag that appears in job and nonce frames.

### 3.2 Packet types (overview)

K230 to K210: detect (0x10), sync (0x12), job parameters (0x20), job id
(0x21), coinbase (0x22), Merkle branches (0x23), header template (0x24),
target (0x25), job finish (0x26), rolled versions (0x27), set-finish
(0x31), set-frequency (0x32), poll (0x33), set-work-level (0x41),
set-voltage (0x43), level parameters (0x70), firmware upgrade (0x90).

K210 to K230: detect ack (0x11), sync ack (0x13), nonce report (0x50),
main status (0x51), per-ASIC counters (0x52), frequency report (0x53),
per-ASIC PVT (0x54), power supply (0x55), status (0x60), level-parameter
table (0x71), upgrade ack (0x91).

Endianness differs per packet type; the per-type tables in
`analysis/` give the byte order for each field.

### 3.3 Bring-up

1. `0x10` detect, expect `0x11` detect-ack (carries the controller
   identity, firmware version, hardware/software type, and the maximum
   work level).
2. `0x12` sync (echoes the identity), expect `0x13`.
3. `0x41` set-work-level.
4. local init (target temperature, counters), then `0x31` set-finish.

### 3.4 Job burst

On every `mining.notify` for the current pool, the host sends, in order:

```
0x20 parameters -> 0x27 rolled versions -> 0x25 target -> [0x21 job id]
-> 0x22 coinbase (ceil(len/128) frames) -> 0x23 Merkle branches
-> 0x24 header template (4 frames of 32 bytes) -> 0x26 finish
```

The host hands the K210 the raw coinbase with a zeroed extranonce2 slot,
plus the extranonce2 offset, size, per-chip start and range. The K210
rolls extranonce2, ntime and version itself and computes each chip's
Merkle root for the chip's own extranonce2. A clean-jobs flush makes the
next `0x20` carry a restart word so the controller drops stale work.

### 3.5 Polling and results

The host polls with `0x33` on a ~100 ms loop. The controller answers with
exactly one frame per poll, rotating through the telemetry and status
types and inserting a nonce report (`0x50`) whenever it has results. A
nonce record carries the job tag, the pool index, the extranonce2, the
nonce in block-header order, the ASIC index, an ntime offset, and the
rolled-version index. The submit path matches the record's job tag against
the kept job snapshots and, on a diff-1 miss, applies a one-step retry
against the adjacent extranonce2 round. Full rule in `analysis/PROTOCOL.md`
section 3.

---

## 4. Performance (stock firmware, live)

Captured on the live unit running the stock firmware:

| Metric | Value |
|--------|-------|
| Hashrate | ~4.1 TH/s (controller-reported GHSmm 4129.44) |
| Active chips | 10 / 10 |
| ASIC temperature | 89 to 93 C against a 90 C target |
| Work level | 2 of 3 |
| PLL frequencies at level 2 | 100 / 574 / 594 / 614 MHz |
| On-chip target cap | difficulty 4096 |

The independent driver (section 6) was run against the owner's own pool
and had its shares accepted; see that section for the end-to-end result.

---

## 5. Security and Privacy Findings (stock firmware)

Stated as conclusions, with the location in the binary where relevant, not
as exploit recipes, consistent with responsible disclosure. Full detail
and per-claim evidence are in `SECURITY.md`.

| Area | Finding |
|------|---------|
| Accounts and SSH | Stock firmware has an `admin` account with full `sudo` but no known default password, and does not start SSH. On this unit SSH and `admin` / `admin` came from the public "nanojb" jailbreak. Web UI reported root / root. |
| Web UI timezone injection | The timezone setting is passed to a shell unvalidated, so web UI access gives root. This is the jailbreak's entry point. |
| Unauthenticated API | cgminer API on port 4028 with no authentication; it accepts write commands. |
| World-readable OTA key | The OTA AES key/IV file is world-readable and in the dump. |
| API upgrade heap overflow | The firmware-upgrade path writes attacker-controlled chunks into a heap buffer with no end-of-buffer check. |
| SIGUSR1 starts a flash | A `SIGUSR1` to the root-run miner begins a K210 firmware flash. |
| Outbound telemetry | Not re-tested on this unit; the stock Nano 3s report documents stock phone-home behaviour. |

This work exploited none of these. The two code-path findings were confirmed
by reading the disassembly; no working proof was built. The timezone injection
is known from its use: the unit was jailbroken through it before the work began.

---

## 6. The Independent Driver

Because the Nano 3 exposes the entire mining protocol over one UART, the
driver does not have to run on the miner. It runs on a PC, which made
iteration fast and kept the analysis cleanly separated from the device.

Three small tools, all in `tools/`:

- **`uartbridge.c`** (static riscv64): a thin relay that runs on the miner
  and forwards `/dev/ttyS1` to and from a TCP socket, so the protocol logic
  can run on another machine. It takes an exclusive lock on the UART and
  supports a clean shutdown (a `BRXT` control word on the socket) so the
  stock `btcminer` can be restarted at once instead of waiting for its
  watchdog. It has no authentication, so it listens on `127.0.0.1` only
  and the PC reaches it through an SSH tunnel
  (`ssh -N -L 9113:127.0.0.1:9113 admin@<miner>`).
- **`nano3ctl.c`** (static riscv64): a read-only tool that runs a detect
  and polls without ever sending work, for safe observation.
- **`nano3miner.py`**: the host-side driver. It speaks Stratum V1 with
  version-rolling to a pool, builds the whole job burst, sends it to the
  K210 through the bridge, reads the nonce reports, reconstructs each
  share against its rolled version and rolled ntime, and submits.

The build recipes (`*.nix`) pin an exact nixpkgs revision and cross-compile
static riscv64 binaries, so the on-device tools have no runtime
dependencies.

### 6.1 What it proved

- **Static vs wire:** every CRC valid across more than 7,000 live frames in
  both directions, and 68 of 68 checkable captured nonces re-hashed to
  valid shares from their captured job frames.
- **Our own work accepted by the controller:** job bursts built entirely
  by `nano3miner.py` were accepted by the K210, which returned valid nonce
  records for them.
- **Pool acceptance:** shares reconstructed and submitted by the driver
  were accepted by a real Stratum pool with zero rejects and zero CRC
  errors (for example, a run of 5 of 5 accepted and an earlier run of 11
  of 11 accepted). The pool's accepted-share accounting is the final
  authority.

### 6.2 The fixes that mattered

Three corrections moved the submit path from rejected to accepted, each
confirmed only when the pool accepted the result:

1. **Version rolling:** submit the rolled-version delta against the base
   version, not the full version word.
2. **Previous-block hash:** the notify prev-hash is word-swapped before it
   goes into the header template.
3. **Coinbase extranonce2 width:** write exactly the pool's extranonce2
   size into the coinbase slot without resizing the buffer.

---

## 7. Methodology

**Phase 1 - Recon and dump.** Read-only inspection of the running stock
system over SSH, and a full read-only filesystem and flash dump. Hardware,
flash layout, boot sequence, services, and the K210 firmware header. See
`PHASE1-RECON.md`.

**Phase 2 - Static analysis.** Disassembly of `btcminer` with
`llvm-objdump` using the T-Head vendor extensions (the stock Ghidra
decompiler does not model those instructions and silently drops byte swaps
and indexed loads, so it was used only as a cross-check, never to overrule
the objdump reading). Recovery of the frame format, every packet type, the
bring-up, job and polling sequences, and the nonce-report format. Two
independent Ghidra cross-checks (`analysis/crosscheck-*.md`).

**Phase 3 - Live capture.** A static riscv64 `strace` attached to the
running `btcminer` on `/dev/ttyS1`, capturing passive and full-bring-up
traces totalling more than 7,000 frames, decoded and nonce-verified with
the tools in `tools/`.

**Phase 4 - Independent driver and pool verification.** The PC-side driver
over the UART-over-TCP bridge, submitting to a real pool, with
pool-accepted shares as the correctness authority.

---

## 8. Differences from the A3197S / Nano 3s report

This report is a companion to the A3197S report in the repository root, not
a copy of it. The hardware and the architecture differ:

| Aspect | Nano 3s (A3197S report) | Nano 3 (this report) |
|--------|-------------------------|----------------------|
| ASIC | A3197S | A3198S |
| Chips on the unit | 12 | 10 |
| Cores per chip | 10 SHA-256 engines (~985 cores stated) | 177 cores (indicated by live PLL core counts) |
| SoC | K230D, dual-core | K230, single Linux hart |
| ASIC controller | a second RISC-V core of the K230D | a separate Kendryte K210 microcontroller |
| Host-to-controller link | shared-memory rings + a message channel | one UART (`/dev/ttyS1`), `CN` framed, CRC-16/XMODEM |
| Flash | 256 MiB SPI-NAND | 128 MiB SPI-NAND |
| ASIC chain bus | characterised by logic-analyzer capture | not probed; controller treated as a black box |
| Disclosure | low-level detail withheld | full protocol tables and driver source included |

The two reports agree on the Avalon-Nano design language (A/B flash slots,
a target-temperature "heater" control loop, the same default-credential and
telemetry posture) while documenting genuinely different silicon and a
genuinely different control architecture.

---

## 9. Conclusion

The Avalon Nano 3 drives its A3198S chips through a separate K210
controller over a single UART, and that protocol is now fully documented
and proven. The complete framed protocol was recovered from the stock
binary, confirmed against thousands of live frames, and then driven
independently by a PC-side driver that mined to a real pool with accepted
shares and zero rejects.

The practical consequence is that the Nano 3 is straightforward to drive
from independent software: anything that can open the UART and speak the
`CN` protocol can feed the chips work and collect results, without any
vendor software in the loop.

---

## Repository layout (this directory)

| Path | Content |
|------|---------|
| `README.md` | This report |
| `PHASE1-RECON.md` | Hardware, flash layout, boot sequence, services, K210 firmware header |
| `SECURITY.md` | Security findings in the stock firmware |
| `FLASHING.md` | Flashing notes (A/B slots, recovery) |
| `JOURNAL.md` | Chronological work log |
| `analysis/PROTOCOL.md` | Protocol overview: frame, every packet type, sequences, live validation |
| `analysis/protocol-job.md` | Job and work frames (K230 to K210) |
| `analysis/protocol-rx.md` | Polling and every K210 to K230 response |
| `analysis/protocol-control.md` | Detect/sync, frequency, voltage, work level, upgrade |
| `analysis/crosscheck-*.md` | Two independent Ghidra cross-checks |
| `analysis/NOTES.md` | Binary overview, UART settings, framing notes |
| `tools/` | `uartbridge.c`, `nano3ctl.c`, `nano3miner.py`, the `*.nix` build recipes, and the capture/decode helpers |

Not included here, and kept out of the public repository on purpose: the
raw filesystem and flash dump, the vendor `btcminer` binary and its
disassembly, and the live captures. Those contain vendor binaries and
device secrets (the shadow file, the SSH host key, the OTA key). Local
network addresses, the owner's pool and wallet, and the unit's hardware
identity have been redacted from the documents that are included.

### License

nano3-os and this report are released under the MIT License (`LICENSE`). The
`nano3d` binary also contains `golang.org/x/image`, `golang.org/x/text` and the Go
fonts, which are under their own BSD-style licenses (Copyright The Go Authors; the
fonts by Bigelow & Holmes). Running it on a miner, or flashing it, is at your own
risk: see `FLASHING.md` and `SECURITY.md`.

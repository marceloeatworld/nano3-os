# Avalon Nano 3: K230 <-> K210 (MM) protocol

Reconstructed by static analysis of the stock `btcminer` (sha256 `e6c11630...6751`), 2026-10-27.
This file is the overview. Field-level tables with instruction addresses are in:

- `protocol-job.md`: job/work frames, K230 -> MM
- `protocol-rx.md`: polling and every MM -> K230 response
- `protocol-control.md`: bring-up, frequency, voltage, work level, factory params, firmware upgrade

Tags used in those files: CONFIRMED = seen in instructions; INFERRED = reasoned from names, strings or arithmetic.
Section 7 adds LIVE results: three strace captures of the running miner (passive 20 s, passive 180 s, full bring-up after a restart), 7,000+ frames, every CRC valid, and 68 of 68 checkable nonces re-hashed to valid shares from the captured job frames.

Validation sources, in order of authority: the live captures (`../captures/`), then the llvm-objdump disassembly with T-Head extensions (`btcminer.annot.asm`), then Ghidra pseudocode (`ghidra/`). Ghidra does not model the T-Head vendor instructions and silently drops byte swaps, indexed loads and the response jump table, so it is never used to overrule the other two. Two independent Ghidra cross-checks (`crosscheck-control.md`, `crosscheck-job-rx.md`, 2026-10-27) confirmed 161 of 269 claims directly and found no wrong wire byte, type code, offset or byte order; the one real protocol correction they surfaced (the 0x50 retry rule) is folded into section 3 below.

## 1. Transport

| Item | Value |
|---|---|
| Port | `/dev/ttyS1` (K230 UART1, 0x91401000) |
| Settings | 115200 baud, 8N1, raw, no flow control, VMIN=1 VTIME=0 |
| Writer | `data_toast_send`: one `uart_write` of 12+len bytes under a global mutex (frames never interleave) |
| Reader | `data_toast_recv_thread`: resyncs on "CN", checks len <= 0x80 and CRC, queues whole frames (5 slots of 0x8c bytes, oldest overwritten) |
| Request/response | `avalonnano_xfer_pkg` (inlined): send, wait up to 200 ms for any queued frame, up to 5 sends |

## 2. Frame

```
off  size  field
0    2     magic "CN" (0x43 0x4E)
2    2     CRC16, little endian, over bytes [4, 12+len)
4    1     type
5    1     opt, always 0 in every sender
6    2     idx, u16 LE: 0-based index of this frame in its group (0 for single frames)
8    2     cnt, u16 LE: frames in the group (1 for single frames)
10   2     len, u16 LE: payload length, max 0x80
12   len   payload
```

CRC: CRC-16/XMODEM (poly 0x1021, init 0, MSB first, no final xor). The same routine also hashes stratum job id strings (see 0x21 and 0x50).

Example, poll request with zero payload (CRC not filled in): `43 4E cc cc 33 00 00 00 01 00 04 00 00 00 00 00`.

The receive side reads only `type`, `idx` (for 0x52, 0x54) and `len`; it ignores `opt` and `cnt`.

## 3. Packet types

Endianness differs per packet type. Check the "Byte order" column before encoding anything.

### K230 -> MM

| Type | Name | Payload | Byte order | Sent how | Detail |
|---|---|---|---|---|---|
| 0x10 | AVANANO_P_DETECT | 4 zero bytes | n/a | request/response, expects 0x11 | control 3.1 |
| 0x12 | AVANANO_P_SYNC | 17: HDNA echo (16) + byte 1 | n/a | request/response, expects 0x13 | control 3.3 |
| 0x20 | stratum params (unnamed) | 28, or 32 after a flush | BE u32 | job burst | job 6.1 |
| 0x21 | JOB_ID | 4: crc16(job_id) + pool_no | BE crc16, LE pool_no | job burst, only when changed | job 6.4 |
| 0x22 | coinbase chunk | <= 128 per frame, idx/cnt | raw bytes | job burst | job 6.5 |
| 0x23 | merkle branch | 32 per frame, idx/cnt | raw bytes | job burst | job 6.6 |
| 0x24 | header template | 4 frames x 32 = 128-byte `header_bin` | each u32 word byte-reversed | job burst | job 6.7 |
| 0x25 | TARGET | 32: 256-bit target (difficulty capped at 4096) | LE | job burst | job 6.3 |
| 0x26 | stratum finish | 0 | n/a | job burst, last frame | job 6.8 |
| 0x27 | rolled versions | 16: four u32 versions (version OR mask parts) | BE u32 | job burst | job 6.2 |
| 0x31 | SET_FINISH | 0 | n/a | fire and forget, end of bring-up | control 3.6 |
| 0x32 | SET_FREQ | 32: 4 PLL MHz, 100, ASIC timeout, 0, ASIC selector | LE u32 | pending slot | control 4 |
| 0x33 | poll (unnamed) | 4: 0, or `00 00 00 01` to request MM reset | LE u32 | request/response every poll | rx 2 |
| 0x41 | SET_WORKLEVEL | 4: level | LE u32 | fire and forget at bring-up, else pending slot | control 3.5 |
| 0x42 | RESET_K210 | 0 | n/a | no caller found (dead code); real reset is a GPIO 61 pulse | control 6.4 |
| 0x43 | SET_VOLTAGE | 2: value 300..350 | LE u16 | pending slot | control 5.1 |
| 0x70 | SET_LEVELPARAM | 2 (cmd, level) or 16 (+ pout, temp, voltage, f0..f3) | LE u16 | pending slot | control 6.2 |
| 0x90 | AVANANO_P_UPGRADE | 128-byte chunks of the AUP payload, idx/cnt = chunk/total; end frame idx=cnt, len 0 | raw bytes | request/response per chunk, expects 0x91 | control 7.2 |

"Pending slot": the frame is parked at `info+0x3820` and sent by `avalonnano_scanhash` on its next pass, without waiting for any ack. Only one can be pending; a second producer fails ("set hash freq failed!!").

### MM -> K230

| Type | Name | Payload | Byte order | Handled in | Detail |
|---|---|---|---|---|---|
| 0x11 | AVANANO_P_DETECT_ACK | 88 live: "BTC\0", HDNA (16 ASCII), HVERSION (32, = K210 fw version), 16 bytes ("1366" live), u8 (0x10 live), HWTYPE\0, SWTYPE\0, max_level | ASCII | detect_modules | control 3.2, section 7 |
| 0x13 | SYNC_ACK | byte 3 = mm_status | n/a | detect_modules | control 3.4 |
| 0x15 | AVANANO_P_INFO_RES | not parsed (debug log only) | n/a | polling | rx 3.9 |
| 0x50 | nonce report | N x 16-byte records + trailing u32 | mixed, see below | polling | rx 3.1 |
| 0x51 | main status | >= 52: HashStatus, hashrates, ASIC count, temps, work level, target temp, SoftOFF, adapter | LE | polling | rx 3.2 |
| 0x52 | per-ASIC counters | >= 21: 2 counters, 4 PLL counts, 4 core freqs; ASIC in header idx | BE | polling | rx 3.3 |
| 0x53 | freq report | >= 12: 4 set freqs + nonce mask | LE | polling | rx 3.4 |
| 0x54 | per-ASIC PVT | >= 6: temp x10, voltage x10, raw; ASIC in header idx | LE u16 | polling | rx 3.5 |
| 0x55 | power supply | 1 byte `00` (no data) or 13: valid bit, u32, 4 x u16 (`PS[...]`; live 2738, 4, 124, 328) | LE | polling | rx 3.6, section 7 |
| 0x60 | logged as "AVANANO_P_GET_STATUS" | >= 24: 4 set freqs | BE u16 | polling | rx 3.7 |
| 0x71 | level parameter table | 3 + 14 x max_level (MPO, ADJ, BAR, then per level: power?, target temp, voltage, PLL0..3 MHz) | LE u16 | polling | rx 3.8, section 7 |
| 0x91 | UPGRADE_ACK | byte 0 = 1 on the end frame | n/a | upgrade_send_data | control 7.2 |

Any other type is logged as "Unknown response" and dropped.

### Nonce record (type 0x50), 16 bytes each

| Off | Field | Notes |
|---|---|---|
| 0..1 | job_id | BE, = crc16(stratum job_id string) |
| 2..3 | pool_no | LE |
| 4..7 | nonce2 | LE |
| 8..11 | nonce | block-header byte order |
| 12 | asic_id | must be < ASIC count |
| 13 | miner_id | must be 0 |
| 14 | ntime offset | seconds added to ntime |
| 15 | low nibble: version index n (0..3, picks one of the four 0x27 versions); high nibble: valid flag |

The trailing u32 after the records is a nonce2 wrap value (`wrap`), used only on the retry path below. TA is the ASIC count from 0x51.

Submit and retry (verified against both decode copies in the asm, 0x2da94..0x2dad2 and 0x2db88; cross-check row E27):
- The record's job_id (crc16) is matched against three kept snapshots in order pool0, pool1, pool2. On a match the host calls `submit_nonce2_nonce`.
- If that returns < 0 (hash fails diff1), the host retries once:
  - `nonce2 >= TA`: retry on the **same** snapshot that matched, with `nonce2 - TA`.
  - `nonce2 < TA`: retry on the **next older** snapshot (pool0 to pool1, pool1 to pool2, pool2 to none), if it exists and has a job_id, with nonce2 = `wrap - TA + 1 + nonce2`.
- Interpretation (INFERRED): a result arrives one round of TA nonce2 values late and belongs to the same job one round back; only when that would go below 0 does it belong to the end of the previous job, whose last nonce2 is `wrap`.
- `clear_new_nonce(thr)` (not `(thr, older)`) is called before the retry: it zeroes the cgpu last-nonce memory so the resubmit of the same nonce is not rejected as a duplicate.
- The 68 nonces captured live all passed on the first submit, so this retry path was never exercised on the wire. The per-job trailer values seen live roughly equal the previous job's final nonce2, consistent with this reading.

## 4. Sequences

### 4.1 Bring-up (`detect_modules`, at start and every 30 s while no module)

1. 0x10 DETECT -> expect 0x11 DETECT_ACK (5 x 200 ms). Store HDNA, HWTYPE, SWTYPE, max_level.
2. 0x12 SYNC (HDNA echo) -> expect 0x13; payload byte 3 = mm_status. Failure is not fatal.
3. 0x41 SET_WORKLEVEL: configured level if < max_level, else 1.
4. Local init: target temperature 90 C, counters cleared, module marked enabled.
5. 0x31 SET_FINISH.

### 4.2 Job burst (`avalonnano_sswork_update`, on every `mining.notify` for the current pool)

Checks first: stratum pool, merkles <= 30, nonce2 size >= 3, post-hash coinbase <= 6144 bytes.

```
0x20 params -> 0x27 versions -> 0x25 target -> [0x21 job id] -> 0x22 x ceil(cb/128)
-> 0x23 x merkles -> 0x24 x 4 -> 0x26 finish
```

No ack, no delay, send errors ignored. Frames = 8 + (1 if job id changed) + ceil(coinbase/128) + merkles.
A flush (clean jobs) sends nothing itself; it makes the next 0x20 carry the 32-byte form with restart word `00 00 00 01`.
The MM gets the raw coinbase with a zeroed nonce2 slot, plus the nonce2 offset, size (capped at 4), start (`device_id * range`) and range. It rolls nonce2, ntime and version itself.

### 4.3 Polling (`polling`, every `opt_avalonnano_polling_delay`, default 100 ms)

1. Drain and decode all queued MM frames.
2. Send 0x33 (payload `00 00 00 01` if a reset was requested by status 4 or 7 in 0x51).
3. Wait up to 5 x 200 ms; decode the first reply frame. Further replies wait for the next cycle.
4. After 30 consecutive failed polls the module is marked detached and bring-up runs again.

### 4.4 Settings from web/API

Frequency (0x32), voltage (0x43), work level (0x41), factory params (0x70) go through the pending slot and are sent on the next scanhash pass, without any ack.

### 4.5 K210 firmware upgrade

`/opt/upgrade_aup` header is checked (magic, hardware/software IDs, header and payload CRC32; the format_version field at 0x10 is only logged, not validated, per protocol-control.md), then the payload CRC32 + payload is sent as 0x90 chunks of 128 bytes, each acked by 0x91, then an end frame (idx = cnt, len 0) whose ack must have byte 0 = 1. If the payload size is an exact multiple of 128, the last chunk is dropped (probable stock bug).

## 5. Things that look like settings but never reach the K210

`--avalon10-freq`, `--avalon10-voltage`, `--avalon10-voltage-level`, the ASIC OTP setter, `softon`/`softoff`, `led`, `fan-spd`, `loop`, `pdelay`, `password`: they only write globals or are stubs. `hashpower 0` only logs. `factory resetfac` resets K230 config files only. Details in `protocol-control.md` section 1 and 5.

## 6. Open questions

Answered by the live captures (section 7):
- Reply to 0x33: exactly one frame per poll. The MM rotates 0x71, 0x51, 0x52, 0x54, 0x53, 0x55 and inserts 0x50 when it has nonces. 0x60, 0x15 never appeared.
- HDNA is ASCII (`0201000000000000` on this unit).
- nonce2 is written little endian into the coinbase at `n2_off`; the version index n selects word n of 0x27; header_bin words are byte-swapped on the wire.
- The restart word (`00 00 00 01` in a 32-byte 0x20) is sent on a new block, followed by an empty-block template and then the full template.
- GPIO 61 at start: `direction` <- "high", `value` <- "1" (hold the K210 running). btcminer start does not reset the K210 (it answered DETECT in 24 ms).

Still open:
1. Does the MM ack 0x32, 0x43, 0x70 (none were sent during the captures)? Needs a capture while changing settings in the web UI.
2. The 0x50 trailing u32 `wrap` (live values 0x8, 0x3f8, 0x3fc, 0x3fd, 0x400, 0x47a, 0x48e). It is the previous job's final nonce2 (see section 3 retry path); confirm whether it is exactly last-nonce2 or last+1.
3. With a pool nonce2 size above 4, which 4 bytes of the slot does the MM write? This pool uses 4.
4. MM behaviour on a missing or corrupt job chunk (no ack, no job sequence number).
5. Units: 0x43 voltage value (300..350; live per-ASIC PVT reads 311..315 at level voltage 330, so likely mV per ASIC), 0x55 values (27.38 V input? 124 W?), 0x71 first field (60/90/125: power in W?), DETECT_ACK "1366" and 0x10, SYNC_ACK bytes 0..2 (`01 03 01`), 0x51 bytes 1..4, 0x53 nonce mask (live 0x1).
6. 0x52 counter A: zero most of the time, then one rotation over all 10 ASICs with values near 4.0e8 (as BE u32), e.g. 403256064 on ASIC 2. Meaning unknown.
7. PLLCNT in 0x52: four counts per ASIC that sum to 177 in every frame of every ASIC, while the split changes over time (ASIC 7 went from 0/99/75/3 to 0/176/1/0; ASIC 8 reached 0/177/0/0). Strongly suggests 177 cores per A3198S, which the MM moves between the PLL1..PLL3 frequency domains (574/594/614 MHz) at run time. PLL0 (100 MHz) never holds cores.

## 7. Live validation (2026-10-27)

Method: static riscv64 strace (`../tools/strace-static.nix`) copied to `/tmp` on the miner, attached to `btcminer` with `-P /dev/ttyS1`. Capture and decoding tools: `../tools/decode_capture.py`, `../tools/verify_nonces.py`, `../tools/bringup-capture.sh`. Raw and decoded captures: `../captures/`.

| Capture | Length | Frames | Notes |
|---|---|---|---|
| cap1-passive-20s | 20 s | 178 polls, 178 replies, 1 job | attached with `-p`, clean detach |
| cap2-passive-180s | 180 s | 1600 polls, 1600 replies, 6 jobs | includes a new block (restart word) |
| cap3-bringup | ~4 min | 2108 polls, 7 jobs, full bring-up | btcminer restarted under strace |

Every frame in both directions had a valid CRC and no byte fell outside a frame.

| Item | Static prediction | Live |
|---|---|---|
| UART | 115200 8N1 raw | `TCSETS {c_cflag=B115200|CS8|CREAD|CLOCAL, c_iflag=, c_lflag=}` after `TCFLSH TCIFLUSH` |
| Frame, CRC, idx/cnt | as section 2 | confirmed on 7,000+ frames |
| Bring-up order | 0x10, 0x11, 0x12, 0x13, 0x41, 0x31 | identical, 46 ms total, first 0x33 110 ms later |
| Poll rate | 100 ms sleep + transfer | about 8.9 polls/s |
| Job burst order | 0x20, 0x27, 0x25, 0x21, 0x22..., 0x23..., 0x24 x4, 0x26 | identical on 14 jobs; coinbase chunks = ceil(cb_len/128) |
| 0x25 target | difficulty capped at 4096 | target `00000000000ffff0...` (difficulty 4096) |
| Nonce semantics | section 3 | 68/68 checkable nonces re-hash to >= 44 leading zero bits (difficulty 4096 floor, exponential above) |

Live device facts seen in the traffic:
- 10 ASICs, model string "A3198S" (0x51 Core). Not the A3197S of the Nano 3s report.
- About 4.1 TH/s (GHSmm 4129.44), ASIC temps 89..93 C against a 90 C target, work level 2 of 3.
- PLLs 100 / 574 / 594 / 614 MHz, equal to the level-2 record of 0x71.
- 0x71 records: (60, 80, 300, 100, 293, 313, 333), (90, 85, 310, 100, 447, 467, 487), (125, 90, 330, 100, 574, 594, 614).
- DETECT_ACK: `BTC\0`, HDNA `0201000000000000`, HVERSION `24071801_b906c52_6223725` (same build as `/data/upgrade_aup`), `1366`, 0x10, `PMMv1_X1`, `MM318_X2`, max_level 3.

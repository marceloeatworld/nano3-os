# K230 <-> K210 (MM) protocol: receive / polling path

Source binary: `btcminer` (see NOTES.md). Functions read: `polling` (0x2d060), `data_toast_receive` (0xb57a0),
`avalonnano_api_stats` (0x2ba10), plus targeted reads of `submit_nonce2_nonce` (0x22600),
`gen_stratum_work` (0x201c0), `parse_notify` (0x36360), `detect_modules` (0x2ad20),
`avalonnano_scanhash` (0x2e770), `lcd_hot_warn` (0xb9bf0), `lcd_adapter` (0xb9c10).
Static reading only.

## Conventions

- CONFIRMED: directly seen in instructions (address given). INFERRED: reasoned from names, format strings or arithmetic.
- `info` = `cgpu->device_data` (`ld s10, 0x28(s1)` @0x2d0d2). Offsets like `info+0x37e1` are byte offsets in that struct.
- In `polling` the RX frame buffer is at `sp+0x150` (0x8c bytes). Frame offset = sp offset - 0x150. Payload offset = sp offset - 0x15c.
  Example: `lbu 0x154(sp)` = frame byte 4 (type); `lw 0x15d(sp)` = payload bytes 1..4.
- "LE"/"BE" refers to the byte order on the wire. A plain `lw`/`lhu`/`ld` on RISC-V is LE. `th.revw` = 32-bit byte swap. The shift/or pattern `(x<<8 | x>>8)` + `th.extu ...,0xf,0x0` = 16-bit byte swap.
- Many payload fields are unaligned (odd offsets). The firmware uses plain unaligned loads.
- `polling` contains two inlined copies of `decode_pkg`: copy 1 drains queued frames (jump table 0x155640), copy 2 decodes the direct reply to the poll request (jump table 0x1556c8). Parsing is identical; only the tail differs (see section 1). Addresses below are copy 1 unless noted.
- gp = 0x239ba8 (`load_gp` @0x1555e). `gp+0x388` = `total_pools` (0x239f30), `gp+0x390` = `pools` (0x239f38). CONFIRMED via objects.txt.

## 1. Polling cycle

Caller: `avalonnano_scanhash` calls `polling(cgpu)` under a rwlock (@0x2e828). CONFIRMED.

1. `cgsleep_ms(opt_avalonnano_polling_delay)` @0x2d0a2. Global at 0x236a90, default 100 ms (bytes `64 00 00 00`). CONFIRMED.
2. If `info+0x3761` (module attached flag, set to 1 by detect_modules @0x2b388) is 0: return -1 @0x2d0ae. CONFIRMED.
3. Drain loop: `data_toast_receive(buf, 0)` @0x2d0cc; each frame goes to decode copy 1; loop until the queue is empty (`data_toast_receive` returns non-zero). CONFIRMED.
4. Build the poll request (type 0x33, section 2) @0x2d19c..0x2d1c6. If `info+0x3762` (reset request flag) is set, first clear `info+0x3761` and `info+0x3762` (`sh zero, 0x761` @0x2dc30), log "DO IN POLLING READY TO SEND RST", and set payload to 0x01000000 (`lui s2, 0x1000` @0x2dc40). CONFIRMED.
5. Send/retry: up to 5 attempts (retry idx 4..0). Each: `data_toast_send` @0x2d1ec, then `data_toast_receive(buf, 200)` @0x2d1f6. Log on failure: "send type=0x%X, retry idx=%d, interval=%d" (func name `avalonnano_xfer_pkg`, inlined). CONFIRMED.
6. On the first received frame: decode copy 2 (@0x2dbe2), then clear the failure counter `info+0x643c` (@0x2dca6) and return 0. This happens for any received frame: decoded types, 0x11/0x15, and unknown types all fall through to the same clear-and-return-0 path (`LAB_ram_0002dd84` -> `LAB_ram_0002dca0`). Only this one frame is decoded inline; any further reply frames stay queued and are decoded by copy 1 on the next call. CONFIRMED.
7. If all 5 attempts time out: `info+0x643c++` (u8) @0x2d25c. If it exceeds 29 (`li a5, 0x1d; bltu` @0x2d264): clear `info+0x3761` and log "%s-%d: Module detached! ID[%d]" (inlined `detach_module`) @0x2dd22. Return -1. CONFIRMED.
8. Re-detect: `avalonnano_scanhash` calls `detect_modules` when more than 30.0 s elapsed (constant 0x155a88 = 30.0) and `info+0x18` (MM Count) is 0 @0x2e7ae..0x2e8ae. CONFIRMED.

`data_toast_receive(buf, timeout_ms)` (0xb57a0): locks the queue mutex, copies one whole 0x8c-byte slot (raw frame starting with "CN") from slot `base+0x88+head*0x8c` (@0xb57da..0xb57ea), `head = (head+1)%5`, `count--`. Returns 0 on success. Queue empty and `timeout<=0`: returns -1 at once (@0xb5824). Otherwise waits on a condvar until `now+timeout_ms`; on timeout returns the `pthread_cond_timedwait` error code (non-zero). CONFIRMED. The receive thread does no type filtering (it only checks "CN", length <= 0x80 and CRC, and overwrites the oldest slot when full: "toast q overwrite"). CONFIRMED (string refs in `data_toast_recv_thread`).

Header bytes read by `polling`: only frame offset 4 (type), offset 6 (u16 LE, index, for 0x52 and 0x54) and offset 10 (u16 LE, payload length, for 0x50). Offsets 5, 8, 9 and the CRC are never read in this path. CONFIRMED (complete list of `sp+0x15x` accesses in `polling`).

## 2. Request sent by polling

### Type 0x33, K230 -> MM, name: unnamed (INFERRED "get status / poll")

Built @0x2d19c..0x2d1c6: `sh 0x4e43` at frame+0, `sd 0x0004000100000033` at frame+4, `sw s2` at frame+12, `crc16(frame+4, 12)` stored at frame+2. CONFIRMED.

| Frame offset | Value | Evidence |
|---|---|---|
| 0..1 | `43 4E` ("CN") | `sh a4` @0x2d1b2, a4 = 0x4e43 |
| 2..3 | CRC16/XMODEM LE over bytes 4..15 | `crc16(sp+0xc4, 0xc)` @0x2d1c2, `sh a0, 0xc2(sp)` @0x2d1c6 |
| 4 | 0x33 | low byte of 0x0004000100000033, `sd` @0x2d1b6 |
| 5 | 0x00 | same constant |
| 6..7 | 0x0000 | same constant |
| 8..9 | 0x0001 (bytes `01 00`) | same constant |
| 10..11 | payload length 4 (bytes `04 00`) | same constant |

Payload (4 bytes):

| Payload offset | Field | Size | Endian | Meaning | Evidence |
|---|---|---|---|---|---|
| 0..3 | flags | 4 | LE | 0x00000000 normally. 0x01000000 (bytes `00 00 00 01`) when a reset was requested (byte 3 = 1). CONFIRMED values; INFERRED meaning "ask MM to reset/reboot" from log "DO IN POLLING READY TO SEND RST" | `sw s2, 0xcc(sp)` @0x2d1b0, s2 = 0 @0x2d190 or 0x01000000 @0x2dc40 |

Full normal frame (CRC not computed here): `43 4E cc cc 33 00 00 00 01 00 04 00 00 00 00 00`.

The reset flag `info+0x3762` is set by the 0x51 handler when MM status byte is 4 or 7 (section 3.2).

The string "%s-%d-%d: AVANANO_P_GET_STATUS" is NOT used for 0x33; it is logged when a 0x60 frame is received (section 3.7). CONFIRMED.

## 3. Response dispatch (MM -> K230)

Dispatch on frame byte 4 @0x2d0ec..0x2d116 (copy 1) and @0x2dbfc..0x2dc2c (copy 2): types 0x50..0x71 via a 34-entry jump table indexed by `type-0x50`; types <= 0x4f compared against 0x11 and 0x15; everything else logs "%s-%d-%d: Unknown response 0x%X" (@0x2d14e / @0x2ddba). CONFIRMED.

| Type | Handler copy 1 | Handler copy 2 | Name | Content |
|---|---|---|---|---|
| 0x11 | 0x2d8bc | 0x2e3b8 | AVANANO_P_DETECT_ACK (log "rece AVANANO_P_DETECT_ACK ") | debug log only here |
| 0x15 | 0x2d874 | 0x2e516 | AVANANO_P_INFO_RES (log "rece AVANANO_P_INFO_RES ") | debug log only here |
| 0x50 | 0x2d664 | 0x2dfc4 | unnamed (INFERRED nonce report) | N x 16-byte nonce records + trailing u32 |
| 0x51 | 0x2d598 | 0x2e2b2 | unnamed (INFERRED main status) | MM status, hashrates, ASIC count, temps, work level |
| 0x52 | 0x2d480 | 0x2deac | unnamed (INFERRED per-ASIC counters/PLL) | per-ASIC counters, PLL counts, core freqs |
| 0x53 | 0x2d44c | 0x2ddde | unnamed (INFERRED freq/nonce-mask report) | 4 set freqs (LE) + nonce mask |
| 0x54 | 0x2d3ec | 0x2e1b4 | unnamed (INFERRED per-ASIC PVT) | per-ASIC temp/voltage/raw |
| 0x55 | 0x2d3c8 | 0x2e37c | unnamed (INFERRED power supply) | PS telemetry |
| 0x60 | 0x2d328 | 0x2e212 | logged as "AVANANO_P_GET_STATUS" | 4 set freqs (BE) |
| 0x71 | 0x2d2a4 | 0x2de1e | unnamed | MPO/ADJ/BAR + "ATA" records |

Jump table bytes (0x155640): 0x50->0x2d664, 0x51->0x2d598, 0x52->0x2d480, 0x53->0x2d44c, 0x54->0x2d3ec, 0x55->0x2d3c8, 0x60->0x2d328, 0x71->0x2d2a4, all others -> 0x2d118 (unknown). Copy 2 table (0x1556c8) maps the same types. CONFIRMED.

Types 0x56..0x5f, 0x61..0x70, 0x72..0xff and <= 0x4f other than 0x11/0x15: "Unknown response". CONFIRMED.

No handler checks the payload length, except 0x50 which uses it as a record count. The minimum lengths below are the highest byte read. INFERRED (from read offsets).

### 3.1 Type 0x50: nonce report (MM -> K230)

Header: off 5, 6, 8 not read. Off 10 = payload length; record count `N = len >> 4` (@0x2d664..0x2d66c, `srli 4`). CONFIRMED. If N = 0 the frame is ignored (@0x2d67e). CONFIRMED.
Payload = N records of 16 bytes, then one u32 at payload offset `16*N` (read unconditionally @0x2d674 `th.lrw a3, s3, (N<<4)`). INFERRED: len = 16*N + 4.

Record layout (record base r = 16*i):

| Rec offset | Field | Size | Endian | Meaning | Evidence |
|---|---|---|---|---|---|
| 0..1 | job_id | 2 | BE | `(r[0]<<8) | r[1]`. Compared with `crc16(pool->swork.job_id, strlen)` (same CRC16/XMODEM routine 0x14d6a0 as the frame CRC). CONFIRMED | lbu @0x2d6da, @0x2d6ec; combine @0x2d73a..0x2d73e; compare @0x2d744 |
| 2..3 | pool_no | 2 | LE | `(r[3]<<8) | r[2]`, index into `pools[]`. If `>= total_pools`: log "Wrong pool_no %d", record skipped, no HW error counted. CONFIRMED | @0x2d6ac..0x2d6ca; `pools[pool_no]` @0x2d712 |
| 4..7 | nonce2 | 4 | LE | Passed as `nonce2` to submit_nonce2_nonce, which stores it zero-extended in `pool->nonce2` (pool+0xce0); `gen_stratum_work` then copies `n2size` bytes of that u64 LE into the coinbase at `nonce2_offset`. CONFIRMED | `th.lurw a1` @0x2d6e4; submit @0x22674; memcpy @0x2023e..0x20256 |
| 8..11 | nonce | 4 | see note | Loaded LE then `th.revw`, passed as `nonce`. submit stores it at work->data+0x4c; the 80-byte header is word-swapped before SHA256 (@0x227e8 loop). Net effect: header bytes 76..79 = r[8], r[9], r[10], r[11]. So the record carries the nonce in block-header byte order (nonce u32 LE). CONFIRMED (byte flow) | `th.lurw a0` @0x2d6e0; `th.revw a4` @0x2d996; `sw s7, 0x4c(s0)` @0x227d8 |
| 12 | asic_id | 1 | - | Must be `< info+0x381d` (ASIC count); else log "Wrong miner_id:%d or asic_id:%d", record skipped. Used as index for per-chip counter MW. CONFIRMED | `zext.b s4, s8` @0x2d836; bound @0x2d842..0x2d84a |
| 13 | miner_id | 1 | - | Must be 0, else same "Wrong miner_id" log + skip. CONFIRMED | `th.extu s7, s8, 15, 8` @0x2d832; `bnez s7` @0x2d83e |
| 14 | ntime_offset | 1 | - | Passed as `ntime` arg: submit does `data.ntime = bswap(bswap(data.ntime) + n)` (adds n seconds to the BE ntime word) and `work+0x158 += n`. Running max kept in `info+0x37f8` (not printed). CONFIRMED | `th.extu s7, s8, 23, 16` @0x2d6f4; max @0x2d6fe..0x2d702; submit @0x2269c..0x226b0 |
| 15 bits 0..3 | micro_job (version index) | 4 bits | - | Passed as 7th arg n. submit: `work+0x140 = (1<<n) & 0xffff` (0 if n = 0), `work->data[0..3] = *(u32*)(pool + 0x64 + 4*(n ? 1<<n : 0))`. parse_notify fills that table with `bswap(version | mask)`: index 0,1 use mask at pool+0x140, index 2 uses pool+0x13c, index 4 uses pool+0x138, index 8 uses pool+0x134. So n = 0..3 selects one of 4 rolled block versions. CONFIRMED (table accesses); INFERRED that only n = 0..3 is valid (16-entry table, 1<<n overflows it for n > 3) | `th.extu a6, s8, 27, 24` @0x2d99a; submit @0x22772..0x22792; parse_notify @0x364f4..0x36554 |
| 15 bits 4..7 | valid | 4 bits | - | If zero the record is skipped silently. CONFIRMED | `th.extu a5, s8, 31, 28` @0x2d82c; `beqz` @0x2d830 |

Trailing field:

| Payload offset | Field | Size | Endian | Meaning | Evidence |
|---|---|---|---|---|---|
| 16*N | nonce2 wrap value | 4 | LE | Only used by the fallback nonce2 rewind below. INFERRED: last/maximum nonce2 value, i.e. nonce2 counts modulo (value+1) | @0x2d674, used @0x2daaa..0x2dab4 |

Submission flow per valid record (copy 1; copy 2 identical at 0x2e014..0x2e1b2, 0x2e4a0..0x2e600):

1. `real_pool = pools[pool_no]`. Job match is NOT done against `real_pool`. It is done against three stratum snapshots kept in `info`: pool0 at `info+0xc8`, pool1 at `info+0x12f8`, pool2 at `info+0x2528` (each 0x1230 bytes; `swork.job_id` pointer at +0xd00). Order: pool0 (@0x2d706), pool1 (@0x2d75a), pool2 (@0x2d7a4). Each mismatch logs "avalon12: job_id doesn't match! [%04x:%04x (%s)]" (inlined `job_idcmp`). CONFIRMED.
2. No match: log "Cannot match to any stratum! (0x%X)" with the job_id, `inc_hw_errors(thr)`, `info+0x6068++` (HW). CONFIRMED @0x2db90, @0x2d7fe..0x2d810.
3. Match on poolK: `ret = submit_nonce2_nonce(thr, poolK, real_pool, nonce2, nonce, ntime_offset, micro_job)` @0x2d9aa. CONFIRMED.
   - Return values (from submit_nonce2_nonce): 1 = hash meets diff1 (submitted async if it also meets the pool target, @0x228ce / @0x229a2); 0 = same nonce as the previous one on this cgpu ("duplicate share detected as HW error", @0x22aa8); -1 = hash word 7 non-zero, fails diff1 (@0x2281a..0x22820). CONFIRMED.
   - ret > 0: `info+0x6460 += cgpu->diff1 delta` (diff1 sum used for GHSavg/WU) and `info+0x6070 + 8*asic_id` += 1 (MW counter). CONFIRMED @0x2d9ba..0x2d9e2.
   - ret = 0: `info+0x6058++` (DHW). CONFIRMED @0x2d9ec..0x2d9f8.
   - ret < 0: retry once, with the branch chosen by nonce2 vs TA (TA = `info+0x37e8`, ASIC count from status 0x51; wrap = the trailing u32):
     - `nonce2 >= TA`: resubmit on the **same** snapshot that matched the job_id, with nonce2 = `nonce2 - TA`. The older snapshot is neither used nor checked, so a pool2 match still gets this retry. CONFIRMED @0x2db88, @0x2dab8..0x2dad2.
     - `nonce2 < TA`: resubmit on the **next older** snapshot (pool0 -> pool1, pool1 -> pool2, pool2 -> none), only if it exists and its job_id pointer is non-NULL, with nonce2 = `wrap - TA + 1 + nonce2`. CONFIRMED @0x2da94..0x2dab4.
     - Before the resubmit, `clear_new_nonce(thr)` (one argument) zeroes the cgpu last-nonce memory so the same nonce is not rejected as a duplicate. If the resubmit is not > 0: HW error path (`inc_hw_errors`, `info+0x6068++`). CONFIRMED @0x2dac0..0x2dad6.
   INFERRED interpretation: a result arrives one round of TA nonce2 values late and belongs to the same job one round back; only when `nonce2 - TA` would go below 0 does it belong to the end of the previous job, whose last nonce2 is `wrap`. The 68 nonces captured live all passed on the first submit, so this retry path was never exercised on the wire.
4. test_nonce / submit_nonce (the other cgminer helpers) are not called by `polling`. CONFIRMED (no call sites in polling).

Minimum length: 16*N + 4. INFERRED.

### 3.2 Type 0x51: main status (MM -> K230)

Header: off 5, 6, 8 not read. Minimum payload length 0x34 (52). INFERRED.

| Payload offset | Field | Size | Endian | Meaning / storage | Evidence |
|---|---|---|---|---|---|
| 0x00 | mm_status | 1 | - | Stored `info+0x37e1`, printed "HashStatus[%d]". 4: set reset flag `info+0x3762=1` and `lcd_hot_warn(0)` (sets LCD flag bit 0x4). 7: log "DO HASH READY TO REBOOT", set `info+0x3762=1`. Other: `lcd_hot_warn(1)` (clears bit 0x4). CONFIRMED. INFERRED: 4 = overheat, 7 = MM wants reboot | @0x2d598..0x2d5b2, @0x2db18, @0x2db6c; lcd_hot_warn @0xb9bf0; api_stats @0x2bac0 |
| 0x01..0x04 | unknown | 4 | LE | Stored `info+0x37e4` (u32). No reader found in avalon functions. CONFIRMED store | `lw` @0x2d5b6, `sw` @0x2d5c4 |
| 0x05..0x08 | LW | 4 | LE | Zero-extended to u64 at `info+0x6050`, printed "LW[%lu]". INFERRED: local work count | `lwu` @0x2d5c0, `sd` @0x2d5cc; api_stats @0x2bbd4 |
| 0x09..0x0c | GHSmm (MH/s) | 4 | LE | `info+0x647c` (u32). api_stats prints `value/1000.0` as "GHSmm[%.2f]", so raw unit is MH/s. CONFIRMED arithmetic | `ld 0x165(sp)` @0x2d5d2 (8 bytes), `sd` @0x2d5de; api_stats @0x2be6c..0x2be80 (const 0x155aa4 = 1000.0f) |
| 0x0d..0x10 | GHSspd (MH/s) | 4 | LE | `info+0x6480` (u32), printed `value/1000.0` as "GHSspd[%.2f]". CONFIRMED | same 8-byte copy; api_stats @0x2be1a..0x2be44 (const 0x153da8 = 1000.0) |
| 0x11..0x18 | DH | 8 | LE IEEE-754 double | `info+0x6468`, printed "DH[%.3f%%]". CONFIRMED (fld/fsd) | `fld 0x16d(sp)` @0x2d5ce, `fsd` @0x2d5da; api_stats @0x2bc60 |
| 0x19..0x20 | DHspd | 8 | LE IEEE-754 double | `info+0x6470`, printed "DHspd[%.3f%%]". CONFIRMED | `ld 0x175(sp)` @0x2d5d6, `sd` @0x2d5e8; api_stats @0x2be1e |
| 0x21..0x24 | TA (ASIC count) | 4 | LE | u32 at `info+0x37e8` ("TA[%d]"); low byte also at `info+0x381d`, which bounds asic_id in 0x50, indexes 0x52/0x54, and sizes every per-ASIC list in api_stats. Also the nonce2 rewind step. CONFIRMED | `lw 0x17d(sp)` @0x2d5e2; `sw` @0x2d5ee; `sb` @0x2d5f4; api_stats @0x2c034 |
| 0x25..0x2b | Core | 7 | bytes | Copied to `info+0x37ec..0x37f2`, printed "Core[%s]". CONFIRMED copy; INFERRED: ASIC/core model string | @0x2d5f8..0x2d61a; api_stats @0x2c04e |
| 0x2c..0x2f | MM temp max | 4 | LE (signed) | `info+0x3aa0`; "TMax[%d]" = max(this, max of all PVT_T). Initialized to -273 by detect. CONFIRMED; INFERRED unit deg C | `lw 0x188(sp)` @0x2d61e, `sw` @0x2d622; api_stats @0x2bd0c..0x2bd1c; detect @0x2b40e |
| 0x30 | work level | 1 | - | If `<= info+0x37e0` ("max level" from DETECT_ACK) and different from `syscfg_work_level()`: `syscfg_set_work_level(v)`, `dict_setint("syscfg","work_level",v)`, set dirty bit in dev_super byte 3. CONFIRMED | `lbu 0x18c(sp)` @0x2d60e; compare @0x2d632; @0x2da10..0x2da42 |
| 0x31 | target temp | 1 | - | `info+0x6484` and `set_target_temp(v)` ("TarT[%d]" via get_target_temp). Detect initializes it to 90. CONFIRMED; INFERRED unit deg C | `lbu 0x18d(sp)` @0x2d600; `sw` @0x2d626; call @0x2d62a; detect @0x2b3a8 |
| 0x32 | SoftOFF | 1 | - | `info+0x37f4`, printed "SoftOFF[%d]". CONFIRMED | `lbu 0x18e(sp)` @0x2d636, `sw` @0x2d644; api_stats @0x2c092 |
| 0x33 | adapter | 1 | - | `info+0x64d0`. 0xff: `lcd_adapter(0)` (sets LCD flag bit 0x40) and stores 0. Else `lcd_adapter(1)` (clears bit 0x40). CONFIRMED; INFERRED: power adapter status, 0xff = fault/unknown | `lbu 0x18f(sp)` @0x2d63a; @0x2d64e..0x2d65c, @0x2da00; lcd_adapter @0xb9c10 |

### 3.3 Type 0x52: per-ASIC counters, PLL counts, core frequencies (MM -> K230)

Header: off 6 = u16 LE global index `idx`. `miner = idx / N`, `asic = idx % N`, N = `info+0x381d`. Frame ignored if N = 0. No upper bound check on idx. CONFIRMED @0x2d480..0x2d498. Minimum payload length 0x15 (21). INFERRED.
Storage stride: 0x18 bytes per ASIC and 0xb40 per miner for the 0x5508 block; 0x10 per ASIC and 0x780 per miner for CF. INFERRED from this: max 120 ASICs per miner.

| Payload offset | Field | Size | Endian | Meaning / storage | Evidence |
|---|---|---|---|---|---|
| 0x00 | has_counters | 1 | - | If non-zero, the two counters below are stored; otherwise they are left unchanged. CONFIRMED | `lbu 0x15c(sp)` @0x2d490; `beqz` @0x2d4a0 |
| 0x01..0x04 | counter A | 4 | BE | `info+0x5508 + miner*0xb40 + asic*0x18`, printed as "C_%02d_00[...]" list | `lw` @0x2d4b0, `th.revw` @0x2d4c2, `sw` @0x2d4c6; api_stats @0x2c916.. |
| 0x05..0x08 | counter B | 4 | BE | `+0x550c`, printed "C_%02d_01[...]". "ERATIO%d[...]" = `B*100/(A+B)` "%6.2f%%" (0 if A = 0). CONFIRMED arithmetic; INFERRED A = good, B = error count | `lw` @0x2d4be, `th.revw` @0x2d4ca, `sw` @0x2d4ce; api_stats @0x2c8b0..0x2c8d8 (const 100.0) |
| 0x09 | PLLCNT[0] | 1 | - | `+0x5510` (widened to u32) | `lbu 0x165(sp)` @0x2d50a, `sw` @0x2d54e |
| 0x0a | PLLCNT[1] | 1 | - | `+0x5514` | @0x2d512, @0x2d552 |
| 0x0b | PLLCNT[2] | 1 | - | `+0x5518` | @0x2d516, @0x2d556 |
| 0x0c | PLLCNT[3] | 1 | - | `+0x551c`. Printed "PLLCNT%d_%d[...]"; "PLL%d[...]" = sum over ASICs per k | @0x2d51a, @0x2d55a; api_stats @0x2c7ca.., @0x2c170.. |
| 0x0d..0x0e | CF[0] | 2 | BE | `info+0x4d84 + miner*0x780 + asic*0x10` | `lhu 0x169(sp)` + swap @0x2d4dc..0x2d530, `sw` @0x2d576 |
| 0x0f..0x10 | CF[1] | 2 | BE | `+0x4d88` | @0x2d4f2.., `sw` @0x2d588 |
| 0x11..0x12 | CF[2] | 2 | BE | `+0x4d8c` | @0x2d4fa.., `sw` @0x2d58c |
| 0x13..0x14 | CF[3] | 2 | BE | `+0x4d90`. Printed "CF%d_%d[...]" | @0x2d4ee.., `sw` @0x2d590; api_stats @0x2c712.. |

Per-ASIC "GHSmm%02d[...]" = `sum_k(F[k]*PLLCNT[k]) / 1000.0`, where F = CF of that ASIC if HVer (`info+0x378d`) starts with "851", else the per-miner SF from 0x53/0x60. CONFIRMED @0x2ca96..0x2cb0a. INFERRED: CF/SF in MHz, PLLCNT = number of cores (or core groups) clocked from PLL k.

### 3.4 Type 0x53: set frequencies + nonce mask (MM -> K230)

Header not read. Minimum payload length 12. INFERRED.

| Payload offset | Field | Size | Endian | Meaning / storage | Evidence |
|---|---|---|---|---|---|
| 0x00..0x01 | SF[0] | 2 | LE | `info+0x4d74` (miner 0), printed "SF%d[...]" | `lhu 0x15c(sp)` @0x2d44c, `sw` @0x2d460 |
| 0x02..0x03 | SF[1] | 2 | LE | `info+0x4d78` | @0x2d450, @0x2d464 |
| 0x04..0x05 | SF[2] | 2 | LE | `info+0x4d7c` | @0x2d454, @0x2d468 |
| 0x06..0x07 | SF[3] | 2 | LE | `info+0x4d80` | @0x2d458, @0x2d46c; api_stats @0x2c216..0x2c24a |
| 0x08..0x0b | Nonce Mask | 4 | LE | `info+0x6498`, API field "Nonce Mask" (api_add_uint32) | `lw 0x164(sp)` @0x2d476, `sw` @0x2d47a; api_stats @0x2ba7e..0x2ba94 |

INFERRED: reply/confirmation to the 0x32 set-frequency command (frequencies in MHz). Note the same SF slots are written big-endian by 0x60.

### 3.5 Type 0x54: per-ASIC PVT (MM -> K230)

Header: off 6 = u16 LE ASIC index, must be `< info+0x381d`; `info+0x381c` (miner count) must be non-zero. Miner is not used (miner 0 slots only). CONFIRMED @0x2d3ec..0x2d40a. Minimum payload length 6. INFERRED.

| Payload offset | Field | Size | Endian | Meaning / storage | Evidence |
|---|---|---|---|---|---|
| 0x00..0x01 | temp x10 | 2 | LE | `float(v/10)` (integer division first) at `info+0x38c0 + asic*4`, printed "PVT_T%d[...]" with %3d; feeds TMax, MTmax, MTavg, TAvg. Detect initializes to -273.0. CONFIRMED; INFERRED unit 0.1 deg C | `lhu 0x15c(sp)` @0x2d40e, `divuw` @0x2d418, `fsw` @0x2d43c; detect @0x2b3d8 |
| 0x02..0x03 | voltage x10 | 2 | LE | `v/10` at `info+0x3aa4 + asic*0x20`, printed "PVT_V%d[...]". CONFIRMED; unit unknown (INFERRED mV) | `lhu 0x15e(sp)` @0x2d414, `divuw` @0x2d434, `sw` @0x2d440 |
| 0x04..0x05 | raw | 2 | LE | `info+0x4b84 + asic*4`, printed "PVT_R%d[...]" (only in extended stats). CONFIRMED; meaning unknown | `lhu 0x160(sp)` @0x2d428, `sw` @0x2d444; api_stats @0x2c65a.. |

### 3.6 Type 0x55: power supply telemetry (MM -> K230)

Header not read. Processed only if `payload[0] & 1` (@0x2d3c8..0x2d3ce). Minimum payload length 13. INFERRED.

| Payload offset | Field | Size | Endian | Meaning / storage | Evidence |
|---|---|---|---|---|---|
| 0x00 | flags | 1 | - | bit 0 = data valid. CONFIRMED | @0x2d3c8 |
| 0x01..0x04 | unknown | 4 | LE | `info+0x644c` (u32). No reader found | `lw 0x15d(sp)` @0x2d3d8, `sw` @0x2d3dc |
| 0x05..0x06 | PS a | 2 | LE | `info+0x6456`, 5th number of "PS[0 0 0 %d %d %d %d]" | `ld 0x161(sp)` (8 bytes) @0x2d3e0, `sd` @0x2d3e4 |
| 0x07..0x08 | PS b | 2 | LE | `info+0x6458`, 4th number | same |
| 0x09..0x0a | PS c | 2 | LE | `info+0x645a`, 6th number | same |
| 0x0b..0x0c | PS d | 2 | LE | `info+0x645c`, 7th number | same; api_stats @0x2bde6..0x2be00 (args order 0x6458, 0x6456, 0x645a, 0x645c) |

"PS" label comes from the literal `" PS["` built @0x2bdd0. The first three PS numbers are hard-coded 0. CONFIRMED. Field meanings (voltage/current/power) INFERRED only from the "PS" name.

### 3.7 Type 0x60: logged as AVANANO_P_GET_STATUS (MM -> K230)

Header not read. Debug log "%s-%d-%d: AVANANO_P_GET_STATUS" (level <= 20) @0x2da48. Minimum payload length 0x18 (24). INFERRED.

| Payload offset | Field | Size | Endian | Meaning / storage | Evidence |
|---|---|---|---|---|---|
| 0x00..0x01 | valid marker | 2 | BE | If 0xffff the frame is ignored. CONFIRMED; meaning unknown | swap16 @0x2d336..0x2d350 |
| 0x02..0x0f | unknown | 14 | - | not read | - |
| 0x10..0x11 | SF[0] | 2 | BE | `info+0x4d74` | `lhu 0x16c(sp)` + swap @0x2d354.., `sw` @0x2d3b6 |
| 0x12..0x13 | SF[1] | 2 | BE | `info+0x4d78` | @0x2d35c.., @0x2d3ba |
| 0x14..0x15 | SF[2] | 2 | BE | `info+0x4d7c` | @0x2d364.., @0x2d3be |
| 0x16..0x17 | SF[3] | 2 | BE | `info+0x4d80` | @0x2d36c.., @0x2d3c2 |

### 3.8 Type 0x71: MPO / ADJ / BAR / ATA records (MM -> K230)

Header not read. Record count = `info+0x37e0` ("max level", set by detect_modules from DETECT_ACK, log "max level:%d" @0x2b5ae). Minimum payload length `3 + 14*count`. INFERRED.

| Payload offset | Field | Size | Endian | Meaning / storage | Evidence |
|---|---|---|---|---|---|
| 0x00 | MPO | 1 | - | `info+0x64a1`, printed "MPO[%d]" | `lbu 0x15c(sp)` @0x2d2a4, `sb` @0x2d2ae; api_stats @0x2c4ea |
| 0x01 | ADJ | 1 | - | `info+0x64a2`, printed "ADJ[%d]" | `lhu 0x15d(sp)` @0x2d2b2, `sh` @0x2d2ba; api_stats @0x2c502 |
| 0x02 | BAR | 1 | - | `info+0x64a3`, printed "BAR[%d]" only when ADJ != 1 | same `sh`; api_stats @0x2c514..0x2c5ac |
| 0x03 + 14*i | ATA record i | 14 | 7 x u16 LE | Copied (8+4+2 bytes) to `dev_super()+0xfc+14*i` and `info+0x64a4+14*i`. api_stats prints the first 3 as "ATA%d[%d-%d-%d-%d:%d:%d:%d]" (7 u16 at +0,+2,...,+0xc; skipped if the first u16 is 0) | loop @0x2d2d0..0x2d322; api_stats @0x2c45e..0x2c584 |

INFERRED: per-work-level settings or schedule (one 14-byte record per work level). Meaning of MPO/ADJ/BAR/ATA unknown.

### 3.9 Types 0x11 (DETECT_ACK) and 0x15 (INFO_RES) in polling

`polling` only logs them ("rece AVANANO_P_DETECT_ACK " @0x2d8d6, "rece AVANANO_P_INFO_RES " @0x2d88e, debug level <= 20) and parses nothing. In copy 2 they still count as a reply (failure counter cleared). CONFIRMED.
Cross-reference only (owned by the control-command analysis): detect_modules parses the 0x11 reply to the 0x10 DETECT request (`li s0, 0x11` @0x2aec0). Fields that api_stats prints: payload 4..19 -> `info+0x377c` "HDNA[%s]" (@0x2af26..0x2af32), payload 20..51 -> `info+0x378d` "HVer[%s]" (@0x2af3e), a later byte -> `info+0x37e0` "max level" (@0x2b010). The SYNC ack (type 0x13, @0x2b2e6) payload[3] -> `info+0x37e1` "mm_status" (@0x2b45c). CONFIRMED addresses.

## 4. Device struct fields filled by the RX path (as printed by avalonnano_api_stats)

| info offset | Type | API label | Filled by |
|---|---|---|---|
| 0x18 | int | MM Count | not RX (cleared by scanhash when detached) |
| 0xc8 / 0x12f8 / 0x2528 | struct pool | (job match snapshots pool0/1/2) | job path (copy_pool_stratum) |
| 0x3761 | u8 | (attached flag; api_stats prints full stats only if set) | detect (1), polling RST/detach (0) |
| 0x3762 | u8 | (reset request) | 0x51 status 4 or 7 |
| 0x377c | char[16] | HDNA | DETECT_ACK |
| 0x378d | char[32] | HVer | DETECT_ACK |
| 0x37e0 | u8 | (max work level) | DETECT_ACK |
| 0x37e1 | u8 | HashStatus | 0x51[0], SYNC ack [3] |
| 0x37e4 | u32 | (none) | 0x51[1..4] |
| 0x37e8 | u32 | TA | 0x51[0x21..0x24] |
| 0x37ec | char[7] | Core | 0x51[0x25..0x2b] |
| 0x37f4 | u32 | SoftOFF | 0x51[0x32] |
| 0x37f8 | u32 | (none; max ntime offset) | 0x50 rec[14] |
| 0x381c | u8 | (miner count, 1) | detect |
| 0x381d | u8 | (ASIC count) | 0x51[0x21] low byte |
| 0x38c0 + m*0x1e0 + a*4 | float | PVT_T, TMax, MTmax, MTavg | 0x54[0..1]/10 |
| 0x3aa0 | s32 | TMax (with PVT_T) | 0x51[0x2c..0x2f] |
| 0x3aa4 + m*0xf00 + a*0x20 | u32 | PVT_V | 0x54[2..3]/10 |
| 0x4b84 + m*0x1e0 + a*4 | u32 | PVT_R | 0x54[4..5] |
| 0x4d74 + m*0x10 + k*4 | u32 | SF | 0x53[0..7] LE, 0x60[0x10..0x17] BE |
| 0x4d84 + m*0x780 + a*0x10 + k*4 | u32 | CF | 0x52[0xd..0x14] BE |
| 0x5504 | u32 | Vo | not RX (not written in polling) |
| 0x5508 + m*0xb40 + a*0x18 | u32 | C_mm_00, ERATIO | 0x52[1..4] BE |
| 0x550c + ... | u32 | C_mm_01, ERATIO | 0x52[5..8] BE |
| 0x5510 + ... + k*4 | u32 | PLLCNT, PLL, GHSmm%02d | 0x52[9..12] |
| 0x6050 | u64 | LW | 0x51[5..8] |
| 0x6058 | u64 | DHW | 0x50 duplicate (submit ret 0) |
| 0x6060 / 0x6068 | u64 | HW / MH | 0x50 HW errors (0x6068++, copied to 0x6060 by api_stats) |
| 0x6070 + m*0x3c0 + a*8 | u64 | MW | 0x50 accepted nonce per asic_id |
| 0x642c.. / 0x6430.. / 0x6438 | u32 | ECHU / ECMM / CRC | not written in polling (zeroed by detect) |
| 0x643c | u8 | (poll failure counter) | polling; also zeroed by `detect_modules` on re-attach |
| 0x644c | u32 | (none) | 0x55[1..4] |
| 0x6456..0x645c | 4 x u16 | PS | 0x55[5..12] |
| 0x6460 | u64 | GHSavg, WU, MGHS (diff1 sum) | 0x50 accepted nonces |
| 0x6468 | double | DH | 0x51[0x11..0x18] |
| 0x6470 | double | DHspd | 0x51[0x19..0x20] |
| 0x6478 | float | Freq | not written in polling (no writer found) |
| 0x647c | u32 | GHSmm (/1000) | 0x51[9..12] |
| 0x6480 | u32 | GHSspd (/1000) | 0x51[0xd..0x10] |
| 0x6484 | u32 | TarT (via set_target_temp) | 0x51[0x31] |
| 0x6498 | u32 | Nonce Mask | 0x53[8..11] |
| 0x64a1 / 0x64a2 / 0x64a3 | u8 | MPO / ADJ / BAR | 0x71[0] / [1] / [2] |
| 0x64a4 + 14*i | 7 x u16 | ATA%d | 0x71[3+14i..] |
| 0x64d0 | u32 | (adapter, LCD flag 0x40) | 0x51[0x33] |

api_stats derived values (CONFIRMED arithmetic): GHSavg = `info+0x6460 / elapsed * 4.294967296` (2^32/1e9), WU = `info+0x6460 / elapsed * 60`, elapsed = `tdiff(now, info+0x3768)`; MTavg = mean of positive PVT_T of device 0 (or -273).

## 5. Open questions

1. Resolved by live capture: the MM replies with exactly one frame per 0x33 poll, rotating 0x71, 0x51, 0x52, 0x54, 0x53, 0x55 and inserting 0x50 when it has nonces (0x60 and 0x15 never appeared). See PROTOCOL.md section 7.
2. Exact meaning of the 0x50 trailing u32 `wrap`: live values roughly equal the previous job's final nonce2 (consistent with the retry path in section 3.1); confirm whether it is last-nonce2 or last+1, and how the MM assigns nonce2 to ASICs.
3. Official names of types 0x50..0x55 and 0x71. The binary has no AVANANO_P_* string for them. The only name, "AVANANO_P_GET_STATUS", is logged for received type 0x60, not for the 0x33 request.
4. Meaning of 0x51 payload 1..4, 0x55 payload 1..4 and the 4 PS values, 0x60 payload 0..15, PVT_V/PVT_R units, ATA/MPO/ADJ/BAR, 0x52 counters A/B.
5. Whether micro_job values above 3 can occur (they index past the 16-entry version table at pool+0x64).
6. The version masks at pool+0x134..0x140 (set_vmask/parse_vmask). They decide which rolled version each micro_job value means; owned by the job-sending analysis.
7. `info+0x6478` (Freq) and `info+0x5504` (Vo) have no writer in the RX path.

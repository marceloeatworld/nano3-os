# K230 -> K210 (MM) job path: how a stratum job reaches the MM

Scope: `copy_pool_stratum`, `avalonnano_sswork_update` (with `avalon12_stratum_pkgs` inlined), `avalon12_sswork_flush`, `avalonnano_scanhash`, `avalon12_prepare`, plus the cgminer core code that triggers them. Static reading of `btcminer` only.

Status tags:
- CONFIRMED: seen directly in instructions or data at the cited address.
- INFERRED: reasoned from names, strings, upstream cgminer conventions, or arithmetic.

All addresses are virtual addresses in `btcminer`. "pool+X" means an offset in cgminer `struct pool`. "info+X" means an offset in the driver `device_data` (`cgpu+0x28`).

## 1. Short answer

1. A `mining.notify` for the current pool makes btcminer send one burst of frames to the MM. The burst holds `8 + (job id changed) + ceil(coinbase/128) + merkles` frames (merkles <= 30, chunk index/count are 8-bit). Nothing in the burst code caps the coinbase chunk count, so the earlier "9 to 47 frames" figure was only a typical range, not a bound (crosscheck-job-rx.md B41 / Corrections needed 3). CONFIRMED formula (section 2).
2. Each burst goes out in this order: `0x20` stratum params, `0x27` four rolled versions, `0x25` target, `0x21` job id (only when changed), `0x22` coinbase chunks, `0x23` merkle branches, `0x24` header (4 frames), `0x26` finish. CONFIRMED (straight-line code in `avalonnano_sswork_update` 0x2a160).
3. The K230 sends the whole raw coinbase in 128-byte chunks. It sends no SHA-256 midstate. CONFIRMED.
4. The coinbase holds coinb1, extranonce1 and coinb2, with zeros in the nonce2 slot. The MM fills in nonce2 itself. The K230 gives it the nonce2 offset, the nonce2 size (capped at 4), a start value and a range. CONFIRMED.
5. Header offsets 6 and 8 in every job frame hold a 0-based sequence index and a total count. Single frames use index 0 and count 1. CONFIRMED.
6. The K230 sends the burst with no ACK wait and no delay. It ignores the return value of `data_toast_send`. CONFIRMED (no `data_toast_receive` or sleep in 0x2a160..0x2ad1c; for example, the result of 0x2a326 is never tested).

## 2. Trigger chain

| Step | What happens | Evidence | Status |
|---|---|---|---|
| 1 | `parse_notify` handles `mining.notify`. If the pool is `current_pool()`, it sets the global `opt_work_update` (0x23a368) to 1. | 0x37082 `current_pool`, 0x37086 compare, 0x3708c `sb a4, 0x7c0(gp)` (gp = 0x239ba8, so 0x23a368 = `opt_work_update` in objects.txt) | CONFIRMED |
| 2 | `cgminer_thread` sees `opt_work_update`, sets `thr->work_update` (thr+0x7e) = 1 on every mining thread, then clears the flag. | 0x28658 test, 0x2869a `sb a1, 0x7e(a3)`, 0x286b6 clear | CONFIRMED |
| 3 | `hash_driver_work` (drv+0x80) loops: it calls `scanwork` (drv+0x90 = `avalonnano_scanhash`). If `thr->work_update` is set, it calls `update_work` (drv+0xb0 = `avalonnano_sswork_update`) and clears the flag. | 0x257d6 `ld a5,0x90(s3)`, 0x25824 `lbu 0x7e`, 0x2582a `ld a5,0xb0(s3)`, 0x25836 clear | CONFIRMED |
| 4 | Clean jobs or a new block: `restart_thread` sets `thr->work_restart` (thr+0x7d) and calls `flush_work` (drv+0xa8 = `avalon12_sswork_flush`). | 0x1c634 `sb a3,0x7d(a4)`, 0x1c694 `ld a5,0xa8(a5)`, 0x1c696 `jalr` | CONFIRMED |
| 5 | `avalon12_sswork_flush` sends nothing. If `cgpu->thr[0]->work_restart` is set, it sets `info->work_restart` (info+0x3758) = 1. The next `0x20` frame then carries the restart word. | 0x29780..0x29798, log "DO SET  info->work_restart  IS TRUE  0000" (0x154130) | CONFIRMED |
| 6 | The `avalonnano_drv` table (0x236bf0): +0x60 `avalon12_prepare`, +0x80 `hash_driver_work`, +0x90 scanhash, +0xa8 flush, +0xb0 update, +0xe0 `max_diff` = 4096.0 (0x40b0000000000000). | data at 0x236bf0 | CONFIRMED |

Scanwork and update_work run on the same miner thread, one after the other. So a job burst never interleaves with the polling exchange or with the deferred control frame sent from scanhash. CONFIRMED (both are called from the `hash_driver_work` loop). `data_toast_send` also takes a global mutex (0x44d490) around each `uart_write`, so every frame is written atomically. CONFIRMED (0xb58d4..0xb58f8).

`avalonnano_sswork_update` also takes the info lock: mutex info+0x68 plus write-locked rwlock info+0x90. It holds the lock for the whole burst. `avalonnano_scanhash` read-locks the same lock around `polling`. CONFIRMED (0x2a1e4, 0x2a1f2 and 0x2e806, 0x2e814).

Latency from notify to burst is up to one scanhash iteration. `polling` starts with `cgsleep_ms(opt_avalonnano_polling_delay)`, and that default is 100 (0x236a90 = 0x64). INFERRED.

## 3. Pre-send checks (no frame is sent if any check fails)

| Check | Evidence | Log string | Status |
|---|---|---|---|
| Pool must be stratum: `pool+0xcf8` != 0 | 0x2a190 `lbu a5,0x4f9(s2)` (s2 = pool+0x7ff), 0x2a194 | "%s-%d: MM has to use stratum pools" (0x154270, at 0x2a7ca) | CONFIRMED |
| Post-hash coinbase length <= 6144 (0x1800). posthash = coinbase_len - (nonce2_offset - nonce2_offset % 64). The log prints posthash+64 against 6208 (0x1840). | 0x2a19c..0x2a1c6 | "%s-%d: MM pool modified coinbase length(%d) is more than %d" (0x154298) | CONFIRMED |
| merkles <= 30 | 0x2a1ca `lw a5,0x180(a3)`, 0x2a1ce `li s3,0x1e`, 0x2a1d0 `blt` | "%s-%d: MM merkles has to be less then %d" (0x1542d8), prints 30 | CONFIRMED |
| n2size >= 3 | 0x2a1d4 `lw a4,0x4e9(s2)` (pool+0xce8), 0x2a1da `bge 2, n2size` | "%s-%d: MM nonce2 size has to be >= 3 (%d)" (0x154308) | CONFIRMED |

The coinbase length check is a leftover from upstream Avalon drivers, which sent a midstate plus the post-hash part. This build sends the full coinbase (section 6.5), so the check does not limit what goes on the wire. INFERRED.

## 4. Pool snapshot bookkeeping (no frames)

`avalonnano_sswork_update` shifts three pool copies, then calls `copy_pool_stratum` 3 times (0x2a238, 0x2a244, 0x2a24c):
- info+0x2528 (pool2) <- info+0x12f8 (pool1)
- info+0x12f8 (pool1) <- info+0xc8 (pool0)
- info+0xc8 (pool0) <- current pool

CONFIRMED. The stride is 0x1230. Each copy's `data_lock` is initialised in `avalon12_prepare`: 0x2c0/0x2e8, 0x14f0/0x1518, 0x2720/0x2748. CONFIRMED (0x29b74..0x29bce).

`copy_pool_stratum(dst=a0, src=a1)` returns early if the source has no job id, or if the CRC16 of both job id strings matches. CONFIRMED (0x29e64..0x29eb4). It copies these fields: coinbase (cgcalloc + memcpy, coinbase_len bytes), merkle_bin[] (32 bytes each), coinbase_len and nonce2_offset (one 8-byte copy at +0x10f8), sdiff (+0x1200), n2size (+0xce8), merkles (+0x1180), job_id and nonce1 (strdup), 12 bytes at +0x11e0 (ntime), 0x80 bytes at +0x1100 (header_bin), 0x40 bytes at +0x64 (vmask_001), 0x90 bytes at +0xa4 (vmask_002), and 16 bytes at +0x134..+0x143 (version mask fields). CONFIRMED. These copies serve the receive path, for nonce validation against older jobs. INFERRED. The frames in section 6 are built from the live `current_pool()`, not from these copies. CONFIRMED (s1 = result of 0x2a188).

`avalon12_prepare` (0x29b20) sends no frames. It zeroes counters, takes timestamps and initialises the locks. CONFIRMED.

## 5. Common header for job frames

| Offset | Size | Meaning in job frames | Status |
|---|---|---|---|
| 0 | 2 | "CN" (`sh 0x4e43`, 0x4e43 = 0x5000-0x1bd) | CONFIRMED |
| 2 | 2 | CRC16 LE over [4, 12+len) | CONFIRMED (every sender calls `crc16(pkt+4, 8+len)`) |
| 4 | 1 | type | CONFIRMED |
| 5 | 1 | always 0 | CONFIRMED (byte 5 is 0 in every constant or `sh` used) |
| 6 | 2 | u16 LE: 0-based index of this frame within its multi-frame group, 0 for single frames | CONFIRMED |
| 8 | 2 | u16 LE: total frames in the group, 1 for single frames | CONFIRMED |
| 10 | 2 | u16 LE: payload length | CONFIRMED |

For `0x22` and `0x23`, the index and count are zero-extended from 8 bits (`zext.b`) before `sh`. The high byte is always 0, and counts above 255 would wrap. CONFIRMED (0x2a4ee, 0x2a514, 0x2a56e, 0x2a5a6).

## 6. Packet details, in send order

### 6.1 Type 0x20: stratum static parameters (STATIC)

- Direction: K230 -> MM. CONFIRMED.
- Name: unnamed (no string). It plays the role of upstream `AVA10_P_STATIC`. INFERRED.
- Builder: 0x2a250..0x2a326 in `avalonnano_sswork_update`, sent at 0x2a326. CONFIRMED.
- Header: off4 = `sw 0x20` at 0x2a304, which also writes off5 = 0 and off6 = 0x0000. off8 = 0x0001 (`sh` at 0x2a312). off10 = 28 (0x1c, `li a3,0x1c` at 0x2a2ea), or 32 when `info->work_restart` is set (0x2aa02). CRC length 0x24 or 0x28. CONFIRMED.

| Payload off | Field | Size | Endian | Meaning | Evidence | Status |
|---|---|---|---|---|---|---|
| 0 | coinbase_len | 4 | BE | Full coinbase length: coinb1 + extranonce1 + nonce2 + coinb2 | 0x2a288 `lw 0xf8(pool+0x1000)`, 0x2a290 `th.revw`, 0x2a294 `sw 0x6c(sp)`; sum built in `parse_notify` 0x36ee4..0x36ef6 | CONFIRMED |
| 4 | nonce2_offset | 4 | BE | Byte offset of nonce2 in the coinbase (= len(coinb1) + len(extranonce1)) | 0x2a296..0x2a2a2; `parse_notify` 0x36ee0 `addw a4, s3, n1_len`, 0x36ee6 `sw 0xfc` | CONFIRMED |
| 8 | nonce2_size | 4 | BE | min(pool n2size, 4) | 0x2a2a4 load, 0x2a2ae `slt`, 0x2a2b4 `th.mveqz`, 0x2a2b8 `th.revw` | CONFIRMED |
| 12 | merkle_offset | 4 | BE | Constant 36 (bytes 00 00 00 24). This is the merkle root offset in the 80-byte header (4 + 32). | 0x2a2a8 `lui a4,0x24000`, 0x2a2ac `sw` | value CONFIRMED, meaning INFERRED |
| 16 | merkle_count | 4 | BE | Number of merkle branches (0..30) | 0x2a2be `lw 0x180`, 0x2a2c2 `th.revw` | CONFIRMED |
| 20 | nonce2_start | 4 | BE | device_id * range (32-bit `mulw`) | 0x2a2d8 `lw 0x10(cgpu)` = device_id, 0x2a2e2 `mulw`, 0x2a2ec `th.revw`, 0x2a2f0 `sw 0x80(sp)` | CONFIRMED |
| 24 | nonce2_range | 4 | BE | n2size == 3: 0xffffff / total_devices (signed `divw`; 0xffffff if total_devices == 0). Otherwise: 0xffffffff / total_devices (`divuw`; 0xffffffff if 0). | 0x2a2c8 `lw 0x4d4(gp)` = `total_devices` (0x23a07c); 0x2a2cc, 0x2a890..0x2a8a2, 0x2a79c..0x2a7a6, 0x2aa74..0x2aa80, 0x2a2d4..0x2a2dc; stored 0x2a2dc `sw 0x84(sp)` | CONFIRMED |
| 28 | work_restart | 4 | BE | Value 1 (00 00 00 01). Present only when `info->work_restart` (info+0x3758) was set; the flag is then cleared. | 0x2a2f2 `lbu 0x758(info+0x3000)`, 0x2a9f4 `sb zero`, 0x2a9f8 `lui a5,0x1000`, 0x2a9fc `sw 0x88(sp)` | CONFIRMED |

On a single Nano 3, total_devices = 1 and device_id = 0, so start = 0 and range = 0xffffffff (or 0xffffff for 3-byte nonce2). INFERRED (runtime values not observed).

The restart branch logs "HERE  info->work_restart  IS TRUE" (0x154338) at zlog line 0x376 in function "avalon12_stratum_pkgs". CONFIRMED (0x2aacc).

Example frame, not captured, built from the rules above: coinbase_len 250, nonce2_offset 90, n2size 4, 12 merkles, no restart.
`43 4E cc cc 20 00 00 00 01 00 1C 00 | 00 00 00 FA 00 00 00 5A 00 00 00 04 00 00 00 24 00 00 00 0C 00 00 00 00 FF FF FF FF` (cc = CRC). INFERRED.

### 6.2 Type 0x27: four rolled block versions (version rolling / AsicBoost)

- Direction: K230 -> MM. CONFIRMED.
- Name: unnamed (no string). Role: the version-rolling words, like an upstream VMASK packet. INFERRED.
- Builder: 0x2a32a..0x2a398, sent at 0x2a398. CONFIRMED.
- Header: one `sd` of the 8-byte constant at 0x155a50 = `27 00 00 00 01 00 10 00`. That gives type 0x27, off5 0, off6 0x0000, off8 0x0001, len 16. CRC length 0x18. CONFIRMED (0x2a364, 0x2a368, 0x2a382).

| Payload off | Field | Size | Endian | Meaning | Evidence | Status |
|---|---|---|---|---|---|---|
| 0 | version_0 | 4 | BE | pool->vmask_001[0] = version OR pool+0x140 | 0x2a362 `lw 0x64(s1)`, 0x2a36c `sw 0x6c(sp)`; `parse_notify` 0x36500, 0x36516 `or`, 0x36518 `th.revw`, 0x36528 `sw 0x64(s10)` | CONFIRMED |
| 4 | version_1 | 4 | BE | vmask_001[2] = version OR pool+0x13c (the lowest non-zero nibble of the mask, in place) | 0x2a36e `lw 0x6c(s1)`; `parse_notify` 0x36514, 0x36524, 0x36568 | CONFIRMED |
| 8 | version_2 | 4 | BE | vmask_001[4] = version OR pool+0x138 (mask minus that nibble) | 0x2a376 `lw 0x74(s1)`; `parse_notify` 0x36512, 0x36520, 0x36564 | CONFIRMED |
| 12 | version_3 | 4 | BE | vmask_001[8] = version OR full mask (pool+0x134) | 0x2a37a `lw 0x84(s1)`; `parse_notify` 0x3650e, 0x3651c, 0x3655c | CONFIRMED |

Supporting facts:
- `version` is the `mining.notify` param 5 parsed with `strtol(...,16)`. CONFIRMED (0x364e0..0x364f0; param index 5 at 0x36772).
- The mask comes from `mining.set_version_mask` (parse_method.part.0 0x386d6..0x38734) or from the `mining.configure` reply in `initiate_stratum` (0x39c34, 0x39c38). pool+0x134 = mask. pool+0x13c = lowest non-zero nibble of the mask, kept in place: `remw` at 0x38b88 when bits 0..3 are non-zero, otherwise the shift loop at 0x386f4..0x38728 (s2 = 0 there from `strncasecmp` == 0 at 0x37f92). pool+0x138 = mask - pool+0x13c. pool+0x60 = vmask enabled. CONFIRMED.
- Example: mask 0x1fffe000 gives version, version|0xe000, version|0x1fff0000 and version|0x1fffe000. INFERRED (arithmetic).
- No code writes pool+0x140 for a pool, so version_0 = version. INFERRED (writers searched; pools are allocated zeroed).
- Frame 0x27 goes out on every job, even when the vmask enabled flag is 0. Neither `parse_notify` nor `avalonnano_sswork_update` tests pool+0x60 or reads pool+0x134. With no mask, all 4 words equal version. CONFIRMED (no such access in 0x2a160..0x2ad1c).
- The mask itself is never sent. CONFIRMED.

### 6.3 Type 0x25: share target (TARGET)

- Direction: K230 -> MM. CONFIRMED.
- Name: TARGET, from the format string "%s-%d: Pool stratum target: %s" (0x154360). It is logged just before this send when `opt_debug` (0x23a440) is set. CONFIRMED (0x2a3e6, 0x2ab04..0x2ab44).
- Builder: 0x2a39c..0x2a416, sent at 0x2a416. CONFIRMED.
- Header: `sd` of the constant at 0x155a60 = `25 00 00 00 01 00 20 00`. That gives type 0x25, off6 0, off8 1, len 32. CRC length 0x28. CONFIRMED (0x2a3f8, 0x2a402).

| Payload off | Field | Size | Endian | Meaning | Evidence | Status |
|---|---|---|---|---|---|---|
| 0 | target | 32 | LE (256-bit integer; byte 31 most significant) | `set_target(min(pool->sdiff, 4096.0))` | 0x2a3a0 `fld 0x200(pool+0x1000)`, 0x2a3a8 `fld 4096.0` from 0x155a58, 0x2a3ae `fle.d`, 0x2a3ba `jal set_target`, copied as 8 words 0x2a3be..0x2a3e4; `set_target` stores the most significant u64 at +24 (0x200be) and the least at +0 (0x200c4), native LE | CONFIRMED |

The difficulty cap 4096 also appears as drv `max_diff` (drv+0xe0). CONFIRMED. pool+0x1200 is the stratum difficulty (`sdiff`, set from `mining.set_difficulty` through next_diff when the next notify arrives). INFERRED (strong: 0x380e0..0x38128 and 0x36eac..0x36ec6 follow the cgminer pattern).

### 6.4 Type 0x21: job id (JOB_ID)

- Direction: K230 -> MM. CONFIRMED.
- Name: JOBS_ID, from the format string "%s-%d: Pool stratum message JOBS_ID[%04x]: %s" (0x154380). CONFIRMED (0x2a968..0x2a9b4).
- Builder: 0x2a41a..0x2a4b2, sent at 0x2a4b2. CONFIRMED.
- The K230 sends this frame only when `(crc16(job_id) << 16) | pool_no` differs from `info->last_jobid` (info+0x375c), then updates the field. CONFIRMED (0x2a456..0x2a46c).
- Header: `sd` of 0x0004000100000021 (`lui 0x40001; slli 20; addi 0x21`). That gives type 0x21, off6 0, off8 1, len 4. CRC length 0xc. CONFIRMED (0x2a472..0x2a496).

| Payload off | Field | Size | Endian | Meaning | Evidence | Status |
|---|---|---|---|---|---|---|
| 0 | job_id_crc | 2 | BE | CRC-16/XMODEM (same `crc16` at 0x14d6a0) over the ASCII job id string (pool+0xd00, strlen bytes) | 0x2a426..0x2a43e; byte swap 0x2a47a `slliw 8`, 0x2a482 `srliw 8`, 0x2a48e `or`, 0x2a49a `sh 0x6c(sp)` | CONFIRMED |
| 2 | pool_no | 2 | LE | `pool->pool_no` (pool+0) | 0x2a464 `lhu a2,0(s1)`, 0x2a49e `sh 0x6e(sp)` | CONFIRMED |

### 6.5 Type 0x22: coinbase chunks (COINBASE)

- Direction: K230 -> MM. CONFIRMED.
- Name: unnamed (no packet-name string). Role: coinbase transfer. CONFIRMED by content.
- Builder: full chunks in the loop 0x2a4b6..0x2a54c (send 0x2a544). Last partial chunk at 0x2aa08..0x2aa6c (send 0x2aa6c). Coinbase shorter than 128 bytes: 0x2aa84..0x2aac4. CONFIRMED.
- Let N_full = coinbase_len / 128, rem = coinbase_len % 128, N = N_full + (rem ? 1 : 0).
  - Full chunk i (0 <= i < N_full): off6 = i, off8 = N, len = 128. Payload = coinbase[128*i .. 128*i+127]. CRC length 0x88. CONFIRMED (0x2a504 `slli s0,7`, 0x2a51e `sh 0x66`, 0x2a52a `sh s3` (N), 0x2a52e `sh 0x80`).
  - Last partial chunk (if rem != 0): off6 = N_full, off8 = N_full + 1, len = rem, CRC length rem + 8. The payload is not padded on the wire. CONFIRMED (0x2aa1c, 0x2aa2e, 0x2aa36, 0x2aa44, 0x2aa52..0x2aa5a).
- Source: `pool->coinbase` (pointer at pool+0x10f0), from offset 0, full length. No SHA-256 prehash or midstate is computed or sent. CONFIRMED (no `sha256` call in the function; memcpy source 0x2a500/0x2a50c).

| Payload off | Field | Size | Endian | Meaning | Evidence | Status |
|---|---|---|---|---|---|---|
| 0 | coinbase bytes | 128, or rem for the last chunk | raw byte stream | coinbase[128*index ...] | 0x2a500..0x2a510, 0x2aa16..0x2aa26 | CONFIRMED |

Coinbase contents, built in `parse_notify` (0x3792e..0x37988 and 0x37a34..0x37a52): calloc'd buffer = coinb1, then extranonce1 binary (pool+0xcd8, n1_len at pool+0x10e8) at offset len(coinb1), then a zeroed nonce2 slot of n2size bytes, then coinb2. CONFIRMED. The MM must write nonce2 at `nonce2_offset` using `nonce2_size` bytes. INFERRED (byte order unknown, see open questions).

### 6.6 Type 0x23: merkle branches (MERKLES)

- Direction: K230 -> MM. CONFIRMED.
- Name: MERKLES, from the format string "%s-%d: Pool stratum message MERKLES: %d" (0x1543b0). CONFIRMED (0x2a920..0x2a962).
- Builder: loop 0x2a588..0x2a5de, one frame per branch, sent at 0x2a5d6. No frames when merkles = 0. CONFIRMED.
- Header: type 0x23 (`sh` 0x23 at 0x2a5b8), off6 = i (0-based), off8 = merkle_count, len 32 (0x2a5c0). CRC length 0x28. CONFIRMED.

| Payload off | Field | Size | Endian | Meaning | Evidence | Status |
|---|---|---|---|---|---|---|
| 0 | merkle_branch[i] | 32 | raw (hex2bin of the stratum branch string, used as-is in sha256d(root + branch)) | `pool->swork.merkle_bin[i]` (pointer array at pool+0xd08) | 0x2a594 `ld 0x509(s2)`, 0x2a59e `th.lrd a1,a5,s0,3`, 0x2a5a2 memcpy 32 | field CONFIRMED, hashing use INFERRED |

### 6.7 Type 0x24: block header template (HEADER)

- Direction: K230 -> MM. CONFIRMED.
- Name: HEADER, from the format string "%s-%d: Pool stratum message HEADER: 4" (0x1543d8). CONFIRMED (0x2a8dc..0x2a91a).
- Builder: loop 0x2a60c..0x2a676, 4 frames, sent at 0x2a672. CONFIRMED.
- Header: type 0x24 (0x2a656), off6 = i in 0..3 (0x2a64e), then `sw 0x00200004` at off8 (0x2a65a). That gives off8 = 4 and len = 32. CRC length 0x28. CONFIRMED.
- Payload of frame i = `pool->header_bin[32*i .. 32*i+31]` (header_bin = pool+0x1100, 128 bytes). CONFIRMED (0x2a5f0..0x2a5fc base, 0x2a66e `addi s1,s1,0x20`).

`header_bin` is `hex2bin` of `snprintf("%s%s%s%s%s%s%s", ...)` in `parse_notify` (format 0x1563d8 at 0x36f86, hex decode loop at 0x36fd0..0x3700c into pool+0x1100). The pieces, in order: bbversion (pool+0x11c8, notify param 5), prevhash (pool+0x1184, param 1, raw string via "%s"), 64 hex zeros (0x156390), ntime (pool+0x11e0, param 7), nbits (pool+0x11d4, param 6), "00000000" (0x1563c8), workpadding (pointer at 0x236050 -> 0x152a60). CONFIRMED.

Absolute header_bin layout across the 4 frames:

| header_bin off | Frame/payload off | Field | Size | Byte order | Status |
|---|---|---|---|---|---|
| 0 | f0/0 | version | 4 | Stratum hex order, e.g. "20000000" -> 20 00 00 00 (BE value) | CONFIRMED |
| 4 | f0/4 | prevhash | 32 | Stratum notify order (raw hex2bin of param 1) | CONFIRMED |
| 36 | f1/4 | merkle root placeholder | 32 | All zero; the MM computes the root | CONFIRMED |
| 68 | f2/4 | ntime | 4 | Stratum hex order (BE value) | CONFIRMED |
| 72 | f2/8 | nbits | 4 | Stratum hex order (BE value) | CONFIRMED |
| 76 | f2/12 | nonce | 4 | Zero | CONFIRMED |
| 80 | f2/16 | SHA-256 padding | 48 | `00 00 00 80`, 40 zero bytes, `80 02 00 00` | CONFIRMED |

The padding bytes `00 00 00 80 .. 80 02 00 00` are the standard SHA-256 padding for 640 bits, with each 32-bit word byte-reversed. So header_bin is the real block header with every 4-byte word byte-reversed (cgminer's usual "swapped" layout). Read each 4-byte group as a little-endian u32 to get SHA-256 message words; reverse each group to get the 80-byte block header as serialized. INFERRED (strong).

### 6.8 Type 0x26: end of job (stratum finish)

- Direction: K230 -> MM. CONFIRMED.
- Name: "stratum_finish", from the log "DO avalon12_stratum_finish" (0x154400, zlog line 0x76c in `avalonnano_sswork_update`). CONFIRMED (0x2a8a4..0x2a8d6).
- Builder: 0x2a69e..0x2a6d4, sent at 0x2a6d4. It is sent after the pool data lock is released (0x2a67c) and before the info lock is released (0x2a6da, 0x2a6e4). CONFIRMED.
- Header: `sd` of 0x0000000100000026: type 0x26, off6 0, off8 1, len 0. CRC length 8. CONFIRMED (0x2a6aa..0x2a6c0).
- Payload: none. CONFIRMED.

This frame probably tells the MM to commit the job assembled from the frames before it. INFERRED.

## 7. Frames per job and wire size

| Frame | Count | Bytes on wire |
|---|---|---|
| 0x20 | 1 | 40 (44 with restart word) |
| 0x27 | 1 | 28 |
| 0x25 | 1 | 44 |
| 0x21 | 0 or 1 | 16 |
| 0x22 | ceil(coinbase_len / 128) | 12 * N + coinbase_len |
| 0x23 | merkle_count (0..30) | 44 * merkle_count |
| 0x24 | 4 | 176 |
| 0x26 | 1 | 12 |

- Frames per job = 8 + (job id changed ? 1 : 0) + ceil(coinbase_len / 128) + merkle_count. CONFIRMED (structure).
- Example: coinbase 250 bytes, 12 branches, new job id = 23 frames, 1118 bytes. That is about 97 ms at 115200 8N1 (11520 B/s). INFERRED (arithmetic).

## 8. Other frames sent from this slice's functions (owned by other slices)

| Type | Sender | Content | Status |
|---|---|---|---|
| (stored frame) | `avalonnano_scanhash` 0x2e7c2..0x2e7ea | If info+0x381e != 0, sends the prebuilt frame at info+0x3820 ("DO SEND ASSET CMD", 0x154d58), clears the flag and returns 0 without polling. The frame is filled by `set_avalon_device_hash_power`, `_worklevel`, `_factory`, `_freq` and `avalon_set_heater_level` (writers of info+0x381e). | CONFIRMED |
| detect | `avalonnano_scanhash` 0x2e8ac | Calls `detect_modules` when more than 30.0 s (0x155a88) passed since info+0x58 and info+0x18 == 0. | CONFIRMED |
| 0x33 | `polling` 0x2d19c..0x2d1ec (called from scanhash) | Sent when no frame is pending. Header 0x0004000100000033: off6 0, off8 1, len 4. Payload u32 = 0, or `00 00 00 01` when info+0x3762 is set ("DO IN POLLING READY TO SEND RST", 0x154d18). Retried up to 5 times with a 200 ms receive timeout ("send type=0x%X, retry idx=%d, interval=%d"). | CONFIRMED |

`copy_pool_stratum`, `avalon12_prepare` and `avalon12_sswork_flush` send no frames. CONFIRMED.

## 9. Struct offsets used (reference)

pool (cgminer `struct pool`, size 0x1230 in the info copies):

| Offset | Field | Status |
|---|---|---|
| 0x000 | pool_no (int) | CONFIRMED (printed as "Pool %d") |
| 0x060 | vmask enabled (bool) | CONFIRMED (0x3873a) |
| 0x064 | vmask_001[16] (u32, stored BE) | CONFIRMED |
| 0x0a4 | vmask_002[16][9] (strings) | INFERRED |
| 0x134 / 0x138 / 0x13c | version mask / mask minus low nibble group / low nibble group | CONFIRMED |
| 0xcd0 | nonce1 (char *) | INFERRED (strdup'd in copy_pool_stratum) |
| 0xcd8 | nonce1bin | CONFIRMED (coinbase build) |
| 0xce8 | n2size | CONFIRMED |
| 0xcf8 | has_stratum | CONFIRMED (check plus log) |
| 0xd00 | swork.job_id (char *) | CONFIRMED |
| 0xd08 | swork.merkle_bin (u8 **) | CONFIRMED |
| 0x10e8 | n1_len | CONFIRMED |
| 0x10f0 / 0x10f8 / 0x10fc | coinbase ptr / coinbase_len / nonce2_offset | CONFIRMED |
| 0x1100 | header_bin[128] | CONFIRMED |
| 0x1180 | merkles | CONFIRMED |
| 0x1184 / 0x11c8 / 0x11d4 / 0x11e0 | prev_hash / bbversion / nbit / ntime (hex strings) | CONFIRMED |
| 0x1200 | sdiff (double) | INFERRED (strong) |

info (`cgpu->device_data`):

| Offset | Field | Status |
|---|---|---|
| 0x20 | pool_no of the last job sent (0x2a232) | CONFIRMED |
| 0x48 | time of the last update_work (0x2a184) | CONFIRMED |
| 0x68 / 0x90 | info lock (mutex / rwlock) | CONFIRMED |
| 0xc8 / 0x12f8 / 0x2528 | pool0 / pool1 / pool2 copies | CONFIRMED |
| 0x3758 | work_restart flag | CONFIRMED |
| 0x375c | last_jobid = (crc16(job_id) << 16) OR pool_no (bitwise) | CONFIRMED |
| 0x381e / 0x3820 | pending control frame flag / frame buffer | CONFIRMED |

## 10. Open questions

1. Nonce2 byte order and rolling. The MM gets offset, size (max 4), start and range, but nothing on the K230 side says in which byte order the MM writes nonce2 into the coinbase, or whether it walks start..start+range-1. Answer this from the nonce report frame (receive slice) and `submit_nonce2_nonce`, or from a UART capture.
2. n2size > 4. The size field is capped at 4 while the coinbase keeps n2size zero bytes. Does the MM write the low 4 bytes, the first 4 bytes, or something else? This matters for pools with 8-byte extranonce2.
3. Version slot mapping. Which of the 4 words in 0x27 does each midstate or ASIC core use? How does a returned nonce say which version it was found with? This needs the receive slice.
4. Reliability. There is no ACK, retry or sequence number across jobs; only index and count inside a group. Does the MM drop a job when a CRC fails or a chunk is missing, and does frame 0x26 trigger a completeness check?
5. Job id suppression. Frame 0x21 is skipped when crc16(job_id) and pool_no are unchanged. Does the MM then keep the previous id for the new coinbase and header, and can a crc16 collision between two job ids merge two jobs?
6. Restart word. Does `00 00 00 01` at STATIC offset 28 make the MM abort in-flight work right away, or only when frame 0x26 arrives?
7. Coinbase size limit. The K230 limit is post-hash <= 6144 bytes, but it sends the full coinbase and the chunk count is 8 bits (max 255 chunks = 32640 bytes). What is the MM's real buffer size?
8. pool+0x140. version_0 is version OR pool+0x140, and nothing writes pool+0x140, so version_0 should equal the plain version. Confirm on a live capture.

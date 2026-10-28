# Cross-check: JOB and RECEIVE path documents vs Ghidra 12.1.4 pseudocode

Scope: every concrete claim in `protocol-job.md` and `protocol-rx.md`, checked against `ghidra/*.c`. Where Ghidra is silent or garbled, and wherever the two disagree, the same address was read in `btcminer.annot.asm`. Static reading only. Nothing from the dump was executed.

## Method

1. Ghidra libc calls (`FUN_ram_000XXXXX`) were renamed with `plt-map.txt` (slot = 0x14450 + offset) in a scratch copy. The files under `ghidra/` were not modified.
2. Each claim got one verdict:
   - MATCH: the pseudocode shows the same offset, constant, order or logic.
   - MISMATCH: the pseudocode and the document disagree. The asm at the cited address decides which side is right.
   - NOT VISIBLE: Ghidra has no output for the function, or the T-Head instruction is rendered as `custom0`. The Evidence column says whether the asm, raw bytes from the binary, or the live captures confirm the claim. One NOT VISIBLE row (E27) is flagged "asm contradicts doc": Ghidra is silent there, but the asm shows the document is wrong.
3. Constants were read from the binary with `tools/addr2str.py -x`. The jump tables at 0x155640 and 0x1556c8 were dumped in full.
4. Live references (`PROTOCOL.md` section 7, `../captures/`) are used only as supporting evidence.

Ghidra failure modes seen in this slice (do not trust these spots in the pseudocode):
- `th.revw` dropped: the 0x20 payload fields read as LE values. The nonce2_range field even reads as `n2size`, because the swapped result is never assigned.
- `th.mveqz` dropped: 0x20 nonce2_size shows as the constant 4, and the api_stats TMax merge is lost.
- `th.lrw` jump table: the `polling` handlers for 0x50..0x71 are missing entirely (`(*custom0.rd())()`).
- `submit_nonce2_nonce`: the version index renders as `pool+0x64` only, `work+0x140` renders as `(n != 0)`, and the ntime add loses both byte swaps.
- Without any T-Head instruction involved, Ghidra drops the upper 16 bytes of the 0x25 target copy (row B21).
- api_stats: float arguments show as `in_register_00000500`, so every computed rate is invisible.

## Verdict counts

| Verdict | Rows |
|---|---|
| MATCH | 89 |
| MISMATCH | 2 (B21 document right; B41 Ghidra right) |
| NOT VISIBLE | 80 (78 confirmed by asm, bytes or live capture; 1 contradicted by asm: E27; 1 not re-checked: C15) |
| Total | 171 |

## Verdict table

### A. Trigger chain, locking, pre-send checks, pool snapshots (job sections 2 to 4, 9)

| # | Claim | Document | Ghidra | Verdict | Evidence |
|---|---|---|---|---|---|
| A1 | A notify for the current pool sets `opt_work_update` (0x23a368) | job 2.1 | no output for `parse_notify` | NOT VISIBLE | asm 0x37082 `current_pool`, 0x3708c `sb 0x7c0(gp)` confirm; live: one burst per notify |
| A2 | `cgminer_thread` sets thr+0x7e, then clears the global | job 2.2 | not decompiled | NOT VISIBLE | asm 0x28658, 0x2869a, 0x286b6 confirm |
| A3 | `hash_driver_work`: scanwork (drv+0x90), then update_work (drv+0xb0) if thr+0x7e, then clears it | job 2.3 | not decompiled | NOT VISIBLE | asm 0x257d6, 0x25824, 0x2582a, 0x25836 confirm |
| A4 | `restart_thread` sets thr+0x7d and calls drv+0xa8 | job 2.4 | not decompiled | NOT VISIBLE | asm 0x1c634, 0x1c694, 0x1c696 confirm |
| A5 | Flush sends nothing; sets info+0x3758 = 1 if cgpu->thr[0]->work_restart; logs "DO SET info->work_restart IS TRUE 0000" | job 2.5 | `avalon12_sswork_flush.c`: tests `**(cgpu+200)+0x7d`, writes info+0x3758 = 1, zlog line 0x419 | MATCH | |
| A6 | drv table: +0x60 prepare, +0x80 hash_driver_work, +0x90 scanhash, +0xa8 flush, +0xb0 update, +0xe0 max_diff 4096.0 | job 2.6 | data; `submit_nonce2_nonce.c` clamps difficulty to drv+0xe0 (max) and drv+0xe8 (min) | NOT VISIBLE | bytes 0x236c50/70/80/98/a0 = 0x29b20, 0x25790, 0x2e770, 0x29780, 0x2a160; 0x236cd0 = 4096.0; 0x236cd8 (min) = 0.0 |
| A7 | update_work holds the info mutex +0x68 and wrlock +0x90 for the whole burst; scanhash rdlocks the same lock around `polling` | job 2 | locks at entry of `sswork_update.c`, released after 0x26; `scanhash.c` mutex, rdlock, unlock around `polling()` | MATCH | |
| A8 | `data_toast_send` wraps `uart_write(frame, 12+len)` in global mutex 0x44d490 | job 2 | not decompiled | NOT VISIBLE | asm 0xb58d4..0xb58f8 confirm |
| A9 | Burst: no ACK wait, no sleep, send result ignored | job 1.6 | no receive or sleep in `sswork_update.c`; return values unused | MATCH | |
| A10 | Check: pool+0xcf8 (has_stratum) != 0, else log | job 3 | `(char)puVar6[0x33e]`, log line 0x74c | MATCH | |
| A11 | Check: coinbase_len - (n2off - n2off%64) <= 0x1800; the log prints value+0x40 against 0x1840 | job 3 | lines 67..80 | MATCH | |
| A12 | Check: merkles (pool+0x1180) <= 30 | job 3 | `0x1e < puVar6[0x460]` | MATCH | |
| A13 | Check: n2size (pool+0xce8) >= 3 | job 3 | `puVar6[0x33a] < 3` | MATCH | |
| A14 | Snapshot shift: copy(0x2528 <- 0x12f8), copy(0x12f8 <- 0xc8), copy(0xc8 <- current pool) | job 4 | lines 151..153 | MATCH | |
| A15 | prepare initialises the data locks 0x2c0/0x2e8, 0x14f0/0x1518, 0x2720/0x2748 and the info lock 0x68/0x90 | job 4 | `avalon12_prepare.c` u64 indexes 0xd/0x12, 0x58/0x5d, 0x29e/0x2a3, 0x4e4/0x4e9 | MATCH | |
| A16 | `copy_pool_stratum` returns if the source job_id is NULL, or if the dst job_id is set and both crc16 values are equal | job 4 | lines 20..36 | MATCH | |
| A17 | Copies coinbase (cgcalloc + memcpy of coinbase_len), 8 bytes at +0x10f8, +0x1200, +0xce8, +0x1180, strdup +0xd00/+0xcd0, 0x80 at +0x1100, 0x40 at +0x64, 0x90 at +0xa4, 16 bytes at +0x134 | job 4 | lines 57..110 | MATCH | |
| A18 | merkle_bin[] copied as 32-byte blocks | job 4 | cgrealloc(merkles*8+1), cgmalloc(0x20), memcpy 0x20; pointer indexing is `custom0.rd` | MATCH | asm th.lrd/th.srd 0x2a066..0x2a078 |
| A19 | 12 bytes copied at +0x11e0 (ntime) | job 4 | only the 4-byte copy at +0x11e8 is visible; the rest is `custom0.rd(); custom0()` | NOT VISIBLE | asm 0x29f92 th.lrd + 0x29f9e th.srd (8 bytes), 0x29fa2/0x29fac (4 bytes) confirm |
| A20 | Frames are built from live `current_pool()`, not from the snapshots | job 4 | every frame field reads `puVar6 = current_pool()` | MATCH | |
| A21 | prepare sends no frame: zeroes counters, 3 cgtime calls, lock init | job 4 | `avalon12_prepare.c` | MATCH | |
| A22 | info+0x48 = time of the last update (cgtime first); info+0x20 = pool_no | job 9 | lines 53, 150 | MATCH | |

### B. Frame header and job frames (job sections 5 to 9)

| # | Claim | Document | Ghidra | Verdict | Evidence |
|---|---|---|---|---|---|
| B1 | Magic "CN" written as u16 0x4e43 | job 5 | `uStack_170 = 0x4e43` in every frame | MATCH | |
| B2 | CRC16 at off 2 over [4, 12+len) | job 5 | crc16(hdr+4, n) with n = 0x24/0x28, 0x18, 0x28, 0xc, 0x88, rem+8, 0x28, 0x28, 8 | MATCH | |
| B3 | Off 5 always 0 | job 5 | byte 5 is 0 in every header constant | MATCH | |
| B4 | Off 6 = 0-based idx, off 8 = cnt, off 10 = len, u16 LE | job 5 | header assembly in all loops | MATCH | |
| B5 | 0x22/0x23 idx and cnt zero-extended from 8 bits | job 5 | `& 0xff` on idx; mask `0xffffffffff` clears byte 9 | MATCH | |
| B6 | 0x20 header: idx 0, cnt 1, len 28 (32 with restart), CRC len 0x24 (0x28) | job 6.1 | `CONCAT26(len, 0x100000020)`; 0x24/0x1c or 0x28/0x20 | MATCH | |
| B7 | 0x20 p0: coinbase_len BE | job 6.1 | `uStack_164 = puVar6[0x43e]; custom0()` (swap dropped) | NOT VISIBLE | asm 0x2a288 lw, 0x2a290 th.revw; live confirms |
| B8 | 0x20 p4: nonce2_offset BE | job 6.1 | swap dropped | NOT VISIBLE | asm 0x2a296..0x2a2a2; live confirms |
| B9 | 0x20 p8: min(n2size, 4) BE | job 6.1 | shows the constant `uStack_15c = 4` | NOT VISIBLE | asm 0x2a2ae slt, 0x2a2b4 th.mveqz, 0x2a2b8 th.revw confirm |
| B10 | 0x20 p12: constant 00 00 00 24 | job 6.1 | `uStack_158 = 0x24000000` | MATCH | |
| B11 | 0x20 p16: merkle count BE | job 6.1 | low half of `uStack_154` = puVar6[0x460], unswapped | NOT VISIBLE | asm 0x2a2be..0x2a2c6; live confirms |
| B12 | 0x20 p20: nonce2_start = device_id * range, BE | job 6.1 | `*(int*)(cgpu+0x10) * uVar15` visible, swap dropped | NOT VISIBLE | asm 0x2a2d8, 0x2a2e2 mulw, 0x2a2ec th.revw |
| B13 | 0x20 p24: range = 0xffffff/total_devices (divw) if n2size == 3, else 0xffffffff/total_devices (divuw); full range if 0 devices | job 6.1 | branches, both divisions and 0xffffff00 visible; the stored value reads as n2size because the th.revw result is lost | NOT VISIBLE | asm 0x2a79c..0x2a7a2, 0x2a890..0x2a8a2, 0x2aa74..0x2aa7c confirm; live confirms |
| B14 | 0x20 p28: restart word 00 00 00 01 when info+0x3758 is set; flag cleared; log "HERE info->work_restart IS TRUE", line 0x376 | job 6.1 | lines 209..219 | MATCH | |
| B15 | 0x27 header 27 00 00 00 01 00 10 00, CRC len 0x18 | job 6.2 | `0x10000100000027`, crc16(...,0x18) | MATCH | |
| B16 | 0x27 words = pool+0x64, +0x6c, +0x74, +0x84 | job 6.2 | puVar6[0x19], [0x1b], [0x1d], [0x21] | MATCH | |
| B17 | Words stored BE as version OR {+0x140, +0x13c, +0x138, +0x134} | job 6.2 | `parse_notify` not decompiled | NOT VISIBLE | asm 0x364f4..0x36568 confirm; live: index n selects word n |
| B18 | 0x27 sent on every job; pool+0x60 and pool+0x134 not read | job 6.2 | no such access in `sswork_update.c` | MATCH | |
| B19 | 0x25 header 25 00 00 00 01 00 20 00, CRC len 0x28 | job 6.3 | `0x20000100000025`, crc16(...,0x28) | MATCH | |
| B20 | Difficulty = min(pool+0x1200, 4096.0) | job 6.3 | lines 246..250 | MATCH | |
| B21 | 0x25 payload = all 32 bytes (8 words) of `set_target` output | job 6.3 | copies only 4 words (`uStack_190..184`) to payload 0..15 | MISMATCH | Document right. asm 0x2a3be..0x2a3e4 copies 8 words (sp+0x40..0x5c to sp+0x6c..0x88). Live `target(le)` has non-zero bytes in the top u64. No T-Head op involved; Ghidra dropped the stores |
| B22 | Target is a LE 256-bit value (most significant u64 at +24) | job 6.3 | `set_target` not decompiled | NOT VISIBLE | asm 0x200be..0x200c4, then memcpy 0x20; live confirms |
| B23 | Target debug log when `opt_debug` | job 6.3 | lines 255..264 | MATCH | |
| B24 | 0x21 sent only when (crc16(job_id) << 16 OR pool_no) != info+0x375c; field updated | job 6.4 | lines 281..284 | MATCH | |
| B25 | 0x21 header 0x0004000100000021, CRC len 0xc | job 6.4 | line 286, crc16(...,0xc) | MATCH | |
| B26 | 0x21 p0..1 = crc16(job_id, strlen) BE | job 6.4 | `(crc<<8) OR (crc>>8)` | MATCH | |
| B27 | 0x21 p2..3 = pool_no LE | job 6.4 | `CONCAT22((short)pool_no, ...)` | MATCH | |
| B28 | 0x22 full chunks: coinbase+128i, idx i, cnt N, len 128, CRC 0x88 | job 6.5 | lines 305..316 | MATCH | |
| B29 | 0x22 last partial chunk: idx N_full, cnt N_full+1, CRC len rem+8, no padding | job 6.5 | lines 318..328 | MATCH | |
| B30 | Partial chunk len field = rem | job 6.5 | len is a `custom0.rs1.rs2` output | NOT VISIBLE | asm 0x2aa36 th.extu a3, s0, 15, 0, then 0x2aa5a sh |
| B31 | Coinbase < 128 bytes uses its own path | job 6.5 | branch on a garbled value (`extraout_a2 < 0x81`) | NOT VISIBLE | asm 0x2aa84..0x2aac4 confirm (the 0x2aac4 branch is always taken) |
| B32 | No SHA-256 or midstate computed | job 6.5 | no sha256 call | MATCH | |
| B33 | Coinbase = coinb1, en1, zeroed n2 slot, coinb2 | job 6.5 | `parse_notify` not decompiled | NOT VISIBLE | live: nonce2 LE at n2_off re-hashes valid |
| B34 | 0x23: one frame per branch, idx i, cnt merkles, len 32, CRC 0x28, none when 0 | job 6.6 | lines 338..353 | MATCH | |
| B35 | 0x23 payload = merkle_bin[i] (32 bytes) | job 6.6 | pointer comes from `custom0.rd()` | NOT VISIBLE | asm 0x2a594 ld, 0x2a59e th.lrd; live confirms |
| B36 | 0x24: 4 frames, idx 0..3, cnt 4, len 32 (`0x200004`), CRC 0x28, payload pool+0x1100+32i | job 6.7 | lines 360..384 | MATCH | |
| B37 | header_bin content and word-swapped layout | job 6.7 | `parse_notify` not decompiled | NOT VISIBLE | live re-hash with swapped words confirms |
| B38 | 0x26: idx 0, cnt 1, len 0, CRC len 8 | job 6.8 | `0x100000026` | MATCH | |
| B39 | 0x26 sent after pool data lock release, before info lock release | job 6.8 | lines 385..414 | MATCH | |
| B40 | Frames per job = 8 + (job id changed) + ceil(cb/128) + merkles | job 7 | structure of `sswork_update.c` | MATCH | |
| B41 | "The burst holds 9 to 47 frames" | job 1.1 | chunk count = coinbase_len/128 (+1); the only coinbase limit is the post-hash check (A11) | MISMATCH | Ghidra right; asm 0x2a4b6..0x2a54c agrees. Nothing caps the chunks at 8, e.g. a 6144-byte coinbase needs 48 chunks. Job section 10 Q7 allows 255 |
| B42 | scanhash sends the parked frame at info+0x3820 when info+0x381e is set, clears the flag, returns 0 without polling | job 8 | `scanhash.c` lines 34..43 | MATCH | |
| B43 | detect runs when tdiff(now, info+0x58) > 30.0 and info+0x18 == 0 | job 8 | `scanhash.c` lines 27..33 | MATCH | |
| B44 | Pool offsets 0x0, 0x64..0x84, 0xa4, 0x134..0x143, 0xcd0, 0xce8, 0xcf8, 0xd00, 0xd08, 0x10f0/0x10f8/0x10fc, 0x1100, 0x1180, 0x1200 | job 9 | same roles in `sswork_update.c`, `copy_pool_stratum.c`, `submit_nonce2_nonce.c` | MATCH | |
| B45 | Pool offsets 0x60, 0xcd8, 0x10e8, 0x1184/0x11c8/0x11d4/0x11e0 | job 9 | used only by `parse_notify` | NOT VISIBLE | asm 0x3873a, 0x36ee0..0x36ef6 spot-checked |
| B46 | Info offsets 0x20, 0x48, 0x68/0x90, 0xc8/0x12f8/0x2528, 0x3758, 0x375c, 0x381e/0x3820 | job 9 | all visible with these roles | MATCH | |

### C. Polling cycle and 0x33 request (rx sections 1 and 2)

| # | Claim | Document | Ghidra | Verdict | Evidence |
|---|---|---|---|---|---|
| C1 | info = cgpu+0x28 | rx conv. | `*(param_1+0x28)` | MATCH | |
| C2 | scanhash calls `polling` under the info rdlock | rx 1 | `scanhash.c` lines 44..68 | MATCH | |
| C3 | `cgsleep_ms(opt_avalonnano_polling_delay)` first | rx 1.1 | line 23 | MATCH | |
| C4 | Default delay 100 ms (0x236a90) | rx 1.1 | data | NOT VISIBLE | bytes `64 00 00 00` at 0x236a90; live about 8.9 polls/s |
| C5 | Return -1 when info+0x3761 == 0 | rx 1.2 | line 24 | MATCH | |
| C6 | Drain: `data_toast_receive(buf, 0)` until non-zero, decode copy 1 | rx 1.3 | lines 25..69 | MATCH | |
| C7 | RST: if info+0x3762, one u16 store clears 0x3761/0x3762, log, payload 0x01000000 | rx 1.4 | lines 72..82 | MATCH | |
| C8 | 0x33 header `43 4E cc cc 33 00 00 00 01 00 04 00`, CRC over 12 bytes | rx 2 | `uStack_18c = 0x33`, `uStack_188 = 0x40001`, crc16(...,0xc) | MATCH | |
| C9 | Payload u32: 0, or bytes 00 00 00 01 | rx 2 | memset, then `auStack_184[0] = 0x1000000` | MATCH | |
| C10 | Up to 5 sends, each with `data_toast_receive(buf, 200)`; log "send type=0x%X, retry idx=%d, interval=%d", idx 4..0 | rx 1.5 | lines 87..144 | MATCH | |
| C11 | First reply decoded inline (copy 2), info+0x643c = 0, return 0; further replies stay queued | rx 1.6 | lines 92..131 | MATCH | asm: s11 (= 0) is not reused in copy 2, so the return really is 0 |
| C12 | 5 failures: info+0x643c++ (u8); if > 29, clear info+0x3761, log "Module detached! ID[%d]", return -1 | rx 1.7 | lines 145..159 | MATCH | |
| C13 | Re-detect after 30 s when MM Count (info+0x18) == 0 | rx 1.8 | `scanhash.c` lines 24..33 | MATCH | |
| C14 | `data_toast_receive`: 0x8c-byte slot at base+0x88+head*0x8c, head = (head+1)%5, count--, -1 when empty and timeout <= 0, else the timedwait error code | rx 1 | not decompiled | NOT VISIBLE | asm 0xb57d0..0xb57fc, 0xb5824, 0xb585e..0xb5872 confirm |
| C15 | RX thread checks "CN", len <= 0x80 and CRC, and overwrites the oldest slot | rx 1 | not decompiled | NOT VISIBLE | not re-checked here; live: every captured frame had a valid CRC |
| C16 | `polling` reads only header offsets 4, 6 (0x52/0x54) and 10 (0x50) | rx 1 | only offset 4 visible (handlers missing) | NOT VISIBLE | asm: header loads only at sp+0x154/0x156/0x15a |
| C17 | "AVANANO_P_GET_STATUS" is logged for received 0x60, not for 0x33 | rx 2 | handler missing | NOT VISIBLE | asm 0x2d328 -> 0x2da48 log; string 0x154c98; line 0x2aa |

### D. Response dispatch (rx section 3)

| # | Claim | Document | Ghidra | Verdict | Evidence |
|---|---|---|---|---|---|
| D1 | Dispatch on byte 4: > 0x71 unknown; < 0x50 only 0x11/0x15; 0x50..0x71 through a 34-entry table | rx 3 | `type < 0x72`, `(byte)(type+0xb0) < 0x22`, `(*custom0.rd())()` | MATCH | |
| D2 | Copy-1 table 0x155640: 0x50 0x2d664, 0x51 0x2d598, 0x52 0x2d480, 0x53 0x2d44c, 0x54 0x2d3ec, 0x55 0x2d3c8, 0x60 0x2d328, 0x71 0x2d2a4, others 0x2d118 | rx 3 | th.lrw load, rendered `custom0.rd` | NOT VISIBLE | table bytes match exactly |
| D3 | Copy-2 table 0x1556c8: 0x2dfc4, 0x2e2b2, 0x2deac, 0x2ddde, 0x2e1b4, 0x2e37c, 0x2e212, 0x2de1e; others 0x2dd84 | rx 3 | same | NOT VISIBLE | table bytes match exactly |
| D4 | 0x11/0x15 only logged (level <= 20), nothing parsed; they count as a reply in copy 2 | rx 3.9 | lines 34..50, 95..111 | MATCH | |
| D5 | Other types log "Unknown response 0x%X" | rx 3 | lines 58..65, 119..127 | MATCH | |

### E. Type 0x50 nonce report and `submit_nonce2_nonce` (rx section 3.1)

| # | Claim | Document | Ghidra | Verdict | Evidence |
|---|---|---|---|---|---|
| E1 | N = len (off 10) >> 4; frame ignored if N = 0 | rx 3.1 | handler missing | NOT VISIBLE | asm 0x2d664..0x2d67e |
| E2 | Trailing u32 at payload+16N, read unconditionally | rx 3.1 | handler missing | NOT VISIBLE | asm 0x2d674 th.lrw |
| E3 | rec 0..1 job_id BE, compared with crc16(snapshot job_id) | rx 3.1 | handler missing | NOT VISIBLE | asm 0x2d6da/0x2d6ec, 0x2d73c..0x2d744; live confirms |
| E4 | rec 2..3 pool_no LE; >= total_pools (gp+0x388) gives "Wrong pool_no", skip, no HW count | rx 3.1 | handler missing | NOT VISIBLE | asm 0x2d6ac..0x2d6ca, 0x2d92c..0x2d984 |
| E5 | rec 4..7 nonce2 LE | rx 3.1 | handler missing | NOT VISIBLE | asm 0x2d6e4 th.lurw; live confirms |
| E6 | submit stores nonce2 zero-extended at snapshot pool+0xce0 | rx 3.1 | 8-byte store to +0xce0 visible; the extension is `custom0.rs1.rs2` | NOT VISIBLE | asm 0x2266e th.extu s2,s2,31,0; 0x22674 sd |
| E7 | `gen_stratum_work` copies n2size bytes of the u64 (LE) to coinbase+nonce2_offset | rx 3.1 | not decompiled | NOT VISIBLE | asm 0x2023e..0x20256; live nonce2=le |
| E8 | rec 8..11 nonce: LE load, th.revw, passed as `nonce` | rx 3.1 | handler missing | NOT VISIBLE | asm 0x2d6e0, 0x2d996 |
| E9 | submit stores the nonce at work+0x4c; 20 header words swapped, then sha256d | rx 3.1 | `puVar6[0x13] = nonce`, 20-step loop, two sha256 | MATCH | per-word swap is custom0 (asm 0x227e8..0x227f6); live 68/68 |
| E10 | rec 12 asic_id < info+0x381d, else "Wrong miner_id/asic_id" | rx 3.1 | handler missing | NOT VISIBLE | asm 0x2d836..0x2d84a, 0x2d8e4 |
| E11 | rec 13 miner_id must be 0 | rx 3.1 | handler missing | NOT VISIBLE | asm 0x2d832 th.extu 15,8; 0x2d83e |
| E12 | rec 14 ntime_offset; running max in info+0x37f8 | rx 3.1 | handler missing | NOT VISIBLE | asm 0x2d6f4, 0x2d6fe..0x2d702 |
| E13 | submit: data.ntime = bswap(bswap(ntime) + n) | rx 3.1 | shows a plain `ntime += n` between two `custom0` | NOT VISIBLE | asm 0x2268a..0x226ae; live ntime+N re-hash |
| E14 | submit: work+0x158 += n | rx 3.1 | `puVar6[0x56] += param_6` | MATCH | |
| E15 | rec 15 low nibble n: work+0x140 = (1<<n) & 0xffff, 0 for n = 0 | rx 3.1 | shows `(ushort)(n != 0)` | NOT VISIBLE | asm 0x22772..0x22782 sllw + th.extu; 0x22918 for n = 0 |
| E16 | Version word = *(u32 *)(pool+0x64+4*(n ? 1<<n : 0)) | rx 3.1 | shows a fixed pool+0x64 | NOT VISIBLE | asm 0x22786 th.addsl, 0x2278a lw; live ver_idx n = 0x27 word n |
| E17 | n > 3 indexes past the 16-entry table | rx 3.1 | n/a | NOT VISIBLE | asm `parse_notify` 0x36528..0x3655c fills only 0x64..0xa0 |
| E18 | rec 15 high nibble = valid; zero skips the record silently | rx 3.1 | handler missing | NOT VISIBLE | asm 0x2d82c..0x2d830 |
| E19 | Match order pool0 (info+0xc8), pool1 (+0x12f8), pool2 (+0x2528); NULL job_id skipped; mismatch log | rx 3.1 | handler missing | NOT VISIBLE | asm 0x2d70e, 0x2d762, 0x2d7a6 |
| E20 | No match: "Cannot match to any stratum!", `inc_hw_errors(thr)`, info+0x6068++ | rx 3.1 | handler missing | NOT VISIBLE | asm 0x2d7ee..0x2d810, 0x2db90 |
| E21 | Call `submit_nonce2_nonce(thr, poolK, real_pool, nonce2, nonce, ntime, n)` | rx 3.1 | signature: param_2 locked and used for `gen_stratum_work`; param_3 stored as work->pool (+0x178); param_5 duplicate check and work+0x4c; param_6 ntime; param_7 version index | MATCH | caller asm 0x2d98c..0x2d9aa |
| E22 | Return 1 when hash word 7 == 0; submitted async only if `fulltest` passes | rx 3.1 | lines 165..225 | MATCH | |
| E23 | Return 0 when nonce == cgpu+0x130 (duplicate) | rx 3.1 | lines 141..149 | MATCH | |
| E24 | Return -1 when hash word 7 != 0 | rx 3.1 | lines 164..165 | MATCH | |
| E25 | ret > 0: info+0x6460 += cgpu+0xf8 delta; info+0x6070+8*asic_id++ | rx 3.1 | handler missing | NOT VISIBLE | asm 0x2d98e (cgpu+0xf8 before), 0x2d9ba..0x2d9e2 |
| E26 | ret = 0: info+0x6058++ | rx 3.1 | handler missing | NOT VISIBLE | asm 0x2d9ec..0x2d9f8 |
| E27 | ret < 0: always retry on the next older snapshot, with nonce2-TA if nonce2 >= TA, else wrap-TA+1+nonce2 | rx 3.1 step 3 | handler missing | NOT VISIBLE | **asm contradicts doc.** nonce2 >= TA: retry on the same snapshot with nonce2-TA (0x2d9b0 reloads a1 = poolK, 0x2da96 -> 0x2db88 -> 0x2dab8). nonce2 < TA: retry on the next older snapshot, which must exist and have a job_id, with wrap-TA+1+nonce2 (0x2da9a..0x2dab4). Copy 2 is the same (0x2e5bc, 0x2e6d2) |
| E28 | `clear_new_nonce(thr, older)` before the resubmit | rx 3.1 | not decompiled | NOT VISIBLE | asm 0x23e00: one argument (thr); it zeroes cgpu+0x130, the duplicate-check memory |
| E29 | Resubmit not > 0: HW error (`inc_hw_errors`, info+0x6068++) | rx 3.1 | handler missing | NOT VISIBLE | asm 0x2dad6..0x2dada -> 0x2d7fe |
| E30 | TA = info+0x37e8 | rx 3.1 | handler missing | NOT VISIBLE | asm 0x2d0de (copy 1), 0x2dbf0 (copy 2) |
| E31 | `polling` never calls test_nonce or submit_nonce | rx 3.1 | handlers missing, absence unprovable | NOT VISIBLE | asm: only `submit_nonce2_nonce` and `clear_new_nonce` are called |

### F. Types 0x51 to 0x71 (rx sections 3.2 to 3.8)

All handler bodies are missing from `polling.c`, so storage rows are NOT VISIBLE and were re-read in the asm. Rows where api_stats or detect show the consumer side are marked by that part.

| # | Claim | Document | Ghidra | Verdict | Evidence |
|---|---|---|---|---|---|
| F1 | 0x51[0] -> 0x37e1; 4: 0x3762=1 + lcd_hot_warn(0); 7: log + 0x3762=1; else lcd_hot_warn(1) | rx 3.2 | handler missing | NOT VISIBLE | asm 0x2d598..0x2d5b2, 0x2db18..0x2db24, 0x2db6c..0x2db86 |
| F2 | 0x51[1..4] -> 0x37e4 | rx 3.2 | handler missing | NOT VISIBLE | asm 0x2d5b6, 0x2d5c4 |
| F3 | 0x51[5..8] -> 0x6050 (u64); printed LW | rx 3.2 | api_stats `LW[%lu]` reads 0x6050 | NOT VISIBLE | asm 0x2d5c0 lwu, 0x2d5cc sd |
| F4 | 0x51[9..16] -> 0x647c/0x6480 in one 8-byte copy | rx 3.2 | handler missing | NOT VISIBLE | asm 0x2d5d2, 0x2d5de |
| F5 | GHSmm = 0x647c/1000.0f, GHSspd = 0x6480/1000.0 | rx 3.2 | float args lost | NOT VISIBLE | asm 0x2be6c..0x2be80 (0x155aa4 = 1000.0f), 0x2be1a..0x2be3a (0x153da8 = 1000.0) |
| F6 | 0x51[0x11..0x18] double -> 0x6468, printed DH | rx 3.2 | api_stats `DH[%.3f%%]` with 0x6468 | MATCH | store asm 0x2d5ce/0x2d5da |
| F7 | 0x51[0x19..0x20] double -> 0x6470, printed DHspd | rx 3.2 | api_stats DHspd with 0x6470 | MATCH | store asm 0x2d5d6/0x2d5e8 |
| F8 | 0x51[0x21..0x24] TA -> 0x37e8 (u32), low byte -> 0x381d | rx 3.2 | api_stats TA reads 0x37e8; per-ASIC lists sized by 0x381d | MATCH | store asm 0x2d5e2..0x2d5f4 |
| F9 | 0x51[0x25..0x2b] Core (7 bytes) -> 0x37ec | rx 3.2 | api_stats `Core[%s]` at 0x37ec | MATCH | store asm 0x2d5f8..0x2d61a; live "A3198S" |
| F10 | 0x51[0x2c..0x2f] -> 0x3aa0; TMax = max(0x3aa0, every PVT_T); detect sets -273 | rx 3.2 | detect `0x3aa0 = 0xfffffeef` visible; TMax merge (th.mveqz) lost | NOT VISIBLE | asm 0x2d61e/0x2d622, 0x2bd0c..0x2bd1c |
| F11 | 0x51[0x30] work level: if <= 0x37e0 and != syscfg level, set it, dict_setint, dev_super[3] OR= 1 | rx 3.2 | handler missing | NOT VISIBLE | asm 0x2d632, 0x2da10..0x2da46 |
| F12 | 0x51[0x31] -> 0x6484 + set_target_temp; detect sets 90 | rx 3.2 | detect `0x6484 = 0x5a; set_target_temp(0x5a)` visible | NOT VISIBLE | store asm 0x2d600, 0x2d626, 0x2d62a |
| F13 | 0x51[0x32] -> 0x37f4, printed SoftOFF | rx 3.2 | api_stats SoftOFF reads 0x37f4 | MATCH | store asm 0x2d636/0x2d644 |
| F14 | 0x51[0x33] -> 0x64d0; 0xff: lcd_adapter(0), store 0; else lcd_adapter(1) | rx 3.2 | handler missing | NOT VISIBLE | asm 0x2d63a..0x2d65c, 0x2da00..0x2da06 |
| F15 | 0x52 idx = off 6; miner = idx/N, asic = idx%N; N = 0 ignored; no upper bound | rx 3.3 | handler missing | NOT VISIBLE | asm 0x2d480..0x2d498 |
| F16 | 0x52[0] gates the counters | rx 3.3 | handler missing | NOT VISIBLE | asm 0x2d490, 0x2d4a0 |
| F17 | 0x52[1..8] counters A/B BE at 0x5508/0x550c + m*0xb40 + a*0x18 | rx 3.3 | api_stats ERATIO walks 0x5508, stride 0x18 and 0xb40 | MATCH | store asm 0x2d4a2..0x2d4ce |
| F18 | 0x52[9..12] PLLCNT -> 0x5510..0x551c | rx 3.3 | handler missing | NOT VISIBLE | asm 0x2d50a..0x2d55a |
| F19 | 0x52[0xd..0x14] CF BE -> 0x4d84 + m*0x780 + a*0x10 | rx 3.3 | handler missing | NOT VISIBLE | asm 0x2d4dc..0x2d590 |
| F20 | ERATIO = B*100/(A+B), 0 when A = 0 | rx 3.3 | A = 0 case visible; result float lost | NOT VISIBLE | asm 0x2c8b0..0x2c8d8 |
| F21 | Per-ASIC GHSmm uses CF if HVer starts with "851", else SF | rx 3.3 | '8','5','1' test at 0x378d..0x378f visible | MATCH | sum in asm 0x2cab8..0x2cb04 |
| F22 | 0x53[0..7] SF LE -> 0x4d74.. | rx 3.4 | handler missing | NOT VISIBLE | asm 0x2d44c..0x2d46c |
| F23 | 0x53[8..11] -> 0x6498, API "Nonce Mask" uint32 | rx 3.4 | `api_add_uint32(.., "Nonce Mask", info+0x6498)` | MATCH | store asm 0x2d476/0x2d47a |
| F24 | 0x54: idx < 0x381d, 0x381c != 0, miner-0 slots only | rx 3.5 | handler missing | NOT VISIBLE | asm 0x2d3ec..0x2d40a |
| F25 | 0x54[0..1]/10 -> float at 0x38c0 + a*4 | rx 3.5 | api_stats loops 0x38c0, stride 0x1e0 | NOT VISIBLE | asm 0x2d40e..0x2d43c |
| F26 | 0x54[2..3]/10 -> 0x3aa4 + a*0x20 | rx 3.5 | handler missing | NOT VISIBLE | asm 0x2d414..0x2d440 |
| F27 | 0x54[4..5] -> 0x4b84 + a*4; PVT_R only in extended stats | rx 3.5 | PVT_R inside `if (cgpu+0x21c != 0)` | MATCH | store asm 0x2d428/0x2d444 |
| F28 | 0x55[0] bit 0 gates the frame | rx 3.6 | handler missing | NOT VISIBLE | asm 0x2d3c8..0x2d3ce |
| F29 | 0x55[1..4] -> 0x644c | rx 3.6 | handler missing | NOT VISIBLE | asm 0x2d3d8/0x2d3dc |
| F30 | 0x55[5..12] -> 0x6456..0x645d; printed `PS[0 0 0 %d %d %d %d]` from 0x6458, 0x6456, 0x645a, 0x645c | rx 3.6 | api_stats lines 141..149 | MATCH | store asm 0x2d3e0/0x2d3e4 |
| F31 | 0x60: debug log; marker 0xffff in [0..1] (BE) means ignore | rx 3.7 | handler missing | NOT VISIBLE | asm 0x2d328..0x2d350, 0x2da48 |
| F32 | 0x60[0x10..0x17] 4 BE u16 -> SF 0x4d74.. | rx 3.7 | handler missing | NOT VISIBLE | asm 0x2d354..0x2d3c2 |
| F33 | 0x71[0] -> 0x64a1 MPO; [1..2] -> 0x64a2/0x64a3; BAR printed only when ADJ != 1 | rx 3.8 | api_stats lines 604..610 | MATCH | store asm 0x2d2a4..0x2d2ba |
| F34 | 0x71 records: count 0x37e0; 14 bytes from payload 3+14i to dev_super+0xfc+14i and 0x64a4+14i; api prints 3 records of 7 u16, skipping zero | rx 3.8 | api_stats lines 396..418 | MATCH | copy loop asm 0x2d2c6..0x2d322; live records |

### G. Device struct fields and api_stats (rx section 4)

| # | Claim | Document | Ghidra | Verdict | Evidence |
|---|---|---|---|---|---|
| G1 | 0x18 MM Count cleared by scanhash when detached; API "MM Count" | rx 4 | `scanhash.c` line 25; `api_add_int("MM Count", info+0x18)` | MATCH | |
| G2 | 0x3761: detect sets 1; polling clears it (RST, detach); api_stats details only if set | rx 4 | detect line 262; polling lines 73, 149; api_stats line 54 | MATCH | |
| G3 | 0x3762 set by 0x51 status 4 or 7 | rx 4 | handler missing | NOT VISIBLE | asm 0x2db1a, 0x2db82 |
| G4 | HashStatus 0x37e1, HVer 0x378d, HDNA 0x377c | rx 4 | api_stats lines 55..66 | MATCH | |
| G5 | 0x381c (miner count) = 1 from detect | rx 4 | detect line 261 | MATCH | |
| G6 | Elapsed base 0x3768 | rx 4 | detect `cgtime(0x3768)`; api_stats `tdiff(now, 0x3768)` | MATCH | |
| G7 | DHW 0x6058; HW 0x6060 = copy of 0x6068 when 0x381c is set | rx 4 | api_stats lines 85..94 | MATCH | |
| G8 | CRC 0x6438 printed; 0x6430/0x6438 zeroed by detect, not written by polling | rx 4 | api_stats line 391; detect lines 325..329 | MATCH | |
| G9 | Vo 0x5504 and Freq 0x6478 have no writer in polling | rx 4 | api reads 0x5504; Freq arg lost; handlers missing | NOT VISIBLE | asm: no 0x504/0x478 offset in `polling` |
| G10 | GHSavg = 0x6460/elapsed * 4.294967296; WU = 0x6460/elapsed * 60 | rx 4 | float args lost | NOT VISIBLE | asm 0x2be5a..0x2bed6; constants 0x155a70 = 4.294967296, 0x153dd8 = 60.0 |
| G11 | MTavg = mean of positive PVT_T of device 0, else -273 | rx 4 | api_stats lines 199..231 | MATCH | |
| G12 | PVT_T feeds TAvg | rx 3.5 | TAvg = roundf(dev_super()+0x68), no PVT_T read in api_stats | NOT VISIBLE | holds indirectly: 0xb5fa6 stores `get_miner_temp_avg()` (0x2f420, averages positive info+0x38c0) at dev_super+0x68 |

### H. Cross-references into detect_modules (rx section 3.9)

| # | Claim | Document | Ghidra | Verdict | Evidence |
|---|---|---|---|---|---|
| H1 | 0x11 payload 4..19 -> 0x377c HDNA; 20..51 -> 0x378d HVer | rx 3.9 | `0x3778 = payload[0..3]`, 8 bytes via custom0, `0x3784 = payload[12..19]`, `memcpy(0x378d, payload+20, 0x20)` | MATCH | |
| H2 | A later 0x11 byte -> 0x37e0 (max level) | rx 3.9 | `custom0.rd()` | NOT VISIBLE | asm 0x2b004 th.lrbu, 0x2b010 sb 0x7e0; live max_level 3 |
| H3 | 0x13 SYNC ack payload[3] -> 0x37e1 | rx 3.9 | `uStack_184._3_1_` stored at 0x37e1 | MATCH | |
| H4 | detect sets 0x3761 = 1 (0x2b388) | rx 1.2 | detect line 262 | MATCH | |

## Corrections needed

1. **0x50 fallback rewind (rx 3.1 step 3; also `PROTOCOL.md` section 3 "Nonce record" paragraph and section 6 open question 2).** The document says every failed record is retried on the next older snapshot. The asm (both decode copies) does this:
   - `nonce2 >= TA`: retry on the **same** snapshot that matched the job_id, with `nonce2 - TA`. The older snapshot is neither used nor checked, so a pool2 match still gets this retry.
   - `nonce2 < TA`: retry on the **next older** snapshot (pool0 -> pool1, pool1 -> pool2, pool2 -> none). It must be non-NULL and have a job_id. The nonce2 used is `wrap - TA + 1 + nonce2`.
   - Corrected interpretation (INFERRED): a result arrives one round of TA nonce2 values late. It belongs to the same job, one round back. Only when that would go below 0 does it belong to the end of the previous job, whose last nonce2 is the trailing u32 (`wrap`).
2. **`clear_new_nonce` (rx 3.1 step 3).** It takes only `thr` and zeroes `cgpu+0x130`, the last-nonce memory used by the duplicate check. The `older` argument in the document is not a parameter. Its purpose: without the reset, the resubmit of the same nonce would return 0 (duplicate).
3. **"The burst holds 9 to 47 frames" (job 1.1).** Nothing in the burst code caps the coinbase at 8 chunks. Only the post-hash length check limits it, and that check does not bound the total length. State the formula only (job 7), or say "8 + jobid + ceil(cb/128) + merkles, merkles <= 30, chunk index/count 8-bit".
4. **0x643c failure counter (rx 4 table, "Filled by: polling").** `detect_modules` also zeroes it (`detect_modules.c` line 280), so a re-attached module starts from 0.
5. **Copy-2 reply handling (rx 1.6 / 3.9, minor).** Any received frame clears the failure counter and makes `polling` return 0: decoded types, 0x11/0x15, and also unknown types (`LAB_ram_0002dd84` falls through to `LAB_ram_0002dca0`).

The document is right and the Ghidra listing shows a wrong value in B21 (0x25 target is 32 bytes) and in the NOT VISIBLE rows B9, B13, E13, E15 and E16, where the dropped T-Head instruction changes the visible result. Do not "fix" the documents from the Ghidra listing in those places.

## New findings

1. **`avalonnano_scanhash` returns 0 immediately while `thr->work_restart` (thr+0x7d) is set** (`scanhash.c` line 18, asm 0x2e770..0x2e778). During a restart window there is no poll, no parked control frame and no detect. Nonce reports pile up in the 5-slot RX queue (oldest overwritten) until the flag clears.
2. **The 30 s re-detect timer restarts on every successful poll.** `cgtime(info+0x58)` runs whenever `polling` returns 0 (asm 0x2e842 -> 0x2e8a4). Consequence: after an RST poll (0x51 status 4 or 7), `polling` clears 0x3761, so the next passes return -1 at once and polling stops. `detect_modules` runs only about 30 s after the last answered poll. After a detach (30 failed polls of up to 1.1 s each), the timer has already expired, so detect runs on the next pass.
3. **Hashrate reported to cgminer.** scanhash returns `((cgpu+0xf8 delta + carry) - (cgpu+0x108 delta)) << 32` (asm 0x2e846..0x2e89c). cgpu+0xf8 is incremented by `work->device_diff` for every diff1 share in `submit_nonce2_nonce` (lines 190..196). device_diff is clamped to [drv+0xe8, drv+0xe0] = [0, 4096]. cgpu+0x108 is a double; by the upstream struct layout it is probably `diff_rejected` (INFERRED).
4. **`submit_nonce2_nonce` side effects not in the documents:**
   - It also rolls the work's ntime hex string (work+0x1b8: hex2bin, bswap, +n, bswap, bin2hex). The share sent to the pool therefore carries the rolled ntime.
   - work->pool (+0x178) = real_pool = `pools[pool_no]`. Coinbase, merkles, header, version table and sdiff (work+0x1c0) come from the snapshot.
   - It increments real_pool+0x44 and sets work+0x160 = 0 and work+0x190 = 1.
   - nonce2 is written into the snapshot's pool+0xce0 under that snapshot's data lock.
5. **Snapshot shift side effect.** `copy_pool_stratum` skips the copy when the crc16 of the job ids match. If a notify repeats the job id that is already in pool0, pool2 <- pool1 and pool1 <- pool0 still happen, but pool0 is not refreshed. Result: pool0 and pool1 hold the same job and the oldest job is lost. Job identity is crc16 only, both here and in the 0x50 match, so a crc16 collision between two job ids merges them.
6. **api_stats gating.** PVT_R, CF, PLLCNT, ERATIO, `C_%02d_%02d` and the per-ASIC `GHSmm%02d` lists all sit inside `if (cgpu+0x21c != 0)` (asm 0x2c4b8). The document marks only PVT_R as extended.
7. **Live trailer values fit correction 1.** Per job, the 0x50 trailer roughly equals the previous job's final nonce2: d06f's last seen n2 0x455 is followed by trailer 0x47a on df16; df16's 0x3a1 by 0x400 on 84b7; 84b7's 0x3a0 by 0x3fd on cace (`../captures/nonce-verification.txt`). The 68 verified nonces all passed on the first submit, so the live data never exercised the rewind. INFERRED.
8. **Small behaviours:**
   - Status 7 in 0x51 does not touch the LCD hot-warn flag. Only 4 sets it and other values clear it.
   - A 0x50 frame is dropped entirely if `cgpu->thr[0]` is NULL (asm 0x2d83a).
   - The info+0x37f8 ntime-offset max is updated only for records whose pool_no passed the bound check.

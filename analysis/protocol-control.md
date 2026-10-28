# K230 / K210 protocol: control and configuration commands

Source: static reading of `btcminer` (`driver-avalon12.c`, `data_toast.c`, `mmu.c`), see NOTES.md for the frame format.
Scope: device bring-up (detect/sync), frequency, voltage, work level / heater level, hash power, factory level parameters, ASIC OTP, K210 reset, firmware upgrade.
Not covered here: job sending (stratum/work packets) and the receive/polling decoder (type 0x33 GET_STATUS and its responses), owned by other slices. Cross references are given where this slice depends on them.

Tags: **CONFIRMED** = read directly from instructions or data bytes. **INFERRED** = deduced from names, strings, or analogy with upstream cgminer Avalon drivers.

## 0. Conventions

### 0.1 Header fields (offsets 4..11)

| Off | Size | Name used here | Meaning | Tag |
|---|---|---|---|---|
| 4 | 1 | type | packet type | CONFIRMED |
| 5 | 1 | opt | always 0 in every packet of this slice | CONFIRMED (all `sd`/`sw`/`sh` header stores below) |
| 6 | 2 LE | idx | index of this packet within a series: 0 for single packets, ASIC index in SET_FREQ, chunk index in UPGRADE | CONFIRMED values; name INFERRED (cgminer `avalon10_init_pkg(pkg, type, idx, cnt)`) |
| 8 | 2 LE | cnt | size of the series: 1 for single packets, miner or ASIC count in SET_FREQ, chunk count in UPGRADE | CONFIRMED values; name INFERRED |
| 10 | 2 LE | len | payload length | CONFIRMED |

Most senders write offsets 4..11 with one `sd` of a 64-bit constant. Example `0x0004000100000010` = bytes `10 00 00 00 01 00 04 00` = type 0x10, opt 0, idx 0, cnt 1, len 4.

### 0.2 Endianness

Every multi-byte payload field in this slice is written with native RISC-V `sw`/`sh`/`sd` stores or read with `lw`/`lhu`/`ld`. No byte swap appears in any function of this slice (no `th.rev*`, no shift/or swap sequences, no swap helper calls). All multi-byte fields are therefore **little endian (CONFIRMED)**. This differs from upstream cgminer Avalon drivers, which use big endian (`be32toh`).

### 0.3 Three send mechanisms

| Mechanism | How | Used by | Tag |
|---|---|---|---|
| A. Synchronous transfer | Inlined `avalonnano_xfer_pkg`: `data_toast_send`, then `data_toast_receive(rx, 200 ms)`, up to 5 sends. Log `send type=0x%X, retry idx=%d, interval=%d` (line 820) on each failure. | DETECT 0x10, SYNC 0x12, UPGRADE 0x90 | CONFIRMED (0x2ae0a..0x2ae5e, 0x2b0ea..0x2b13e, 0x2fc1e..0x2fc78) |
| B. Direct send, no response wait | `data_toast_send` once | SET_WORKLEVEL 0x41 in detect, SET_FINISH 0x31, RESET_K210 0x42 | CONFIRMED (0x2b370, 0x2b50a, 0x2f6f2) |
| C. Pending "ASSET" slot | Producer waits for flag `info+0x381e` to be 0 (5 tries, `usleep(1000)` each), copies the 0x8c-byte frame to `info+0x3820`, sets the flag to 1. Consumer `avalonnano_scanhash` sends it on its next call (log `DO SEND ASSET CMD`), clears the flag, no response wait. Failure reporting on a busy slot depends on the producer: SET_FREQ logs `set hash freq failed!!`; SET_VOLTAGE logs `set hash voltage failed!!`; `avalon_set_heater_level` returns -1 but every caller (web, button, misc_thread) ignores it; API `worklevel,set` returns 0 and writes no reply in both the success and the busy case (0x2b9f4); API `factory` replies `factory set ok` even when the slot stayed busy (0x2cef2, 0x2cf76). | SET_FREQ 0x32, SET_WORKLEVEL 0x41 from API/web/button, SET_VOLTAGE 0x43, SET_LEVELPARAM 0x70 | CONFIRMED (producer 0x2eed4, 0x2f650, 0x29998, 0x2b9f8, 0x2d026, 0x2d03e; consumer 0x2e7c2..0x2e7ea) |

`info` = `cgpu->device_data` (`ld rX, 0x28(cgpu)`).
`data_toast_receive(buf, timeout_ms)` returns 0 and copies one queued 0x8c-byte frame, else non-zero (CONFIRMED 0xb57a0). The response checks below read `rx[4]` (type) and `rx[12+n]` (payload).

## 1. Packet summary

| Type | Dir | Name | idx / cnt | Payload len | Sender / reader | Tag |
|---|---|---|---|---|---|---|
| 0x10 | TX | AVANANO_P_DETECT | 0 / 1 | 4 | detect_modules | CONFIRMED |
| 0x11 | RX | AVANANO_P_DETECT_ACK | not checked | variable, up to 0x80 | detect_modules | CONFIRMED |
| 0x12 | TX | AVANANO_P_SYNC | 0 / 1 | 0x11 | detect_modules | CONFIRMED (name from log strings) |
| 0x13 | RX | SYNC_ACK (name INFERRED from `SYNC_ACKerr:0x%X`) | not checked | at least 4 used | detect_modules | CONFIRMED |
| 0x41 | TX | SET_WORKLEVEL (unnamed; INFERRED from `DO SET WORK LEVEL TO SLAVE: %d`) | 0 / 1 | 4 | detect_modules, avalon_set_heater_level, set_avalon_device_worklevel | CONFIRMED |
| 0x31 | TX | SET_FINISH (unnamed; INFERRED from `avalonnano_set_finish`) | 0 / 1 | 0 | detect_modules (inlined), avalonnano_set_finish | CONFIRMED |
| 0x32 | TX | SET_FREQ / SET_PLL (unnamed; INFERRED from `broadcast set pll`, `avalonnano_set_freq`) | see 4.2 | 0x20 | set_avalon_device_freq | CONFIRMED |
| 0x43 | TX | SET_VOLTAGE (unnamed; INFERRED from inlined `avalon_set_voltage`, `nano set voltage %d`) | 0 / 1 | 2 | set_avalon_device_hash_power | CONFIRMED |
| 0x70 | TX | SET_LEVELPARAM (unnamed; INFERRED from inlined `avalon_set_levelparam`) | 0 / 1 | 2 or 0x10 | set_avalon_device_factory | CONFIRMED |
| 0x71 | RX | LEVELPARAM table report (unnamed; INFERRED) | not checked | 3 + 14 x levels | polling (cross reference only) | CONFIRMED offsets |
| 0x42 | TX | RESET_K210 (unnamed; INFERRED from `avalon12_reset_k210`) | 0 / 1 | 0 | avalon12_reset_k210 (no caller found) | CONFIRMED |
| 0x90 | TX | AVANANO_P_UPGRADE data / end | chunk idx / chunk count | 0x80, last chunk shorter, end 0 | upgrade_send_data | CONFIRMED (name from `AVANANO_P_UPGRADE` log) |
| 0x91 | RX | UPGRADE_ACK (unnamed; INFERRED) | not checked | at least 1 used | upgrade_send_data | CONFIRMED |

Not on the wire (CONFIRMED): `softon`/`softoff` are stubs returning 0 (0x29890, 0x28f20). `hashpower 0` only logs `Power off hash now ...` and sends nothing (0x29a6c..0x29a9e). `factory resetfac` only sets `dev_super+0x160 = 1` (0x2cca2); `misc_thread` then calls `factory_reset(0)` (0xb61b2), which edits K230 config files only. `--avalon10-freq`, `--avalon10-voltage`, `--avalon10-voltage-level` only write globals (section 5). `set_avalon12_asic_otp` builds nothing (section 6.5). `led`, `fan-spd`, `loop`, `pdelay`, `password` build no frame: a scan of the whole binary for header magic stores, `crc16` and `data_toast_send` calls lists only the functions in the table above plus job/polling code.

## 2. Bring-up sequence (detect_modules, 0x2ad20)

Called once from `avalonnano_detect` (0x2b794) at startup, then from `avalonnano_scanhash` (0x2e8ae) every 30 s (`tdiff > 30.0`, double at 0x155a88) while `info->0x18` (module present) is 0 (CONFIRMED 0x2e7a2..0x2e8c0). It returns at once if `info+0x3761` (module enabled flag) is already set (0x2ad34..0x2ad5e).

| Step | Action | Expected response | On failure | Evidence |
|---|---|---|---|---|
| 1 | Send DETECT 0x10 (idx 0, cnt 1, len 4, payload 4 x 0x00). Mechanism A: 5 sends, 200 ms each. | type 0x11 | No frame after 5 tries: log `AVANANO_P_DETECT: recv xfer data with err %d`, return. Frame with type != 0x11: log `AVANANO_P_DETECT ack(0x%x), but 0x%X` (args 0x11, received type), return. | 0x2adbe..0x2ae5e, 0x2aebc..0x2aec2, 0x2b1ec..0x2b244, 0x2ae62..0x2aeb4 |
| 2 | Log `AVANANO_P_DETECT ID[%d] ack(0x%X)`. If the enabled flag is set and the 16-byte HDNA in the ack equals the stored one, return (same module). Otherwise log `DO DETECT check` and parse the ack (section 3.2). Log `%s-%s-%s-%s` (HDNA, field at payload 0x34, HWTYPE, SWTYPE) and `max level:%d`. | none | none | 0x2b294, 0x2aed6..0x2af06, 0x2b19c, 0x2af10..0x2b010, 0x2b5d2, 0x2b590 |
| 3 | Log `begin AVANANO_P_SYNC`. Send SYNC 0x12 (len 0x11: HDNA echo + byte 1). Mechanism A. | type 0x13 | Not fatal. Timeout: log `AVANANO_P_SYNC: recv xfer data with err %d`. Wrong type: log `SYNC_ACKerr:0x%X`. Both continue to step 4. | 0x2b558, 0x2b044..0x2b13e, 0x2b2e2..0x2b2e8, 0x2b6ae, 0x2b142..0x2b194 |
| 4 | On 0x13: `mm_status = rx payload[3]`, stored at `info+0x37e1`, logged `mm_status:%d`. The API reports it as `HashStatus[%d]` (0x2bac0). | none | none | 0x2b454..0x2b48a |
| 5 | Choose work level: `level = syscfg_work_level()` if it is `< max_level` (ack byte, `info+0x37e0`), else `level = 1`. Store in `opt_avalon12_power_level` (0x236be8), log `DO SET WORK LEVEL TO SLAVE: %d`. Send SET_WORKLEVEL 0x41 with payload u32 LE `level`. Mechanism B (no response wait). | none | none | 0x2b2fc..0x2b370, 0x2b612, 0x2b61e |
| 6 | Local init: `info+0x381c = 1` (miner count), `info+0x3761 = 1` (enabled), `cgtime(info+0x3768)`, target temperature 90 (`set_target_temp(0x5a)`), temperature arrays to -273.0, counters cleared. | none | none | 0x2b374..0x2b4c0 |
| 7 | Log `New module detected! ID[%d-%x]` (module 0, last HDNA byte `info+0x378b`). Send SET_FINISH 0x31 (len 0). Mechanism B. Log `after avalonnano set finish`. Return. | none | none | 0x2b65c..0x2b6a8, 0x2b4d4..0x2b550 |

Later, `avalonnano_scanhash` calls `polling`, which sends GET_STATUS 0x33 (other slice).
Cross reference, not detailed here: when `polling` sets `info+0x3762` (after `DO HASH READY TO REBOOT` or a job id mismatch), its next 0x33 frame carries payload u32 `0x01000000` (byte 3 = 1, log `DO IN POLLING READY TO SEND RST`), and it clears `info+0x3761`/`0x3762` (CONFIRMED 0x2dc30, 0x2dc40 `lui s2,0x1000`, 0x2d1b0). That clear makes `scanhash` re-run this whole bring-up about 30 s after the last answered poll: `avalonnano_scanhash` restarts the 30 s timer `info+0x58` after every poll that got a reply (CONFIRMED 0x2e842, 0x2e8a4).

## 3. Bring-up packets

### 3.1 0x10 AVANANO_P_DETECT (TX)

Header: `sd 0x0004000100000010` at 0x2add4: type 0x10, opt 0, idx 0, cnt 1, len 4. CRC `crc16(pkt+4, 0xc)` at 0x2addc (CONFIRMED).

| Payload off | Field | Size | Endian | Meaning | Evidence |
|---|---|---|---|---|---|
| 0 | zero | 4 | n/a | all zero (`memset(payload, 0, 0x80)`) | 0x2ada6..0x2adae CONFIRMED |

Upstream cgminer puts a module ID after the DNA here. This build sends zeros. The `ID[%d]` in logs is the literal 0 (module index), not a payload field; the cgminer `device_id` is the `%d` in the `%s-%d:` log prefix (CONFIRMED 0x2b24c..0x2b254).

### 3.2 0x11 AVANANO_P_DETECT_ACK (RX)

Accepted only if `rx[4] == 0x11` (0x2aebc). The header is not checked beyond the type. RX buffer at `sp+0xc0`, payload at `sp+0xcc`.

| Payload off | Field | Size | Endian | Meaning | Evidence |
|---|---|---|---|---|---|
| 0x00 | unknown32 | 4 | LE | stored to `info+0x3778`; no reader found | `lw 0xcc(sp)` / `sw 0x778(s8)` 0x2af10..0x2af18 CONFIRMED; meaning unknown |
| 0x04 | HDNA | 16 | bytes | stored to `info+0x377c`. Compared with the stored copy to recognize the same module (0x2aee8..0x2b1a0). Printed as `HDNA[%s]` by the API (0x2bb3a) and as the first `%s` of `%s-%s-%s-%s`. Last byte printed in `New module detected! ID[%d-%x]`. Echoed in SYNC. | 0x2af1c..0x2af32 CONFIRMED; "hash board DNA" INFERRED |
| 0x14 | HVERSION | 32 | ASCII | stored to `info+0x378d` (NUL forced at +0x20) and `dev_super+0x13f`. API `HVer[%s]` (0x2bafe) and API `version` key `HVERSION` (0x14714e). | 0x2af2c..0x2af5e CONFIRMED |
| 0x34 | unknown16 | 16 | ASCII? | stored to `info+0x37ae` (NUL at +0x10). Printed as second `%s` of `%s-%s-%s-%s`. | 0x2af5c..0x2af7a CONFIRMED; meaning unknown |
| 0x44 | unknown8 | 1 | n/a | stored to `info+0x37df`; no reader found | 0x2af76..0x2af82 CONFIRMED |
| 0x45 | HWTYPE | L+1 | ASCII, NUL terminated | `strlen` gives L. Copied to `info+0x37bf` and `dev_super+0x7c`. API `HWTYPE` (0x147104). Matched against the AUP hardware list during upgrade. | 0x2af7e..0x2afa6 CONFIRMED |
| 0x46+L | SWTYPE | L2+1 | ASCII, NUL terminated | copied to `info+0x37cf` and `dev_super+0xbc`. API `SWTYPE` (0x14711e). Matched against the AUP software list. | 0x2afaa..0x2aff2 CONFIRMED |
| 0x47+L+L2 | max_level | 1 | n/a | stored to `info+0x37e0`, logged `max level:%d`. Bounds the work level at bring-up, the factory level index, and the 0x71 table length. | 0x2aff6..0x2b010, 0x2b590 CONFIRMED |

The copy into `info+0x37bf` has no length bound (L comes from `strlen`), so an HWTYPE longer than 16 bytes overwrites SWTYPE storage (CONFIRMED: plain `memcpy` with length L, 0x2af96). Values seen in the AUP file suggest HWTYPE `PMMv1_X1` and SWTYPE `MM318_X2` or `MM318_X2_OOW` (INFERRED, section 7.1).

### 3.3 0x12 AVANANO_P_SYNC (TX)

Header: 8-byte constant at 0x155a68 = `12 00 00 00 01 00 11 00`: type 0x12, opt 0, idx 0, cnt 1, len 0x11. Loaded at 0x2b0b4 and stored at 0x2b0be. CRC `crc16(pkt+4, 0x19)` (0x2b090..0x2b0c6) (CONFIRMED).

| Payload off | Field | Size | Endian | Meaning | Evidence |
|---|---|---|---|---|---|
| 0x00 | HDNA | 16 | bytes | echo of DETECT_ACK payload 0x04..0x13 (`info+0x377c`) | 0x2b088..0x2b0a0 CONFIRMED |
| 0x10 | flag | 1 | n/a | constant 1 | `sb 1, 0x4c(sp)` 0x2b0a6 CONFIRMED; meaning unknown |

### 3.4 0x13 SYNC_ACK (RX)

Accepted if `rx[4] == 0x13` (0x2b2e2..0x2b2e8). Name INFERRED from `SYNC_ACKerr`.

| Payload off | Field | Size | Endian | Meaning | Evidence |
|---|---|---|---|---|---|
| 0x03 | mm_status | 1 | n/a | to `info+0x37e1`; log `mm_status:%d`; API `HashStatus[%d]` | `lbu 0xcf(sp)` 0x2b454 CONFIRMED |
| 0x00..0x02 | unknown | 3 | n/a | not read | CONFIRMED (not read) |

### 3.5 0x41 SET_WORKLEVEL (TX)

Header: `sd 0x0004000100000041`: type 0x41, opt 0, idx 0, cnt 1, len 4. CRC over 0xc bytes (CONFIRMED 0x2b35a, 0x2f60e, 0x2b9c6).

| Payload off | Field | Size | Endian | Meaning | Evidence |
|---|---|---|---|---|---|
| 0 | level | 4 | LE | work / heater level | `sw s4, 0x3c(sp)` 0x2b330; `sw s0, 0xc(sp)` 0x2f608; `sw s1, 0x2c(sp)` 0x2b9c0 CONFIRMED |

Three producers (CONFIRMED):
- `detect_modules`, step 5 above. Direct send.
- `avalon_set_heater_level(cgpu, idx, level)` (0x2f5d0). Mechanism C with slot `info+0x3820+idx*0x8c` and flag `info+0x381e+idx`. Every caller passes `idx = 0` (0x4bbce, 0xb605c, 0xb66aa). Returns 0 on success, -1 if the slot stays busy.
  - Web `device_mode_config_process`: level 0..2 (`bgeu 2, level`, 0x4bb98).
  - Button `button_detect_cb` and `misc_thread`: `level = (syscfg work_level + 1) % (info[0x64d0] + 1)`, so the button cycles through the levels (0xb6694..0xb6064).
- API `ascset ... worklevel,set,<n>` (`set_avalon_device_worklevel`, 0x2b8e0). Format `%[^,],%d`, `n` truncated to a byte, accepted only if `n <= 2`. Mechanism C. `worklevel,get` returns `worklevel %d` from syscfg; no packet.

### 3.6 0x31 SET_FINISH (TX)

Header: `sd 0x0000000100000031`: type 0x31, opt 0, idx 0, cnt 1, len 0. `crc16(pkt+4, 8)` (CONFIRMED 0x2f566, 0x2b4f6). No payload. Mechanism B. Sent last in bring-up. The standalone `avalonnano_set_finish` (0x2f540) has no direct caller and no pointer to it was found; the code in `detect_modules` is an inlined copy (CONFIRMED). Upstream cgminer sends the DNA in SET_FIN; this build sends an empty payload (CONFIRMED).

## 4. Frequency (0x32)

### 4.1 Entry point

API `ascset|0,frequency,<arg>`. Dispatch: `avalon12_set_device` (0x29340) over the `device_cmds` table at 0x236a98, entry 2 = `frequency` -> `set_avalon_device_freq` (0x2ea00) (CONFIRMED).

`sscanf(arg, "%d:%d:%d:%d-%d-%d-%d", &f0, &f1, &f2, &f3, &addr, &miner_id, &asic_id)` (0x2ea42). `addr`, `miner_id`, `asic_id` default to 0. `f0..f3` have no default (uninitialized if missing) (CONFIRMED).

Validation (CONFIRMED):
- `f3 > 1195` (0x4ab) gives `Invalid value passed to set_avalon12_device_freq`. Only f3 is range checked (0x2ea46..0x2ea4c).
- `addr != 0` gives `Invalid modular index`.
- `miner_id > 1` gives `Invalid miner id`. With the module enabled, `miner_id > info[0x381c]` (miner count) gives `Invalid miner index`.
- `asic_id > info[0x381d]` (ASIC count) gives `Invalid asic id`.
- Module not enabled (`info+0x3761 == 0`), or slot busy: reply `set hash freq failed!!`.

The values are also kept in `info+0x4d74..0x4d80` (4 x u32) (0x2eade..0x2eaea, 0x2ebf2..0x2ec02).

Units: MHz (INFERRED: log `Update frequency to %d`, statline `%4dMhz`, option help `range:[25, 800] ... example: 550`).

### 4.2 0x32 SET_FREQ header (TX)

| Case | idx (off 6) | cnt (off 8) | len | Evidence |
|---|---|---|---|---|
| `asic_id == 0` (all ASICs, log `broadcast set pll`) | 0 (`sw 0x32` writes type, opt and idx=0) | `info[0x381c]` (miner count, 1 after bring-up) | 0x20 | 0x2ebac..0x2ebc0, 0x2ecb6..0x2ecca CONFIRMED |
| `asic_id = N > 0` (one ASIC) | N-1 | `(info[0x381c] * info[0x381d]) & 0xff` (total ASICs) | 0x20 | 0x2ed7c..0x2edb0, 0x2ee76..0x2eeaa CONFIRMED |

`miner_id` is never encoded. `miner_id` 0 and 1 produce the same frame (CONFIRMED: both code paths build identical layouts). CRC `crc16(pkt+4, 0x28)`. Mechanism C.

### 4.3 0x32 SET_FREQ payload (32 bytes)

| Payload off | Field | Size | Endian | Meaning | Evidence |
|---|---|---|---|---|---|
| 0x00 | freq[0] | 4 | LE | PLL 0 frequency (MHz, INFERRED) | `sw a2, 0x6c(sp)` 0x2eb0a CONFIRMED |
| 0x04 | freq[1] | 4 | LE | PLL 1 | `sw a1, 0x70(sp)` 0x2eb0c CONFIRMED |
| 0x08 | freq[2] | 4 | LE | PLL 2 | `sw a0, 0x74(sp)` 0x2eb0e CONFIRMED |
| 0x0c | freq[3] | 4 | LE | PLL 3 | `sw a5, 0x78(sp)` 0x2eb10 CONFIRMED |
| 0x10 | const100 | 4 | LE | always 100 (0x64) | `sw 0x64, 0x7c(sp)` 0x2eb04, 0x2ec74, 0x2ed52, 0x2edf0 CONFIRMED; meaning unknown |
| 0x14 | asic_timeout | 4 | LE | `(0x19000000 / max(f0,f1,f2,f3,1)) * 83 / 100`, 32-bit unsigned | `divuw`/`mulw 0x53`/`divuw 0x64` 0x2eb5a..0x2eb78 CONFIRMED. Same as upstream `AVA10_ASIC_TIMEOUT_CONST` 419430400 = 2^32*1000/(256*40) (INFERRED) |
| 0x18 | zero | 4 | LE | 0 | `sd zero, 0x84(sp)` 0x2eb06 CONFIRMED |
| 0x1c | asic_sel | 4 | LE | 0xff = all ASICs, else ASIC index N-1 | `sw 0xff, 0x88(sp)` 0x2eb6a; `sw s5/a3, 0x88(sp)` 0x2ed58, 0x2ee56 CONFIRMED |

Example, all ASICs at 500 MHz: payload `f4 01 00 00` x4, `64 00 00 00`, `bd 9f 0a 00`, `00 00 00 00`, `ff 00 00 00`; header idx 0, cnt 1.

### 4.4 Option path

`--avalon10-freq f0:f1:f2:f3` (option table entry at 0x236090) -> `set_avalonnano_freq` (0x2f1d0). Each value must be `<= 1195`. The values go to `opt_avalon12_freq[4]` (0x236ce8). No reader of `opt_avalon12_freq` was found by a static scan, and no packet is built (CONFIRMED / INFERRED unused).

## 5. Voltage

### 5.1 0x43 SET_VOLTAGE (TX), API `hashpower`

API `ascset|0,hashpower,<v>` -> `set_avalon_device_hash_power` (0x298a0), which inlines `avalon_set_voltage`.
- Empty argument: reply `PS[%d %d %d %d %d %d %d]` from `info+0x643e..0x644a` (7 x s16, filled by polling). No packet.
- `sscanf("%d")` must return 1, else `Invalid volt values`.
- `v == 0`: log `Power off hash now ...` and `Update volt adjust switch info`, reply `Power off hash now ...`. **No packet** (CONFIRMED 0x29a6c..0x29a9e).
- Else `300 <= v <= 350` is required (`addiw -0x12c`, `bltu 0x32`, 0x298fa..0x29902), else `Invalid volt values`.
- Log `Setting power of hash: %d`, `nano set voltage %d`. Build 0x43. Mechanism C. Slot busy: `set hash voltage failed!!`. On success the reply becomes the `PS[...]` string (fall-through to 0x299b0).

Header: `sd 0x0002000100000043` (0x29954): type 0x43, opt 0, idx 0, cnt 1, len 2. `crc16(pkt+4, 0xa)` (CONFIRMED).

| Payload off | Field | Size | Endian | Meaning | Evidence |
|---|---|---|---|---|---|
| 0 | voltage | 2 | LE | value 300..350 as typed by the user | `sh s3, 0x3c(sp)` 0x29928 CONFIRMED; unit unknown |

### 5.2 Option path (`1150-1450` and `0-75` strings)

| Option | Callback | Behavior | Tag |
|---|---|---|---|
| `--avalon10-voltage <mV>` (help: `range:[1150, 1450], step: 1`) | `set_avalonnano_voltage` 0x2f320 | `level = (v - 1097) / 4` (signed, truncating). Accepted only if `300 <= level <= 350`, which means `2297 <= v <= 2500`. The help text range does not match the code. `level` is stored to `opt_avalon12_voltage_level` (0x236cf8). | CONFIRMED (0x2f33c..0x2f36a) |
| `--avalon10-voltage-level <n>` (help: `range:[0, 75]`) | `set_avalon12_voltage_level` 0x2f390 | stores `n` to the same global with no range check | CONFIRMED (0x2f3a6..0x2f3ae) |

No reader of `opt_avalon12_voltage_level` was found, so these options never reach the wire (INFERRED unused). Both ranges, 300..350 here and 300..350 in the `hashpower` check, suggest that the 0x43 value uses the same "level" unit, with `mV = 1097 + 4 * value` (INFERRED, unverified). The help strings are left over from Avalon10.

## 6. Work level, factory, OTP, reset

### 6.1 Heater level and work level

Both use 0x41, see 3.5. Valid levels from the API and web: 0..2. At bring-up the level is clamped to `< max_level`, otherwise forced to 1.

### 6.2 0x70 SET_LEVELPARAM (TX), API `factory`

API `ascset|0,factory,<arg>` -> `set_avalon_device_factory` (0x2cb60). `sscanf(arg, "%[^,],%u,%u-%hu-%hu-%hu-%hu:%hu:%hu:%hu", name, &cmd, &level, &h0..&h6)` (0x2cbdc). The return count `n` selects the form.

| Form | Condition | Action | Tag |
|---|---|---|---|
| `param,<cmd>,<level>-<pout>-<temper>-<v>-<f0>:<f1>:<f2>:<f3>` | `cmd <= 1`, `level <= max_level`, `n == 10` | log `levelparam set vf,level:%d,pout:%d,temper:%d,vf:%d-%d:%d:%d:%d`; send 0x70 with len 0x10; reply `factory set ok` | CONFIRMED 0x2cdb4..0x2cdc4, 0x2ce38..0x2cfd6 |
| `param,<cmd>,<level>` | `cmd <= 1`, `level <= max_level`, `n == 3` | send 0x70 with len 2; reply `factory set ok` | CONFIRMED 0x2cdca, 0x2cf0c..0x2cf76 |
| `param,2,<level>` | `level <= max_level` | reply `factory get: level%d %d-%d-%d-%d:%d:%d:%d` from the local table `dev_super+0xfc + level*14` (filled by 0x71). No packet. | CONFIRMED 0x2cd38..0x2cdb0 |
| `param,<cmd>,...` | `cmd <= 1`, `level <= max_level`, `n` not 3 and not 10 (e.g. `param,1,2-60`) | reply `factory set ok`, no packet | CONFIRMED 0x2cdce |
| `param,2,<level>` | `cmd == 2`, `level > max_level` | no reply text at all | CONFIRMED 0x2cd48 |
| `resetfac` | none | `dev_super+0x160 = 1`, reply `resetfac set`. No packet (K230 config reset, see section 1). | CONFIRMED 0x2cc8c..0x2ccae |
| other | none | `factory unknown argument`; `factory set level error` applies only to `cmd <= 1` with `level > max_level` | CONFIRMED |

Header, long form: constant at 0x155a80 = `70 00 00 00 01 00 10 00` (type 0x70, idx 0, cnt 1, len 0x10). `crc16(pkt+4, 0x18)` (0x2ceba..0x2cecc).
Header, short form: `sd 0x0002000100000070` (len 2). `crc16(pkt+4, 0xa)` (0x2cf48..0x2cf52).
Mechanism C for both (CONFIRMED).

| Payload off | Field | Size | Endian | Meaning | Evidence |
|---|---|---|---|---|---|
| 0x00 | cmd | 1 | n/a | 0 or 1 (2 is handled locally as "get") | `sh (cmd \| level<<8), 0x7c(sp)` 0x2ce4c / 0x2cf2a CONFIRMED; meaning of 0 vs 1 unknown |
| 0x01 | level | 1 | n/a | level index, `<= max_level` | same store CONFIRMED |
| 0x02 | pout | 2 | LE | output power (INFERRED from log `pout`) | `sd 0x7e(sp)` from sp+0x50 0x2ce5a CONFIRMED (long form only) |
| 0x04 | temper | 2 | LE | target temperature (INFERRED from log `temper`) | same `sd` CONFIRMED |
| 0x06 | vf_v | 2 | LE | voltage of the level (INFERRED: first value of `vf:%d-%d:%d:%d:%d`) | same `sd` CONFIRMED |
| 0x08 | vf_f0 | 2 | LE | PLL 0 frequency (INFERRED) | same `sd` CONFIRMED |
| 0x0a | vf_f1 | 2 | LE | PLL 1 | `sw 0x86(sp)` 0x2ce68 CONFIRMED |
| 0x0c | vf_f2 | 2 | LE | PLL 2 | same `sw` CONFIRMED |
| 0x0e | vf_f3 | 2 | LE | PLL 3 | `sh 0x8a(sp)` 0x2ce78 CONFIRMED |

The short form carries only bytes 0x00..0x01.

### 6.3 0x71 LEVELPARAM report (RX, cross reference)

Decoded in `polling` (jump table at 0x155640, entry for 0x71 = 0x2d2a4). This is the only source of the `factory get` table (CONFIRMED).

| Payload off | Field | Size | Endian | Meaning | Evidence |
|---|---|---|---|---|---|
| 0x00 | unknown | 1 | n/a | to `info+0x64a1` | 0x2d2a4..0x2d2ae CONFIRMED |
| 0x01 | unknown | 2 | LE | to `info+0x64a2` | 0x2d2b2..0x2d2ba CONFIRMED |
| 0x03 + 14*i | level i params | 14 | 7 x u16 LE | same order as 0x70 payload 0x02..0x0f (pout, temper, v, f0..f3). Copied to `dev_super+0xfc+14*i` and `info+0x64a4+14*i` for `i < max_level`. | 0x2d2c6..0x2d322 CONFIRMED; field names INFERRED |

### 6.4 0x42 RESET_K210 (TX)

`avalon12_reset_k210` (0x2f6b0): header `sd 0x0000000100000042` (type 0x42, idx 0, cnt 1, len 0), `crc16(pkt+4, 8)`, direct send. Then, if the module is enabled, it clears `info+0x3761` and logs `Module detached! ID[%d]` (inlined `detach_module`). It sets `info->0x18 = 0` (CONFIRMED). No direct call and no stored pointer to this function were found, so it looks unused in this build (INFERRED).

Hardware reset of the K210 (not a packet): API `reboot,slave` drives GPIO 61 (0x3d) low, waits 1 ms, then drives it high (0x29840..0x29856). `misc_thread` pulses the same GPIO before `am_system_reboot` (0xb5f3a..0xb5f5e) (CONFIRMED). GPIO 61 being the K210 reset line is INFERRED.

### 6.5 ASIC OTP

`set_avalon12_asic_otp` (0x2f3d0): `sscanf("%d")`, value 0..119 (`<= 0x77`), else `Invalid value passed to avalon12-cinfo-asic`. Stored to `opt_avalon12_asic_otp` (0x236cfc) (CONFIRMED). No pointer to the function exists in the option table, and no reader of the global was found. No OTP packet exists in this slice (INFERRED: dead code from avalon12).

## 7. Firmware upgrade

### 7.1 AUP file format (checked by `upgrade_aup`, 0x2fec0)

Verified against `rootfs/data/upgrade_aup` (202957 bytes) with a standalone Python parser. zlib CRC32 values match the stored ones (CONFIRMED).

| File off | Field | Size | Endian | Value in dump | Check in code | Tag |
|---|---|---|---|---|---|---|
| 0x00 | magic | 16 | ASCII | `AUP format` | first 8 bytes == `AUP form` (0x155a98), u16 at 8 == `at` (0x7461), else `Err08: AUP file header error` | CONFIRMED 0x2fef8..0x2ff4a |
| 0x10 | format_version | 4 | LE | 2 | logged only (`AUP format version: %d`) | CONFIRMED 0x2ff60 |
| 0x14 | payload_len | 4 | LE | 0x31805 | CRC length and write length | CONFIRMED 0x3006e |
| 0x18 | build_id | 64 | ASCII | `24071801_b906c52_6223725` | not read | INFERRED |
| 0x58 | payload_crc32 | 4 | LE | 0xde8760b2 | `crc32(0, payload, payload_len)` must match, else `Err08: AUP Checksum of payload failed` | CONFIRMED 0x30072..0x3008a |
| 0x5c | hw_count | 4 | LE | 1 | list size | CONFIRMED 0x2ff9c |
| 0x60 | sw_count | 4 | LE | 2 | list size | CONFIRMED 0x2ffa0 |
| 0x64 | names | 32 x (hw+sw) | ASCII | `PMMv1_X1`; `MM318_X2`, `MM318_X2_OOW` | hw names compared (`strncmp`, 32) with HWTYPE (`dev_super+0x7c`); sw names compared with SWTYPE (`dev_super+0xbc`). No match: `Err08: AUP is NOT for this hardware/software!` | CONFIRMED 0x2ffe2..0x3006a |
| H = 0x64+32*(hw+sw) | header_crc32 | 4 | LE | 0xe60a968b at 0xc4 | `crc32(0, file, H)` must match, else `Err08: AUP Checksum of header failed` | CONFIRMED 0x2ffa6..0x2ffc2 |
| H+4 | payload | payload_len | opaque (encrypted per NOTES.md) | 0xc8..end | sent to the K210 unchanged | CONFIRMED |

On success `upgrade_aup` writes `/opt/upgrade_aup` = `[u32 LE crc32(payload)] [payload]`. It opens with `O_WRONLY|O_CREAT|O_TRUNC` (0x241, mode 0644) and writes the 4 CRC bytes, then reopens with `O_WRONLY|O_APPEND` (0x401) and writes the payload (CONFIRMED 0x300b4..0x30102). For the dump: 202761 bytes.

How the AUP reaches `upgrade_aup` (K230 side only, not on the UART):
- API `upgrade`: `set_avalon_device_upgrade` (0x306d0) -> `set_avalon_upgrade` (0x30400). The argument is hex text. The first 64 hex chars are a 32-byte block header: byte 0 = API version (must be 2), byte 1 = header size (must be 0x1e), +0x08 uid, +0x14 total size, +0x18 offset, +0x1c u16 chunk length. Data hex follows from char 0x40 (CONFIRMED 0x3041c..0x3048a).
- `avalonnano_trig_upgrade` (0x30800) reads `/data/upgrade_aup` directly. It is the SIGUSR1 handler: `init_signal_handlers` registers it with `sigaction(10, {sa_handler = 0x30800})` (CONFIRMED 0x15840..0x15854), so `kill -USR1 <btcminer pid>` starts a K210 upgrade from `/data/upgrade_aup`. The handler calls `upgrade_aup` (and so rewrites `/opt/upgrade_aup`) before it checks the K210 is connected, and calls `malloc`/`fopen`/`zlog` from signal context.
- Web: `upgrade_load` (0x4e17c).

Each path calls `upgrade_aup` and then marks the miner thread (`thr->0x78 = 1`). `miner_upgrade` (mmu.c, 0xb5d00) waits on a semaphore and calls `avalonnano_upgrade` = `upgrade_send_data` up to 5 times per miner. Then it runs `killall -USR1 miner_upgrade_post.sh` (all succeeded) or `-USR2` (CONFIRMED).

### 7.2 Transfer (`upgrade_send_data`, 0x2fad0)

File: `/opt/upgrade_aup`, size S (`stat`, `st_size` at sp+0x110).
- Chunk size 128 (0x80). `count = ceil(S / 128)`, truncated to 16 bits. `rem = S % 128` (CONFIRMED 0x2fb1e..0x2fb5a).
- For the dump file: S = 202761, count = 1585, last chunk 9 bytes.

**Data chunk k (k = 0 .. count-1)**, TX 0x90:

| Field | Value | Evidence |
|---|---|---|
| type / opt | 0x90 / 0 | `sh 0x90, 0x164(sp)` 0x2fbd6 CONFIRMED |
| idx (off 6) | k (0-based chunk index) | `sh s6, 0x166(sp)` 0x2fbe6 CONFIRMED |
| cnt (off 8) | count | `sh a5, 0x168(sp)` 0x2fbe2 CONFIRMED |
| len (off 10) | 128; last chunk: `rem` | `sh s1, 0x16a(sp)` 0x2fbea; last-chunk select 0x2fba8..0x2fbba CONFIRMED |
| payload | file bytes `[128*k, 128*k + len)`, raw | `lseek(fd, k*128)`, `read`, `memcpy` 0x2fb9a..0x2fbee CONFIRMED |
| CRC | `crc16(pkt+4, len+8)` | 0x2fbf4..0x2fc02 CONFIRMED |

Chunk 0 therefore starts with the 4-byte LE CRC32 of the AUP payload (CONFIRMED from the file layout).
If S is an exact multiple of 128, `rem = 0` and the code still sends the last chunk with len 0 and reads no data, so the last 128 bytes are never sent (CONFIRMED arithmetic; INFERRED to be a bug).

Per-chunk handshake: inner loop of 5 sends with 200 ms receive timeout each. Accepted reply: `rx[4] == 0x91` (CONFIRMED 0x2fc9c..0x2fca4). No payload field of the 0x91 reply is checked for data chunks. A reply of another type, or 5 timeouts, uses one of 5 outer rounds (1 ms sleep each). After 5 failed rounds: log `k230 send msg count > 5`, return -1 (CONFIRMED 0x2fc7c..0x2fc98, 0x2fdd2). Up to 25 sends per chunk. After an ACK: `cgsleep_ms(1)`, next chunk.

**End packet**, TX 0x90 with len 0:

| Field | Value | Evidence |
|---|---|---|
| idx (off 6) | count | `sh a5, 0x166(sp)` 0x2fcdc CONFIRMED |
| cnt (off 8) | count | `sh a5, 0x168(sp)` 0x2fce0 CONFIRMED |
| len | 0 | `sh zero, 0x16a(sp)` 0x2fcd8 CONFIRMED |
| CRC | `crc16(pkt+4, 8)` | 0x2fcd6..0x2fcf0 CONFIRMED |

So `idx == cnt` marks the end. There is no separate start packet: chunk 0 starts the transfer.

**End ACK**, RX 0x91 (mechanism A: 5 sends, 200 ms each):

| Payload off | Field | Size | Meaning | Evidence |
|---|---|---|---|---|
| 0 | verify_ok | 1 | must be 1; log `k210 crc16 verfity ok`; return 0 | `lbu 0x1fc(sp)` == 1 0x2fd76..0x2fd7c CONFIRMED |

Any other type or value, or a timeout: log `AVANANO_P_UPGRADE: recv xfer data with err %d` (0 for a bad ACK, -1 for timeout), return -1 (CONFIRMED 0x2fe56..0x2feb4). The log says "crc16", but the only image checksum carried is the CRC32 in chunk 0 (INFERRED: the K210 checks that CRC32 or its own sum).

## 8. Open questions

1. What do SET_FREQ payload 0x10 (constant 100) and SYNC payload 0x10 (constant 1) mean? What do DETECT_ACK payload 0x00 (u32), 0x34 (16 bytes) and 0x44 (u8) mean? None of these is read back anywhere found.
2. What is the unit of the 0x43 voltage value (300..350)? Does `mV = 1097 + 4 * value` hold? The `--avalon10-voltage` parser implies it, but its result is never used.
3. What are the meanings of `cmd` 0 vs 1 in 0x70 SET_LEVELPARAM? Does the K210 answer 0x70 with 0x71, or does it send 0x71 on its own (for example after SYNC)? That depends on the polling slice.
4. Does the K210 acknowledge 0x31, 0x41, 0x32, 0x43, 0x70 at all? The K230 never waits for an ack (mechanisms B and C). Any reply would land in `polling`'s decoder (types 0x50..0x55, 0x60, 0x71 in the jump table, plus 0x11/0x15).
5. Is HDNA (DETECT_ACK 0x04..0x13) printable ASCII (it is printed with `%s`) or binary? It needs a capture.
6. `max_level` semantics: bring-up uses `level < max_level`, but `factory` accepts `level <= max_level`, and 0x71 holds `max_level` entries. Is this an off-by-one in the stock code?
7. Upgrade edge case: a `/opt/upgrade_aup` whose size is a multiple of 128 loses its last 128 bytes (len-0 last chunk). How does the K210 behave then? Does a 0x91 data-chunk ACK carry a status or index that the K230 ignores?
8. Is `avalon12_reset_k210` (0x42) reached through an indirect call not found by the static scan, and does the K210 still implement 0x42?
9. Is GPIO 61 the K210 reset/boot line? Is a reset needed before the K210 accepts 0x90 chunks, or does the running MM firmware accept them in normal mode?

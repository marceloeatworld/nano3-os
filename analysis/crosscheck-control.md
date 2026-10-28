# Cross-check: protocol-control.md vs Ghidra pseudocode

Date: 2026-10-27. Scope: the CONTROL path only (bring-up, 0x31, 0x32, 0x41, 0x42, 0x43, 0x70, 0x90/0x91, pending slot, settings that never reach the UART).

## Method

- Inputs: `protocol-control.md` (under review), `PROTOCOL.md` (overview and live results), `ghidra/*.c` (Ghidra 12.1.4 pseudocode, 26 files), `btcminer.annot.asm` (llvm-objdump with T-Head extensions decoded).
- Ghidra `FUN_ram_000XXXXX` PLT calls were renamed with `plt-map.txt` (slot = 0x14450 + offset) in a scratch copy. The originals were not modified.
- Every claim was first compared with the Ghidra pseudocode. Where Ghidra shows `custom0...` (T-Head ops it does not model: `th.lrd`, `th.srd`, `th.lrbu`, `th.lurwu`, `th.mula`, `th.extu`, `th.mveqz`, `th.mvnez`, `th.lrw` jump tables), or where the function has no Ghidra file, the same address was read in the asm.
- Strings and constants were read with `tools/addr2str.py`, data tables by reading ELF bytes, cross references with `tools/xref.py` and `tools/findptr.py`. Static reading only; nothing from the dump was executed.
- Verdicts: MATCH = Ghidra shows the same. MISMATCH = Ghidra and the document disagree; the asm decides. NOT VISIBLE = Ghidra output is garbled by `custom0` or the function has no Ghidra file; the asm result is stated.

## Verdict table

### A. Conventions and send mechanisms

| # | Claim | Document | Ghidra | Verdict | Evidence |
|---|---|---|---|---|---|
| A1 | Header layout | off 4 type, 5 opt=0, 6 idx u16, 8 cnt u16, 10 len u16; usually one `sd` of a 64-bit constant | Same constants: `0x4000100000010`, `0x11000100000012`, `0x100000031`, `0x4000100000041`, `CONCAT22(idx,0x32)` | MATCH | detect_modules.c, set_avalon_device_freq.c, avalonnano_set_finish.c |
| A2 | Endianness | All multi-byte fields LE, no byte swap | Native stores only (`CONCAT44`, `sw`, `sh`) | MATCH | No `th.rev*`/`rev8` in any of the 23 control functions extracted from the asm |
| A3 | Mechanism A | send, `data_toast_receive(rx,200)`, up to 5 sends, log line 820 on each failure | Counter 4 down to -1 (5 sends), timeout 200, `avalonnano_xfer_pkg` line 0x334 | MATCH | detect_modules.c (both loops), upgrade_send_data.c (end loop) |
| A4 | Mechanism B | One `data_toast_send`, no wait: 0x41 in detect, 0x31, 0x42 | Same | MATCH | detect_modules.c, avalonnano_set_finish.c, avalon12_reset_k210.c |
| A5 | Mechanism C producer | Wait for flag `info+0x381e` == 0 (5 tries, `usleep(1000)`), copy 0x8c bytes to `info+0x3820`, set flag | Same loop in freq, hash_power, worklevel, factory, heater_level | MATCH | asm 0x2eed4, 0x29998, 0x2b9f8, 0x2d026, 0x2d03e, 0x2f650 |
| A6 | Mechanism C consumer | `avalonnano_scanhash` logs `DO SEND ASSET CMD`, sends `info+0x3820`, clears flag, no wait | Same (`plVar1 + 0x704` = info+0x3820) | MATCH | asm 0x2e7c2..0x2e7ea (see new finding N2) |
| A7 | Mechanism C failure reporting | "If the slot stays busy the producer fails (`set hash freq failed!!`, `set hash voltage failed!!`, or return -1)" | API worklevel returns 0 with no reply text; factory falls through to `factory set ok` | MISMATCH, Ghidra right | asm 0x2b9f4 `j 0x2b950` (no reply written); 0x2cef2 and 0x2cf76 `j 0x2cdce` = `sprintf("factory set %s","ok")` |
| A8 | `data_toast_receive` | Returns 0 and copies one 0x8c frame, else non-zero | No Ghidra file | NOT VISIBLE, asm confirms | 0xb57d4..0xb57ea `memcpy(...,0x8c)`; 0xb5824 returns -1 when empty and timeout <= 0; else `pthread_cond_timedwait` result |
| A9 | `info` pointer | `info = cgpu->device_data` (`ld 0x28(cgpu)`) | `*(long *)(param_1 + 0x28)` | MATCH | all files |

### B. Packet summary and "not on the wire"

| # | Claim | Document | Ghidra | Verdict | Evidence |
|---|---|---|---|---|---|
| B1 | Packet table | Types, directions, idx/cnt, lengths of section 1 | Consistent with every builder below | MATCH | rows C, D, E, F, G, H |
| B2 | `softon`/`softoff` stubs | Return 0 | No Ghidra file | NOT VISIBLE, asm confirms | 0x28f20 and 0x29890: `li a0,0; ret`; table entries 8/9 at 0x236b58/0x236b70 |
| B3 | `hashpower 0` | Logs only, no packet | Two logs, reply `Power off hash now ...`, return | MATCH | set_avalon_device_hash_power.c; asm 0x29a6c..0x29a9e |
| B4 | `factory resetfac` | Only `dev_super+0x160 = 1` | Same, reply `resetfac set` | MATCH | set_avalon_device_factory.c; asm 0x2cca2 |
| B5 | `factory_reset(0)` | Called by misc_thread, edits K230 config only | No Ghidra file | NOT VISIBLE, asm confirms | jal at 0xb61b2; `factory_reset` calls only `iniparser_*`, `fopen`, `fsync`, `system`, `sprintf`, `zlog` |
| B6 | `--avalon10-freq/-voltage/-voltage-level` | Only write globals | freq and voltage callbacks store globals, build no frame; voltage-level has no Ghidra file | MATCH | asm 0x2f3a6..0x2f3ae (voltage-level); `xref.py` finds no reader of 0x236ce8..0x236cfc |
| B7 | `set_avalon12_asic_otp` | Builds nothing | Stores global only | MATCH | set_avalon12_asic_otp.c |
| B8 | `led`, `fan-spd`, `loop`, `pdelay`, `password` | Build no frame | No Ghidra files | NOT VISIBLE, asm confirms | Only callers of `data_toast_send`: detect_modules, avalonnano_set_finish, avalon12_reset_k210, avalonnano_scanhash, polling, avalonnano_sswork_update, upgrade_send_data. `crc16` adds only the 5 pending-slot producers, copy_pool_stratum and the RX thread |

### C. Bring-up (detect_modules, 0x2ad20)

| # | Claim | Document | Ghidra | Verdict | Evidence |
|---|---|---|---|---|---|
| C1 | Early return | Return if `info+0x3761` set | Same | MATCH | asm 0x2ad34..0x2ad5e |
| C2 | Callers and 30 s timer | From avalonnano_detect and scanhash; scanhash calls it when `tdiff > 30.0` and `info->0x18 == 0` | Same; `info->0x18 = info[0x3761]` after the call | MATCH | jal 0x2b794, 0x2e8ae; double 30.0 at 0x155a88 |
| C3 | DETECT 0x10 frame | idx 0, cnt 1, len 4, payload 4 x 0, CRC over 0xc | `memset(payload,0,0x80)`, `0x4000100000010`, `crc16(..,0xc)` | MATCH | asm 0x2adbe..0x2ade4 |
| C4 | DETECT failure | Timeout after 5: log `recv xfer data with err`, return. Wrong type: log `ack(0x%x), but 0x%X` (0x11, type), return | Same; wrong type returns at once, no retry | MATCH | asm 0x2ae62..0x2aeb4, 0x2b1ec..0x2b244 |
| C5 | Same-module shortcut | If enabled and the 16-byte HDNA matches, return | Same test, first 8 bytes via `custom0.rd` | MATCH | asm `th.lrd` 0x2aeec, `ld` 0x2b19c (see N4: unreachable) |
| C6 | ACK payload 0x00 | u32 to `info+0x3778` | `*(int*)(info+0x3778) = payload[0..3]` | MATCH | asm 0x2af10..0x2af18 |
| C7 | ACK HDNA 0x04..0x13 | To `info+0x377c` | `custom0()` then store at 0x3784 only | NOT VISIBLE (first 8 bytes), asm confirms | `th.srd a5,s3,s5` (s5=0x377c) 0x2af26; `sd` 0x2af32 |
| C8 | ACK HVERSION 0x14 | 32 bytes to `info+0x378d`, NUL at +0x20, `dev_super+0x13f` | Same | MATCH | asm 0x2af3e..0x2af5e |
| C9 | ACK 0x34 | 16 bytes to `info+0x37ae`, NUL at +0x10 | `custom0()` then 0x37b6 and NUL at 0x37be | NOT VISIBLE (first 8 bytes), asm confirms | `th.srd` 0x2af66, `sd` 0x2af72, `sb zero` 0x2af7a |
| C10 | ACK 0x44 | u8 to `info+0x37df` | Same | MATCH | asm 0x2af76..0x2af82 |
| C11 | HWTYPE at 0x45 | `strlen` L, copy to `info+0x37bf` and `dev_super+0x7c` | `memcpy(info+0x37bf, payload+0x45, L)`, `strncpy(dev_super+0x7c, .., L)` | MATCH | asm 0x2af7e..0x2afa6 (see N5) |
| C12 | SWTYPE at 0x46+L | To `info+0x37cf` and `dev_super+0xbc` | `&uStack_184 + (L + 0x46)`, same targets | MATCH | asm 0x2afaa..0x2aff2 |
| C13 | max_level at 0x47+L+L2 | To `info+0x37e0` | `custom0.rd()` | NOT VISIBLE, asm confirms | `th.lrbu a5,a5,s0` with a5 = payload, s0 = (L+0x46)+(L2+1), 0x2b004 |
| C14 | Unbounded HWTYPE copy | `memcpy` length L, no bound | Same | MATCH | asm 0x2af96 |
| C15 | Bring-up logs | `%s-%s-%s-%s` (HDNA, 0x34 field, HWTYPE, SWTYPE), `max level:%d` | Same arguments | MATCH | detect_modules.c |
| C16 | SYNC 0x12 frame | Header constant at 0x155a68, len 0x11, CRC over 0x19, HDNA echo + byte 1 | Header `0x11000100000012`, `crc16(..,0x19)`, payload `custom0.rd()` + `info+0x3784` + byte 1, rest zeroed | MATCH (first 8 payload bytes NOT VISIBLE; asm `th.lrd` 0x2b088 confirms) | asm 0x2b044..0x2b0ca |
| C17 | SYNC failure | Not fatal; timeout log; wrong type logs `SYNC_ACKerr:0x%X`; both continue | Both paths jump to the work-level step; wrong type does not retry; mm_status is not written | MATCH | asm 0x2b142..0x2b198, 0x2b2e2..0x2b2f8, 0x2b6ae..0x2b6ea |
| C18 | mm_status | SYNC_ACK payload[3] to `info+0x37e1` | `uStack_184._3_1_` to 0x37e1 | MATCH | asm `lbu 0xcf(sp)` 0x2b454 |
| C19 | Work level step | `syscfg_work_level()` if `< max_level` else 1; `opt_avalon12_power_level`; 0x41 u32 LE; direct send | Same | MATCH | asm 0x2b304..0x2b370 |
| C20 | Local init | `info+0x381c=1`, `0x3761=1`, `cgtime(0x3768)`, `set_target_temp(0x5a)`, -273.0 arrays, counters cleared | Same, plus extra fields (N3) | MATCH | asm 0x2b374..0x2b4c0; float -273.0 at 0x155aa0 |
| C21 | Step 7 | `New module detected! ID[%d-%x]` (0, `info+0x378b`), inline SET_FINISH, log `after avalonnano set finish` | Same | MATCH | asm 0x2b65c..0x2b6a8, 0x2b4d4..0x2b550 |
| C22 | Bring-up order | 0x10, 0x11, 0x12, 0x13, 0x41, 0x31 | Same order | MATCH | Also confirmed live (PROTOCOL.md section 7) |
| C23 | `ID[%d]` in logs | "The `ID[%d]` in logs is the cgminer `device_id`" (section 3.1) | `ID[%d]` argument is the literal 0; `device_id` is the `%d` of the `%s-%d:` prefix | MISMATCH, Ghidra right | asm 0x2b24c..0x2b254: `lw a4,0x10(s2)` stored to 8(sp) (prefix), `sd zero,0x10(sp)` (ID) |
| C24 | RST cross reference | polling sends payload `0x01000000`, clears `info+0x3761/0x3762` | Halfword store 0 at 0x3761, payload 0x1000000 | MATCH | polling.c |
| C25 | Re-run timing after RST | "makes scanhash re-run this whole bring-up within 30 s" | scanhash refreshes the timer `info+0x58` after every poll that returns 0 (reply received); detect runs only when `tdiff > 30.0` | MISMATCH, Ghidra right | asm 0x2e842 `beqz s4` to 0x2e8a4 `cgtime(s2)`; 0x2e7b6 `flt.d`. Bring-up runs about 30 s after the last answered poll, not within 30 s |

### D. Work level, finish, reset, OTP

| # | Claim | Document | Ghidra | Verdict | Evidence |
|---|---|---|---|---|---|
| D1 | 0x41 frame | idx 0, cnt 1, len 4, CRC 0xc, payload u32 level | Same in 3 producers | MATCH | asm 0x2b35a, 0x2f60e, 0x2b9c6 |
| D2 | `avalon_set_heater_level` slot | Slot `info+0x3820+idx*0x8c`, flag `info+0x381e+idx`, 0 or -1 | Flag uses idx; slot shown as `info+0x3820` (idx*0x8c dropped) | NOT VISIBLE (slot offset), asm confirms | `th.mula s4,s3,0x8c` 0x2f658 |
| D3 | heater_level callers | idx 0; web 0..2; button and misc_thread `(wl+1) % (info[0x64d0]+1)` | No Ghidra files | NOT VISIBLE, asm confirms | 0x4bb94..0x4bbd0; 0xb6044..0xb6064; 0xb6692..0xb66b2 (`remuw`, `li a1,0`) |
| D4 | API worklevel | `%[^,],%d`, n truncated to a byte, `n <= 2`, mechanism C; `get` replies `worklevel %d` from syscfg | Same (`bVar1 < 3`, syscfg+0xa88) | MATCH | asm 0x2b916, 0x2b93a; strings 0x1546a8, 0x1546b8 |
| D5 | SET_FINISH 0x31 | len 0, CRC over 8, direct send; standalone copy has no caller | Same frame; no caller | MATCH | No `jal`; `findptr.py`: no data pointer |
| D6 | RESET_K210 0x42 | len 0, CRC 8, direct send, detach if enabled, `info->0x18 = 0` | Same | MATCH | avalon12_reset_k210.c |
| D7 | 0x42 unused | No call, no pointer | No caller visible | MATCH | No `jal`; `findptr.py`, `xref.py`: none |
| D8 | GPIO 61 pulse | `reboot,slave` and misc_thread drive GPIO 0x3d low 1 ms then high | No Ghidra files | NOT VISIBLE, asm confirms | 0x29840..0x29856; 0xb5f3a..0xb5f5e |
| D9 | ASIC OTP | 0..119 to 0x236cfc; no option-table pointer | `0x77 < v` rejects; stores global | MATCH | `findptr.py 0x2f3d0`: no data pointer |

### E. Frequency (0x32)

| # | Claim | Document | Ghidra | Verdict | Evidence |
|---|---|---|---|---|---|
| E1 | Dispatch | `device_cmds` at 0x236a98, entry 2 `frequency` to 0x2ea00 | "Could not recover jumptable"; call rendered through entry 0 | NOT VISIBLE, data confirms | 0x236ac8: name `frequency`, func 0x2ea00, flag 1 |
| E2 | Parse | `%d:%d:%d:%d-%d-%d-%d`; addr, miner, asic default 0; f0..f3 uninitialized | Same | MATCH | asm 0x2ea14..0x2ea42 (only 0x44/0x48/0x4c zeroed) |
| E3 | Validation | Only f3 range-checked (> 1195); addr != 0; miner > 1; miner > count when enabled; asic > `info[0x381d]`; not enabled or busy gives `set hash freq failed!!` | Same order and strings | MATCH | asm 0x2ea46..0x2ea90 |
| E4 | Stored copy | `info+0x4d74..0x4d80` | Same | MATCH | asm 0x2eade..0x2eaea |
| E5 | Header, asic 0 | idx 0, cnt `info[0x381c]`, len 0x20 | `uStack_cc = 0x32`, cnt `info[0x381c]`, len 0x20 | MATCH | asm 0x2ebac..0x2ebc0 |
| E6 | Header, asic N | idx N-1, cnt `(info[0x381c]*info[0x381d]) & 0xff` | Same | MATCH | asm 0x2ed7c..0x2edb0, 0x2ee76..0x2eeaa |
| E7 | CRC and send | `crc16(pkt+4,0x28)`, mechanism C | Same | MATCH | |
| E8 | miner_id | Not encoded; both paths same layout | Both paths build identical frames | MATCH | |
| E9 | Payload | f0..f3 at 0x00..0x0c, 100 at 0x10, 0 at 0x18, selector at 0x1c (0xff or N-1) | Same | MATCH | asm 0x2eb04..0x2eb6a |
| E10 | ASIC timeout | `(0x19000000 / max(f0..f3,1)) * 83 / 100`, u32 | Divisor shown as f1 (asic 0), f0 (asic N), or max(f0,f1); the max chain is dropped | NOT VISIBLE (garbled), asm confirms the document | `th.mveqz`/`th.mvnez` chains 0x2eaf6..0x2eb56, 0x2eddc..0x2ee42, 0x2ec4c..0x2ec58, 0x2ed36..0x2ed42 |
| E11 | 500 MHz example | timeout bytes `bd 9f 0a 00` | Formula gives 838860 * 83 / 100 = 696253 = 0x000a9fbd | MATCH | arithmetic |
| E12 | Option path | `set_avalonnano_freq`, each value <= 1195, `opt_avalon12_freq[4]` at 0x236ce8, no reader | Same (single value fills f0 only) | MATCH | asm 0x2f252..0x2f264 |

### F. Voltage (0x43)

| # | Claim | Document | Ghidra | Verdict | Evidence |
|---|---|---|---|---|---|
| F1 | Empty argument | Reply `PS[...]` from `info+0x643e..0x644a` | Same | MATCH | asm 0x299b0..0x299dc |
| F2 | Parse and range | `%d` must return 1; `300 <= v <= 350`; else `Invalid volt values` | Same (string is the return value) | MATCH | asm 0x298fa..0x29902 |
| F3 | Header and payload | `0x0002000100000043`, CRC 0xa, u16 LE voltage | Same | MATCH | asm 0x29928, 0x29954 |
| F4 | Busy and success | Busy: `set hash voltage failed!!`; success: `PS[...]` reply | Same | MATCH | asm 0x29982, 0x299ae |
| F5 | `--avalon10-voltage` formula | `level = (v - 1097) / 4` signed truncating; accept 300..350, so 2297 <= v <= 2500; store 0x236cf8 | Same expression | MATCH | asm 0x2f33c..0x2f36a |
| F6 | `--avalon10-voltage-level` | Stores n to 0x236cf8, no range check | No Ghidra file | NOT VISIBLE, asm confirms | 0x2f3a6..0x2f3ae |

### G. Factory (0x70)

| # | Claim | Document | Ghidra | Verdict | Evidence |
|---|---|---|---|---|---|
| G1 | Parse | `%[^,],%u,%u-%hu-%hu-%hu-%hu:%hu:%hu:%hu`, return count n selects the form | Same | MATCH | string 0x154a68 |
| G2 | Long form | `cmd <= 1`, `level <= max_level`, `n == 10`, header 0x155a80 (len 0x10), CRC 0x18, reply `factory set ok` | Same | MATCH | asm 0x2cdb4..0x2cdc4, 0x2ceba..0x2cecc |
| G3 | Long payload | cmd, level, pout, temper, v, f0, f1, f2, f3 at 0x00..0x0e | Same stores | MATCH | asm 0x2ce4c, 0x2ce5a, 0x2ce68, 0x2ce78 |
| G4 | Short form | `n == 3`, `0x0002000100000070`, CRC 0xa, payload u16 `cmd + (level << 8)` | Same | MATCH | asm 0x2cf0c..0x2cf52 |
| G5 | Get form | `cmd == 2`: reply from `dev_super+0xfc + level*14` | Index shown as `max_level*0xe` | NOT VISIBLE (garbled), asm confirms level*14 | 0x2cd52..0x2cd8e (`th.lrhu` with level*14) |
| G6 | resetfac | `dev_super+0x160 = 1`, `resetfac set` | Same | MATCH | |
| G7 | Other inputs | "other: `factory unknown argument`, or `factory set level error` if `level > max_level`" | `cmd <= 1` with n not 3 or 10: `factory set ok`, no packet. Busy slot: `factory set ok`. `cmd == 2` with level > max_level: no reply text | MISMATCH, Ghidra right | asm 0x2cdce..0x2cde4 (n fall-through and busy), 0x2cd48 `bltu` to epilogue 0x2cc38 |
| G8 | 0x71 decode | Jump table 0x155640, entry 0x2d2a4; payload 0, 1..2, 3+14*i | Indirect call through `custom0.rd()`, table not recovered | NOT VISIBLE, asm and data confirm | 0x2d110..0x2d116 `th.lrw`; 0x155640[0x21] = 0x2d2a4; 0x2d2a4..0x2d322 |

### H. Firmware upgrade (0x90/0x91)

| # | Claim | Document | Ghidra | Verdict | Evidence |
|---|---|---|---|---|---|
| H1 | AUP magic | 8 bytes `AUP form` and u16 `at` | `0x6d726f6620505541`, `0x7461` | MATCH | asm 0x2fef8..0x2ff4a |
| H2 | format_version | Logged only | Same | MATCH | |
| H3 | Header CRC | `crc32(0,file,H)`, H = 0x64 + 32*(hw+sw), stored u32 at H | `crc32(0,file,H)` visible; stored value is `custom0.rd()` | NOT VISIBLE (stored value), asm confirms | `th.lurwu s0,s1,a2` 0x2ffb2 |
| H4 | Name lists | hw names vs `dev_super+0x7c`, sw names vs `dev_super+0xbc`, `strncmp` 32 | Same | MATCH | |
| H5 | Payload CRC | `crc32(0,payload,payload_len)` vs u32 at 0x58 | Same | MATCH | asm 0x3006e..0x3008a |
| H6 | `/opt/upgrade_aup` | flags 0x241 (`O_WRONLY`, `O_CREAT`, `O_TRUNC`) mode 0644, CRC32 (4 bytes), reopen `O_APPEND`, payload | Same; payload length dropped by custom0 | MATCH | asm 0x300b4..0x30102 (`th.extu a2,s0` = payload_len) |
| H7 | API block header | byte 0 = 2, byte 1 = 0x1e, uid +0x08, total +0x14, offset +0x18, u16 len +0x1c, data from char 0x40 | Same | MATCH | asm 0x3041c..0x30478 (see N7) |
| H8 | trig_upgrade source | Reads `/data/upgrade_aup` | Same | MATCH | (caller: see N1) |
| H9 | Web path | `upgrade_load` 0x4e17c | No Ghidra file | NOT VISIBLE, asm confirms | `jal upgrade_aup` at 0x4e17c |
| H10 | Thread marking | Each path sets `thr->0x78 = 1` after `upgrade_aup` | Same in set_avalon_upgrade and trig_upgrade | MATCH | |
| H11 | miner_upgrade | Up to 5 calls of `avalonnano_upgrade` (= `upgrade_send_data`) per miner; `killall -USR1/-USR2 miner_upgrade_post.sh` | No Ghidra file | NOT VISIBLE, asm confirms | 0x307f0 `j upgrade_send_data`; 0xb5d60..0xb5d6c; string 0x17b4c0 |
| H12 | Chunk count | `count = ceil(S/128)` truncated to 16 bits, `rem = S % 128` | Garbled (`custom0` with `% 0x80`) | NOT VISIBLE, asm confirms | 0x2fb26..0x2fb5a (`th.extu` bits 22..7, `th.mvnez`) |
| H13 | Chunk header | type 0x90, idx k, cnt count, len 128 or rem | type and idx shown; cnt and last len shown as low 16 bits of `st_size` (wrong) | NOT VISIBLE, asm confirms the document | 0x2fbd6, 0x2fbe2 (`ld 0x28(sp)` = count), 0x2fbe6, 0x2fbea |
| H14 | Chunk payload | `lseek(k*128)`, `read`, `memcpy`, raw bytes | Same | MATCH | asm 0x2fb9a..0x2fbee |
| H15 | Chunk CRC | `crc16(pkt+4, len+8)` | `crc16(&hdr, lVar9 + 8)` | MATCH | |
| H16 | Multiple-of-128 bug | rem = 0 gives a last chunk with len 0; last 128 bytes never sent | Last-chunk length hidden in custom0 | NOT VISIBLE, asm confirms | count = q when rem = 0 (0x2fb52); k == count-1 selects len = rem (0x2fba8..0x2fbba) |
| H17 | Chunk handshake | 5 sends x 200 ms; type 0x91 accepted, payload not checked; other type or 5 timeouts uses one of 5 rounds (1 ms); then `k230 send msg count > 5`, -1; up to 25 sends; 1 ms after ACK | Same | MATCH | asm 0x2fc06..0x2fcbc |
| H18 | End packet | idx = cnt = count, len 0, CRC 8 | idx and cnt from the same variable, shown as low 16 bits of `st_size` | NOT VISIBLE (value), asm confirms count | 0x2fcd4..0x2fce0 |
| H19 | End ACK | 0x91 and payload[0] == 1: log `k210 crc16 verfity ok`, return 0; else err 0 (bad ACK) or -1 (timeout), return -1 | Same | MATCH | asm 0x2fd6a..0x2fd7c, 0x2fe56..0x2feb4 |
| H20 | Chunk 0 content | Starts with LE CRC32 of the payload | Follows from H6 write order | MATCH | |

### I. Transport settings used by this slice (PROTOCOL.md section 1)

| # | Claim | Document | Ghidra | Verdict | Evidence |
|---|---|---|---|---|---|
| I1 | UART | `/dev/ttyS1`, 115200 8N1 raw, VMIN=1 VTIME=0 | `open()` flags 0x102 (`O_RDWR`, `O_NOCTTY`), cflag 0x8b0 (CS8, CREAD, CLOCAL), B115200 (0x1002), CSTOPB cleared, c_cc[VTIME]=0, c_cc[VMIN]=1, `tcflush(TCIFLUSH)`, `tcsetattr(TCSANOW)` | MATCH | uart_init.c |

### Totals

MATCH 72, MISMATCH 4, NOT VISIBLE 22 (98 claims). In every NOT VISIBLE case the asm confirms the document, including the cases where Ghidra renders a wrong value (E10, G5, H13, H18). Ghidra is right in all 4 mismatches. None of them changes a byte on the wire: they concern log text, API replies and timing.

## Corrections needed

1. Section 3.1, last sentence: `ID[%d]` is the literal 0 (module index), not the cgminer `device_id`. The `device_id` is the `%d` in the `%s-%d:` prefix (asm 0x2b24c..0x2b254).
2. Section 0.3, mechanism C: failure reporting depends on the producer. SET_FREQ: `set hash freq failed!!`. SET_VOLTAGE: `set hash voltage failed!!`. `avalon_set_heater_level`: returns -1, but every caller (web, button, misc_thread) ignores it. API `worklevel,set`: returns 0 and writes no reply in both the success and the busy case. API `factory`: replies `factory set ok` even when the slot stayed busy.
3. Section 6.2 factory table: add two rows. (a) `cmd <= 1`, `level <= max_level`, n not 3 and not 10 (for example `param,1,2-60`): reply `factory set ok`, no packet (asm 0x2cdce). (b) `cmd == 2`, `level > max_level`: no reply text at all (asm 0x2cd48). `factory set level error` applies only to `cmd <= 1`.
4. Section 2, cross reference paragraph: replace "within 30 s" with "about 30 s after the last answered poll". `avalonnano_scanhash` restarts the 30 s timer `info+0x58` after every poll that got a reply (asm 0x2e842, 0x2e8a4).
5. Section 2, step 3 (wording only): on SYNC failure the flow goes to step 5 and `info+0x37e1` (mm_status) keeps its previous value.
6. Section 7.1: add the fourth trigger, SIGUSR1 (N1).
7. Not from Ghidra, but stale against PROTOCOL.md section 7: open question 5 (HDNA is ASCII) is answered, and DETECT_ACK payload 0x00 / 0x34 / 0x44 were seen live as `BTC\0`, `1366`, 0x10. The INFERRED `mV = 1097 + 4 * value` for 0x43 is weakened by the live PVT readings (311..315 at level voltage 330); keep it marked unverified.

## New findings

N1. `avalonnano_trig_upgrade` (0x30800) is the SIGUSR1 handler. `init_signal_handlers` calls `sigaction(10, {sa_handler = 0x30800})` (asm 0x15840..0x15854). `kill -USR1 <btcminer>` therefore starts a K210 upgrade from `/data/upgrade_aup`. The handler calls `upgrade_aup` (and so rewrites `/opt/upgrade_aup`) before it checks that the K210 is connected. It also calls `malloc`, `fopen` and `zlog` from signal context.

N2. When the pending slot is full, `avalonnano_scanhash` sends it and returns at once (asm 0x2e7ea..0x2e7fe). That pass sends no 0x33 poll, so a setting change costs one poll cycle.

N3. Bring-up writes fields the document does not list: `info+0x64a0 = 0` before the work-level step (0x2b300), `info+0x4d68 = 0x71` (0x2b398), `info+0x6484 = 0x5a` (0x2b3a8), `info+0x38b4 = 0`, `info+0x38ac = 0`, int `info+0x3aa0 = -273`, and `info+0x643c = 0` (0x2b4c0). `info+0x643c` is the polling failure counter (polling detaches the module when it exceeds 29), so bring-up resets it. No reader of `info+0x64a0` or `info+0x4d68` was found.

N4. The same-module HDNA shortcut (step 2) is unreachable. The function already returned at entry when `info+0x3761` is set, and the later test reads the same flag on the same `info` (asm 0x2ad34 and 0x2aee2).

N5. The HWTYPE and SWTYPE copies in `info` are NUL-terminated at index L+1, not L (asm 0x2afaa..0x2afc6, 0x2afec..0x2b00c). Index L keeps its old content. The `dev_super+0x7c/+0xbc` copies use `strncpy(n = L)` and get no terminator. This is harmless while the K210 always reports the same strings.

N6. `upgrade_send_data` leaks the file descriptor when a data chunk fails 5 rounds: the `k230 send msg count > 5` path returns without `close` (asm 0x2fc88..0x2fc9a, 0x2fdd2..0x2fe0c). `miner_upgrade` retries up to 5 times, so up to 5 descriptors leak per miner. The `read()` return value is also ignored.

N7. K230 side, not on the UART: `set_avalon_upgrade` never checks `offset + len <= total`. It writes `len` decoded bytes at `buf + offset` into a `malloc(total)` buffer (asm 0x30438..0x30478), so an API `upgrade` block can overflow the heap. Its length check compares hex characters with a byte count (`strlen - 0x40 < len`). `upgrade_aup` takes no buffer length and trusts `hw_count`, `sw_count` and `payload_len`.

N8. The `AUP is NOT for this hardware/software` errors print the hardcoded strings `MM4v1_X3` and `MM318_X2` (0x1552b0, 0x155340), not the device's real HWTYPE and SWTYPE.

N9. `device_cmds` flag at +0x10 (1 = argument required, otherwise `missing %s setting`): set for pdelay, frequency, led, fan-spd, factory, reboot, upgrade, worklevel and password; clear for loop, hashpower, softoff, softon and help. This explains why an empty `hashpower` reaches the `PS[...]` reply while an empty `frequency` never reaches `set_avalon_device_freq`.

N10. In `detect_modules`, the code path for a miner count other than 1 falls through into the mm_status store (asm 0x2b446..0x2b454). It is a compiler artifact and dead code, because the count was just forced to 1.

# Avalon Nano 3 - btcminer analysis notes (2026-10-27)

Source: `rootfs/mnt/heater/app/btcminer` (sha256 e6c11630a187d677f55178fa1dc7f2f1a52805856c538fae70cfbf0038ca6751).
Not stripped. Canaan cgminer fork built from `k230_sdk/app_heater/cgminer/driver-avalon12.c`.

## Architecture

K230 Linux (`btcminer`, root) --/dev/ttyS1--> K210 "MM" MCU --> ASIC chain.
The MM receives stratum data (coinbase, merkles, nonce2 >= 3 bytes) and builds per-chip work itself.
K210 firmware: `/data/upgrade_aup`, "AUP format" v2, build `24071801_b906c52_6223725`,
hardware IDs `PMMv1_X1`, `MM318_X2`, `MM318_X2_OOW`. Header layout (verified parser, see
`analysis/protocol-control.md`): payload_crc32 at 0x58, header_crc32 at 0xc4, payload from 0xc8.
Payload from 0xc8 is encrypted (entropy ~7.99 bit/byte).

## UART (uart_init @ 0xbe4e0)

- `/dev/ttyS1`, `open(O_RDWR|O_NOCTTY)`
- 115200 baud (`cfset[io]speed(0x1002)`), 8N1 (`c_cflag = CLOCAL|CREAD|CS8`, CSTOPB cleared)
- raw: iflag/oflag/lflag = 0, VMIN=1, VTIME=0

## Frame (data_toast_send @ 0xb58d0, data_toast_recv_thread @ 0xb54b0)

| Offset | Size | Field |
|---|---|---|
| 0 | 2 | magic `C` `N` (0x43 0x4E) |
| 2 | 2 | CRC16, little endian, over bytes [4, 12+len) |
| 4 | 1 | packet type |
| 5 | 1 | opt, 0 in all senders |
| 6 | 2 | idx, u16 LE: 0-based index of the frame in its group |
| 8 | 2 | cnt, u16 LE: frames in the group (1 for single frames) |
| 10 | 2 | payload length, little endian, max 0x80 |
| 12 | len | payload |

Total frame = 12 + len. RX queue: 5 slots x 0x8c bytes.
CRC: CRC-16/XMODEM (poly 0x1021, init 0, MSB first, no final xor). Table at 0x238840.

## Packet types

Full list, sequences and open questions: `PROTOCOL.md`.
Correction: an earlier version of this file read the DETECT header bytes 8..9 as `00 01`. They are `01 00` (cnt = 1); the shift arithmetic was wrong.

## Tooling notes

- REA/Ghidra decompiler fails to launch on this host ("Decompiler executable may not be compatible").
- nixpkgs rizin 0.9.1 has no RISC-V plugin.
- Working: `nix shell nixpkgs#llvm -c llvm-objdump -d --mattr=+m,+a,+f,+d,+c,+v,+zfh,+xtheadba,+xtheadbb,+xtheadbs,+xtheadcondmov,+xtheadmac,+xtheadmemidx,+xtheadmempair,+xtheadsync,+xtheadfmemidx,+xtheadcmo`
  Full listing: `analysis/btcminer.asm`.

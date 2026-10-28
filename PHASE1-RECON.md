# Avalon Nano 3 - Phase 1 recon (2026-10-27)

Read-only inspection of the running stock system over SSH, plus a full read-only dump.
Raw output: `recon/recon.txt` (produced by `recon/recon.sh`).

## Access

| Item | Value |
|---|---|
| Address | <MINER_IP> (Wi-Fi, `wlan0`) |
| SSH | OpenSSH `sshd` on port 22, user `admin` (uid 1001, gid 1002), password auth. Not stock: enabled by the "nanojb" jailbreak at 10:09, which also set the `admin` password (`SECURITY.md` findings 1 and 2) |
| Privilege | `admin` has `(ALL) ALL` in sudo (password required); this rule is stock (`/etc/sudoers` dated 2024-07-18) |
| Web UI | port 80, served by `btcminer` (mongoose); login reported as root/root |
| Other users | `nano` (home `/home/nano`) |

Note: stock `admin` has full sudo but no known default password, and stock firmware does not start SSH. The web UI timezone setting gives root regardless (`SECURITY.md` finding 2).

## SoC and kernel

| Item | Value |
|---|---|
| SoC | Kendryte K230 (device tree model `kendryte,k230`) |
| CPUs visible to Linux | 1 hart, `rv64imafdcvxthead`, sv39 |
| SBI | v0.3, implementation ID 0x1, version 0x9 (TIME, IPI, RFENCE, HSM) |
| RAM | 129016 KiB mapped, 105592 KiB available to Linux |
| Kernel | Linux 5.10.4 #2 SMP Fri Mar 8 14:48:45 CST 2024 (Xuantie-900 toolchain V2.6.0) |
| Userland | Buildroot 2021.02-git, BusyBox v1.33.0 (2024-07-18) |
| SDK version | `unkonwn-20240718-200655-root-31f6b1649a69-unkonwn` (`/etc/version/release_version`) |
| Kernel cmdline | `ubi.mtd=9 rootfstype=ubifs rw root=ubi0_0 console=ttyS0,115200 loglevel=5 earlycon=sbi` |

## Flash (Winbond SPI-NAND, 128 MiB, 128 KiB blocks, 2048 B pages, 64 B OOB)

| mtd | Name | Size | Offset | Use at boot |
|---|---|---|---|---|
| 0 | uboot_spl_a | 512 KiB | 0x0000000 | |
| 1 | uboot_spl_b | 512 KiB | 0x0080000 | |
| 2 | uboot_a | 1 MiB | 0x0100000 | |
| 3 | uboot_b | 1 MiB | 0x0200000 | |
| 4 | uboot_env_a | 512 KiB | 0x0300000 | |
| 5 | uboot_env_b | 512 KiB | 0x0380000 | |
| 6 | linux_a | 8 MiB | 0x0400000 | |
| 7 | linux_b | 8 MiB | 0x0c00000 | |
| 8 | rootfs_ubi_a | 24 MiB | 0x1400000 | |
| 9 | rootfs_ubi_b | 24 MiB | 0x2c00000 | mounted as `/` (UBI volume name `ubi_rootfs_part_a`) |
| 10 | app_ubi_a | 16 MiB | 0x4400000 | |
| 11 | app_ubi_b | 16 MiB | 0x5400000 | mounted as `/mnt` (chosen by u-boot env `app_partition`) |
| 12 | data | 28 MiB | 0x6400000 | mounted as `/data` (logs, cores, `upgrade_aup`) |
| 13 | all_flash | 128 MiB | 0x0000000 | whole-flash view |

The partition names match the Nano 3s report; the size (128 MiB vs 256 MiB) does not.

## Peripherals seen from Linux

| Device | Detail |
|---|---|
| `ttyS0` 0x91400000 | console, 115200 |
| `ttyS1` 0x91401000 | link to the K210 "MM" microcontroller (used by `btcminer`, see `analysis/NOTES.md`) |
| `ttyS3` 0x91403000 | present, use unknown |
| SPI0 | SPI-NAND |
| SPI1 | ST7789V LCD, 240x240, `fb0`, 25 MHz, rotate 90 |
| I2C | only `i2c-2` (0x91407000) |
| GPIO | single-line chips: gpio0, gpio10, gpio23, gpio24, gpio44, gpio61; `gpio_keys` input |
| PWM | `/sys/class/pwm/pwmchip0` (fan) |
| ADC | `iio:device0`, read by `btcminer` (`in_voltage%d_raw`, temperatures) |
| OTP | `/sys/bus/nvmem/devices/kendryte_otp0/nvmem`, read by `btcminer` |
| LED | `/dev/ws2812` |
| Wi-Fi | SDIO card on `mmc0` (0x91580000), `wpa_supplicant` + `udhcpc` |
| Other | watchdog, hwrng, Kendryte AES/SHA256/RSA drivers, `canaan_thermal` tsensor |

No remoteproc, rpmsg or IPC device is visible. On this unit the ASIC chain is reached through the K210 on `ttyS1`, not through a second K230 core.

## Boot sequence (`/etc/init.d/rcS`)

1. Runs `S01syslogd` .. `S80swupdate`, including `S50sshd.sh` (`/usr/sbin/sshd`, added by the jailbreak).
2. Reads u-boot env `app_partition` (default A), attaches mtd10 or mtd11, mounts it on `/mnt`.
3. Attaches mtd12, mounts `/data`; core dumps go to `/data/core/`.
4. Starts `swupdate -w "--document-root /var/www/swupdate/ --port 9090" -k /etc/ota_public.pem -K /etc/ota_aes_key_iv`.
5. Runs `/etc/user_permission_chg.sh` (gives `admin` ownership of `/mnt/heater` and `/data`).
6. `cd /mnt/heater/app && ./btcminer &` (runs as root).

The OTA AES key/IV file `/etc/ota_aes_key_iv` (99 bytes) is world-readable and is in the dump, so K230-side swupdate images can likely be decrypted. Not yet tested.

## Processes and network

| Port | Service |
|---|---|
| 22/tcp | sshd (added by the jailbreak) |
| 80/tcp | `btcminer` web UI |
| 4028/tcp | `btcminer` cgminer API |
| 9090/tcp | swupdate web upload |
| 123/udp | ntpd |

Outbound at recon time: `<stock-default-pool-ip>:3303` (configured pool).

## App partition (`/mnt/heater`)

- `app/btcminer`: the miner (2,508,296 bytes, sha256 `e6c11630a187d677f55178fa1dc7f2f1a52805856c538fae70cfbf0038ca6751`)
- `confiles/`: `hostapd.conf`, `udhcpd.conf`, `wpa_supplicant.conf`, `zlog.conf`, `usrcon/systemcfg_bak.ini`
- `www/html/`: web UI pages
- `wifi_test.sh`

## K210 firmware (`/data/upgrade_aup`)

202,957 bytes, sha256 `b5bcb05dbb483d0539a08feb4e753193785c042fda9bdead4db3e89b17ec901b`.

| Offset | Bytes | Meaning (from strings in `btcminer`) |
|---|---|---|
| 0x00 | `AUP format` | magic |
| 0x10 | `02 00 00 00` | AUP format version 2 |
| 0x14 | `05 18 03 00` | payload length, 0x31805 = 202,757 = file size minus 0xc8 (exact match) |
| 0x18 | `24071801_b906c52_6223725` | software version |
| 0x58 | `b2 60 87 de` | payload CRC32 (`crc32(payload)` = 0xde8760b2, confirmed by the parser; see protocol-control.md) |
| 0x5c | `01 00 00 00` | unknown |
| 0x60 | `02 00 00 00` | likely count of hardware IDs |
| 0x64 | `PMMv1_X1` | ID |
| 0x84 | `MM318_X2` | ID |
| 0xa4 | `MM318_X2_OOW` | ID |
| 0xc4 | `8b 96 0a e6` | header CRC32 (`crc32(file[0..0xc4))` = 0xe60a968b, confirmed by the parser; see protocol-control.md) |
| 0xc8.. | high-entropy data | encrypted payload (~7.99 bit/byte, does not compress) |

The header field meanings marked "likely" come from the AUP error strings in `btcminer` and still need confirmation from its parser.

## Dump inventory

See `README.md`.

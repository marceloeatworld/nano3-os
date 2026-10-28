#!/usr/bin/env python3
# Decode an strace capture of btcminer's /dev/ttyS1 traffic into K230 <-> K210 frames.
# Capture: strace -f -tt -xx -s 4096 -e trace=read,write -P /dev/ttyS1 -o cap.txt -p <pid>
# Usage: python3 -I decode_capture.py cap.txt [--summary-only]
# Field layouts follow analysis/PROTOCOL.md; anything not listed there is printed as raw hex.
import collections
import re
import struct
import sys

NAMES = {
    0x10: "DETECT", 0x11: "DETECT_ACK", 0x12: "SYNC", 0x13: "SYNC_ACK", 0x15: "INFO_RES",
    0x20: "JOB_PARAMS", 0x21: "JOB_ID", 0x22: "COINBASE", 0x23: "MERKLE", 0x24: "HEADER",
    0x25: "TARGET", 0x26: "JOB_FINISH", 0x27: "VERSIONS",
    0x31: "SET_FINISH", 0x32: "SET_FREQ", 0x33: "POLL",
    0x41: "SET_WORKLEVEL", 0x42: "RESET_K210", 0x43: "SET_VOLTAGE",
    0x50: "NONCE", 0x51: "STATUS", 0x52: "ASIC_COUNTERS", 0x53: "FREQ_REPORT",
    0x54: "ASIC_PVT", 0x55: "POWER", 0x60: "GET_STATUS(0x60)", 0x70: "SET_LEVELPARAM",
    0x71: "LEVELPARAM", 0x90: "UPGRADE", 0x91: "UPGRADE_ACK",
}

LINE = re.compile(r'^(\d+)\s+(\d\d:\d\d:\d\d\.\d+)\s+(.*)$')
CALL = re.compile(r'(?:(read|write)\((\d+),\s*|<\.\.\. (read|write) resumed>,?)\s*"((?:\\x[0-9a-f]{2})*)"(\.\.\.)?,\s*\d+\)\s*=\s*(-?\d+)')


def crc16_xmodem(data):
    crc = 0
    for b in data:
        crc ^= b << 8
        for _ in range(8):
            crc = ((crc << 1) ^ 0x1021) if crc & 0x8000 else (crc << 1)
            crc &= 0xFFFF
    return crc


WRITE_UNFINISHED = re.compile(r'write\((\d+),\s*"((?:\\x[0-9a-f]{2})*)"(\.\.\.)?,\s*\d+\s*<unfinished \.\.\.>')
WRITE_RESUMED = re.compile(r'<\.\.\. write resumed>\)\s*=\s*(-?\d+)')


def hexbytes(s):
    return bytes(int(h, 16) for h in re.findall(r"\\x([0-9a-f]{2})", s))


READ_UNFINISHED = re.compile(r'read\((\d+),?\s*<unfinished \.\.\.>')
OPEN_TTY = re.compile(r'open(?:at)?\((?:AT_FDCWD,\s*)?"((?:\\x[0-9a-f]{2})*)".*\)\s*=\s*(\d+)')


def parse_strace(path, fd=10):
    """Yield (time, direction, bytes) for each completed read/write on the ttyS1 fd.

    With -f, a syscall can be split into an "<unfinished ...>" line and a later "resumed" line.
    For write the data is on the first line; for read it is on the resumed line.
    If the capture contains the open of /dev/ttyS1, its returned fd replaces the default.
    """
    pending_write, pending_read = {}, {}
    for raw in open(path, encoding="latin-1"):
        m = LINE.match(raw.rstrip("\n"))
        if not m:
            continue
        pid, ts, rest = m.groups()
        o = OPEN_TTY.search(rest)
        if o and hexbytes(o.group(1)) == b"/dev/ttyS1":
            fd = int(o.group(2))
            continue
        w = WRITE_UNFINISHED.search(rest)
        if w:
            if int(w.group(1)) == fd:
                pending_write[pid] = hexbytes(w.group(2))
            continue
        w = WRITE_RESUMED.search(rest)
        if w:
            data, ret = pending_write.pop(pid, b""), int(w.group(1))
            if ret > 0 and data:
                yield ts, "TX", data[:ret]
            continue
        r = READ_UNFINISHED.search(rest)
        if r:
            pending_read[pid] = int(r.group(1))
            continue
        c = CALL.search(rest)
        if not c:
            continue
        name = c.group(1) or c.group(3)
        call_fd = int(c.group(2)) if c.group(2) else pending_read.pop(pid, None)
        if call_fd != fd:
            continue
        ret = int(c.group(6))
        if ret <= 0:
            continue
        data = hexbytes(c.group(4))[:ret]
        yield ts, ("TX" if name == "write" else "RX"), data


def split_frames(chunks):
    """Reassemble per-direction byte streams and cut them into CN frames."""
    buf = {"TX": bytearray(), "RX": bytearray()}
    for ts, d, data in chunks:
        b = buf[d]
        b += data
        while True:
            i = b.find(b"CN")
            if i < 0:
                del b[:max(0, len(b) - 1)]
                break
            if i:
                yield ts, d, None, bytes(b[:i])
                del b[:i]
            if len(b) < 12:
                break
            ln = struct.unpack_from("<H", b, 10)[0]
            if ln > 0x80:
                yield ts, d, None, bytes(b[:2])
                del b[:2]
                continue
            if len(b) < 12 + ln:
                break
            yield ts, d, bytes(b[:12 + ln]), None
            del b[:12 + ln]


def u16le(p, o): return struct.unpack_from("<H", p, o)[0]
def u32le(p, o): return struct.unpack_from("<I", p, o)[0]
def u32be(p, o): return struct.unpack_from(">I", p, o)[0]


def cstr(p, o):
    end = p.find(b"\0", o)
    end = len(p) if end < 0 else end
    return p[o:end].decode("latin-1"), end + 1


def describe(t, idx, cnt, p):
    """Return a short decoded description for known payload layouts."""
    try:
        if t == 0x50:
            n = len(p) >> 4
            recs = []
            for i in range(n):
                r = p[16 * i:16 * i + 16]
                recs.append("job=%04x pool=%d n2=%08x nonce=%s asic=%d miner=%d ntime+%d ver=%d valid=%x" % (
                    (r[0] << 8) | r[1], u16le(r, 2), u32le(r, 4), r[8:12].hex(), r[12], r[13], r[14],
                    r[15] & 0xF, r[15] >> 4))
            tail = u32le(p, 16 * n) if len(p) >= 16 * n + 4 else None
            return "%d record(s), trailer=%s | %s" % (n, None if tail is None else hex(tail), " ; ".join(recs))
        if t == 0x20:
            f = [u32be(p, o) for o in range(0, len(p) - len(p) % 4, 4)]
            names = ["cb_len", "n2_off", "n2_size", "mroot_off", "merkles", "n2_start", "n2_range", "restart"]
            return " ".join("%s=%d" % (k, v) if k != "n2_range" else "%s=%#x" % (k, v) for k, v in zip(names, f))
        if t == 0x27:
            return "versions=" + ",".join("%08x" % u32be(p, o) for o in range(0, 16, 4))
        if t == 0x21:
            return "job_crc=%04x pool=%d" % ((p[0] << 8) | p[1], u16le(p, 2))
        if t == 0x25:
            return "target=%s" % p[::-1].hex()  # LE on the wire, printed most significant first
        if t == 0x32:
            f = [u32le(p, o) for o in range(0, 32, 4)]
            return "pll_mhz=%s c100=%d timeout=%d zero=%d sel=%#x" % (f[0:4], f[4], f[5], f[6], f[7])
        if t == 0x33 or t == 0x41:
            return "value=%#x" % u32le(p, 0)
        if t == 0x43:
            return "value=%d" % u16le(p, 0)
        if t == 0x11:
            hdna = p[4:20]
            hver = p[20:52].split(b"\0")[0].decode("latin-1")
            hw, o = cstr(p, 0x45)
            sw, o = cstr(p, o)
            ml = p[o] if o < len(p) else None
            return "u32=%#x HDNA=%s HVERSION=%r unk34=%s unk44=%#x HWTYPE=%r SWTYPE=%r max_level=%s" % (
                u32le(p, 0), hdna.hex(), hver, p[0x34:0x44].hex(), p[0x44], hw, sw, ml)
        if t == 0x51:
            core = p[0x25:0x2c].split(b"\0")[0].decode("latin-1")
            return ("HashStatus=%d unk1=%#x LW=%d GHSmm=%.2f GHSspd=%.2f DH=%.3f%% DHspd=%.3f%% TA=%d Core=%r "
                    "TMax=%d worklevel=%d TarT=%d SoftOFF=%d adapter=%d") % (
                p[0], u32le(p, 1), u32le(p, 5), u32le(p, 9) / 1000, u32le(p, 0xd) / 1000,
                struct.unpack_from("<d", p, 0x11)[0], struct.unpack_from("<d", p, 0x19)[0], u32le(p, 0x21), core,
                struct.unpack_from("<i", p, 0x2c)[0], p[0x30], p[0x31], p[0x32], p[0x33])
        if t == 0x71:
            recs = [struct.unpack_from("<7H", p, 3 + 14 * i) for i in range((len(p) - 3) // 14)]
            return "MPO=%d ADJ=%d BAR=%d ATA=%s" % (p[0], p[1], p[2], recs)
        if t == 0x13:
            return "mm_status=%d raw=%s" % (p[3], p.hex())
        if t == 0x54:
            return "asic=%d temp=%.1f volt=%.1f raw=%d" % (idx, u16le(p, 0) / 10, u16le(p, 2) / 10, u16le(p, 4))
        if t == 0x53:
            return "set_freq=%s nonce_mask=%#x" % ([u16le(p, o) for o in range(0, 8, 2)], u32le(p, 8))
        if t == 0x60:
            return "set_freq_be=%s rest=%s" % ([struct.unpack_from(">H", p, o)[0] for o in range(16, 24, 2)], p[:16].hex())
        if t == 0x52:
            return "asic=%d cntA=%d cntB=%d pll=%s cf=%s" % (
                idx, struct.unpack_from(">I", p, 1)[0], struct.unpack_from(">I", p, 5)[0], list(p[9:13]),
                [struct.unpack_from(">H", p, o)[0] for o in range(13, 21, 2)])
    except (IndexError, struct.error) as e:
        return "decode error: %s" % e
    return ""


def main():
    path = sys.argv[1]
    summary_only = "--summary-only" in sys.argv
    counts = collections.Counter()
    bad_crc = collections.Counter()
    junk = collections.Counter()
    sizes = collections.defaultdict(set)
    after_poll = collections.Counter()
    last_tx = None
    for ts, d, frame, garbage in split_frames(parse_strace(path)):
        if frame is None:
            garbage = garbage or b""
            junk[d] += len(garbage)
            if not summary_only:
                print("%s %s junk %s" % (ts, d, garbage.hex()))
            continue
        t = frame[4]
        idx, cnt, ln = u16le(frame, 6), u16le(frame, 8), u16le(frame, 10)
        p = frame[12:]
        ok = u16le(frame, 2) == crc16_xmodem(frame[4:])
        key = (d, t)
        counts[key] += 1
        sizes[key].add(ln)
        if not ok:
            bad_crc[key] += 1
        if d == "TX":
            last_tx = t
        elif last_tx == 0x33:
            after_poll[t] += 1
            last_tx = None
        if not summary_only:
            print("%s %s %02x %-15s opt=%d idx=%d cnt=%d len=%d crc=%s %s\n    %s" % (
                ts, d, t, NAMES.get(t, "?"), frame[5], idx, cnt, ln, "ok" if ok else "BAD",
                describe(t, idx, cnt, p), p.hex()))
    print("\n== summary ==")
    for (d, t), n in sorted(counts.items()):
        print("%s %02x %-15s frames=%-5d payload_len=%s bad_crc=%d" % (
            d, t, NAMES.get(t, "?"), n, sorted(sizes[(d, t)]), bad_crc[(d, t)]))
    print("first RX frame type after each TX 0x33 poll:", {"%02x" % k: v for k, v in after_poll.items()})
    print("bytes outside frames:", dict(junk))


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
# Verify captured 0x50 nonce reports by rebuilding the block header from the captured job frames.
# Proves (or disproves) the field semantics in analysis/PROTOCOL.md: nonce2 placement and byte order,
# version index -> 0x27 word mapping, ntime offset, nonce byte order, header_bin word order.
# Usage: python3 -I verify_nonces.py cap.strace.txt [more captures...]
import hashlib
import os
import struct
import sys

# python3 -I leaves the script directory off sys.path; add it back for the sibling module.
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import decode_capture as dc  # noqa: E402


def dsha(b):
    return hashlib.sha256(hashlib.sha256(b).digest()).digest()


def wordswap(b):
    return b"".join(b[i:i + 4][::-1] for i in range(0, len(b), 4))


def jobs_and_nonces(path):
    jobs, nonces, cur = [], [], None
    for ts, d, frame, _ in dc.split_frames(dc.parse_strace(path)):
        if frame is None:
            continue
        t, idx, p = frame[4], struct.unpack_from("<H", frame, 6)[0], frame[12:]
        if d == "TX" and t == 0x20:
            # btcminer omits 0x21 when the job id is unchanged (protocol-job.md 6.4),
            # so a burst inherits the previous job's id until a 0x21 replaces it
            cur = {"ts": ts, "params": [struct.unpack_from(">I", p, o)[0] for o in range(0, len(p), 4)],
                   "cb": {}, "mk": {}, "hdr": {}, "job_crc": jobs[-1]["job_crc"] if jobs else None}
        elif d == "TX" and cur is not None:
            if t == 0x27:
                cur["versions"] = [struct.unpack_from(">I", p, o)[0] for o in range(0, 16, 4)]
            elif t == 0x21:
                cur["job_crc"] = (p[0] << 8) | p[1]
            elif t == 0x22:
                cur["cb"][idx] = p
            elif t == 0x23:
                cur["mk"][idx] = p
            elif t == 0x24:
                cur["hdr"][idx] = p
            elif t == 0x26:
                jobs.append(cur)
                cur = None
        elif d == "RX" and t == 0x50:
            n = len(p) >> 4
            trailer = struct.unpack_from("<I", p, 16 * n)[0] if len(p) >= 16 * n + 4 else None
            for i in range(n):
                rec = p[16 * i:16 * i + 16]
                if rec[15] >> 4:  # high nibble 0 = empty record slot
                    nonces.append((ts, rec, trailer))
    return jobs, nonces


def header_for(job, rec, header_order, n2_order, version_slot):
    cb_len, n2_off, n2_size = job["params"][0], job["params"][1], job["params"][2]
    cb = bytearray(b"".join(job["cb"][i] for i in sorted(job["cb"])))[:cb_len]
    n2 = struct.unpack_from("<I", rec, 4)[0]
    cb[n2_off:n2_off + n2_size] = struct.pack("<I" if n2_order == "le" else ">I", n2)[:n2_size]
    root = dsha(bytes(cb))
    for i in sorted(job["mk"]):
        root = dsha(root + job["mk"][i])
    hb = b"".join(job["hdr"][i] for i in sorted(job["hdr"]))[:80]
    h = bytearray(wordswap(hb) if header_order == "swapped" else hb)
    h[0:4] = struct.pack("<I", job["versions"][version_slot])
    h[36:68] = root
    ntime = struct.unpack_from("<I", h, 68)[0] + rec[14]
    h[68:72] = struct.pack("<I", ntime)
    h[76:80] = rec[8:12]
    return bytes(h)


def leading_zero_bits(hash_le):
    v = int.from_bytes(hash_le, "little")
    return 256 - v.bit_length()


def main():
    # Match nonces only against jobs from the same capture: timestamps are
    # HH:MM:SS strings and do not order events across files.
    captures = [jobs_and_nonces(path) for path in sys.argv[1:]]
    print("jobs captured: %d, nonce records: %d" % (
        sum(len(j) for j, _ in captures), sum(len(n) for _, n in captures)))
    ok_combo = {}
    results = []
    for jobs, ts, rec, trailer in ((j, *x) for j, n in captures for x in n):
        job_crc, ver_idx = (rec[0] << 8) | rec[1], rec[15] & 0xF
        cands = [j for j in jobs if j["job_crc"] == job_crc and j["ts"] <= ts]
        if not cands:
            results.append((ts, job_crc, "job not captured", None))
            continue
        job = cands[-1]
        best = None
        for header_order in ("swapped", "asis"):
            for n2_order in ("le", "be"):
                for slot in range(4):
                    h = header_for(job, rec, header_order, n2_order, slot)
                    z = leading_zero_bits(dsha(h))
                    if best is None or z > best[0]:
                        best = (z, header_order, n2_order, slot)
        assert best is not None
        z, ho, no, slot = best
        if z >= 32:
            ok_combo.setdefault((ho, no), []).append((ver_idx, slot))
        results.append((ts, job_crc, "zero_bits=%d header=%s nonce2=%s ver_idx=%d -> 0x27 word %d asic=%d ntime+%d n2=%#x trailer=%s" % (
            z, ho, no, ver_idx, slot, rec[12], rec[14], struct.unpack_from("<I", rec, 4)[0],
            "none" if trailer is None else hex(trailer)), z))
    for ts, jc, msg, _ in results:
        print("%s job=%04x %s" % (ts, jc, msg))
    valid = [r for r in results if r[3] is not None and r[3] >= 32]
    checked = [r for r in results if r[3] is not None]
    print("\nvalid (>= 32 leading zero bits, i.e. >= difficulty 1): %d of %d checkable" % (len(valid), len(checked)))
    for (ho, no), pairs in ok_combo.items():
        print("layout header=%s nonce2=%s: ver_idx -> word mapping %s" % (ho, no, sorted(set(pairs))))


if __name__ == "__main__":
    main()

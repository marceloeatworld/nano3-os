#!/usr/bin/env python3
"""
nano3miner - independent host-side driver for the Avalon Nano 3, running on a PC
and reaching the K210 (MM) through uartbridge on the miner (UART over TCP).

Stage 2a (default, --verify): connect to the pool, take one mining.notify, send it
to the K210 as a job burst (0x20..0x27), poll (0x33), collect 0x50 nonce reports,
rebuild each block header and check it locally. It does NOT submit to the pool and
sends NO hardware-setting command (no SET_FREQ/VOLTAGE/WORKLEVEL/FINISH); the K210
keeps the settings btcminer left it with. This proves our job encoder is correct.

Stage 2b (--submit): additionally submit shares that meet the pool target.

Usage: uartbridge listens on the miner's 127.0.0.1 only, so tunnel to it first:
       ssh -N -L 9113:127.0.0.1:9113 admin@<MINER_IP> &
       python3 -I nano3miner.py --bridge 127.0.0.1:9113 \
          --pool <POOL_HOST>:3303 \
          --user <YOUR_BTC_ADDRESS>.<worker> [--seconds 120] [--submit]
"""
import argparse
import hashlib
import json
import os
import socket
import struct
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

HDR = 12
MAGIC = b"CN"


def crc16_xmodem(data):
    crc = 0
    for b in data:
        crc ^= b << 8
        for _ in range(8):
            crc = ((crc << 1) ^ 0x1021) & 0xFFFF if crc & 0x8000 else (crc << 1) & 0xFFFF
    return crc


def dsha(b):
    return hashlib.sha256(hashlib.sha256(b).digest()).digest()


def frame(ftype, idx, cnt, payload):
    f = bytearray(HDR + len(payload))
    f[0:2] = MAGIC
    f[4] = ftype
    f[5] = 0
    struct.pack_into("<HHH", f, 6, idx, cnt, len(payload))
    f[HDR:] = payload
    struct.pack_into("<H", f, 2, crc16_xmodem(bytes(f[4:])))
    return bytes(f)


class Bridge:
    """Framed link to the K210 through uartbridge."""

    def __init__(self, host, port):
        last = RuntimeError("no connection attempt made")
        for _ in range(15):
            try:
                self.s = socket.create_connection((host, port), 2)
                break
            except OSError as e:
                last = e
                time.sleep(1)
        else:
            raise last
        self.s.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
        self.buf = bytearray()

    def send(self, f):
        self.s.sendall(f)

    def close(self):
        try:
            self.s.close()
        except OSError:
            pass

    @staticmethod
    def stop(host, port):
        """Open a short connection and send the BRXT control word so the bridge exits."""
        try:
            s = socket.create_connection((host, port), 3)
            s.sendall(b"BRXT")
            s.close()
            return True
        except OSError:
            return False

    def recv_frame(self, deadline):
        while True:
            i = self.buf.find(MAGIC)
            if i < 0:
                del self.buf[:max(0, len(self.buf) - 1)]
            elif i:
                del self.buf[:i]
            if len(self.buf) >= HDR:
                ln = struct.unpack_from("<H", self.buf, 10)[0]
                if ln > 0x80:
                    del self.buf[:2]
                    continue
                if len(self.buf) >= HDR + ln:
                    f = bytes(self.buf[:HDR + ln])
                    del self.buf[:HDR + ln]
                    ok = struct.unpack_from("<H", f, 2)[0] == crc16_xmodem(f[4:])
                    return f, ok
            timeout = deadline - time.monotonic()
            if timeout <= 0:
                return None, False
            self.s.settimeout(timeout)
            try:
                d = self.s.recv(4096)
            except socket.timeout:
                return None, False
            if not d:
                raise ConnectionError("bridge closed")
            self.buf += d


def granted_vmask(mask_hex):
    """BIP310: roll only bits inside the mask we requested; None when none are left."""
    mask = int(mask_hex, 16) & 0x1FFFE000
    return "%08x" % mask if mask else None


class Stratum:
    """Minimal Stratum V1 client (subscribe, authorize, notify, set_difficulty)."""

    def __init__(self, host, port, user, password="x"):
        self.s = socket.create_connection((host, port), 10)
        self.user = user
        self.password = password
        self.id = 0
        self.rx = bytearray()
        self.extranonce1 = b""
        self.n2size = 0
        self.difficulty = 1.0
        self.notify = None
        self.vmask = None  # version-rolling mask if the pool grants it

    def _call(self, method, params):
        self.id += 1
        self.s.sendall((json.dumps({"id": self.id, "method": method, "params": params}) + "\n").encode())
        return self.id

    def _readline(self, timeout=None):
        """Return the next JSON message, or None on timeout. timeout=None blocks."""
        while True:
            i = self.rx.find(b"\n")
            if i >= 0:
                line = bytes(self.rx[:i])
                del self.rx[:i + 1]
                if line.strip():
                    return json.loads(line.decode())
                continue
            self.s.settimeout(timeout)
            try:
                d = self.s.recv(4096)
            except socket.timeout:
                return None
            if not d:
                raise ConnectionError("pool closed")
            self.rx += d

    def connect(self, suggest_diff=None):
        cid = self._call("mining.configure",
                         [["version-rolling"], {"version-rolling.mask": "1fffe000"}])
        sid = self._call("mining.subscribe", ["nano3miner/0.1"])
        aid = self._call("mining.authorize", [self.user, self.password])
        if suggest_diff:
            self._call("mining.suggest_difficulty", [suggest_diff])
        # drain responses and notifications until authorize result seen
        authorized = None
        while authorized is None:
            m = self._readline()
            if m is None:
                continue
            self._dispatch(m)
            if m.get("id") == cid and isinstance(m.get("result"), dict) and m["result"].get("version-rolling"):
                self.vmask = granted_vmask(m["result"].get("version-rolling.mask", "0"))
            if m.get("id") == sid and m.get("result"):
                _sub, en1, n2 = m["result"]
                self.extranonce1 = bytes.fromhex(en1)
                self.n2size = n2
            if m.get("id") == aid:
                authorized = bool(m.get("result"))
        return authorized

    def _dispatch(self, m):
        meth = m.get("method")
        if meth == "mining.notify":
            p = m["params"]
            self.notify = {
                "job_id": p[0], "prevhash": p[1], "coinb1": p[2], "coinb2": p[3],
                "merkles": p[4], "version": p[5], "nbits": p[6], "ntime": p[7], "clean": p[8],
            }
        elif meth == "mining.set_difficulty":
            self.difficulty = m["params"][0]
        elif meth == "mining.set_version_mask":
            self.vmask = granted_vmask(m["params"][0])

    def pump(self, timeout):
        m = self._readline(timeout)
        if m is None:
            return False
        self._dispatch(m)
        return True

    def submit(self, job_id, extranonce2_hex, ntime_hex, nonce_hex, version_hex=None):
        params = [self.user, job_id, extranonce2_hex, ntime_hex, nonce_hex]
        if version_hex is not None:
            params.append(version_hex)
        return self._call("mining.submit", params)


def target_from_diff(diff):
    # Bitcoin difficulty-1 target, 256-bit, divided by diff.
    diff1 = 0x00000000FFFF0000000000000000000000000000000000000000000000000000
    return int(diff1 / diff)


def build_job(st, nonce2_start=0, nonce2_range=None, restart=False):
    """Return the ordered list of frames (0x20..0x27) for the current notify."""
    nt = st.notify
    coinb1 = bytes.fromhex(nt["coinb1"])
    coinb2 = bytes.fromhex(nt["coinb2"])
    en1 = st.extranonce1
    n2size = st.n2size
    if nonce2_range is None:  # as btcminer on a single device (protocol-job.md 6.1)
        nonce2_range = 0xFFFFFF if n2size == 3 else 0xFFFFFFFF
    # coinbase with a zeroed nonce2 slot; the MM fills nonce2 itself
    nonce2_zero = b"\x00" * n2size
    coinbase = coinb1 + en1 + nonce2_zero + coinb2
    nonce2_off = len(coinb1) + len(en1)
    merkles = [bytes.fromhex(h) for h in nt["merkles"]]

    version = int(nt["version"], 16)
    vmask = int(st.vmask, 16) if st.vmask else 0
    # four rolled versions as btcminer does: base, +low group, +rest, +full mask.
    # The low group is the mask's lowest non-zero nibble, in place (protocol-job.md 6.2).
    lowgrp = next((vmask & (0xF << s) for s in range(0, 32, 4) if vmask & (0xF << s)), 0)
    words = [version, version | lowgrp, version | (vmask & ~lowgrp), version | vmask]

    nbits = int(nt["nbits"], 16)
    ntime = int(nt["ntime"], 16)
    # The notify prevhash is swapEndianWords(block.prevHash): each 4-byte word is
    # byte-reversed. Undo that to get the real prevHash the pool puts in the header.
    ph_notify = bytes.fromhex(nt["prevhash"])
    prevhash = b"".join(ph_notify[i:i + 4][::-1] for i in range(0, 32, 4))

    # 80-byte header template (merkle root + nonce left zero; the MM fills them in).
    hdr80 = bytearray(80)
    struct.pack_into("<I", hdr80, 0, version)
    hdr80[4:36] = prevhash
    struct.pack_into("<I", hdr80, 68, ntime)
    struct.pack_into("<I", hdr80, 72, nbits)
    # 128-byte header_bin as btcminer sends it: 80-byte header + SHA-256 padding
    # (0x80, zeros, 64-bit big-endian bit length 640 = 0x0280), each 32-bit word byte-reversed.
    hb = bytearray(128)
    hb[0:80] = hdr80
    hb[80] = 0x80
    struct.pack_into(">Q", hb, 120, 640)
    hdr_wire = b"".join(hb[i:i + 4][::-1] for i in range(0, 128, 4))

    diff = min(st.difficulty, 4096)
    tgt = target_from_diff(diff).to_bytes(32, "big")[::-1]  # little-endian 256-bit

    frames = []
    # 0x20 params, big-endian u32 fields
    p20 = struct.pack(">IIIIIII", len(coinbase), nonce2_off, min(n2size, 4), 36,
                      len(merkles), nonce2_start, nonce2_range)
    if restart:
        p20 += struct.pack(">I", 1)
    frames.append(frame(0x20, 0, 1, p20))
    # 0x27 versions, big-endian
    frames.append(frame(0x27, 0, 1, b"".join(struct.pack(">I", w) for w in words)))
    # 0x25 target, little-endian 256-bit
    frames.append(frame(0x25, 0, 1, tgt))
    # 0x21 job id (crc16 of ascii job_id) + pool_no
    jc = crc16_xmodem(nt["job_id"].encode())
    frames.append(frame(0x21, 0, 1, struct.pack(">H", jc) + struct.pack("<H", 0)))
    # 0x22 coinbase chunks, 128 bytes each
    chunks = [coinbase[i:i + 128] for i in range(0, len(coinbase), 128)]
    for i, c in enumerate(chunks):
        frames.append(frame(0x22, i, len(chunks), c))
    # 0x23 merkle branches
    for i, mk in enumerate(merkles):
        frames.append(frame(0x23, i, len(merkles), mk))
    # 0x24 header template, 4 frames x 32 bytes = the 128-byte wire header_bin
    for i in range(4):
        frames.append(frame(0x24, i, 4, hdr_wire[i * 32:i * 32 + 32]))
    # 0x26 finish
    frames.append(frame(0x26, 0, 1, b""))
    return frames, {"coinbase": coinbase, "nonce2_off": nonce2_off, "n2size": n2size,
                    "merkles": merkles, "hdr80": bytes(hdr80), "words": words,
                    "ntime": ntime, "job_id": nt["job_id"], "jc": jc, "target": target_from_diff(diff),
                    "pool_diff": st.difficulty}


def rebuild_header(ctx, rec, n2=None):
    if n2 is None:
        n2 = struct.unpack_from("<I", rec, 4)[0]
    cb = bytearray(ctx["coinbase"])
    # The MM writes a sent_n2size-byte nonce2 (LE) at nonce2_off; the rest of the
    # slot stays zero. Write exactly that many bytes without changing cb length.
    w = min(ctx["n2size"], 4)
    cb[ctx["nonce2_off"]:ctx["nonce2_off"] + w] = struct.pack("<I", n2)[:w]
    root = dsha(bytes(cb))
    for mk in ctx["merkles"]:
        root = dsha(root + mk)
    h = bytearray(ctx["hdr80"])
    slot = rec[15] & 0xF
    struct.pack_into("<I", h, 0, ctx["words"][slot] if slot < 4 else ctx["words"][0])
    h[36:68] = root
    struct.pack_into("<I", h, 68, ctx["ntime"] + rec[14])
    h[76:80] = rec[8:12]
    return bytes(h), n2


def rewind(ctxs, jc, n2, ta, wrap):
    """0x50 retry rule (PROTOCOL.md section 3): a result can arrive one round of TA
    nonce2 values late. Below zero it belongs to the end of the next older job, whose
    last nonce2 is the 0x50 trailer (wrap). Return (ctx, nonce2) to retry, or (None, None)."""
    if n2 >= ta:
        return ctxs[jc], n2 - ta
    order = list(ctxs)
    k = order.index(jc)
    if k == 0 or wrap is None:
        return None, None
    n2 = wrap - ta + 1 + n2
    # btcminer does this in 32-bit unsigned math: a result below zero wraps to a
    # nonce2 the older job never reached, so there is nothing to retry
    if not 0 <= n2 <= 0xFFFFFFFF:
        return None, None
    return ctxs[order[k - 1]], n2


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--bridge", required=True, help="host:port of uartbridge on the miner")
    ap.add_argument("--pool", required=True, help="host:port of the stratum pool")
    ap.add_argument("--user", required=True)
    ap.add_argument("--password", default="x")
    ap.add_argument("--seconds", type=int, default=120)
    ap.add_argument("--submit", action="store_true", help="stage 2b: submit shares meeting pool target")
    ap.add_argument("--suggest-diff", type=float, default=None,
                    help="request this difficulty from the pool (e.g. 4096 for fast test shares)")
    ap.add_argument("--keep-bridge", action="store_true",
                    help="leave the bridge running on exit (default: tell it to stop so btcminer restarts)")
    args = ap.parse_args()

    bh, bp = args.bridge.split(":")
    ph, pp = args.pool.rsplit(":", 1)
    print(f"[pool] connecting {ph}:{pp} as {args.user}")
    st = Stratum(ph, int(pp), args.user, args.password)
    if not st.connect(args.suggest_diff):
        print("[pool] authorize failed")
        return 1
    print(f"[pool] authorized; extranonce1={st.extranonce1.hex()} n2size={st.n2size} "
          f"vmask={st.vmask} diff={st.difficulty}")
    while st.notify is None:
        st.pump(10)
    # a set_difficulty or a newer notify can trail the first notify: take them
    # before the first burst so its 0x25 target and template are current
    while st.pump(0.5):
        pass

    print(f"[bridge] connecting {bh}:{bp}")
    br = Bridge(bh, int(bp))

    ctxs = {}        # jc -> job context; keep the few most recent so a nonce
    cur_jc = None    # that arrives for a just-replaced job still reconstructs

    def send_job(restart=False):
        nonlocal cur_jc
        frames, ctx = build_job(st, restart=restart)
        for f in frames:
            br.send(f)
        nbytes = sum(len(f) for f in frames)
        print(f"[job] {ctx['job_id']} (crc {ctx['jc']:04x}) diff={st.difficulty} "
              f"restart={int(restart)} sent {len(frames)} frames, {nbytes} bytes")
        if restart:  # a new block: every job sent before it is stale for the pool
            for old in ctxs.values():
                old["stale"] = True
        ctxs.pop(ctx["jc"], None)  # re-insert at the end: dict order is job age
        ctxs[ctx["jc"]] = ctx
        for old in list(ctxs)[:-3]:  # bound memory to the 3 most recent jobs
            del ctxs[old]
        cur_jc = ctx["jc"]
        return ctx

    stats = {"poll": 0, "nonce_rec": 0, "diff1": 0, "pool_ok": 0, "raw50": 0, "rewound": 0,
             "submitted": 0, "accepted": 0, "rejected": 0, "status": 0, "crc_bad": 0}
    best = 0
    jcs_seen = set()
    submitted_ids = {}
    ta = None  # ASIC count from 0x51: the nonce2 step of the 0x50 retry rule
    # Always stop the bridge on the way out, errors and Ctrl-C included, so the
    # session script restarts btcminer at once instead of on its watchdog.
    try:
        # the first burst always restarts the MM: it drops the job btcminer left queued
        send_job(restart=True)
        poll = frame(0x33, 0, 1, b"\x00\x00\x00\x00")
        t_end = time.monotonic() + args.seconds
        while time.monotonic() < t_end:
            br.send(poll)
            stats["poll"] += 1
            f, ok = br.recv_frame(time.monotonic() + 0.3)
            if f is not None and not ok:
                stats["crc_bad"] += 1  # corrupt frame: do not parse it
            elif f is not None and f[4] == 0x51:
                stats["status"] += 1
                p = f[HDR:]
                if len(p) >= 0x25:
                    ta = struct.unpack_from("<I", p, 0x21)[0]
                # btcminer resets the MM on 4 (overheat) and 7 (MM wants a reboot).
                # This driver sends no control command, so it hands back to btcminer.
                if p and p[0] in (4, 7):
                    print(f"[mm] status {p[0]}: stopping so btcminer can reset the MM")
                    break
            elif f is not None and f[4] == 0x50:
                p = f[HDR:]
                n = len(p) >> 4
                wrap = struct.unpack_from("<I", p, 16 * n)[0] if len(p) >= 16 * n + 4 else None
                for i in range(n):
                    rec = p[16 * i:16 * i + 16]
                    if (rec[15] >> 4) == 0:
                        continue
                    stats["raw50"] += 1
                    jc = (rec[0] << 8) | rec[1]
                    jcs_seen.add(jc)
                    ctx = ctxs.get(jc)  # the job this nonce belongs to, if still held
                    if ctx is None:
                        continue
                    stats["nonce_rec"] += 1
                    hdr, n2 = rebuild_header(ctx, rec)
                    hv = int.from_bytes(dsha(hdr), "little")
                    if hv > target_from_diff(1) and ta:
                        rctx, rn2 = rewind(ctxs, jc, n2, ta, wrap)
                        if rctx is not None:
                            rhdr, _ = rebuild_header(rctx, rec, rn2)
                            rhv = int.from_bytes(dsha(rhdr), "little")
                            if rhv <= target_from_diff(1):
                                ctx, n2, hv = rctx, rn2, rhv
                                stats["rewound"] += 1
                    zbits = 256 - hv.bit_length()
                    best = max(best, zbits)
                    if hv <= target_from_diff(1):
                        stats["diff1"] += 1
                    # pools judge a share by the difficulty in force when its job was sent
                    if hv <= target_from_diff(ctx["pool_diff"]):
                        stats["pool_ok"] += 1
                        # a job from before the last new block is stale for the pool:
                        # verify it, do not submit it
                        if args.submit and not ctx.get("stale"):
                            en2 = (struct.pack("<I", n2) + b"\x00" * st.n2size)[:st.n2size].hex()
                            ntime_hex = struct.pack(">I", ctx["ntime"] + rec[14]).hex()
                            nonce_hex = rec[8:12][::-1].hex()
                            slot = rec[15] & 0xF
                            slot = slot if slot < 4 else 0  # same fallback as rebuild_header
                            # public-pool XORs this into block.version, so send the rolled
                            # bits only (words[slot] ^ base version), not the full version.
                            vdelta = ctx["words"][slot] ^ ctx["words"][0]
                            ver_hex = struct.pack(">I", vdelta).hex() if st.vmask else None
                            sid = st.submit(ctx["job_id"], en2, ntime_hex, nonce_hex, ver_hex)
                            submitted_ids[sid] = (ctx["job_id"], n2, zbits)
                            stats["submitted"] += 1
            # drain pool messages: submit results and new jobs
            new_job = new_block = False
            while True:
                m = st._readline(0.001)
                if m is None:
                    break
                if m.get("id") in submitted_ids:
                    jid, n2v, zb = submitted_ids.pop(m["id"])
                    acc = bool(m.get("result"))
                    stats["accepted" if acc else "rejected"] += 1
                    print(f"[pool] submit {jid} n2={n2v:#x} zbits={zb}: "
                          f"{'ACCEPTED' if acc else 'REJECTED ' + str(m.get('error'))}")
                else:
                    prev = st.notify["job_id"] if st.notify else None
                    st._dispatch(m)
                    # send every new job, as btcminer does; tested live 2026-10-27: the K210
                    # keeps producing nonces when non-clean updates go out with restart=0
                    if (m.get("method") == "mining.notify" and st.notify
                            and st.notify["job_id"] != prev):
                        new_job = True
                        new_block = new_block or bool(st.notify["clean"])
            if new_job:
                # restart the MM only for a new block, even if a non-clean update
                # followed the clean one in the same drain pass
                send_job(restart=new_block)
    finally:
        br.close()  # free the bridge so it can accept the quit connection
        if not args.keep_bridge and Bridge.stop(bh, int(bp)):
            print("[bridge] sent BRXT; bridge exits and the session script restarts btcminer")

    jcs = sorted(f"{x:04x}" for x in jcs_seen)
    print(f"[done] polls={stats['poll']} status={stats['status']} crc_bad={stats['crc_bad']} "
          f"raw_0x50_records={stats['raw50']} job_crcs_seen={jcs} our_job_crc={cur_jc:04x} "
          f"matched={stats['nonce_rec']} rewound={stats['rewound']} diff1_valid={stats['diff1']} "
          f"meet_pool_target={stats['pool_ok']} submitted={stats['submitted']} "
          f"ACCEPTED={stats['accepted']} rejected={stats['rejected']} best_zbits={best}")
    return 0


if __name__ == "__main__":
    sys.exit(main())

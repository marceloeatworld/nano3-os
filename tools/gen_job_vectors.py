#!/usr/bin/env python3
"""Write the job-burst test vectors for nano3d's Go encoder.

The bursts are built by build_job in tools/nano3miner.py, the reference driver that
was proven live against the K210 and a pool, from fixed public inputs (the Bitcoin
genesis coinbase and synthetic jobs). internal/work/job_test.go checks that the Go
encoder produces the same bytes.

Usage: python3 -I tools/gen_job_vectors.py > internal/work/testdata/job_vectors.json
"""
import hashlib
import json
import os
import sys
import types

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import nano3miner as nm  # noqa: E402

GENESIS_COINBASE = bytes.fromhex(
    "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff4d04ffff"
    "001d0104455468652054696d65732030332f4a616e2f32303039204368616e63656c6c6f72206f6e206272696e"
    "6b206f66207365636f6e64206261696c6f757420666f722062616e6b73ffffffff0100f2052a01000000434104"
    "678afdb0fe5548271967f1a67130b7105cd6a828e03909a67962e0ea1f61deb649f6bc3f4cef38c4f35504e51e"
    "c112de5c384df7ba0b8d578a4c702b6bf11d5fac00000000")


def h(label, n=32):
    return hashlib.sha256(label.encode()).digest()[:n]


cases = [
    {   # genesis coinbase split as coinb1 | extranonce1 (4) | nonce2 slot (4) | coinb2
        "name": "genesis", "job_id": "genesis", "prevhash": "00" * 32,
        "coinb1": GENESIS_COINBASE[:40].hex(), "en1": GENESIS_COINBASE[40:44].hex(),
        "n2size": 4, "coinb2": GENESIS_COINBASE[48:].hex(), "merkles": [],
        "version": "00000001", "nbits": "1d00ffff", "ntime": "495fab29",
        "vmask": None, "diff": 1.0, "restart": True,
    },
    {   # Ocean-like: 8-byte nonce2, BIP310 mask, many merkles, 3 coinbase chunks
        "name": "rolling-n2size8", "job_id": "6ae09a18d2c00f02", "prevhash": h("prev").hex(),
        "coinb1": (h("cb1") * 4)[:100].hex(), "en1": "b34cf001", "n2size": 8,
        "coinb2": (h("cb2") * 6)[:180].hex(), "merkles": [h("mk%d" % i).hex() for i in range(9)],
        "version": "20000000", "nbits": "17034219", "ntime": "671e2f00",
        "vmask": "1fffe000", "diff": 262144.0, "restart": False,
    },
    {   # 3-byte nonce2 (nonce2 range 0xffffff) and a mask whose lowest nibble is not 0xe000
        "name": "mask1fff0000-n2size3", "job_id": "1eb", "prevhash": h("prev2").hex(),
        "coinb1": h("a").hex(), "en1": "3b34860e", "n2size": 3, "coinb2": h("b").hex(),
        "merkles": [h("m").hex()], "version": "20000004", "nbits": "1703a30c", "ntime": "671e4a10",
        "vmask": "1fff0000", "diff": 2048.0, "restart": True,
    },
]


out = []
for c in cases:
    st = types.SimpleNamespace(
        notify={"job_id": c["job_id"], "prevhash": c["prevhash"], "coinb1": c["coinb1"],
                "coinb2": c["coinb2"], "merkles": c["merkles"], "version": c["version"],
                "nbits": c["nbits"], "ntime": c["ntime"], "clean": c["restart"]},
        extranonce1=bytes.fromhex(c["en1"]), n2size=c["n2size"], vmask=c["vmask"],
        difficulty=c["diff"])
    frames, _ = nm.build_job(st, restart=c["restart"])
    out.append(dict(c, frames=[f.hex() for f in frames]))
json.dump(out, sys.stdout, indent=1)
print()

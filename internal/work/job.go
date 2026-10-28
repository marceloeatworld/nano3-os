// Package work turns a stratum job into the K210 job burst (frames 0x20..0x27) and
// rebuilds the block header for each nonce the K210 reports, so it can be checked and
// submitted. It is a port of tools/nano3miner.py (build_job, rebuild_header, rewind),
// which was proven live against the K210 and a pool. Layouts: analysis/protocol-job.md.
package work

import (
	"crypto/sha256"
	"encoding/binary"
	"math/big"

	"nano3-os/internal/k210"
)

// Template is one decoded mining.notify.
type Template struct {
	JobID    string
	PrevHash []byte // 32 bytes as sent in mining.notify: each 4-byte word byte-reversed
	Coinb1   []byte
	Coinb2   []byte
	Merkles  [][]byte // 32 bytes each
	Version  uint32
	NBits    uint32
	NTime    uint32
	Clean    bool
	Diff     float64 // pool difficulty when the notify arrived; 0 = use the Pool's
}

// Pool is the per-connection state the job encoding needs.
type Pool struct {
	ExtraNonce1 []byte
	N2Size      int     // pool extranonce2 size in bytes
	VMask       uint32  // granted version-rolling mask, 0 when none
	Difficulty  float64 // pool share difficulty
	// Tag fills the bytes of the extranonce2 slot after the 4 the K210 writes, when it
	// fits (N2Size - 4 bytes or fewer); the rest of the slot stays zero. The same
	// bytes go back in every submit, so the pool rebuilds the same coinbase.
	Tag []byte
}

// Job is kept per job sent, to rebuild the headers of its nonces.
type Job struct {
	JobID     string
	JC        uint16 // crc16(JobID): the job tag in 0x21 and in every nonce record
	Coinbase  []byte // coinb1 + extranonce1 + zeroed nonce2 slot + coinb2
	N2Off     int
	N2Size    int
	Merkles   [][]byte
	HeaderTpl [80]byte  // version, prevhash, ntime, nbits; merkle root and nonce left zero
	Words     [4]uint32 // the four rolled versions sent in 0x27
	NTime     uint32
	PoolDiff  float64  // pool difficulty when the job was sent
	Target    *big.Int // share target for PoolDiff, computed once
	Stale     bool     // set once a newer block was sent: no longer submittable
}

// MaxChipDiff caps the K210 share target: btcminer sends min(pool difficulty, 4096).
const MaxChipDiff = 4096

func dsha(b []byte) []byte {
	h := sha256.Sum256(b)
	h = sha256.Sum256(h[:])
	return h[:]
}

func wordSwap(b []byte) []byte {
	out := make([]byte, len(b))
	for i := 0; i+4 <= len(b); i += 4 {
		out[i], out[i+1], out[i+2], out[i+3] = b[i+3], b[i+2], b[i+1], b[i]
	}
	return out
}

var (
	diff1     = new(big.Int).Lsh(big.NewInt(0xFFFF), 208)
	maxTarget = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
)

// Target returns the 256-bit share target for a difficulty: diff1 / diff, capped at
// 2^256-1 (a pool difficulty under 2^-32 would otherwise not fit the 32-byte target).
func Target(diff float64) *big.Int {
	if diff <= 0 {
		diff = 1
	}
	q := new(big.Rat).Quo(new(big.Rat).SetInt(diff1), new(big.Rat).SetFloat64(diff))
	t := new(big.Int).Quo(q.Num(), q.Denom())
	if t.Cmp(maxTarget) > 0 {
		t.Set(maxTarget)
	}
	return t
}

// HashValue is the double SHA-256 of a header read as a little-endian integer, the
// number compared against targets.
func HashValue(h [80]byte) *big.Int {
	d := dsha(h[:])
	be := make([]byte, 32)
	for i := range d {
		be[31-i] = d[i]
	}
	return new(big.Int).SetBytes(be)
}

// ZeroBits is the count of leading zero bits of a hash value (for logs).
func ZeroBits(v *big.Int) int { return 256 - v.BitLen() }

// RolledVersions returns the four versions btcminer sends in 0x27: the base version,
// plus the mask's lowest non-zero nibble (in place), plus the rest of the mask, plus
// the full mask (analysis/protocol-job.md, 6.2).
func RolledVersions(version, mask uint32) [4]uint32 {
	var low uint32
	for s := 0; s < 32; s += 4 {
		if n := mask & (0xF << s); n != 0 {
			low = n
			break
		}
	}
	return [4]uint32{version, version | low, version | (mask &^ low), version | mask}
}

// Build returns the job burst for a template, in btcminer's send order, and the Job
// needed to check its nonces. restart sets the restart word that makes the MM drop
// queued work: send it for a new block (clean job) and for the first job.
func Build(t Template, p Pool, restart bool) ([][]byte, *Job) {
	// A set_difficulty applies to the jobs that follow it: a job keeps the difficulty
	// in force when its notify arrived, not one that came in the same batch after it.
	diff := p.Difficulty
	if t.Diff > 0 {
		diff = t.Diff
	}
	j := &Job{
		JobID:    t.JobID,
		JC:       k210.CRC16([]byte(t.JobID)),
		N2Off:    len(t.Coinb1) + len(p.ExtraNonce1),
		N2Size:   p.N2Size,
		Merkles:  t.Merkles,
		Words:    RolledVersions(t.Version, p.VMask),
		NTime:    t.NTime,
		PoolDiff: diff,
		Target:   Target(diff),
	}
	cb := make([]byte, 0, j.N2Off+p.N2Size+len(t.Coinb2))
	cb = append(cb, t.Coinb1...)
	cb = append(cb, p.ExtraNonce1...)
	slot := make([]byte, p.N2Size) // the MM writes nonce2 in its first min(N2Size, 4) bytes
	if len(p.Tag) > 0 && len(p.Tag) <= p.N2Size-4 {
		copy(slot[4:], p.Tag)
	}
	cb = append(cb, slot...)
	j.Coinbase = append(cb, t.Coinb2...)

	// Header template: notify prevhash words are byte-reversed; merkle root and nonce stay zero.
	binary.LittleEndian.PutUint32(j.HeaderTpl[0:], t.Version)
	copy(j.HeaderTpl[4:36], wordSwap(t.PrevHash))
	binary.LittleEndian.PutUint32(j.HeaderTpl[68:], t.NTime)
	binary.LittleEndian.PutUint32(j.HeaderTpl[72:], t.NBits)
	// header_bin as btcminer sends it: header + SHA-256 padding (0x80, zeros, bit
	// length 640 big endian), 128 bytes, each 32-bit word byte-reversed.
	hb := make([]byte, 128)
	copy(hb, j.HeaderTpl[:])
	hb[80] = 0x80
	binary.BigEndian.PutUint64(hb[120:], 640)
	wire := wordSwap(hb)

	chipDiff := diff
	if chipDiff > MaxChipDiff {
		chipDiff = MaxChipDiff
	}
	tgt := Target(chipDiff).FillBytes(make([]byte, 32))
	for i, k := 0, 31; i < k; i, k = i+1, k-1 { // little endian on the wire
		tgt[i], tgt[k] = tgt[k], tgt[i]
	}

	n2sent := min(p.N2Size, 4)
	n2range := uint32(0xFFFFFFFF)
	if p.N2Size == 3 { // single device: btcminer uses 0xffffff for a 3-byte nonce2
		n2range = 0xFFFFFF
	}
	params := []uint32{uint32(len(j.Coinbase)), uint32(j.N2Off), uint32(n2sent), 36,
		uint32(len(t.Merkles)), 0, n2range}
	if restart {
		params = append(params, 1)
	}
	be := func(vs ...uint32) []byte {
		b := make([]byte, 4*len(vs))
		for i, v := range vs {
			binary.BigEndian.PutUint32(b[4*i:], v)
		}
		return b
	}

	var frames [][]byte
	frames = append(frames, k210.Encode(k210.TypeJobParams, 0, 1, be(params...)))
	frames = append(frames, k210.Encode(k210.TypeVersions, 0, 1, be(j.Words[:]...)))
	frames = append(frames, k210.Encode(k210.TypeTarget, 0, 1, tgt))
	frames = append(frames, k210.Encode(k210.TypeJobID, 0, 1, []byte{byte(j.JC >> 8), byte(j.JC), 0, 0}))
	n := (len(j.Coinbase) + 127) / 128
	for i := 0; i < n; i++ {
		frames = append(frames, k210.Encode(k210.TypeCoinbase, uint16(i), uint16(n), j.Coinbase[128*i:min(128*i+128, len(j.Coinbase))]))
	}
	for i, mk := range t.Merkles {
		frames = append(frames, k210.Encode(k210.TypeMerkle, uint16(i), uint16(len(t.Merkles)), mk))
	}
	for i := 0; i < 4; i++ {
		frames = append(frames, k210.Encode(k210.TypeHeader, uint16(i), 4, wire[32*i:32*i+32]))
	}
	frames = append(frames, k210.Encode(k210.TypeJobFinish, 0, 1, nil))
	return frames, j
}

// Record is one 16-byte nonce record from a 0x50 frame (analysis/PROTOCOL.md, 3).
type Record []byte

func (r Record) JC() uint16     { return uint16(r[0])<<8 | uint16(r[1]) }
func (r Record) N2() uint32     { return binary.LittleEndian.Uint32(r[4:]) }
func (r Record) Valid() bool    { return r[15]>>4 != 0 }
func (r Record) NTimeOff() byte { return r[14] }
func (r Record) Slot() int {
	if s := int(r[15] & 0xF); s < 4 {
		return s
	}
	return 0
}

// Records splits a 0x50 payload into its records and the trailing wrap value
// (the previous job's last nonce2), which the retry rule needs.
func Records(p []byte) (recs []Record, wrap uint32, hasWrap bool) {
	n := len(p) / 16
	for i := 0; i < n; i++ {
		if r := Record(p[16*i : 16*i+16]); r.Valid() {
			recs = append(recs, r)
		}
	}
	if len(p) >= 16*n+4 {
		return recs, binary.LittleEndian.Uint32(p[16*n:]), true
	}
	return recs, 0, false
}

// Header rebuilds the block header for a record, with the given nonce2. The MM
// writes min(n2size, 4) bytes of nonce2 little endian at N2Off; the rest stays zero.
func (j *Job) Header(r Record, n2 uint32) [80]byte {
	cb := append([]byte(nil), j.Coinbase...)
	var n2b [4]byte
	binary.LittleEndian.PutUint32(n2b[:], n2)
	copy(cb[j.N2Off:j.N2Off+min(j.N2Size, 4)], n2b[:])
	root := dsha(cb)
	for _, mk := range j.Merkles {
		root = dsha(append(append([]byte(nil), root...), mk...))
	}
	h := j.HeaderTpl
	binary.LittleEndian.PutUint32(h[0:], j.Words[r.Slot()])
	copy(h[36:68], root)
	binary.LittleEndian.PutUint32(h[68:], j.NTime+uint32(r.NTimeOff()))
	copy(h[76:80], r[8:12])
	return h
}

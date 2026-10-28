package work

import (
	"encoding/binary"
	"encoding/hex"
	"math/big"
)

// Book holds the most recent jobs sent to the MM, oldest first, so nonces that
// arrive just after a job was replaced still resolve.
type Book struct{ jobs []*Job }

const keepJobs = 3

// Add records a job just sent. newBlock marks every earlier job stale: it no longer
// counts for the pool.
func (b *Book) Add(j *Job, newBlock bool) {
	if newBlock {
		for _, old := range b.jobs {
			old.Stale = true
		}
	}
	for i, old := range b.jobs { // a resent job id moves to the end: order is job age
		if old.JC == j.JC {
			b.jobs = append(b.jobs[:i], b.jobs[i+1:]...)
			break
		}
	}
	b.jobs = append(b.jobs, j)
	if len(b.jobs) > keepJobs {
		b.jobs = b.jobs[len(b.jobs)-keepJobs:]
	}
}

// Get returns the job with this tag, or nil.
func (b *Book) Get(jc uint16) *Job {
	for _, j := range b.jobs {
		if j.JC == jc {
			return j
		}
	}
	return nil
}

// Rewind applies btcminer's 0x50 retry rule (analysis/PROTOCOL.md, 3): a result can
// arrive one round of TA nonce2 values late. With nonce2 >= TA it belongs to the same
// job at nonce2 - TA; below that, to the end of the next older job, whose last nonce2
// is the frame trailer (wrap). btcminer computes this in 32-bit unsigned math, so a
// result outside 0..2^32-1 means there is nothing to retry.
func (b *Book) Rewind(jc uint16, n2, ta, wrap uint32, hasWrap bool) (*Job, uint32, bool) {
	if ta == 0 {
		return nil, 0, false
	}
	if n2 >= ta {
		j := b.Get(jc)
		return j, n2 - ta, j != nil
	}
	for i, j := range b.jobs {
		if j.JC != jc {
			continue
		}
		if i == 0 || !hasWrap {
			return nil, 0, false
		}
		v := int64(wrap) - int64(ta) + 1 + int64(n2)
		if v < 0 || v > 0xFFFFFFFF {
			return nil, 0, false
		}
		return b.jobs[i-1], uint32(v), true
	}
	return nil, 0, false
}

// Share is a checked nonce, ready to submit.
type Share struct {
	Job   *Job
	N2    uint32
	Rec   Record
	Value *big.Int // hash value
}

// Check rebuilds the header of a record and verifies it reaches difficulty 1, trying
// the retry rule when it does not. ok is false for a nonce that fails both.
func (b *Book) Check(r Record, ta, wrap uint32, hasWrap bool) (s Share, rewound, ok bool) {
	j := b.Get(r.JC())
	if j == nil {
		return Share{}, false, false
	}
	one := diff1 // difficulty 1, the MM's floor; no allocation per nonce
	v := HashValue(j.Header(r, r.N2()))
	if v.Cmp(one) <= 0 {
		return Share{Job: j, N2: r.N2(), Rec: r, Value: v}, false, true
	}
	if rj, rn2, found := b.Rewind(r.JC(), r.N2(), ta, wrap, hasWrap); found {
		if rv := HashValue(rj.Header(r, rn2)); rv.Cmp(one) <= 0 {
			return Share{Job: rj, N2: rn2, Rec: r, Value: rv}, true, true
		}
	}
	return Share{Job: j, N2: r.N2(), Rec: r, Value: v}, false, false
}

// SubmitParams returns the mining.submit fields for a share: extranonce2, ntime,
// nonce, and the rolled version bits ("" when version rolling is off). Version bits
// are sent as the delta from the job version (public-pool XORs them in; for
// BIP310 pools the masked result is the same).
func (s Share) SubmitParams(n2size int, rolling bool) (en2, ntime, nonce, version string) {
	// The slot as sent to the MM (zeros, or a tag after the first 4 bytes), with the
	// nonce2 the MM wrote: the pool rebuilds the coinbase from exactly these bytes.
	b := make([]byte, max(n2size, 4))
	copy(b, s.Job.Coinbase[s.Job.N2Off:s.Job.N2Off+min(n2size, s.Job.N2Size)])
	binary.LittleEndian.PutUint32(b, s.N2)
	en2 = hex.EncodeToString(b[:n2size])
	var t [4]byte
	binary.BigEndian.PutUint32(t[:], s.Job.NTime+uint32(s.Rec.NTimeOff()))
	ntime = hex.EncodeToString(t[:])
	nonce = hex.EncodeToString([]byte{s.Rec[11], s.Rec[10], s.Rec[9], s.Rec[8]})
	if rolling {
		binary.BigEndian.PutUint32(t[:], s.Job.Words[s.Rec.Slot()]^s.Job.Words[0])
		version = hex.EncodeToString(t[:])
	}
	return
}

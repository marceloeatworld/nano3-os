package work

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"testing"

	"nano3-os/internal/k210"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func parseU32(t *testing.T, s string) uint32 {
	t.Helper()
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		t.Fatal(err)
	}
	return uint32(v)
}

type vector struct {
	Name     string   `json:"name"`
	JobID    string   `json:"job_id"`
	PrevHash string   `json:"prevhash"`
	Coinb1   string   `json:"coinb1"`
	En1      string   `json:"en1"`
	N2Size   int      `json:"n2size"`
	Coinb2   string   `json:"coinb2"`
	Merkles  []string `json:"merkles"`
	Version  string   `json:"version"`
	NBits    string   `json:"nbits"`
	NTime    string   `json:"ntime"`
	VMask    *string  `json:"vmask"`
	Diff     float64  `json:"diff"`
	Restart  bool     `json:"restart"`
	Frames   []string `json:"frames"`
}

func (v vector) inputs(t *testing.T) (Template, Pool) {
	tp := Template{JobID: v.JobID, PrevHash: mustHex(t, v.PrevHash), Coinb1: mustHex(t, v.Coinb1),
		Coinb2: mustHex(t, v.Coinb2), Version: parseU32(t, v.Version), NBits: parseU32(t, v.NBits),
		NTime: parseU32(t, v.NTime), Clean: v.Restart}
	for _, m := range v.Merkles {
		tp.Merkles = append(tp.Merkles, mustHex(t, m))
	}
	p := Pool{ExtraNonce1: mustHex(t, v.En1), N2Size: v.N2Size, Difficulty: v.Diff}
	if v.VMask != nil {
		p.VMask = parseU32(t, *v.VMask)
	}
	return tp, p
}

func loadVectors(t *testing.T) []vector {
	b, err := os.ReadFile("testdata/job_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vs []vector
	if err := json.Unmarshal(b, &vs); err != nil {
		t.Fatal(err)
	}
	return vs
}

// The job burst must match, byte for byte, what the live-proven reference driver
// (tools/nano3miner.py) builds for the same input (tools/gen_job_vectors.py).
func TestBuildMatchesReferenceDriver(t *testing.T) {
	for _, v := range loadVectors(t) {
		tp, p := v.inputs(t)
		frames, _ := Build(tp, p, v.Restart)
		if len(frames) != len(v.Frames) {
			t.Fatalf("%s: %d frames, reference %d", v.Name, len(frames), len(v.Frames))
		}
		for i, f := range frames {
			if got := hex.EncodeToString(f); got != v.Frames[i] {
				t.Fatalf("%s: frame %d (type %02x)\n got %s\nwant %s", v.Name, i, f[4], got, v.Frames[i])
			}
		}
	}
}

const genesisHash = "000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f"

// genesis returns the genesis job and the nonce record the MM would report for the
// genesis block: nonce2 is the coinbase bytes that sit in the nonce2 slot.
func genesis(t *testing.T) (*Job, Record, uint32) {
	var v vector
	for _, x := range loadVectors(t) {
		if x.Name == "genesis" {
			v = x
		}
	}
	tp, p := v.inputs(t)
	_, j := Build(tp, p, true)
	full := append(append(append([]byte(nil), tp.Coinb1...), p.ExtraNonce1...), mustHex(t, "ff001d01")...)
	full = append(full, tp.Coinb2...)
	n2 := binary.LittleEndian.Uint32(full[j.N2Off:])
	rec := make(Record, 16)
	rec[0], rec[1] = byte(j.JC>>8), byte(j.JC)
	binary.LittleEndian.PutUint32(rec[4:], n2)
	copy(rec[8:12], mustHex(t, "1dac2b7c")) // the genesis nonce as it sits in the header
	rec[15] = 0x10                          // valid, version slot 0
	return j, rec, n2
}

func displayHash(h [80]byte) string {
	d := dsha(h[:])
	for i, k := 0, 31; i < k; i, k = i+1, k-1 {
		d[i], d[k] = d[k], d[i]
	}
	return hex.EncodeToString(d)
}

func TestHeaderRebuildsGenesisBlock(t *testing.T) {
	j, rec, n2 := genesis(t)
	if got := displayHash(j.Header(rec, n2)); got != genesisHash {
		t.Fatalf("rebuilt hash %s, want %s", got, genesisHash)
	}
	var b Book
	b.Add(j, true)
	s, rewound, ok := b.Check(rec, 10, 0, false)
	if !ok || rewound || ZeroBits(s.Value) != 43 {
		t.Fatalf("check ok=%v rewound=%v zero bits %d", ok, rewound, ZeroBits(s.Value))
	}
	en2, ntime, nonce, ver := s.SubmitParams(4, false)
	if en2 != "ff001d01" || ntime != "495fab29" || nonce != "7c2bac1d" || ver != "" {
		t.Fatalf("submit params %s %s %s %q", en2, ntime, nonce, ver)
	}
}

func TestRetryRuleSameJob(t *testing.T) {
	j, rec, n2 := genesis(t)
	var b Book
	b.Add(j, true)
	late := append(Record(nil), rec...)
	binary.LittleEndian.PutUint32(late[4:], n2+10) // reported one round of TA=10 late
	s, rewound, ok := b.Check(late, 10, 0, false)
	if !ok || !rewound || s.N2 != n2 {
		t.Fatalf("ok=%v rewound=%v n2=%#x, want %#x", ok, rewound, s.N2, n2)
	}
}

func TestRetryRuleOlderJob(t *testing.T) {
	j, rec, n2 := genesis(t)
	newer := &Job{JobID: "newer", JC: k210.CRC16([]byte("newer"))}
	var b Book
	b.Add(j, true)
	b.Add(newer, false)
	// tagged with the newer job, nonce2 below TA: it belongs to the end of the older
	// job, whose last nonce2 is wrap: wrap - TA + 1 + 3 = n2
	late := append(Record(nil), rec...)
	late[0], late[1] = byte(newer.JC>>8), byte(newer.JC)
	binary.LittleEndian.PutUint32(late[4:], 3)
	s, rewound, ok := b.Check(late, 10, n2+10-1-3, true)
	if !ok || !rewound || s.Job != j || s.N2 != n2 {
		t.Fatalf("ok=%v rewound=%v job=%v n2=%#x", ok, rewound, s.Job == j, s.N2)
	}
}

func TestRetryRuleOutOfRangeDoesNotPanic(t *testing.T) {
	// wrap 0x8 (seen live) with TA 10 and nonce2 0 gives -1: nothing to retry
	var b Book
	b.Add(&Job{JC: 1}, true)
	b.Add(&Job{JC: 2}, false)
	if _, _, found := b.Rewind(2, 0, 10, 8, true); found {
		t.Fatal("negative nonce2 accepted")
	}
}

func TestStaleOnlyAfterNewBlock(t *testing.T) {
	a, c, d := &Job{JC: 1}, &Job{JC: 2}, &Job{JC: 3}
	var b Book
	b.Add(a, true)
	b.Add(c, false) // an update on the same block: a stays valid
	if a.Stale {
		t.Fatal("update marked the previous job stale")
	}
	b.Add(d, true) // new block
	if !a.Stale || !c.Stale || d.Stale {
		t.Fatalf("stale a=%v c=%v d=%v", a.Stale, c.Stale, d.Stale)
	}
}

func TestRolledVersions(t *testing.T) {
	for _, c := range []struct {
		mask uint32
		want [4]uint32
	}{
		{0x1FFFE000, [4]uint32{0x20000000, 0x2000E000, 0x3FFF0000, 0x3FFFE000}},
		{0x1FFF0000, [4]uint32{0x20000000, 0x200F0000, 0x3FF00000, 0x3FFF0000}},
		{0, [4]uint32{0x20000000, 0x20000000, 0x20000000, 0x20000000}},
	} {
		if got := RolledVersions(0x20000000, c.mask); got != c.want {
			t.Errorf("mask %08x: % x, want % x", c.mask, got, c.want)
		}
	}
}

// A coinbase tag fills bytes 5..8 of an 8-byte extranonce2 slot, is left out of a
// 4-byte one, and comes back in every submit.
func TestCoinbaseTag(t *testing.T) {
	tpl := Template{JobID: "1", PrevHash: make([]byte, 32), Coinb1: []byte{1, 2}, Coinb2: []byte{3}}
	tag := []byte("HODL")
	_, j := Build(tpl, Pool{ExtraNonce1: []byte{9, 9, 9, 9}, N2Size: 8, Difficulty: 1, Tag: tag}, true)
	if got := j.Coinbase[j.N2Off : j.N2Off+8]; string(got) != "\x00\x00\x00\x00HODL" {
		t.Fatalf("slot %x", got)
	}
	s := Share{Job: j, N2: 0x01020304, Rec: make(Record, 16)}
	if en2, _, _, _ := s.SubmitParams(8, false); en2 != "04030201484f444c" {
		t.Fatalf("extranonce2 %s", en2)
	}
	_, j = Build(tpl, Pool{ExtraNonce1: []byte{9, 9, 9, 9}, N2Size: 4, Difficulty: 1, Tag: tag}, true)
	if got := j.Coinbase[j.N2Off : j.N2Off+4]; string(got) != "\x00\x00\x00\x00" {
		t.Fatalf("4-byte slot %x", got)
	}
	if en2, _, _, _ := (Share{Job: j, N2: 1, Rec: make(Record, 16)}).SubmitParams(4, false); en2 != "01000000" {
		t.Fatalf("4-byte extranonce2 %s", en2)
	}
}

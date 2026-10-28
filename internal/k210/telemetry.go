package k210

import (
	"encoding/binary"
	"math"
)

// Telemetry frame types (analysis/protocol-rx.md, 3.3 to 3.8).
const (
	TypeCounters   = 0x52 // per-ASIC good/error counters and PLL core counts
	TypeFreqs      = 0x53 // the four PLL frequencies, MHz
	TypePower      = 0x55 // input power report
	TypeLevelParam = 0x71 // one record per work level
)

// Power is a valid 0x55 report. The MM sends one about every 4 s (59 of 346 frames in
// the bring-up capture; the rest carry a single 00 byte). Field meanings are inferred:
// live values 27.38 V, 4 A, 124 W and 328 match a 28 V USB-C PD input at level 2,
// whose 0x71 record lists 125.
type Power struct {
	InputV float64 // payload 0x05, in 10 mV
	InputA int     // payload 0x07, whole amperes
	Watts  int     // payload 0x09
	Code   int     // payload 0x0b, close to the level's voltage setting (330 at level 2)
}

// ParsePower reads a 0x55 payload; ok is false for the 1-byte "no data" frames.
func ParsePower(p []byte) (pw Power, ok bool) {
	if len(p) < 13 || p[0]&1 == 0 {
		return Power{}, false
	}
	u := func(off int) int { return int(binary.LittleEndian.Uint16(p[off:])) }
	return Power{InputV: float64(u(5)) / 100, InputA: u(7), Watts: u(9), Code: u(11)}, true
}

// StatusExtra is what 0x51 carries besides the fields nano3d already reads.
type StatusExtra struct {
	SpeedMHs uint32  // GHSspd, MH/s: the MM's expected hashrate
	DHPct    float64 // DH, %: share of nonces the MM counts as hardware errors
}

// ParseStatusExtra reads GHSspd (payload 0x0d) and DH (0x11, a little-endian double).
func ParseStatusExtra(p []byte) (StatusExtra, bool) {
	if len(p) < 0x19 {
		return StatusExtra{}, false
	}
	dh := math.Float64frombits(binary.LittleEndian.Uint64(p[0x11:]))
	if math.IsNaN(dh) || math.IsInf(dh, 0) { // 0/0 before any nonce; JSON cannot carry it
		dh = 0
	}
	return StatusExtra{SpeedMHs: binary.LittleEndian.Uint32(p[0x0d:]), DHPct: dh}, true
}

// ParseFreqs reads the four PLL frequencies of a 0x53 payload, MHz.
func ParseFreqs(p []byte) (f [4]int, ok bool) {
	if len(p) < 8 {
		return f, false
	}
	for i := range f {
		f[i] = int(binary.LittleEndian.Uint16(p[2*i:]))
	}
	return f, true
}

// ErrorPct reads one ASIC's counters from a 0x52 payload and returns the error share
// btcminer prints as ERATIO: B*100/(A+B), counters big endian. ok is false when the
// frame carries no counters.
func ErrorPct(p []byte) (pct float64, ok bool) {
	if len(p) < 9 || p[0] == 0 {
		return 0, false
	}
	a := float64(binary.BigEndian.Uint32(p[1:]))
	b := float64(binary.BigEndian.Uint32(p[5:]))
	if a+b == 0 {
		return 0, true
	}
	return b * 100 / (a + b), true
}

// Level is one work level's 14-byte 0x71 record: 7 u16 (power, target temp, voltage,
// PLL0..PLL3). Live: (60, 80, 300, 100, 293, 313, 333), (90, 85, 310, 100, 447, 467,
// 487), (125, 90, 330, 100, 574, 594, 614) for levels 0, 1, 2. The first field is the
// level's nominal power in watts (inferred: 125 matches the 0x55 reading at level 2),
// and the PLLs match the 0x53 clocks seen at each level.
type Level struct {
	Watts int
	PLL   [4]int // MHz
}

// Levels reads the records of a 0x71 payload, one per work level.
func Levels(p []byte, levels int) []Level {
	var out []Level
	for i := 0; i < levels && 3+14*(i+1) <= len(p); i++ {
		r := p[3+14*i:]
		u := func(k int) int { return int(binary.LittleEndian.Uint16(r[2*k:])) }
		out = append(out, Level{Watts: u(0), PLL: [4]int{u(3), u(4), u(5), u(6)}})
	}
	return out
}

// CoreMHz is the mean of PLL1..PLL3, the clocks the cores run on (PLL0 clocks no core:
// PLLCNT[0] is 0 live).
func CoreMHz(pll [4]int) float64 { return float64(pll[1]+pll[2]+pll[3]) / 3 }

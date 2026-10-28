// Package board drives the Nano 3's fan, hardware watchdog and Wi-Fi the way the
// stock btcminer does. Every behavior here was read from btcminer's disassembly;
// addresses are given next to each rule.
package board

import (
	"errors"
	"io/fs"
	"math"
	"os"
	"strconv"
	"strings"
)

const pwmDir = "/sys/class/pwm/pwmchip0"

// Fan is the PWM fan on pwmchip0 channel 2, period 40000 ns (25 kHz).
type Fan struct{ last int }

func writeSys(path string, v int) error {
	return os.WriteFile(path, []byte(strconv.Itoa(v)), 0)
}

// InitFan sets up the PWM like btcminer's fan_init (0xb7230): export channel 2,
// period 40000, duty 10000 (25 %), enable. No polarity write.
//
// One difference: on a warm start (the PWM already running faster than 25 %, as a
// previous miner left it) the current duty is kept. btcminer drops to 25 % here, and
// with hot ASICs that let them reach 103 C before the PID caught up (seen live).
func InitFan() (*Fan, error) {
	if _, err := os.Stat(pwmDir + "/pwm2"); errors.Is(err, fs.ErrNotExist) {
		if err := writeSys(pwmDir+"/export", 2); err != nil {
			return nil, err
		}
	}
	start := 10000
	if b, err := os.ReadFile(pwmDir + "/pwm2/enable"); err == nil && strings.TrimSpace(string(b)) == "1" {
		if b, err := os.ReadFile(pwmDir + "/pwm2/duty_cycle"); err == nil {
			if d, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && d > start && d <= 40000 {
				start = d
			}
		}
	}
	for _, w := range []struct {
		file string
		v    int
	}{{"period", 40000}, {"duty_cycle", start}, {"enable", 1}, {"duty_cycle", start}} {
		if err := writeSys(pwmDir+"/pwm2/"+w.file, w.v); err != nil {
			return nil, err
		}
	}
	return &Fan{last: start / 40}, nil
}

// SeedPID starts a PID at the fan's current duty, so it continues from there
// instead of from 25 %.
func (f *Fan) SeedPID() PID { return PIDAt(f.last) }

// PIDAt returns a PID whose output starts at the given duty in per mille.
func PIDAt(permille int) PID { return PID{acc: permille * 1023 / 1000} }

// Set writes a duty in per mille, clamped to 100..1000 as set_fan_duty_cycle
// (0xb7290) does; duty_cycle = permille * 40. It writes only on a change.
func (f *Fan) Set(permille int) error {
	permille = min(max(permille, 100), 1000)
	if permille == f.last {
		return nil
	}
	if err := writeSys(pwmDir+"/pwm2/duty_cycle", permille*40); err != nil {
		return err
	}
	f.last = permille
	return nil
}

// Permille is the duty last written.
func (f *Fan) Permille() int { return f.last }

// PID is btcminer's fan_pid_control (0xb5ae0): an incremental PID on a 0..1023
// accumulator. Constants read from the binary: 100.0, 50.0, Kp 35.0, 2.0, 0.5.
type PID struct {
	acc    int
	e1, e2 float32 // previous two errors
}

// Step takes the mean ASIC temperature and the target (both deg C) and returns the
// fan duty in per mille. Called every 2 s, as misc_thread does.
func (p *PID) Step(temp float32, target int) int {
	switch {
	case temp > 100: // 0xb5b10: too hot, full speed, history kept
		p.acc = 1023
		return 1000
	case temp < 50: // 0xb5b68: cold, or no reading yet (-273)
		p.acc = 256
		return 250
	}
	// e is the error truncated to half a degree (0xb5b80..0xb5bb4)
	e := float32(float64(int32(2*(temp-float32(target)))) * 0.5)
	d := 35*(e-p.e1) + e + (e - 2*p.e1 + p.e2)
	p.e2, p.e1 = p.e1, e
	delta := int(float32(math.Round(float64(d)))) // roundf, then truncate
	delta = min(max(delta, -100), 50)             // 0xb5be0..0xb5bfe
	acc := p.acc + delta
	switch {
	case acc > 1023:
		p.acc = 1023
		return 1000
	case acc <= 255:
		p.acc = 256
		return 250
	}
	p.acc = acc
	return 1000 * acc / 1023
}

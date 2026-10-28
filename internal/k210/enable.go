package k210

import (
	"errors"
	"io/fs"
	"os"
)

// EnableLine drives GPIO 61 high, as btcminer does at start ("direction" <- "high",
// captured by strace). The line holds the K210 running; driving it low resets it
// (analysis/protocol-control.md, 6.4). Writing "high" to direction sets it as an
// output that is already high, with no low glitch.
func EnableLine() error {
	const dir = "/sys/class/gpio/gpio61"
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		if err := os.WriteFile("/sys/class/gpio/export", []byte("61"), 0); err != nil {
			return err
		}
	}
	return os.WriteFile(dir+"/direction", []byte("high"), 0)
}

// HoldReset drives GPIO 61 low, which holds the K210 in reset: it stops driving the
// ASIC chain. btcminer only pulses it (1 ms, protocol-control.md 6.4); nano3d holds it
// to stop mining on overheat. EnableLine releases it.
func HoldReset() error {
	return os.WriteFile("/sys/class/gpio/gpio61/direction", []byte("low"), 0)
}

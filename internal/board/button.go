package board

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// OpenButton opens the front button, the "gpio_keys" input device (btcminer finds it
// by the same name, "gpio_keys/input/input"). Presses delivers one value per press.
func OpenButton() (*os.File, error) {
	names, _ := filepath.Glob("/sys/class/input/event*/device/name")
	for _, n := range names {
		b, err := os.ReadFile(n)
		if err != nil || strings.TrimSpace(string(b)) != "gpio_keys" {
			continue
		}
		return os.Open(filepath.Join("/dev/input", filepath.Base(filepath.Dir(filepath.Dir(n)))))
	}
	return nil, errors.New("no gpio_keys input device")
}

// Presses reads key events from an input device and sends one value per key press
// (EV_KEY, value 1). A press that arrives while the last one is not taken yet is
// dropped. The channel is closed when the device fails.
func Presses(f *os.File) <-chan struct{} {
	ch := make(chan struct{}, 1)
	go func() {
		defer close(ch)
		ev := make([]byte, 24) // struct input_event on 64-bit: timeval, u16 type, u16 code, s32 value
		for {
			if _, err := f.Read(ev); err != nil {
				return
			}
			if binary.LittleEndian.Uint16(ev[16:]) == 1 && int32(binary.LittleEndian.Uint32(ev[20:])) == 1 {
				select {
				case ch <- struct{}{}:
				default:
				}
			}
		}
	}()
	return ch
}

package board

import (
	"os"
	"syscall"
	"unsafe"
)

// Linux watchdog ioctls (include/uapi/linux/watchdog.h).
const (
	wdiocSetOptions = 0x80045704 // _IOR('W', 4, int)
	wdiocKeepAlive  = 0x80045705 // _IOR('W', 5, int)
	wdiocSetTimeout = 0xC0045706 // _IOWR('W', 6, int)
	wdiosDisable    = 1
	wdiosEnable     = 2
)

// Watchdog is the hardware watchdog, used like btcminer's watchdog_init (0xbe840):
// timeout 89 s, card enabled, fed with WDIOC_KEEPALIVE. The caller feeds it from its
// main loop, so a stalled loop reboots the board.
type Watchdog struct{ f *os.File }

func wdIoctl(fd uintptr, req uintptr, v int32) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(&v))); e != 0 {
		return e
	}
	return nil
}

// OpenWatchdog arms the watchdog with an 89 s timeout.
func OpenWatchdog() (*Watchdog, error) {
	f, err := os.OpenFile("/dev/watchdog", os.O_WRONLY, 0)
	if err != nil {
		return nil, err
	}
	fd := f.Fd()
	if err := wdIoctl(fd, wdiocSetTimeout, 89); err != nil {
		f.Close()
		return nil, err
	}
	if err := wdIoctl(fd, wdiocSetOptions, wdiosEnable); err != nil {
		f.Close()
		return nil, err
	}
	return &Watchdog{f: f}, nil
}

// Feed restarts the 89 s countdown.
func (w *Watchdog) Feed() error { return wdIoctl(w.f.Fd(), wdiocKeepAlive, 0) }

// Close disables the watchdog and releases it, as btcminer's exit path does
// (process_before_exit, 0x15600), so the next miner can take it over.
func (w *Watchdog) Close() error {
	wdIoctl(w.f.Fd(), wdiocSetOptions, wdiosDisable)
	return w.f.Close()
}

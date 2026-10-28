package k210

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// OpenUART opens the K210 link raw at 115200 8N1, the settings btcminer's uart_init
// uses (confirmed by strace: CS8|CREAD|CLOCAL, no flow control). It takes an exclusive
// flock, the same lock as tools/uartbridge, so two UART users never run at once: two
// readers corrupt each other's frames (see JOURNAL.md, 20:39). The stock btcminer takes
// no lock, so it must still be stopped before nano3d starts.
//
// The fd is non-blocking, which makes the returned File pollable: read deadlines work.
func OpenUART(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	fail := func(what string, err error) (*os.File, error) {
		syscall.Close(fd)
		return nil, fmt.Errorf("%s %s: %w", what, path, err)
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fail("lock (another UART user is running)", err)
	}
	var t syscall.Termios
	if err := ioctl(fd, syscall.TCGETS, uintptr(unsafe.Pointer(&t))); err != nil {
		return fail("get termios", err)
	}
	t.Iflag, t.Oflag, t.Lflag = 0, 0, 0
	t.Cflag = syscall.CS8 | syscall.CREAD | syscall.CLOCAL | syscall.B115200
	t.Cc[syscall.VMIN], t.Cc[syscall.VTIME] = 1, 0
	if err := ioctl(fd, tcflsh, tciflush); err != nil {
		return fail("flush", err)
	}
	if err := ioctl(fd, syscall.TCSETS, uintptr(unsafe.Pointer(&t))); err != nil {
		return fail("set termios", err)
	}
	return os.NewFile(uintptr(fd), path), nil
}

// TCFLSH/TCIFLUSH have the generic Linux values on riscv64 and amd64; Go's syscall
// package defines them for riscv64 only, so they are spelled out for host builds.
const (
	tcflsh   = 0x540B
	tciflush = 0
)

func ioctl(fd int, req, arg uintptr) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, arg); e != 0 {
		return e
	}
	return nil
}

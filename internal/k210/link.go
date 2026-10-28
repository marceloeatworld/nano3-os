package k210

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"time"
)

// ErrTimeout means no complete frame arrived before the deadline.
var ErrTimeout = errors.New("k210: timeout")

// Link is a framed connection to the K210 over the UART.
type Link struct {
	f   *os.File
	sp  Splitter
	buf []byte
}

// NewLink wraps a UART opened with OpenUART.
func NewLink(f *os.File) *Link { return &Link{f: f, buf: make([]byte, 4096)} }

// Close releases the UART and its lock.
func (l *Link) Close() error { return l.f.Close() }

// Send writes one encoded frame.
func (l *Link) Send(frame []byte) error {
	_, err := l.f.Write(frame)
	return err
}

// Recv returns the next frame, or ErrTimeout. A frame with a bad CRC comes back
// together with ErrCRC.
func (l *Link) Recv(timeout time.Duration) (Frame, error) {
	deadline := time.Now().Add(timeout)
	for {
		if f, ok, err := l.sp.Next(); ok {
			return f, err
		}
		if err := l.f.SetReadDeadline(deadline); err != nil {
			return Frame{}, err
		}
		n, err := l.f.Read(l.buf)
		if n > 0 {
			l.sp.Push(l.buf[:n])
			continue
		}
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return Frame{}, ErrTimeout
		}
		if err != nil {
			return Frame{}, err
		}
	}
}

// Info is what the K210 reports in DETECT_ACK (analysis/protocol-control.md, 3.2).
type Info struct {
	HDNA     []byte // 16 bytes, echoed back in SYNC
	Version  string // K210 firmware version
	HWType   string
	SWType   string
	MaxLevel int
}

func cstr(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

func parseDetectAck(p []byte) (Info, error) {
	if len(p) < 0x46 {
		return Info{}, fmt.Errorf("k210: DETECT_ACK too short (%d bytes)", len(p))
	}
	info := Info{HDNA: append([]byte(nil), p[4:20]...), Version: cstr(p[20:52])}
	rest := p[0x45:]
	hw, rest, _ := bytes.Cut(rest, []byte{0})
	sw, rest, _ := bytes.Cut(rest, []byte{0})
	info.HWType, info.SWType = string(hw), string(sw)
	if len(rest) > 0 {
		info.MaxLevel = int(rest[0])
	}
	return info, nil
}

// request sends a frame and waits for a reply of type want, the way btcminer's
// synchronous transfer does: 200 ms per try, up to 5 sends. Stale or corrupt frames
// are skipped; when 200 ms pass without the wanted reply the request is sent again.
func (l *Link) request(frame []byte, want byte) (Frame, error) {
	for try := 0; try < 5; try++ {
		if err := l.Send(frame); err != nil {
			return Frame{}, err
		}
		deadline := time.Now().Add(200 * time.Millisecond)
	wait:
		for {
			f, err := l.Recv(time.Until(deadline))
			switch {
			case err == nil && f.Type == want:
				return f, nil
			case err == ErrTimeout:
				break wait
			case err != nil && err != ErrCRC:
				return Frame{}, err
			}
		}
	}
	return Frame{}, fmt.Errorf("k210: no reply 0x%02x to 0x%02x after 5 tries", want, frame[4])
}

// BringUp runs btcminer's start sequence, captured live (captures/cap4-bringup):
// DETECT -> DETECT_ACK, SYNC (HDNA echo + 1) -> SYNC_ACK, SET_WORKLEVEL, SET_FINISH.
// A SYNC failure is not fatal, as in btcminer.
func (l *Link) BringUp(level int) (Info, error) {
	f, err := l.request(Encode(TypeDetect, 0, 1, make([]byte, 4)), TypeDetectAck)
	if err != nil {
		return Info{}, err
	}
	info, err := parseDetectAck(f.Payload)
	if err != nil {
		return Info{}, err
	}
	// max_level counts the levels (3 live, for levels 0..2); btcminer clamps below it.
	if level < 0 || level >= info.MaxLevel {
		return info, fmt.Errorf("k210: work level %d outside 0..%d", level, info.MaxLevel-1)
	}
	_, _ = l.request(Encode(TypeSync, 0, 1, append(append([]byte(nil), info.HDNA...), 1)), TypeSyncAck)
	return info, l.SetWorkLevel(level)
}

// SetWorkLevel sends SET_WORKLEVEL then SET_FINISH, the pair btcminer sends at the
// end of its bring-up. The MM reports the level in effect in 0x51 (payload 0x30).
func (l *Link) SetWorkLevel(level int) error {
	lv := make([]byte, 4)
	binary.LittleEndian.PutUint32(lv, uint32(level))
	if err := l.Send(Encode(TypeSetWorkLevel, 0, 1, lv)); err != nil {
		return err
	}
	return l.Send(Encode(TypeSetFinish, 0, 1, nil))
}

// PollFrame is the 0x33 poll. With reset set it carries 00 00 00 01, which asks the
// MM to reset (btcminer sends it after a status of 4 or 7).
func PollFrame(reset bool) []byte {
	p := make([]byte, 4)
	if reset {
		p[3] = 1
	}
	return Encode(TypePoll, 0, 1, p)
}

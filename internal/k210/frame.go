// Package k210 speaks the K230 <-> K210 ("MM") UART protocol of the Avalon Nano 3:
// "CN" frames protected by a CRC-16/XMODEM. Field layouts are in analysis/PROTOCOL.md.
package k210

import (
	"bytes"
	"encoding/binary"
	"errors"
)

// Frame layout: "CN", CRC16 (LE, over bytes 4..end), type, opt, idx (LE), cnt (LE), len (LE), payload.
const (
	HeaderLen  = 12
	MaxPayload = 0x80
)

// Frame types used by nano3d (analysis/PROTOCOL.md, section 2).
const (
	TypeDetect       = 0x10
	TypeDetectAck    = 0x11
	TypeSync         = 0x12
	TypeSyncAck      = 0x13
	TypeJobParams    = 0x20
	TypeJobID        = 0x21
	TypeCoinbase     = 0x22
	TypeMerkle       = 0x23
	TypeHeader       = 0x24
	TypeTarget       = 0x25
	TypeJobFinish    = 0x26
	TypeVersions     = 0x27
	TypeSetFinish    = 0x31
	TypePoll         = 0x33
	TypeSetWorkLevel = 0x41
	TypeNonce        = 0x50
	TypeStatus       = 0x51
	TypePVT          = 0x54 // per-ASIC temperature, voltage, raw
)

// ErrCRC marks a frame whose CRC does not match; its fields must not be trusted.
var ErrCRC = errors.New("k210: frame CRC mismatch")

// CRC16 is CRC-16/XMODEM: poly 0x1021, init 0, no reflection, no final xor.
func CRC16(b []byte) uint16 {
	var c uint16
	for _, x := range b {
		c ^= uint16(x) << 8
		for i := 0; i < 8; i++ {
			if c&0x8000 != 0 {
				c = c<<1 ^ 0x1021
			} else {
				c <<= 1
			}
		}
	}
	return c
}

// Frame is one received frame.
type Frame struct {
	Type     byte
	Idx, Cnt uint16
	Payload  []byte
}

// Encode returns the wire bytes of a frame.
func Encode(typ byte, idx, cnt uint16, payload []byte) []byte {
	f := make([]byte, HeaderLen+len(payload))
	f[0], f[1] = 'C', 'N'
	f[4] = typ
	binary.LittleEndian.PutUint16(f[6:], idx)
	binary.LittleEndian.PutUint16(f[8:], cnt)
	binary.LittleEndian.PutUint16(f[10:], uint16(len(payload)))
	copy(f[HeaderLen:], payload)
	binary.LittleEndian.PutUint16(f[2:], CRC16(f[4:]))
	return f
}

// Splitter cuts a received byte stream into frames. The UART delivers frames in
// pieces of 9..12 bytes, so bytes are pushed as they come and frames pulled out.
type Splitter struct{ buf []byte }

// Push appends received bytes.
func (s *Splitter) Push(b []byte) { s.buf = append(s.buf, b...) }

var magic = []byte("CN")

// Next returns the next complete frame. ok is false when more bytes are needed.
// A complete frame with a bad CRC is returned with err == ErrCRC.
func (s *Splitter) Next() (f Frame, ok bool, err error) {
	for {
		i := bytes.Index(s.buf, magic)
		if i < 0 {
			if n := len(s.buf); n > 0 {
				s.buf = append(s.buf[:0], s.buf[n-1]) // the last byte may be a 'C'
			}
			return Frame{}, false, nil
		}
		s.buf = s.buf[i:]
		if len(s.buf) < HeaderLen {
			return Frame{}, false, nil
		}
		n := int(binary.LittleEndian.Uint16(s.buf[10:]))
		if n > MaxPayload { // a false "CN": skip it and resync
			s.buf = s.buf[2:]
			continue
		}
		if len(s.buf) < HeaderLen+n {
			return Frame{}, false, nil
		}
		raw := s.buf[:HeaderLen+n]
		f = Frame{
			Type:    raw[4],
			Idx:     binary.LittleEndian.Uint16(raw[6:]),
			Cnt:     binary.LittleEndian.Uint16(raw[8:]),
			Payload: append([]byte(nil), raw[HeaderLen:]...),
		}
		good := binary.LittleEndian.Uint16(raw[2:]) == CRC16(raw[4:])
		s.buf = s.buf[HeaderLen+n:]
		if !good {
			return f, true, ErrCRC
		}
		return f, true, nil
	}
}

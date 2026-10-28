package k210

import (
	"bytes"
	"testing"
)

func TestCRC16CheckValue(t *testing.T) {
	// CRC-16/XMODEM check value from the CRC catalogue.
	if got := CRC16([]byte("123456789")); got != 0x31C3 {
		t.Fatalf("CRC16 = %04x, want 31c3", got)
	}
}

func TestPollFrameMatchesSpec(t *testing.T) {
	// analysis/PROTOCOL.md: 43 4E cc cc 33 00 00 00 01 00 04 00 | 00 00 00 00
	f := PollFrame(false)
	want := []byte{0x43, 0x4E, 0, 0, 0x33, 0, 0, 0, 1, 0, 4, 0, 0, 0, 0, 0}
	if !bytes.Equal(f[4:], want[4:]) || f[0] != 'C' || f[1] != 'N' {
		t.Fatalf("poll frame % x", f)
	}
	if r := PollFrame(true); !bytes.Equal(r[12:], []byte{0, 0, 0, 1}) {
		t.Fatalf("reset poll payload % x, want 00 00 00 01", r[12:])
	}
}

func TestSplitterReassemblesAndResyncs(t *testing.T) {
	a := Encode(TypeStatus, 0, 1, bytes.Repeat([]byte{0x51}, 52))
	b := Encode(TypeNonce, 0, 1, bytes.Repeat([]byte{0xAB}, 20))
	// junk, a false "CN" whose length field exceeds MaxPayload, then two frames
	// delivered in 9-byte pieces as the UART does
	stream := append([]byte{0x00, 0x43, 0x4E, 1, 2, 3, 4, 5, 6, 7, 0xFF, 0xFF}, a...)
	stream = append(stream, b...)
	var s Splitter
	var got []Frame
	for i := 0; i < len(stream); i += 9 {
		s.Push(stream[i:min(i+9, len(stream))])
		for {
			f, ok, err := s.Next()
			if !ok {
				break
			}
			if err != nil {
				t.Fatalf("unexpected %v", err)
			}
			got = append(got, f)
		}
	}
	if len(got) != 2 || got[0].Type != TypeStatus || got[1].Type != TypeNonce || len(got[1].Payload) != 20 {
		t.Fatalf("got %+v", got)
	}
}

func TestSplitterFlagsBadCRC(t *testing.T) {
	f := Encode(TypeStatus, 0, 1, []byte{1, 2, 3, 4})
	f[2] ^= 0xFF
	var s Splitter
	s.Push(f)
	if _, ok, err := s.Next(); !ok || err != ErrCRC {
		t.Fatalf("ok=%v err=%v, want a frame with ErrCRC", ok, err)
	}
}

func TestParseDetectAckWithoutTerminators(t *testing.T) {
	// a reply whose strings run to the end of the frame must not overrun
	p := append([]byte("BTC\x00"), bytes.Repeat([]byte{'H'}, 124)...)
	info, err := parseDetectAck(p)
	if err != nil || len(info.HDNA) != 16 || info.MaxLevel != 0 {
		t.Fatalf("info %+v err %v", info, err)
	}
	if _, err := parseDetectAck(p[:0x40]); err == nil {
		t.Fatal("short DETECT_ACK accepted")
	}
}

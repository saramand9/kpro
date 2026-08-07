package websocket

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func TestComputeAcceptKeyMatchesRFC6455Example(t *testing.T) {
	const key = "dGhlIHNhbXBsZSBub25jZQ=="
	const want = "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
	if got := computeAcceptKey(key); got != want {
		t.Fatalf("computeAcceptKey(%q) = %q, want %q", key, got, want)
	}
}

func TestReadFrameRejectsReservedOpcodes(t *testing.T) {
	for _, opcode := range []byte{0x3, 0x4, 0x7, 0xb, 0xf} {
		t.Run(string(rune(opcode)), func(t *testing.T) {
			frame := maskedClientFrame(0x80|opcode, []byte("x"))
			if _, err := readFrame(bytes.NewReader(frame), 64); !errors.Is(err, ErrBadFrame) {
				t.Fatalf("opcode %#x error = %v, want %v", opcode, err, ErrBadFrame)
			}
		})
	}
}

func TestReadFrameRejectsNonCanonicalLengths(t *testing.T) {
	tests := []struct {
		name  string
		frame []byte
	}{
		{
			name:  "16-bit encoding below 126",
			frame: []byte{0x80 | opBinary, 0x80 | 126, 0, 125},
		},
		{
			name: "64-bit encoding below 65536",
			frame: func() []byte {
				frame := []byte{0x80 | opBinary, 0x80 | 127}
				var length [8]byte
				binary.BigEndian.PutUint64(length[:], 65535)
				return append(frame, length[:]...)
			}(),
		},
		{
			name: "64-bit length high bit",
			frame: []byte{
				0x80 | opBinary, 0x80 | 127,
				0x80, 0, 0, 0, 0, 0, 0, 0,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := readFrame(bytes.NewReader(test.frame), 1<<20); !errors.Is(err, ErrBadFrame) {
				t.Fatalf("readFrame() error = %v, want %v", err, ErrBadFrame)
			}
		})
	}
}

func FuzzReadFrameConformance(f *testing.F) {
	f.Add(maskedClientFrame(0x80|opBinary, []byte("seed")))
	f.Add(maskedClientFrame(0x80|opPing, []byte("ping")))
	f.Add([]byte{0x82, 0x00})
	f.Add([]byte{0x83, 0x80, 0, 0, 0, 0})
	f.Fuzz(func(t *testing.T, wire []byte) {
		frame, err := readFrame(bytes.NewReader(wire), 4096)
		if err != nil {
			return
		}
		switch frame.opcode {
		case opContinuation, opText, opBinary, opClose, opPing, opPong:
		default:
			t.Fatalf("accepted reserved opcode %#x", frame.opcode)
		}
		if len(frame.payload) > 4096 {
			t.Fatalf("accepted payload length %d above limit", len(frame.payload))
		}
		if frame.opcode >= opClose && (!frame.fin || len(frame.payload) > 125) {
			t.Fatalf("accepted invalid control frame: %+v", frame)
		}
	})
}

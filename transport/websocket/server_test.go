package websocket

import (
	"bytes"
	"errors"
	"testing"
)

func TestReadFrameRejectsUnmaskedClientFrame(t *testing.T) {
	_, err := readFrame(bytes.NewReader([]byte{0x82, 0x00}), 64)
	if !errors.Is(err, ErrBadFrame) {
		t.Fatalf("err = %v, want %v", err, ErrBadFrame)
	}
}

func TestReadFrameAcceptsMaskedClientFrame(t *testing.T) {
	payload := []byte("hello")
	mask := []byte{0x01, 0x02, 0x03, 0x04}
	frame := []byte{0x82, 0x80 | byte(len(payload))}
	frame = append(frame, mask...)
	for i, b := range payload {
		frame = append(frame, b^mask[i%len(mask)])
	}

	got, err := readFrame(bytes.NewReader(frame), 64)
	if err != nil {
		t.Fatalf("read masked frame: %v", err)
	}
	if !got.fin || got.opcode != opBinary || !bytes.Equal(got.payload, payload) {
		t.Fatalf("unexpected frame: fin=%v opcode=%d payload=%q", got.fin, got.opcode, string(got.payload))
	}
}

func TestReadFrameRejectsPayloadOverMaxBeforeReadingMask(t *testing.T) {
	_, err := readFrame(bytes.NewReader([]byte{0x82, 0x80 | 2, 0x01, 0x02, 0x03, 0x04, 0x00, 0x00}), 1)
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err = %v, want %v", err, ErrFrameTooLarge)
	}
}

package legacy

import (
	"bytes"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	want := Message{
		Flags: 7,
		ID:    MsgEcho,
		Seq:   42,
		Data:  []byte("hello"),
	}
	got, err := readFrame(bytes.NewReader(packFrame(want)), 1024)
	if err != nil {
		t.Fatalf("readFrame() error = %v", err)
	}
	if got.Flags != want.Flags || got.ID != want.ID || got.Seq != want.Seq || string(got.Data) != string(want.Data) {
		t.Fatalf("round trip mismatch: got=%+v want=%+v", got, want)
	}
}

func TestFrameTooLarge(t *testing.T) {
	_, err := readFrame(bytes.NewReader(packFrame(Message{ID: MsgEcho, Data: []byte("toolarge")})), 3)
	if err != ErrFrameTooLarge {
		t.Fatalf("readFrame() error = %v, want %v", err, ErrFrameTooLarge)
	}
}

func TestBadMagic(t *testing.T) {
	frame := packFrame(Message{ID: MsgEcho})
	frame[0] = 0
	_, err := readFrame(bytes.NewReader(frame), 1024)
	if err != ErrBadMagic {
		t.Fatalf("readFrame() error = %v, want %v", err, ErrBadMagic)
	}
}

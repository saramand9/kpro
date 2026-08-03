package codec

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/saramand9/kpro/core"
)

func TestWriteKProV1HeaderMatchesEncodedFrame(t *testing.T) {
	message := core.Message{Flags: 3, ID: 17, Seq: 99, Data: []byte("payload")}
	encoded, err := KProV1().Encode(message)
	if err != nil {
		t.Fatal(err)
	}
	var header [FrameHeaderSize]byte
	if err := WriteKProV1Header(header[:], message); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(header[:], encoded[:FrameHeaderSize]) {
		t.Fatalf("header = %x, want %x", header, encoded[:FrameHeaderSize])
	}
	if !bytes.Equal(message.Data, encoded[FrameHeaderSize:]) {
		t.Fatal("payload changed while encoding segmented frame")
	}
}

func TestWriteKProV1HeaderRequiresFixedStorage(t *testing.T) {
	if err := WriteKProV1Header(make([]byte, FrameHeaderSize-1), core.Message{}); !errors.Is(err, io.ErrShortBuffer) {
		t.Fatalf("WriteKProV1Header() error = %v, want io.ErrShortBuffer", err)
	}
}

func TestKProV1CodecDoesNotImplicitlyOptIntoSegmentedFraming(t *testing.T) {
	if _, ok := any(KProV1()).(StreamFrameCodec); ok {
		t.Fatal("KProV1Codec unexpectedly satisfies StreamFrameCodec")
	}
}

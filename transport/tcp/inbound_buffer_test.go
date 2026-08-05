package tcp

import (
	"bytes"
	"testing"

	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
)

func TestReadFrameTransfersReusableBufferOwnership(t *testing.T) {
	server := New(Config{
		Codec:                  codec.KProV1(),
		MaxFrameSize:           1024,
		MaxReadBufferSize:      1024,
		MaxRetainedBufferBytes: 1024 * 1024,
	})
	wireCodec := codec.KProV1()
	firstWire, err := wireCodec.Encode(core.Message{ID: 1, Seq: 1, Data: bytes.Repeat([]byte{1}, 64)})
	if err != nil {
		t.Fatal(err)
	}
	secondWire, err := wireCodec.Encode(core.Message{ID: 1, Seq: 2, Data: bytes.Repeat([]byte{2}, 64)})
	if err != nil {
		t.Fatal(err)
	}
	wire := append(firstWire, secondWire...)
	session := &session{
		id:          1,
		server:      server,
		payloadPool: server.payloadPool.Shard(1),
		done:        make(chan struct{}),
		readBuffer:  newAdaptiveReader(bytes.NewReader(wire), 128, 1024),
	}

	first, firstBuffer, err := session.readFrame()
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if len(firstBuffer) != 64 || !bytes.Equal(first.Message.Data, bytes.Repeat([]byte{1}, 64)) {
		t.Fatalf("first frame = %+v, buffer length = %d", first.Message, len(firstBuffer))
	}
	firstAddress := &firstBuffer[:1][0]
	session.ReleaseTransportBuffer(firstBuffer)

	second, secondBuffer, err := session.readFrame()
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if len(secondBuffer) != 64 || !bytes.Equal(second.Message.Data, bytes.Repeat([]byte{2}, 64)) {
		t.Fatalf("second frame = %+v, buffer length = %d", second.Message, len(secondBuffer))
	}
	if &secondBuffer[:1][0] != firstAddress {
		t.Fatal("second frame did not reuse the released receive buffer")
	}
	session.ReleaseTransportBuffer(secondBuffer)
}

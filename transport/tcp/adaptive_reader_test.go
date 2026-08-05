package tcp

import (
	"bytes"
	"io"
	"testing"

	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
)

func TestAdaptiveReaderGrowsForCoalescedSmallReads(t *testing.T) {
	const (
		frameSize  = 20 + 1024
		frameCount = 16
	)
	wire := make([]byte, frameSize*frameCount*2)
	for index := range wire {
		wire[index] = byte(index)
	}
	reader := newAdaptiveReader(bytes.NewReader(wire), 4*1024, 32*1024)
	decoded := make([]byte, 0, len(wire))
	header := make([]byte, 20)
	payload := make([]byte, 1024)
	for range frameCount * 2 {
		if _, err := io.ReadFull(reader, header); err != nil {
			t.Fatal(err)
		}
		decoded = append(decoded, header...)
		if _, err := io.ReadFull(reader, payload); err != nil {
			t.Fatal(err)
		}
		decoded = append(decoded, payload...)
	}
	if !bytes.Equal(decoded, wire) {
		t.Fatal("adaptive reads changed the byte stream")
	}
	if got := len(reader.buffer); got != 32*1024 {
		t.Fatalf("buffer size = %d, want 32768 after sustained full refills", got)
	}
}

func TestAdaptiveReaderKeepsInitialSizeForPartialRefill(t *testing.T) {
	reader := newAdaptiveReader(bytes.NewReader(make([]byte, 1024)), 4*1024, 32*1024)
	buffer := make([]byte, 1024)
	if _, err := io.ReadFull(reader, buffer); err != nil {
		t.Fatal(err)
	}
	if got := len(reader.buffer); got != 4*1024 {
		t.Fatalf("buffer size = %d, want initial 4096", got)
	}
}

func TestAdaptiveReaderLargeReadBypassesBuffer(t *testing.T) {
	wire := make([]byte, 16*1024)
	reader := newAdaptiveReader(bytes.NewReader(wire), 4*1024, 32*1024)
	decoded := make([]byte, len(wire))
	if _, err := io.ReadFull(reader, decoded); err != nil {
		t.Fatal(err)
	}
	if got := len(reader.buffer); got != 4*1024 {
		t.Fatalf("buffer size after direct read = %d, want 4096", got)
	}
}

func TestAdaptiveReaderReturnsDataBeforeUnderlyingError(t *testing.T) {
	reader := newAdaptiveReader(dataAndErrorReader{}, 4, 8)
	buffer := make([]byte, 4)
	if n, err := reader.Read(buffer[:2]); n != 2 || err != nil || string(buffer[:2]) != "da" {
		t.Fatalf("first Read = (%d, %v, %q)", n, err, buffer[:2])
	}
	if n, err := reader.Read(buffer[:2]); n != 2 || err != nil || string(buffer[:2]) != "ta" {
		t.Fatalf("second Read = (%d, %v, %q)", n, err, buffer[:2])
	}
	if n, err := reader.Read(buffer); n != 0 || err != io.ErrUnexpectedEOF {
		t.Fatalf("terminal Read = (%d, %v), want UnexpectedEOF", n, err)
	}
}

func TestSessionReadsCompleteBufferedKProFramesWithoutAnotherStreamRead(t *testing.T) {
	wireCodec := codec.KProV1()
	first, err := wireCodec.Encode(core.Message{ID: 1, Seq: 1, Data: []byte("first")})
	if err != nil {
		t.Fatal(err)
	}
	second, err := wireCodec.Encode(core.Message{ID: 2, Seq: 2, Data: []byte("second")})
	if err != nil {
		t.Fatal(err)
	}
	server := New(Config{Codec: wireCodec, MaxFrameSize: 64})
	session := &session{server: server, done: make(chan struct{})}
	session.payloadPool = server.payloadPool.Shard(1)
	session.readBuffer = newAdaptiveReader(bytes.NewReader(append(first, second...)), 4096, 4096)

	result, owned, err := session.readFrame()
	if err != nil {
		t.Fatal(err)
	}
	if result.Message.ID != 1 || string(result.Message.Data) != "first" {
		t.Fatalf("first result = %+v", result)
	}
	session.ReleaseTransportBuffer(owned)

	result, owned, complete, err := session.readBufferedFrame()
	if err != nil || !complete {
		t.Fatalf("buffered frame = (complete %v, error %v)", complete, err)
	}
	if result.Message.ID != 2 || result.Message.Seq != 2 || string(result.Message.Data) != "second" {
		t.Fatalf("second result = %+v", result)
	}
	session.ReleaseTransportBuffer(owned)
	if buffered := session.readBuffer.bufferedBytes(); len(buffered) != 0 {
		t.Fatalf("buffered bytes after two frames = %d, want 0", len(buffered))
	}
}

func BenchmarkKProV1DecodeCoalesced16x64(b *testing.B) {
	const (
		frameCount  = 16
		payloadSize = 64
	)
	wireCodec := codec.KProV1()
	wire := make([]byte, 0, frameCount*(codec.FrameHeaderSize+payloadSize))
	for sequence := range frameCount {
		frame, err := wireCodec.Encode(core.Message{
			ID:   100,
			Seq:  uint64(sequence),
			Data: make([]byte, payloadSize),
		})
		if err != nil {
			b.Fatal(err)
		}
		wire = append(wire, frame...)
	}

	b.Run("stream_read_into", func(b *testing.B) {
		source := bytes.NewReader(nil)
		reader := newAdaptiveReader(source, 4096, 4096)
		scratch := make([]byte, 0, payloadSize)
		var sequence uint64
		b.ReportAllocs()
		b.SetBytes(frameCount * payloadSize)
		for range b.N {
			source.Reset(wire)
			resetAdaptiveReaderForBenchmark(reader)
			for range frameCount {
				result, nextScratch, err := wireCodec.ReadInto(reader, payloadSize, scratch)
				if err != nil {
					b.Fatal(err)
				}
				scratch = nextScratch
				sequence = result.Message.Seq
			}
		}
		if sequence != frameCount-1 {
			b.Fatalf("last sequence = %d", sequence)
		}
	})

	b.Run("buffered_after_first", func(b *testing.B) {
		source := bytes.NewReader(nil)
		reader := newAdaptiveReader(source, 4096, 4096)
		scratch := make([]byte, payloadSize)
		var sequence uint64
		b.ReportAllocs()
		b.SetBytes(frameCount * payloadSize)
		for range b.N {
			source.Reset(wire)
			resetAdaptiveReaderForBenchmark(reader)
			result, nextScratch, err := wireCodec.ReadInto(reader, payloadSize, scratch[:0])
			if err != nil {
				b.Fatal(err)
			}
			scratch = nextScratch[:payloadSize]
			sequence = result.Message.Seq
			for range frameCount - 1 {
				bufferedResult, complete, decodeErr := wireCodec.DecodeBuffered(reader.bufferedBytes(), payloadSize)
				if decodeErr != nil || !complete {
					b.Fatalf("DecodeBuffered = (complete %v, error %v)", complete, decodeErr)
				}
				copy(scratch, bufferedResult.Message.Data)
				reader.discardBuffered(bufferedResult.Bytes)
				sequence = bufferedResult.Message.Seq
			}
		}
		if sequence != frameCount-1 {
			b.Fatalf("last sequence = %d", sequence)
		}
	})
}

func resetAdaptiveReaderForBenchmark(reader *adaptiveReader) {
	reader.start = 0
	reader.end = 0
	reader.readErr = nil
}

type dataAndErrorReader struct{}

func (dataAndErrorReader) Read(buffer []byte) (int, error) {
	return copy(buffer, "data"), io.ErrUnexpectedEOF
}

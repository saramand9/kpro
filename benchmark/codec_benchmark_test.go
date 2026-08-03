// File: benchmark/codec_benchmark_test.go
// Purpose: Provides deterministic, in-memory codec performance evidence.

package benchmark

import (
	"bytes"
	"testing"

	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
)

var (
	codecBenchmarkBytes   []byte
	codecBenchmarkMessage core.Message
	codecBenchmarkHeader  [codec.FrameHeaderSize]byte
	codecBenchmarkPayload []byte
)

func BenchmarkKProCodec(b *testing.B) {
	payload := bytes.Repeat([]byte{0xa5}, 1024)
	message := core.Message{
		Flags: 0xa5,
		ID:    42,
		Seq:   99,
		Data:  payload,
	}
	wireCodec := codec.KProV1()
	packet, err := wireCodec.EncodePacket(message)
	if err != nil {
		b.Fatalf("prepare packet: %v", err)
	}

	b.Run("encode_1KiB", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(payload)))
		var encoded []byte
		var encodeErr error
		for i := 0; i < b.N; i++ {
			encoded, encodeErr = wireCodec.Encode(message)
			if encodeErr != nil {
				b.Fatalf("encode: %v", encodeErr)
			}
		}
		codecBenchmarkBytes = encoded
	})

	b.Run("segmented_copy_1KiB", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(payload)))
		copiedPayload := make([]byte, len(payload))
		var encodeErr error
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			encodeErr = codec.WriteKProV1Header(codecBenchmarkHeader[:], message)
			if encodeErr != nil {
				b.Fatalf("write header: %v", encodeErr)
			}
			copy(copiedPayload, message.Data)
		}
		codecBenchmarkBytes = codecBenchmarkHeader[:]
		codecBenchmarkPayload = copiedPayload
	})

	b.Run("decode_packet_1KiB", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(payload)))
		var decoded core.Message
		var decodeErr error
		for i := 0; i < b.N; i++ {
			decoded, decodeErr = wireCodec.DecodePacket(packet)
			if decodeErr != nil {
				b.Fatalf("decode packet: %v", decodeErr)
			}
		}
		codecBenchmarkMessage = decoded
	})
}

package tcp

import (
	"fmt"

	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
)

func (s *session) encodeOutboundFrame(msg core.Message) (outboundFrame, error) {
	if err := validateSegmentedPayloadSize(s.server.config.Codec, len(msg.Data)); err != nil {
		return outboundFrame{}, err
	}
	if _, ok := segmentedHeaderSize(s.server.config.Codec); !ok {
		encoded, err := s.server.config.Codec.Encode(msg)
		return outboundFrame{payload: encoded}, err
	}
	pool := s.outboundPayloadPool()
	payload := pool.GetUninitialized(len(msg.Data))
	frameOwnsPayload := false
	defer func() {
		if !frameOwnsPayload {
			pool.Put(payload)
		}
	}()
	copy(payload, msg.Data)
	msg.Data = payload
	frame, err := s.segmentedFrame(msg, payload)
	if err != nil {
		return outboundFrame{}, err
	}
	frameOwnsPayload = true
	return frame, nil
}

func validateSegmentedPayloadSize(streamCodec codec.StreamCodec, size int) error {
	if isExactKProV1Codec(streamCodec) && uint64(size) > uint64(^uint32(0)) {
		return codec.ErrFrameTooLarge
	}
	return nil
}

func (s *session) segmentedFrame(msg core.Message, payload []byte) (outboundFrame, error) {
	if isExactKProV1Codec(s.server.config.Codec) {
		return s.kproV1Frame(msg, payload)
	}
	framing, ok := s.server.config.Codec.(codec.StreamFrameCodec)
	if !ok {
		return outboundFrame{}, core.ErrOutboundBufferUnsupported
	}
	headerSize := framing.HeaderSize()
	if headerSize <= 0 || headerSize > maxInlineFrameHeader {
		return outboundFrame{}, core.ErrOutboundBufferUnsupported
	}
	return s.customSegmentedFrame(framing, headerSize, msg, payload)
}

func segmentedHeaderSize(streamCodec codec.StreamCodec) (int, bool) {
	if isExactKProV1Codec(streamCodec) {
		return codec.FrameHeaderSize, true
	}
	framing, ok := streamCodec.(codec.StreamFrameCodec)
	if !ok {
		return 0, false
	}
	headerSize := framing.HeaderSize()
	return headerSize, headerSize > 0 && headerSize <= maxInlineFrameHeader
}

func isExactKProV1Codec(streamCodec codec.StreamCodec) bool {
	switch streamCodec.(type) {
	case codec.KProV1Codec, *codec.KProV1Codec:
		return true
	default:
		return false
	}
}

func (s *session) kproV1Frame(msg core.Message, payload []byte) (outboundFrame, error) {
	msg.Data = payload
	frame := outboundFrame{payload: payload, pool: s.outboundPayloadPool()}
	if err := codec.WriteKProV1Header(frame.header[:], msg); err != nil {
		return outboundFrame{}, err
	}
	frame.headerLen = codec.FrameHeaderSize
	return frame, nil
}

func (s *session) customSegmentedFrame(
	framing codec.StreamFrameCodec,
	headerSize int,
	msg core.Message,
	payload []byte,
) (outboundFrame, error) {
	msg.Data = payload[:len(payload):len(payload)]
	frame := outboundFrame{payload: payload, pool: s.outboundPayloadPool()}
	header, err := framing.AppendHeader(frame.header[:0], msg)
	if err != nil {
		return outboundFrame{}, err
	}
	if len(header) != headerSize {
		return outboundFrame{}, fmt.Errorf(
			"tcp: codec %q appended %d-byte header, want %d",
			framing.Name(), len(header), headerSize,
		)
	}
	copy(frame.header[:], header)
	frame.headerLen = uint8(len(header))
	return frame, nil
}

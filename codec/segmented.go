package codec

import "github.com/saramand9/kpro/core"

// StreamFrameCodec is an optional StreamCodec fast path for protocols whose
// wire frame is an encoded header followed by the unchanged message payload.
// Transports can gather-write the returned header and msg.Data without first
// concatenating them. HeaderSize must be positive and stable. AppendHeader
// must return the complete appended header with exactly HeaderSize bytes and
// must not mutate msg.Data. Existing StreamCodec implementations remain
// compatible.
type StreamFrameCodec interface {
	StreamCodec
	HeaderSize() int
	AppendHeader(dst []byte, msg core.Message) ([]byte, error)
}

// File: codec/kpro.go
// Purpose: Defines or tests KPro wire codec encoding and decoding.

package codec

import (
	"encoding/binary"
	"io"

	"github.com/saramand9/bignet/kpro/core"
)

const (
	FrameMagic      uint16 = 0x4b50
	FrameVersion    uint8  = 1
	FrameHeaderSize        = 20
)

// KProV1Codec implements the default KPro binary frame format.
type KProV1Codec struct{}

// KProV1 returns the default KPro version-1 protocol implementation.
func KProV1() KProV1Codec {
	return KProV1Codec{}
}

// Name returns the protocol identifier.
func (KProV1Codec) Name() string {
	return "kpro.v1"
}

// Read decodes one framed message from a stream reader.
func (KProV1Codec) Read(r io.Reader, maxBodySize uint32) (ReadResult, error) {
	var header [FrameHeaderSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return ReadResult{}, err
	}
	msg, length, err := decodeHeader(header[:])
	if err != nil {
		return ReadResult{}, err
	}
	if length > maxBodySize {
		return ReadResult{}, ErrFrameTooLarge
	}
	if length > 0 {
		msg.Data = make([]byte, int(length))
		if _, err := io.ReadFull(r, msg.Data); err != nil {
			return ReadResult{}, err
		}
	}
	return ReadResult{
		Message: msg,
		Bytes:   FrameHeaderSize + int(length),
	}, nil
}

// Encode serializes one message into a framed byte slice.
func (KProV1Codec) Encode(msg core.Message) ([]byte, error) {
	if uint64(len(msg.Data)) > uint64(^uint32(0)) {
		return nil, ErrFrameTooLarge
	}
	size := FrameHeaderSize + len(msg.Data)
	buf := make([]byte, size)
	binary.BigEndian.PutUint16(buf[0:2], FrameMagic)
	buf[2] = FrameVersion
	buf[3] = msg.Flags
	binary.BigEndian.PutUint32(buf[4:8], msg.ID)
	binary.BigEndian.PutUint64(buf[8:16], msg.Seq)
	binary.BigEndian.PutUint32(buf[16:20], uint32(len(msg.Data)))
	copy(buf[FrameHeaderSize:], msg.Data)
	return buf, nil
}

// DecodePacket decodes one complete packet payload.
func (k KProV1Codec) DecodePacket(data []byte) (core.Message, error) {
	if len(data) < FrameHeaderSize {
		if len(data) == 0 {
			return core.Message{}, io.EOF
		}
		return core.Message{}, io.ErrUnexpectedEOF
	}
	msg, length, err := decodeHeader(data[:FrameHeaderSize])
	if err != nil {
		return core.Message{}, err
	}
	if uint64(length) > uint64(len(data)) {
		return core.Message{}, ErrFrameTooLarge
	}
	available := len(data) - FrameHeaderSize
	if uint64(length) > uint64(available) {
		if available == 0 {
			return core.Message{}, io.EOF
		}
		return core.Message{}, io.ErrUnexpectedEOF
	}
	end := FrameHeaderSize + int(length)
	if end != len(data) {
		return core.Message{}, ErrTrailingData
	}
	if length > 0 {
		msg.Data = make([]byte, int(length))
		copy(msg.Data, data[FrameHeaderSize:end])
	}
	return msg, nil
}

// EncodePacket serializes one message for packet transports.
func (k KProV1Codec) EncodePacket(msg core.Message) ([]byte, error) {
	return k.Encode(msg)
}

func decodeHeader(header []byte) (core.Message, uint32, error) {
	if binary.BigEndian.Uint16(header[0:2]) != FrameMagic {
		return core.Message{}, 0, ErrBadMagic
	}
	if header[2] != FrameVersion {
		return core.Message{}, 0, ErrBadVersion
	}
	msg := core.Message{
		Flags: header[3],
		ID:    binary.BigEndian.Uint32(header[4:8]),
		Seq:   binary.BigEndian.Uint64(header[8:16]),
	}
	return msg, binary.BigEndian.Uint32(header[16:20]), nil
}

package protocol

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

type KProV1Protocol struct{}

func KProV1() KProV1Protocol {
	return KProV1Protocol{}
}

func (KProV1Protocol) Name() string {
	return "kpro.v1"
}

func (KProV1Protocol) Read(r io.Reader, maxBodySize uint32) (ReadResult, error) {
	var header [FrameHeaderSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return ReadResult{}, err
	}
	if binary.BigEndian.Uint16(header[0:2]) != FrameMagic {
		return ReadResult{}, ErrBadMagic
	}
	if header[2] != FrameVersion {
		return ReadResult{}, ErrBadVersion
	}
	length := binary.BigEndian.Uint32(header[16:20])
	if length > maxBodySize {
		return ReadResult{}, ErrFrameTooLarge
	}
	msg := core.Message{
		Flags: header[3],
		ID:    binary.BigEndian.Uint32(header[4:8]),
		Seq:   binary.BigEndian.Uint64(header[8:16]),
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

func (KProV1Protocol) Encode(msg core.Message) ([]byte, error) {
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

func (k KProV1Protocol) DecodePacket(data []byte) (core.Message, error) {
	result, err := k.Read(newBytesReader(data), uint32(len(data)))
	return result.Message, err
}

func (k KProV1Protocol) EncodePacket(msg core.Message) ([]byte, error) {
	return k.Encode(msg)
}

type bytesReader []byte

func (r *bytesReader) Read(p []byte) (int, error) {
	if len(*r) == 0 {
		return 0, io.EOF
	}
	n := copy(p, *r)
	*r = (*r)[n:]
	return n, nil
}

func newBytesReader(data []byte) *bytesReader {
	r := bytesReader(data)
	return &r
}

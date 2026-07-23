// File: codec/kpro_test.go
// Purpose: Defines or tests KPro wire codec encoding and decoding.

package codec

import (
	"errors"
	"testing"

	"github.com/saramand9/bignet/kpro/core"
)

func TestKProV1DecodePacketCopiesPayload(t *testing.T) {
	packet, err := KProV1().EncodePacket(core.Message{
		ID:   testMsgEcho,
		Seq:  42,
		Data: []byte("payload"),
	})
	if err != nil {
		t.Fatalf("encode packet: %v", err)
	}

	got, err := KProV1().DecodePacket(packet)
	if err != nil {
		t.Fatalf("decode packet: %v", err)
	}
	packet[FrameHeaderSize] = 'X'

	if string(got.Data) != "payload" {
		t.Fatalf("decoded payload aliased packet: %q", got.Data)
	}
}

func TestKProV1DecodePacketRejectsTrailingData(t *testing.T) {
	packet, err := KProV1().EncodePacket(core.Message{
		ID:   testMsgEcho,
		Seq:  42,
		Data: []byte("payload"),
	})
	if err != nil {
		t.Fatalf("encode packet: %v", err)
	}
	packet = append(packet, 0)

	_, err = KProV1().DecodePacket(packet)
	if !errors.Is(err, ErrTrailingData) {
		t.Fatalf("decode packet error = %v, want %v", err, ErrTrailingData)
	}
}

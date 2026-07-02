// File: protocol/kpro_test.go
// Purpose: Defines or tests KPro wire protocol encoding and decoding.

package protocol

import (
	"testing"

	"github.com/saramand9/bignet/kpro/core"
)

func TestKProV1DecodePacketCopiesPayload(t *testing.T) {
	packet, err := KProV1().EncodePacket(core.Message{
		ID:   core.MsgEcho,
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

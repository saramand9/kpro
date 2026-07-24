// File: codec/kpro_test.go
// Purpose: Defines or tests KPro wire codec encoding and decoding.

package codec

import (
	"bytes"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"

	"github.com/saramand9/kpro/core"
)

func TestKProV1GoldenFrame(t *testing.T) {
	want, err := hex.DecodeString("4b5001a501020304010203040506070800000003616263")
	if err != nil {
		t.Fatal(err)
	}
	wantMessage := core.Message{
		Flags: 0xa5,
		ID:    0x01020304,
		Seq:   0x0102030405060708,
		Data:  []byte("abc"),
	}
	got, err := KProV1().EncodePacket(wantMessage)
	if err != nil {
		t.Fatalf("EncodePacket: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("golden frame changed:\n got %x\nwant %x", got, want)
	}
	decoded, err := KProV1().DecodePacket(want)
	if err != nil {
		t.Fatalf("DecodePacket golden frame: %v", err)
	}
	if !reflect.DeepEqual(decoded, wantMessage) {
		t.Fatalf("decoded = %#v, want %#v", decoded, wantMessage)
	}
}

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

func FuzzKProV1DecodePacket(f *testing.F) {
	valid, err := KProV1().EncodePacket(core.Message{ID: 7, Seq: 9, Data: []byte("seed")})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add([]byte{})
	f.Add([]byte{0x4b, 0x50, 0x01})
	f.Fuzz(func(t *testing.T, packet []byte) {
		message, err := KProV1().DecodePacket(packet)
		if err != nil {
			return
		}
		encoded, err := KProV1().EncodePacket(message)
		if err != nil {
			t.Fatalf("re-encode decoded message: %v", err)
		}
		if !bytes.Equal(encoded, packet) {
			t.Fatalf("successful decode was not canonical: got %x want %x", encoded, packet)
		}
	})
}

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

func TestKProV1ReadIntoReusesScratch(t *testing.T) {
	codec := KProV1()
	first, err := codec.Encode(core.Message{ID: 7, Data: []byte("first")})
	if err != nil {
		t.Fatal(err)
	}
	second, err := codec.Encode(core.Message{ID: 8, Data: []byte("second")})
	if err != nil {
		t.Fatal(err)
	}
	reader := bytes.NewReader(append(first, second...))
	result, scratch, err := codec.ReadInto(reader, 64, make([]byte, 0, 16))
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Message.Data) != "first" {
		t.Fatalf("first payload = %q", result.Message.Data)
	}
	result, nextScratch, err := codec.ReadInto(reader, 64, scratch)
	if err != nil {
		t.Fatal(err)
	}
	if &result.Message.Data[0] != &scratch[0] || &nextScratch[0] != &scratch[0] {
		t.Fatal("ReadInto did not reuse scratch backing storage")
	}
	if string(result.Message.Data) != "second" {
		t.Fatalf("second payload = %q", result.Message.Data)
	}
}

func TestKProV1ReadIntoDoesNotAllocateWithSizedScratch(t *testing.T) {
	wireCodec := KProV1()
	frame, err := wireCodec.Encode(core.Message{ID: 7, Seq: 9, Data: bytes.Repeat([]byte{1}, 64)})
	if err != nil {
		t.Fatal(err)
	}
	reader := bytes.NewReader(frame)
	scratch := make([]byte, 0, 64)
	var messageID uint32

	allocations := testing.AllocsPerRun(1000, func() {
		reader.Reset(frame)
		result, nextScratch, readErr := wireCodec.ReadInto(reader, 64, scratch)
		if readErr != nil {
			panic(readErr)
		}
		scratch = nextScratch
		messageID = result.Message.ID
	})
	if allocations != 0 {
		t.Fatalf("ReadInto allocations = %f, want 0", allocations)
	}
	if messageID != 7 {
		t.Fatalf("message ID = %d, want 7", messageID)
	}
}

func TestKProV1DecodeBufferedWaitsForCompleteFrameAndAliasesPayload(t *testing.T) {
	wireCodec := KProV1()
	frame, err := wireCodec.Encode(core.Message{ID: 7, Seq: 9, Data: []byte("payload")})
	if err != nil {
		t.Fatal(err)
	}
	if _, complete, err := wireCodec.DecodeBuffered(frame[:FrameHeaderSize-1], 64); err != nil || complete {
		t.Fatalf("partial header = (complete %v, error %v), want (false, nil)", complete, err)
	}
	if _, complete, err := wireCodec.DecodeBuffered(frame[:len(frame)-1], 64); err != nil || complete {
		t.Fatalf("partial payload = (complete %v, error %v), want (false, nil)", complete, err)
	}
	result, complete, err := wireCodec.DecodeBuffered(frame, 64)
	if err != nil || !complete {
		t.Fatalf("complete frame = (complete %v, error %v), want (true, nil)", complete, err)
	}
	if result.Bytes != len(frame) || result.Message.ID != 7 || result.Message.Seq != 9 {
		t.Fatalf("result = %+v, want complete message", result)
	}
	frame[FrameHeaderSize] = 'P'
	if string(result.Message.Data) != "Payload" {
		t.Fatalf("buffered payload = %q, want input alias", result.Message.Data)
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

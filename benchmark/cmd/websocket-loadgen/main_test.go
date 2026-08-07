package main

import (
	"encoding/binary"
	"testing"
)

func TestEncodeClientFrameProducesValidMaskedKProPacket(t *testing.T) {
	const (
		payloadSize = 1024
		sequence    = uint64(42)
	)
	packetSize := headerSize + payloadSize
	frame := make([]byte, clientFrameSize(packetSize))
	maskState := uint64(1)
	written := encodeClientFrame(frame, sequence, payloadSize, &maskState)
	if written != len(frame) {
		t.Fatalf("written = %d, want %d", written, len(frame))
	}
	if frame[0] != 0x82 || frame[1] != 0x80|126 {
		t.Fatalf("frame header = %x %x", frame[0], frame[1])
	}
	if got := int(binary.BigEndian.Uint16(frame[2:4])); got != packetSize {
		t.Fatalf("packet length = %d, want %d", got, packetSize)
	}

	mask := frame[4:8]
	packet := append([]byte(nil), frame[8:]...)
	for index := range packet {
		packet[index] ^= mask[index&3]
	}
	if err := validateResponsePacket(packet, sequence, payloadSize); err != nil {
		t.Fatalf("validate unmasked packet: %v", err)
	}
}

func TestValidateResponsePacketRejectsCorruption(t *testing.T) {
	const payloadSize = 64
	packet := make([]byte, headerSize+payloadSize)
	binary.BigEndian.PutUint16(packet[0:2], 0x4b50)
	packet[2] = 1
	binary.BigEndian.PutUint32(packet[4:8], echoMessageID)
	binary.BigEndian.PutUint64(packet[8:16], 7)
	binary.BigEndian.PutUint32(packet[16:20], payloadSize)
	for index := 0; index < payloadSize; index++ {
		packet[headerSize+index] = expectedPayloadByte(7, index)
	}
	packet[len(packet)-1] ^= 0xff
	if err := validateResponsePacket(packet, 7, payloadSize); err == nil {
		t.Fatal("validateResponsePacket accepted corrupted payload")
	}
}

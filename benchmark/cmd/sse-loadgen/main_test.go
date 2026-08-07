package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"testing"
)

func TestReadAndValidateEventValidatesBeforeReaderRefill(t *testing.T) {
	payload := benchmarkSSEPayload(16*1024, 3, 9)
	event := append([]byte("event: message\ndata: "), payload...)
	event = append(event, '\n')
	reader := bufio.NewReaderSize(&splitReader{
		parts: [][]byte{event, []byte("\n")},
	}, 128*1024)

	validationFailure, err := readAndValidateEvent(reader, len(payload), 3, 9)
	if err != nil || validationFailure {
		t.Fatalf("readAndValidateEvent() = validation %v, error %v", validationFailure, err)
	}
}

func TestReadAndValidateEventRejectsSequenceGap(t *testing.T) {
	payload := benchmarkSSEPayload(64, 1, 3)
	event := append([]byte("event: message\ndata: "), payload...)
	event = append(event, '\n', '\n')

	validationFailure, err := readAndValidateEvent(bufio.NewReader(bytes.NewReader(event)), len(payload), 1, 2)
	if err == nil || !validationFailure {
		t.Fatalf("readAndValidateEvent() = validation %v, error %v", validationFailure, err)
	}
}

func benchmarkSSEPayload(size, streamID int, sequence uint64) []byte {
	payload := make([]byte, size)
	copy(payload[:16], fmt.Sprintf("%016x", sequence))
	for index := 16; index < size; index++ {
		payload[index] = byte('a' + (index+streamID)%26)
	}
	return payload
}

type splitReader struct {
	parts [][]byte
}

func (r *splitReader) Read(dst []byte) (int, error) {
	for len(r.parts) > 0 && len(r.parts[0]) == 0 {
		r.parts = r.parts[1:]
	}
	if len(r.parts) == 0 {
		return 0, io.EOF
	}
	written := copy(dst, r.parts[0])
	r.parts[0] = r.parts[0][written:]
	return written, nil
}

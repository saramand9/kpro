package tcp

import (
	"bytes"
	"io"
	"testing"
)

func TestAdaptiveReaderGrowsForCoalescedSmallReads(t *testing.T) {
	const (
		frameSize  = 20 + 1024
		frameCount = 16
	)
	wire := make([]byte, frameSize*frameCount*2)
	for index := range wire {
		wire[index] = byte(index)
	}
	reader := newAdaptiveReader(bytes.NewReader(wire), 4*1024, 32*1024)
	decoded := make([]byte, 0, len(wire))
	header := make([]byte, 20)
	payload := make([]byte, 1024)
	for range frameCount * 2 {
		if _, err := io.ReadFull(reader, header); err != nil {
			t.Fatal(err)
		}
		decoded = append(decoded, header...)
		if _, err := io.ReadFull(reader, payload); err != nil {
			t.Fatal(err)
		}
		decoded = append(decoded, payload...)
	}
	if !bytes.Equal(decoded, wire) {
		t.Fatal("adaptive reads changed the byte stream")
	}
	if got := len(reader.buffer); got != 32*1024 {
		t.Fatalf("buffer size = %d, want 32768 after sustained full refills", got)
	}
}

func TestAdaptiveReaderKeepsInitialSizeForPartialRefill(t *testing.T) {
	reader := newAdaptiveReader(bytes.NewReader(make([]byte, 1024)), 4*1024, 32*1024)
	buffer := make([]byte, 1024)
	if _, err := io.ReadFull(reader, buffer); err != nil {
		t.Fatal(err)
	}
	if got := len(reader.buffer); got != 4*1024 {
		t.Fatalf("buffer size = %d, want initial 4096", got)
	}
}

func TestAdaptiveReaderLargeReadBypassesBuffer(t *testing.T) {
	wire := make([]byte, 16*1024)
	reader := newAdaptiveReader(bytes.NewReader(wire), 4*1024, 32*1024)
	decoded := make([]byte, len(wire))
	if _, err := io.ReadFull(reader, decoded); err != nil {
		t.Fatal(err)
	}
	if got := len(reader.buffer); got != 4*1024 {
		t.Fatalf("buffer size after direct read = %d, want 4096", got)
	}
}

func TestAdaptiveReaderReturnsDataBeforeUnderlyingError(t *testing.T) {
	reader := newAdaptiveReader(dataAndErrorReader{}, 4, 8)
	buffer := make([]byte, 4)
	if n, err := reader.Read(buffer[:2]); n != 2 || err != nil || string(buffer[:2]) != "da" {
		t.Fatalf("first Read = (%d, %v, %q)", n, err, buffer[:2])
	}
	if n, err := reader.Read(buffer[:2]); n != 2 || err != nil || string(buffer[:2]) != "ta" {
		t.Fatalf("second Read = (%d, %v, %q)", n, err, buffer[:2])
	}
	if n, err := reader.Read(buffer); n != 0 || err != io.ErrUnexpectedEOF {
		t.Fatalf("terminal Read = (%d, %v), want UnexpectedEOF", n, err)
	}
}

type dataAndErrorReader struct{}

func (dataAndErrorReader) Read(buffer []byte) (int, error) {
	return copy(buffer, "data"), io.ErrUnexpectedEOF
}

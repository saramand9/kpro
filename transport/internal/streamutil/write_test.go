package streamutil

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestWriteFullWritesUntilComplete(t *testing.T) {
	var dst bytes.Buffer
	writer := &chunkedWriter{
		maxChunk: 2,
		write:    dst.Write,
	}

	if err := WriteFull(writer, []byte("hello")); err != nil {
		t.Fatalf("WriteFull returned error: %v", err)
	}
	if got := dst.String(); got != "hello" {
		t.Fatalf("written data = %q, want hello", got)
	}
}

func TestWriteFullRejectsZeroWriteWithoutError(t *testing.T) {
	writer := &chunkedWriter{
		write: func([]byte) (int, error) {
			return 0, nil
		},
	}

	err := WriteFull(writer, []byte("hello"))
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("error = %v, want %v", err, io.ErrShortWrite)
	}
}

func TestWriteFullReturnsWriterError(t *testing.T) {
	wantErr := errors.New("write failed")
	writer := &chunkedWriter{
		write: func([]byte) (int, error) {
			return 0, wantErr
		},
	}

	err := WriteFull(writer, []byte("hello"))
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

type chunkedWriter struct {
	maxChunk int
	write    func([]byte) (int, error)
}

func (w *chunkedWriter) Write(p []byte) (int, error) {
	if w.maxChunk > 0 && len(p) > w.maxChunk {
		p = p[:w.maxChunk]
	}
	return w.write(p)
}

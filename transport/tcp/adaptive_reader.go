package tcp

import "io"

const maxConsecutiveEmptyReads = 100

// adaptiveReader starts small and grows only after a lower-level read fills
// the complete buffer. This keeps idle connections inexpensive while letting
// sustained coalesced traffic amortize receive syscalls. Large caller reads
// still bypass the buffer, preserving the standard bufio behavior for a
// single large frame.
type adaptiveReader struct {
	source  io.Reader
	buffer  []byte
	start   int
	end     int
	readErr error
	maxSize int
}

func newAdaptiveReader(source io.Reader, initialSize, maxSize int) *adaptiveReader {
	if initialSize < 1 {
		initialSize = 1
	}
	if maxSize < initialSize {
		initialSize = max(maxSize, 1)
	}
	return &adaptiveReader{
		source:  source,
		buffer:  make([]byte, initialSize),
		maxSize: max(maxSize, initialSize),
	}
}

func (r *adaptiveReader) Read(destination []byte) (int, error) {
	if len(destination) == 0 {
		return 0, nil
	}
	if r.start == r.end {
		r.start = 0
		r.end = 0
		if r.readErr != nil {
			return 0, r.takeReadError()
		}
		if len(destination) >= len(r.buffer) {
			return r.source.Read(destination)
		}
		r.fill()
		if r.start == r.end {
			return 0, r.takeReadError()
		}
	}
	n := copy(destination, r.buffer[r.start:r.end])
	r.start += n
	return n, nil
}

func (r *adaptiveReader) fill() {
	for attempts := 0; attempts < maxConsecutiveEmptyReads; attempts++ {
		n, err := r.source.Read(r.buffer[:len(r.buffer)])
		if n < 0 || n > len(r.buffer) {
			panic("tcp: invalid read count")
		}
		r.start = 0
		r.end = n
		r.readErr = err
		if n == len(r.buffer) {
			r.grow()
		}
		if n > 0 || err != nil {
			return
		}
	}
	r.readErr = io.ErrNoProgress
}

func (r *adaptiveReader) grow() {
	if len(r.buffer) >= r.maxSize {
		return
	}
	nextSize := min(len(r.buffer)*2, r.maxSize)
	next := make([]byte, nextSize)
	copy(next, r.buffer[:r.end])
	r.buffer = next
}

func (r *adaptiveReader) takeReadError() error {
	err := r.readErr
	r.readErr = nil
	return err
}

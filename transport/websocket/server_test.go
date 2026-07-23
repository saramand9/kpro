package websocket

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saramand9/bignet/kpro/core"
)

func TestReadFrameRejectsUnmaskedClientFrame(t *testing.T) {
	_, err := readFrame(bytes.NewReader([]byte{0x82, 0x00}), 64)
	if !errors.Is(err, ErrBadFrame) {
		t.Fatalf("err = %v, want %v", err, ErrBadFrame)
	}
}

func TestReadFrameAcceptsMaskedClientFrame(t *testing.T) {
	payload := []byte("hello")
	mask := []byte{0x01, 0x02, 0x03, 0x04}
	frame := []byte{0x82, 0x80 | byte(len(payload))}
	frame = append(frame, mask...)
	for i, b := range payload {
		frame = append(frame, b^mask[i%len(mask)])
	}

	got, err := readFrame(bytes.NewReader(frame), 64)
	if err != nil {
		t.Fatalf("read masked frame: %v", err)
	}
	if !got.fin || got.opcode != opBinary || !bytes.Equal(got.payload, payload) {
		t.Fatalf("unexpected frame: fin=%v opcode=%d payload=%q", got.fin, got.opcode, string(got.payload))
	}
}

func TestReadFrameRejectsPayloadOverMaxBeforeReadingMask(t *testing.T) {
	_, err := readFrame(bytes.NewReader([]byte{0x82, 0x80 | 2, 0x01, 0x02, 0x03, 0x04, 0x00, 0x00}), 1)
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err = %v, want %v", err, ErrFrameTooLarge)
	}
}

func TestReadFrameRejectsReservedBits(t *testing.T) {
	_, err := readFrame(bytes.NewReader(maskedClientFrame(0x80|0x40|opBinary, []byte("x"))), 64)
	if !errors.Is(err, ErrBadFrame) {
		t.Fatalf("err = %v, want %v", err, ErrBadFrame)
	}
}

func TestReadFrameRejectsFragmentedControlFrame(t *testing.T) {
	_, err := readFrame(bytes.NewReader(maskedClientFrame(opPing, []byte("x"))), 64)
	if !errors.Is(err, ErrFragmentedControlFrame) {
		t.Fatalf("err = %v, want %v", err, ErrFragmentedControlFrame)
	}
}

func TestReadFrameRejectsOversizedControlFrame(t *testing.T) {
	_, err := readFrame(bytes.NewReader(maskedClientFrame(0x80|opPing, bytes.Repeat([]byte("x"), 126))), 256)
	if !errors.Is(err, ErrFragmentedControlFrame) {
		t.Fatalf("err = %v, want %v", err, ErrFragmentedControlFrame)
	}
}

func TestReadFrameAcceptsExtended16BitLength(t *testing.T) {
	payload := bytes.Repeat([]byte("a"), 130)

	got, err := readFrame(bytes.NewReader(maskedClientFrame(0x80|opBinary, payload)), 256)

	if err != nil {
		t.Fatalf("read extended 16-bit frame: %v", err)
	}
	if !got.fin || got.opcode != opBinary || !bytes.Equal(got.payload, payload) {
		t.Fatalf("unexpected frame: fin=%v opcode=%d payload len=%d", got.fin, got.opcode, len(got.payload))
	}
}

func TestReadFrameAcceptsExtended64BitLength(t *testing.T) {
	payload := bytes.Repeat([]byte("b"), 65536)

	got, err := readFrame(bytes.NewReader(maskedClientFrame(0x80|opBinary, payload)), 65536)

	if err != nil {
		t.Fatalf("read extended 64-bit frame: %v", err)
	}
	if !got.fin || got.opcode != opBinary || !bytes.Equal(got.payload, payload) {
		t.Fatalf("unexpected frame: fin=%v opcode=%d payload len=%d", got.fin, got.opcode, len(got.payload))
	}
}

func TestWriteFrameWritesCompleteHeaderAndPayload(t *testing.T) {
	var dst bytes.Buffer
	writer := &webSocketChunkedWriter{
		maxChunk: 1,
		write:    dst.Write,
	}

	if err := writeFrame(writer, opBinary, []byte("hello")); err != nil {
		t.Fatalf("writeFrame returned error: %v", err)
	}
	want := append([]byte{0x80 | opBinary, 5}, []byte("hello")...)
	if !bytes.Equal(dst.Bytes(), want) {
		t.Fatalf("frame bytes = %x, want %x", dst.Bytes(), want)
	}
}

func TestWriteFrameUsesExtended16BitLength(t *testing.T) {
	var dst bytes.Buffer
	payload := bytes.Repeat([]byte("x"), 126)

	if err := writeFrame(&dst, opBinary, payload); err != nil {
		t.Fatalf("writeFrame returned error: %v", err)
	}
	wantPrefix := []byte{0x80 | opBinary, 126, 0, 126}
	if !bytes.HasPrefix(dst.Bytes(), wantPrefix) {
		t.Fatalf("frame prefix = %x, want %x", dst.Bytes()[:4], wantPrefix)
	}
	if !bytes.Equal(dst.Bytes()[4:], payload) {
		t.Fatal("frame payload was not written after extended 16-bit header")
	}
}

func TestWriteFrameUsesExtended64BitLength(t *testing.T) {
	var dst bytes.Buffer
	payload := bytes.Repeat([]byte("x"), 65536)

	if err := writeFrame(&dst, opBinary, payload); err != nil {
		t.Fatalf("writeFrame returned error: %v", err)
	}
	wantPrefix := []byte{0x80 | opBinary, 127, 0, 0, 0, 0, 0, 1, 0, 0}
	if !bytes.HasPrefix(dst.Bytes(), wantPrefix) {
		t.Fatalf("frame prefix = %x, want %x", dst.Bytes()[:10], wantPrefix)
	}
	if !bytes.Equal(dst.Bytes()[10:], payload) {
		t.Fatal("frame payload was not written after extended 64-bit header")
	}
}

func TestWriteFrameRejectsZeroWriteWithoutError(t *testing.T) {
	writer := &webSocketChunkedWriter{
		write: func([]byte) (int, error) {
			return 0, nil
		},
	}

	err := writeFrame(writer, opBinary, []byte("hello"))
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("error = %v, want %v", err, io.ErrShortWrite)
	}
}

func TestConnectionAdmissionIsAtomic(t *testing.T) {
	server := New(Config{MaxConnections: 1})
	const contenders = 128
	start := make(chan struct{})
	results := make(chan bool, contenders)
	var wg sync.WaitGroup
	for index := 0; index < contenders; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- server.tryAcquireConnectionSlot()
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	acquired := 0
	for result := range results {
		if result {
			acquired++
		}
	}
	if acquired != 1 {
		t.Fatalf("acquired slots = %d, want 1", acquired)
	}
	if active := atomic.LoadInt64(&server.active); active != 1 {
		t.Fatalf("active connections = %d, want 1", active)
	}
	server.releaseConnectionSlot()
}

func TestUpgradeRejectsConnectionOverLimitBeforeHijack(t *testing.T) {
	server := New(Config{MaxConnections: 1})
	if !server.tryAcquireConnectionSlot() {
		t.Fatal("failed to reserve initial connection slot")
	}
	defer server.releaseConnectionSlot()

	request := httptest.NewRequest(http.MethodGet, "http://example.test/ws", nil)
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Sec-WebSocket-Key", "test-key")
	request.Header.Set("Sec-WebSocket-Version", "13")
	response := httptest.NewRecorder()
	server.serveWS(response, request, nil)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	if !bytes.Contains(response.Body.Bytes(), []byte(ErrTooManyConnections.Error())) {
		t.Fatalf("body = %q, want connection limit error", response.Body.String())
	}
}

func TestHTTPUpgradeServerUsesHandshakeTimeout(t *testing.T) {
	server := New(Config{HandshakeTimeout: 3})
	httpServer := server.buildHTTPServer(nil)
	if httpServer.ReadHeaderTimeout != 3 {
		t.Fatalf("ReadHeaderTimeout = %v, want 3ns", httpServer.ReadHeaderTimeout)
	}
}

func TestListenAndServeRejectsSecondStartAndStopsWithNilContext(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve test address: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	server := New(Config{Addr: addr})
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe(nil, nil)
	}()
	deadline := time.Now().Add(time.Second)
	for {
		conn, dialErr := net.DialTimeout("tcp", addr, 20*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("WebSocket server did not start: %v", dialErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := server.ListenAndServe(context.Background(), nil); !errors.Is(err, core.ErrAlreadyStarted) {
		t.Fatalf("second ListenAndServe() error = %v, want %v", err, core.ErrAlreadyStarted)
	}
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("first ListenAndServe() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("WebSocket server did not stop")
	}
}

func TestShutdownRejectsUpgradeThatStartedBeforeDrain(t *testing.T) {
	server := New(Config{MaxConnections: 1})
	if !server.beginUpgrade() {
		t.Fatal("failed to begin upgrade")
	}
	if !server.tryAcquireConnectionSlot() {
		t.Fatal("failed to acquire connection slot")
	}

	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- server.Shutdown(context.Background())
	}()
	deadline := time.Now().Add(time.Second)
	for {
		server.lifecycleMu.Lock()
		accepting := server.accepting
		server.lifecycleMu.Unlock()
		if !accepting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shutdown did not close the upgrade gate")
		}
		time.Sleep(time.Millisecond)
	}

	client, raw := net.Pipe()
	defer client.Close()
	session := &session{
		id:        1,
		server:    server,
		raw:       raw,
		reader:    bufio.NewReader(raw),
		sendQueue: make(chan wsFrame, 1),
		done:      make(chan struct{}),
	}
	if server.registerSession(session, nil) {
		t.Fatal("registered a WebSocket session after shutdown began")
	}
	_ = raw.Close()
	server.releaseConnectionSlot()
	server.upgrades.Done()
	if err := <-shutdownDone; err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if active := atomic.LoadInt64(&server.active); active != 0 {
		t.Fatalf("active connections = %d, want 0", active)
	}
	found := false
	server.sessions.Range(func(_, _ interface{}) bool {
		found = true
		return false
	})
	if found {
		t.Fatal("session remained registered after shutdown")
	}
}

func maskedClientFrame(firstByte byte, payload []byte) []byte {
	mask := []byte{0x01, 0x02, 0x03, 0x04}
	frame := []byte{firstByte}
	switch length := len(payload); {
	case length < 126:
		frame = append(frame, 0x80|byte(length))
	case length <= 0xffff:
		frame = append(frame, 0x80|126, 0, 0)
		binary.BigEndian.PutUint16(frame[len(frame)-2:], uint16(length))
	default:
		frame = append(frame, 0x80|127, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64(frame[len(frame)-8:], uint64(length))
	}
	frame = append(frame, mask...)
	for i, b := range payload {
		frame = append(frame, b^mask[i%len(mask)])
	}
	return frame
}

type webSocketChunkedWriter struct {
	maxChunk int
	write    func([]byte) (int, error)
}

func (w *webSocketChunkedWriter) Write(p []byte) (int, error) {
	if w.maxChunk > 0 && len(p) > w.maxChunk {
		p = p[:w.maxChunk]
	}
	return w.write(p)
}

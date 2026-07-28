package sse

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"
)

func TestBuildHTTPServerDoesNotUseStreamLifetimeWriteTimeout(t *testing.T) {
	endpoint := New(Config{WriteTimeout: time.Second})

	server := endpoint.buildHTTPServer()

	if server.WriteTimeout != 0 {
		t.Fatalf("http.Server.WriteTimeout = %v, want 0", server.WriteTimeout)
	}
}

func TestInitialStreamFlushHonorsWriteTimeout(t *testing.T) {
	const writeTimeout = 40 * time.Millisecond
	endpoint := New(Config{WriteTimeout: writeTimeout})
	handlerCalled := false
	endpoint.Handle(func(*Context) error {
		handlerCalled = true
		return nil
	})
	writer := newBlockingSSEFlushWriter()

	started := time.Now()
	endpoint.serve(writer, httptest.NewRequest(http.MethodGet, "/events", nil))
	elapsed := time.Since(started)

	if elapsed < writeTimeout/2 {
		t.Fatalf("initial stream flush returned after %v, want it bounded by the configured deadline", elapsed)
	}
	if elapsed > time.Second {
		t.Fatalf("initial stream flush remained blocked for %v", elapsed)
	}
	if handlerCalled {
		t.Fatal("SSE handler ran after the initial stream flush failed")
	}
	if endpoint.active != 0 {
		t.Fatalf("active streams = %d, want 0 after initial flush timeout", endpoint.active)
	}
	deadlines, flushes, writes := writer.snapshot()
	if flushes != 1 {
		t.Fatalf("initial flushes = %d, want 1", flushes)
	}
	if writes != 0 {
		t.Fatalf("writes after failed initial flush = %d, want 0", writes)
	}
	if len(deadlines) != 2 || deadlines[0].IsZero() || !deadlines[1].IsZero() {
		t.Fatalf("initial flush deadlines = %v, want one deadline followed by a clear", deadlines)
	}
}

func TestContextSendRenewsAndClearsWriteDeadline(t *testing.T) {
	const timeout = 100 * time.Millisecond
	writer := newSSEDeadlineRecorder()
	ctx := &Context{
		ResponseWriter: writer,
		Request:        httptest.NewRequest(http.MethodGet, "/events", nil),
		writeTimeout:   timeout,
	}

	firstStarted := time.Now()
	if err := ctx.Send("first", []byte("one")); err != nil {
		t.Fatalf("first Send() error = %v", err)
	}
	time.Sleep(timeout + 20*time.Millisecond)
	secondStarted := time.Now()
	if err := ctx.Send("second", []byte("two")); err != nil {
		t.Fatalf("second Send() error = %v", err)
	}

	deadlines, flushes := writer.snapshot()
	if flushes != 2 {
		t.Fatalf("flushes = %d, want 2", flushes)
	}
	if len(deadlines) != 4 {
		t.Fatalf("deadline calls = %d, want 4", len(deadlines))
	}
	if deadlines[0].Before(firstStarted.Add(timeout)) {
		t.Fatalf("first deadline = %v, want at least %v", deadlines[0], firstStarted.Add(timeout))
	}
	if !deadlines[1].IsZero() {
		t.Fatalf("first cleared deadline = %v, want zero", deadlines[1])
	}
	if deadlines[2].Before(secondStarted.Add(timeout)) {
		t.Fatalf("second deadline = %v, want at least %v", deadlines[2], secondStarted.Add(timeout))
	}
	if !deadlines[3].IsZero() {
		t.Fatalf("second cleared deadline = %v, want zero", deadlines[3])
	}
	if !deadlines[2].After(deadlines[0]) {
		t.Fatalf("second deadline = %v, want after first deadline %v", deadlines[2], deadlines[0])
	}
}

func TestLongLivedStreamRenewsWriteTimeoutForEachSend(t *testing.T) {
	const writeTimeout = 30 * time.Millisecond
	endpoint := New(Config{Path: "/events", WriteTimeout: writeTimeout})
	sendErrors := make(chan error, 2)
	endpoint.Handle(func(ctx *Context) error {
		sendErrors <- ctx.Send("first", []byte("one"))
		time.Sleep(4 * writeTimeout)
		sendErrors <- ctx.Send("second", []byte("two"))
		return nil
	})
	server := httptest.NewUnstartedServer(nil)
	server.Config = endpoint.buildHTTPServer()
	server.Start()
	defer server.Close()

	response, err := server.Client().Get(server.URL + endpoint.config.Path)
	if err != nil {
		t.Fatalf("GET SSE stream: %v", err)
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil {
		t.Fatalf("read SSE stream: %v", readErr)
	}
	if closeErr != nil {
		t.Fatalf("close SSE stream: %v", closeErr)
	}
	for event := 1; event <= 2; event++ {
		if err := <-sendErrors; err != nil {
			t.Fatalf("Send() event %d error = %v", event, err)
		}
	}
	want := "event: first\ndata: one\n\nevent: second\ndata: two\n\n"
	if string(body) != want {
		t.Fatalf("response body = %q, want %q", body, want)
	}
}

func TestContextSendWriteTimeoutUnblocksSlowClient(t *testing.T) {
	const writeTimeout = 40 * time.Millisecond
	endpoint := New(Config{Path: "/events", WriteTimeout: writeTimeout})
	server := endpoint.buildHTTPServer()
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	listener := newSingleSSEConnListener(serverConn)
	endpoint.mu.Lock()
	endpoint.server = server
	endpoint.listener = listener
	endpoint.started = true
	endpoint.mu.Unlock()

	handlerDone := make(chan error, 1)
	endpoint.Handle(func(ctx *Context) error {
		handlerDone <- ctx.Send("message", make([]byte, 256<<10))
		return nil
	})
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()

	if _, err := io.WriteString(clientConn, "GET /events HTTP/1.1\r\nHost: example.test\r\n\r\n"); err != nil {
		t.Fatalf("write SSE request: %v", err)
	}
	if err := clientConn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set client read deadline: %v", err)
	}
	reader := bufio.NewReader(clientConn)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE response headers: %v", err)
		}
		if line == "\r\n" {
			break
		}
	}
	if err := clientConn.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("clear client read deadline: %v", err)
	}

	select {
	case err := <-handlerDone:
		if err == nil {
			t.Fatal("Send() error = nil, want write deadline error")
		}
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			var netErr net.Error
			if !errors.As(err, &netErr) || !netErr.Timeout() {
				t.Fatalf("Send() error = %v, want timeout", err)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("Send() remained blocked after its write deadline")
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	if err := endpoint.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SSE HTTP server did not stop")
	}
}

func TestContextSendConfiguredTimeoutRequiresDeadlineSupport(t *testing.T) {
	writer := httptest.NewRecorder()
	ctx := &Context{
		ResponseWriter: writer,
		Request:        httptest.NewRequest(http.MethodGet, "/events", nil),
		writeTimeout:   time.Second,
	}

	err := ctx.Send("message", []byte("data"))

	if !errors.Is(err, http.ErrNotSupported) {
		t.Fatalf("Send() error = %v, want http.ErrNotSupported", err)
	}
	if writer.Body.Len() != 0 {
		t.Fatalf("response body = %q, want empty", writer.Body.String())
	}
}

type sseDeadlineRecorder struct {
	header http.Header

	mu        sync.Mutex
	deadlines []time.Time
	flushes   int
}

type blockingSSEFlushWriter struct {
	header http.Header

	mu        sync.Mutex
	deadline  time.Time
	deadlines []time.Time
	flushes   int
	writes    int
}

func newBlockingSSEFlushWriter() *blockingSSEFlushWriter {
	return &blockingSSEFlushWriter{header: make(http.Header)}
}

func (w *blockingSSEFlushWriter) Header() http.Header {
	return w.header
}

func (w *blockingSSEFlushWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.writes++
	w.mu.Unlock()
	return len(p), nil
}

func (*blockingSSEFlushWriter) WriteHeader(int) {}

func (w *blockingSSEFlushWriter) FlushError() error {
	w.mu.Lock()
	w.flushes++
	deadline := w.deadline
	w.mu.Unlock()
	if wait := time.Until(deadline); wait > 0 {
		timer := time.NewTimer(wait)
		<-timer.C
	}
	return os.ErrDeadlineExceeded
}

func (w *blockingSSEFlushWriter) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	w.deadline = deadline
	w.deadlines = append(w.deadlines, deadline)
	w.mu.Unlock()
	return nil
}

func (w *blockingSSEFlushWriter) snapshot() ([]time.Time, int, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]time.Time(nil), w.deadlines...), w.flushes, w.writes
}

func newSSEDeadlineRecorder() *sseDeadlineRecorder {
	return &sseDeadlineRecorder{header: make(http.Header)}
}

func (w *sseDeadlineRecorder) Header() http.Header {
	return w.header
}

func (*sseDeadlineRecorder) Write(p []byte) (int, error) {
	return len(p), nil
}

func (*sseDeadlineRecorder) WriteHeader(int) {}

func (w *sseDeadlineRecorder) FlushError() error {
	w.mu.Lock()
	w.flushes++
	w.mu.Unlock()
	return nil
}

func (w *sseDeadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	w.deadlines = append(w.deadlines, deadline)
	w.mu.Unlock()
	return nil
}

func (w *sseDeadlineRecorder) snapshot() ([]time.Time, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]time.Time(nil), w.deadlines...), w.flushes
}

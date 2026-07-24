package sse

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestContextCancellationStopsActiveSSEHandler(t *testing.T) {
	addr := freeSSERateLimitAddr(t)
	endpoint := New(Config{Addr: addr, ShutdownTimeout: time.Second})
	handlerStarted := make(chan struct{})
	handlerStopped := make(chan struct{})
	endpoint.Handle(func(ctx *Context) error {
		close(handlerStarted)
		<-ctx.Done()
		close(handlerStopped)
		return nil
	})

	serveCtx, cancelServe := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- endpoint.ListenAndServe(serveCtx) }()
	waitForSSERateLimitServer(t, addr, serveErr)
	responseCh := make(chan *http.Response, 1)
	requestErr := make(chan error, 1)
	go func() {
		response, err := http.Get("http://" + addr + endpoint.config.Path)
		if err != nil {
			requestErr <- err
			return
		}
		responseCh <- response
	}()
	select {
	case <-handlerStarted:
	case err := <-requestErr:
		t.Fatalf("GET SSE stream: %v", err)
	case <-time.After(time.Second):
		t.Fatal("SSE handler did not start")
	}
	cancelServe()

	select {
	case <-handlerStopped:
	case <-time.After(time.Second):
		t.Fatal("SSE handler context was not cancelled during shutdown")
	}
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("ListenAndServe() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SSE server did not drain after cancelling its active stream")
	}
	select {
	case response := <-responseCh:
		_ = response.Body.Close()
	case err := <-requestErr:
		if err != nil {
			t.Fatalf("GET SSE stream: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SSE client did not receive response headers")
	}
}

func TestShutdownDeadlineForceClosesBlockedSSEWrite(t *testing.T) {
	endpoint := New(Config{Path: "/events"})
	server := endpoint.buildHTTPServer()
	rawServerConn, clientConn := net.Pipe()
	defer clientConn.Close()
	serverConn := newSSEBodyWriteProbeConn(rawServerConn)
	defer serverConn.Close()
	listener := newSingleSSEConnListener(serverConn)
	endpoint.mu.Lock()
	endpoint.server = server
	endpoint.listener = listener
	endpoint.started = true
	endpoint.mu.Unlock()

	handlerStarted := make(chan struct{})
	type handlerResult struct {
		writeErr error
		ctxErr   error
	}
	handlerDone := make(chan handlerResult, 1)
	endpoint.Handle(func(ctx *Context) error {
		close(handlerStarted)
		writeErr := ctx.Send("message", make([]byte, 256<<10))
		handlerDone <- handlerResult{writeErr: writeErr, ctxErr: ctx.Request.Context().Err()}
		return writeErr
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
	case <-handlerStarted:
	case <-time.After(time.Second):
		t.Fatal("SSE handler did not start")
	}
	select {
	case <-serverConn.bodyWriteStarted:
	case result := <-handlerDone:
		t.Fatalf("SSE body write completed before entering blocked I/O: write=%v context=%v", result.writeErr, result.ctxErr)
	case <-time.After(time.Second):
		t.Fatal("SSE handler did not enter the probed response-body write")
	}
	select {
	case result := <-handlerDone:
		t.Fatalf("SSE body write was not blocked before shutdown: write=%v context=%v", result.writeErr, result.ctxErr)
	default:
	}

	shutdownErrors := concurrentSSEShutdown(endpoint, 25*time.Millisecond)
	assertSSEShutdownPreservedDeadline(t, shutdownErrors)

	select {
	case result := <-handlerDone:
		if result.writeErr == nil {
			t.Fatal("blocked SSE write returned nil after forced close")
		}
		if !errors.Is(result.ctxErr, context.Canceled) {
			t.Fatalf("SSE request context error = %v, want canceled", result.ctxErr)
		}
	case <-time.After(time.Second):
		t.Fatal("forced SSE close did not unblock the active write")
	}
	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SSE HTTP server did not stop after forced close")
	}

	assertConcurrentSSEShutdownIsIdempotent(t, endpoint)
}

func concurrentSSEShutdown(endpoint *Endpoint, timeout time.Duration) []error {
	const callers = 8
	start := make(chan struct{})
	errorsSeen := make(chan error, callers)
	var shutdownCtx context.Context
	var ready sync.WaitGroup
	var wait sync.WaitGroup
	for range callers {
		ready.Add(1)
		wait.Add(1)
		go func() {
			defer wait.Done()
			ready.Done()
			<-start
			errorsSeen <- endpoint.Shutdown(shutdownCtx)
		}()
	}
	ready.Wait()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), timeout)
	close(start)
	wait.Wait()
	cancelShutdown()
	close(errorsSeen)

	result := make([]error, 0, callers)
	for err := range errorsSeen {
		result = append(result, err)
	}
	return result
}

func assertSSEShutdownPreservedDeadline(t *testing.T, shutdownErrors []error) {
	t.Helper()
	deadlineSeen := false
	for _, err := range shutdownErrors {
		if err == nil {
			continue
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("concurrent Shutdown() error = %v, want nil or deadline exceeded", err)
		}
		deadlineSeen = true
	}
	if !deadlineSeen {
		t.Fatal("concurrent Shutdown() calls did not preserve a deadline error")
	}
}

func assertConcurrentSSEShutdownIsIdempotent(t *testing.T, endpoint *Endpoint) {
	t.Helper()
	const callers = 8
	start := make(chan struct{})
	errorsSeen := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			errorsSeen <- endpoint.Shutdown(ctx)
		}()
	}
	close(start)
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("repeated concurrent Shutdown() error = %v", err)
		}
	}
}

type singleSSEConnListener struct {
	conn      net.Conn
	accepted  chan struct{}
	closed    chan struct{}
	closeOnce sync.Once
}

func newSingleSSEConnListener(conn net.Conn) *singleSSEConnListener {
	return &singleSSEConnListener{
		conn:     conn,
		accepted: make(chan struct{}, 1),
		closed:   make(chan struct{}),
	}
}

func (l *singleSSEConnListener) Accept() (net.Conn, error) {
	select {
	case l.accepted <- struct{}{}:
		return l.conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *singleSSEConnListener) Close() error {
	l.closeOnce.Do(func() { close(l.closed) })
	return nil
}

func (l *singleSSEConnListener) Addr() net.Addr {
	return l.conn.LocalAddr()
}

type sseBodyWriteProbeConn struct {
	net.Conn
	bodyWriteStarted chan struct{}
	closed           chan struct{}
	closeOnce        sync.Once
	closeErr         error

	mu       sync.Mutex
	tail     []byte
	bodySeen bool
}

func newSSEBodyWriteProbeConn(conn net.Conn) *sseBodyWriteProbeConn {
	return &sseBodyWriteProbeConn{
		Conn:             conn,
		bodyWriteStarted: make(chan struct{}),
		closed:           make(chan struct{}),
	}
}

func (c *sseBodyWriteProbeConn) Write(p []byte) (int, error) {
	if c.observeBodyWrite(p) {
		<-c.closed
		return 0, net.ErrClosed
	}
	return c.Conn.Write(p)
}

func (c *sseBodyWriteProbeConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		c.closeErr = c.Conn.Close()
	})
	return c.closeErr
}

func (c *sseBodyWriteProbeConn) observeBodyWrite(p []byte) bool {
	marker := []byte("event: message\n")
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bodySeen {
		return true
	}
	combined := make([]byte, 0, len(c.tail)+len(p))
	combined = append(combined, c.tail...)
	combined = append(combined, p...)
	if bytes.Contains(combined, marker) {
		c.bodySeen = true
		close(c.bodyWriteStarted)
		c.tail = nil
		return true
	}
	keep := len(marker) - 1
	if len(combined) < keep {
		keep = len(combined)
	}
	c.tail = append(c.tail[:0], combined[len(combined)-keep:]...)
	return false
}

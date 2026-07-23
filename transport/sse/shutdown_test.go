package sse

import (
	"context"
	"net/http"
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

package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"
)

type httpShutdownRequestResult struct {
	response   *http.Response
	requestErr error
	bodyErr    error
}

func TestShutdownDeadlineForceClosesActiveHTTPConnection(t *testing.T) {
	addr := freeHTTPRateLimitAddr(t)
	endpoint := New(Config{Addr: addr})
	handlerStarted := make(chan struct{})
	handlerStopped := make(chan struct{})
	endpoint.Get("/blocked", func(ctx *Context) error {
		close(handlerStarted)
		<-ctx.Context().Done()
		close(handlerStopped)
		return ctx.Context().Err()
	})

	serveCtx, cancelServe := context.WithCancel(context.Background())
	defer cancelServe()
	serveErr := make(chan error, 1)
	go func() { serveErr <- endpoint.ListenAndServe(serveCtx) }()
	waitForHTTPRateLimitServer(t, addr, serveErr)

	requestDone := startHTTPShutdownRequest("http://" + addr + "/blocked")

	select {
	case <-handlerStarted:
	case result := <-requestDone:
		t.Fatalf(
			"HTTP request completed before handler blocked: status=%d request=%v body=%v",
			httpShutdownResponseStatus(result.response),
			result.requestErr,
			result.bodyErr,
		)
	case <-time.After(time.Second):
		t.Fatal("HTTP handler did not start")
	}

	shutdownErrors := concurrentHTTPShutdown(endpoint, 25*time.Millisecond)
	assertHTTPShutdownPreservedDeadline(t, shutdownErrors)

	select {
	case <-handlerStopped:
	case <-time.After(time.Second):
		t.Fatal("forced HTTP close did not cancel the active request context")
	}
	select {
	case result := <-requestDone:
		assertHTTPShutdownRequestFailed(t, result)
	case <-time.After(time.Second):
		t.Fatal("forced HTTP close did not disconnect the client")
	}
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("ListenAndServe() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP server did not stop after forced close")
	}

	assertConcurrentHTTPShutdownIsIdempotent(t, endpoint)
}

func TestListenAndServeReturnsCancellationShutdownError(t *testing.T) {
	addr := freeHTTPRateLimitAddr(t)
	endpoint := New(Config{Addr: addr, ShutdownTimeout: 25 * time.Millisecond})
	handlerStarted := make(chan struct{})
	endpoint.Get("/blocked", func(ctx *Context) error {
		close(handlerStarted)
		<-ctx.Context().Done()
		return ctx.Context().Err()
	})

	serveCtx, cancelServe := context.WithCancel(context.Background())
	defer cancelServe()
	serveErr := make(chan error, 1)
	go func() { serveErr <- endpoint.ListenAndServe(serveCtx) }()
	waitForHTTPRateLimitServer(t, addr, serveErr)
	requestDone := startHTTPShutdownRequest("http://" + addr + "/blocked")
	select {
	case <-handlerStarted:
	case result := <-requestDone:
		t.Fatalf(
			"HTTP request completed before handler blocked: status=%d request=%v body=%v",
			httpShutdownResponseStatus(result.response),
			result.requestErr,
			result.bodyErr,
		)
	case <-time.After(time.Second):
		t.Fatal("HTTP handler did not start")
	}

	cancelServe()
	select {
	case err := <-serveErr:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("ListenAndServe() error = %v, want deadline exceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP server did not return the cancellation shutdown error")
	}
	select {
	case result := <-requestDone:
		assertHTTPShutdownRequestFailed(t, result)
	case <-time.After(time.Second):
		t.Fatal("cancellation shutdown did not disconnect the HTTP client")
	}
}

func startHTTPShutdownRequest(url string) <-chan httpShutdownRequestResult {
	requestDone := make(chan httpShutdownRequestResult, 1)
	go func() {
		transport := &http.Transport{DisableKeepAlives: true}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
		response, requestErr := client.Get(url)
		result := httpShutdownRequestResult{response: response, requestErr: requestErr}
		if response != nil {
			_, result.bodyErr = io.ReadAll(response.Body)
			_ = response.Body.Close()
		}
		requestDone <- result
	}()
	return requestDone
}

func assertHTTPShutdownRequestFailed(t *testing.T, result httpShutdownRequestResult) {
	t.Helper()
	if result.requestErr == nil && result.bodyErr == nil {
		t.Fatalf(
			"forced HTTP close produced a normal response: status=%d",
			httpShutdownResponseStatus(result.response),
		)
	}
}

func httpShutdownResponseStatus(response *http.Response) int {
	if response == nil {
		return 0
	}
	return response.StatusCode
}

func concurrentHTTPShutdown(endpoint *Endpoint, timeout time.Duration) []error {
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

func assertHTTPShutdownPreservedDeadline(t *testing.T, shutdownErrors []error) {
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

func assertConcurrentHTTPShutdownIsIdempotent(t *testing.T, endpoint *Endpoint) {
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

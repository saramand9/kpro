// File: server/middleware_test.go
// Purpose: Tests app-level middleware wiring across message and native protocol runners.

package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saramand9/kpro/core"
	http3x "github.com/saramand9/kpro/transport/http3"
	"github.com/saramand9/kpro/transport/httpx"
	"github.com/saramand9/kpro/transport/sse"
)

func TestEndpointUseWrapsMessageDispatch(t *testing.T) {
	endpoint := &Endpoint{
		transport: &fakeTransport{name: "fake"},
		router: core.NewRouter(core.RouterConfig{
			SyncQueueSize:         4,
			AsyncWorkers:          1,
			AsyncQueueSize:        4,
			MaxInflightPerSession: 4,
			SessionIdleTimeout:    time.Minute,
		}),
	}
	defer func() {
		if err := endpoint.router.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown router: %v", err)
		}
	}()

	callSeen := make(chan struct{}, 1)
	handlerSeen := make(chan struct{}, 1)
	endpoint.Use(func(next core.InvocationHandler) core.InvocationHandler {
		return func(ctx core.Invocation) error {
			if got := ctx.Operation(); got != core.MessageOperation(testMsgEcho) {
				t.Fatalf("operation = %q", got)
			}
			if payload, ok := ctx.Payload().([]byte); !ok || string(payload) != "hello" {
				t.Fatalf("payload = %T %q", ctx.Payload(), ctx.Payload())
			}
			callSeen <- struct{}{}
			return next(ctx)
		}
	})
	endpoint.HandleSync(testMsgEcho, func(ctx core.Context) error {
		handlerSeen <- struct{}{}
		return nil
	})

	session := &fakeSession{id: 10, done: make(chan struct{})}
	if err := endpoint.dispatch(core.NewContext(session, core.Message{
		ID:   testMsgEcho,
		Seq:  1,
		Data: []byte("hello"),
	})); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	select {
	case <-callSeen:
	case <-time.After(time.Second):
		t.Fatal("middleware was not called")
	}
	select {
	case <-handlerSeen:
	case <-time.After(time.Second):
		t.Fatal("handler was not called")
	}
}

func TestServerUseAppliesToFutureMessageEndpoints(t *testing.T) {
	app := New()
	callSeen := make(chan struct{}, 1)
	handlerSeen := make(chan struct{}, 1)
	app.Use(func(next core.InvocationHandler) core.InvocationHandler {
		return func(ctx core.Invocation) error {
			if ctx.Transport() != core.TransportTCP {
				t.Fatalf("transport = %s", ctx.Transport())
			}
			callSeen <- struct{}{}
			return next(ctx)
		}
	})

	endpoint := app.Mount(&fakeTransport{name: string(core.TransportTCP)})
	defer shutdownEndpointRouter(t, endpoint)
	endpoint.HandleSync(testMsgEcho, func(ctx core.Context) error {
		handlerSeen <- struct{}{}
		return nil
	})

	dispatchTestEcho(t, endpoint)
	waitForSignal(t, callSeen, "middleware")
	waitForSignal(t, handlerSeen, "handler")
}

func TestServerUseAppliesToMountedMessageEndpoints(t *testing.T) {
	app := New()
	endpoint := app.Mount(&fakeTransport{name: string(core.TransportTCP)})
	defer shutdownEndpointRouter(t, endpoint)

	callSeen := make(chan struct{}, 1)
	handlerSeen := make(chan struct{}, 1)
	app.Use(func(next core.InvocationHandler) core.InvocationHandler {
		return func(ctx core.Invocation) error {
			callSeen <- struct{}{}
			return next(ctx)
		}
	})
	endpoint.HandleSync(testMsgEcho, func(ctx core.Context) error {
		handlerSeen <- struct{}{}
		return nil
	})

	dispatchTestEcho(t, endpoint)
	waitForSignal(t, callSeen, "middleware")
	waitForSignal(t, handlerSeen, "handler")
}

func TestUseCheckedRejectsNilMiddlewareWithoutMutation(t *testing.T) {
	app := New()
	runner := &customMiddlewareRunner{}
	app.MountRunner(runner)
	valid := func(next core.InvocationHandler) core.InvocationHandler { return next }

	err := app.UseChecked(valid, nil)
	if !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("UseChecked() error = %v, want %v", err, ErrInvalidConfiguration)
	}
	if app.State() != StateCreated {
		t.Fatalf("server state = %v, want %v", app.State(), StateCreated)
	}
	if got := len(app.middlewares); got != 0 {
		t.Fatalf("server middleware count = %d, want 0", got)
	}
	if runner.middlewareCount != 0 {
		t.Fatalf("runner middleware count = %d, want 0", runner.middlewareCount)
	}

	if err := app.UseChecked(valid); err != nil {
		t.Fatalf("UseChecked() after rejection error = %v", err)
	}
	if got := len(app.middlewares); got != 1 {
		t.Fatalf("server middleware count after retry = %d, want 1", got)
	}
	if runner.middlewareCount != 1 {
		t.Fatalf("runner middleware count after retry = %d, want 1", runner.middlewareCount)
	}
}

func TestUseCheckedDoesNotHoldServerLockDuringMiddlewareCompilation(t *testing.T) {
	app := New()
	endpoint := app.Mount(&fakeTransport{name: string(core.TransportTCP)})
	defer shutdownEndpointRouter(t, endpoint)
	endpoint.HandleSync(testMsgEcho, func(core.Context) error { return nil })
	resource := &managedTestResource{}

	result := make(chan error, 1)
	go func() {
		result <- app.UseChecked(func(next core.InvocationHandler) core.InvocationHandler {
			if err := app.ManageChecked(resource); err != nil {
				return func(core.Invocation) error { return err }
			}
			return next
		})
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("UseChecked() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("UseChecked deadlocked during reentrant middleware compilation")
	}
	if got := len(app.resources); got != 1 {
		t.Fatalf("managed resources = %d, want 1", got)
	}
}

func TestConcurrentUseCheckedReturnsBusyAndPreservesOrder(t *testing.T) {
	app := New()
	runner := newBlockingMiddlewareRunner()
	app.MountRunner(runner)
	middlewareA := func(next core.InvocationHandler) core.InvocationHandler { return next }
	middlewareB := func(next core.InvocationHandler) core.InvocationHandler { return next }

	firstResult := make(chan error, 1)
	go func() { firstResult <- app.UseChecked(middlewareA) }()
	waitForSignal(t, runner.entered, "first middleware application")
	if err := app.UseChecked(middlewareB); !errors.Is(err, ErrConfigurationBusy) {
		t.Fatalf("concurrent UseChecked() error = %v, want %v", err, ErrConfigurationBusy)
	}
	close(runner.release)
	if err := <-firstResult; err != nil {
		t.Fatalf("first UseChecked() error = %v", err)
	}
	if err := app.UseChecked(middlewareB); err != nil {
		t.Fatalf("retried UseChecked() error = %v", err)
	}
	if got := runner.applied; got != 2 {
		t.Fatalf("applied middleware count = %d, want 2", got)
	}
	if got := len(app.middlewares); got != 2 {
		t.Fatalf("server middleware count = %d, want 2", got)
	}
}

func TestServerUseAppliesToCustomMiddlewareRunner(t *testing.T) {
	app := New()
	runner := &customMiddlewareRunner{}

	app.MountRunner(runner)
	app.Use(func(next core.InvocationHandler) core.InvocationHandler {
		return func(ctx core.Invocation) error {
			return next(ctx)
		}
	})

	if runner.middlewareCount != 1 {
		t.Fatalf("middleware count = %d, want 1", runner.middlewareCount)
	}
}

func TestServerUseAppliesToHTTPEndpoint(t *testing.T) {
	app := New()
	callSeen := make(chan struct{}, 1)
	middlewareErr := make(chan error, 1)
	app.Use(func(next core.InvocationHandler) core.InvocationHandler {
		return func(ctx core.Invocation) error {
			if ctx.Transport() != core.TransportHTTP {
				return recordServerTestMiddlewareError(middlewareErr, fmt.Errorf("transport = %s", ctx.Transport()))
			}
			if ctx.Operation() != "GET /middleware" {
				return recordServerTestMiddlewareError(middlewareErr, fmt.Errorf("operation = %q", ctx.Operation()))
			}
			callSeen <- struct{}{}
			return next(ctx)
		}
	})

	addr := freeServerTestTCPAddr(t)
	endpoint := httpx.New(httpx.Config{Addr: addr})
	app.MountRunner(endpoint)
	endpoint.Get("/middleware", func(ctx *httpx.Context) error {
		return ctx.Text(http.StatusOK, "ok")
	})
	errCh := startServerTestApp(t, app)
	defer shutdownServerTestApp(t, app, errCh)

	respBody, status := serverTestHTTPGet(t, "http://"+addr+"/middleware", errCh)
	if status != http.StatusOK || respBody != "ok" {
		t.Fatalf("status/body = %d/%q, want %d/ok", status, respBody, http.StatusOK)
	}
	assertNoServerTestMiddlewareError(t, middlewareErr)
	waitForSignal(t, callSeen, "http middleware")
}

func TestServerUseAppliesToHTTP3Endpoint(t *testing.T) {
	app := New()
	endpoint := http3x.New(http3x.Config{Addr: "127.0.0.1:0"})
	app.MountRunner(endpoint)
	callSeen := false
	app.Use(func(next core.InvocationHandler) core.InvocationHandler {
		return func(ctx core.Invocation) error {
			callSeen = true
			if ctx.Transport() != core.TransportHTTP3 {
				t.Fatalf("transport = %s, want %s", ctx.Transport(), core.TransportHTTP3)
			}
			return next(ctx)
		}
	})
	endpoint.Get("/middleware", func(ctx *httpx.Context) error {
		return ctx.Text(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/middleware", nil)
	resp := httptest.NewRecorder()
	endpoint.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK || resp.Body.String() != "ok" {
		t.Fatalf("status/body = %d/%q, want %d/ok", resp.Code, resp.Body.String(), http.StatusOK)
	}
	if !callSeen {
		t.Fatal("HTTP/3 middleware was not called")
	}
}

func TestServerUseAppliesToSSEEndpoint(t *testing.T) {
	app := New()
	callSeen := make(chan struct{}, 1)
	middlewareErr := make(chan error, 1)
	app.Use(func(next core.InvocationHandler) core.InvocationHandler {
		return func(ctx core.Invocation) error {
			if ctx.Transport() != core.TransportSSE {
				return recordServerTestMiddlewareError(middlewareErr, fmt.Errorf("transport = %s", ctx.Transport()))
			}
			if ctx.Operation() != "GET /events" {
				return recordServerTestMiddlewareError(middlewareErr, fmt.Errorf("operation = %q", ctx.Operation()))
			}
			callSeen <- struct{}{}
			return next(ctx)
		}
	})

	addr := freeServerTestTCPAddr(t)
	endpoint := sse.New(sse.Config{Addr: addr, Path: "/events"})
	app.MountRunner(endpoint)
	endpoint.Handle(func(ctx *sse.Context) error {
		return ctx.Send("message", []byte("ok"))
	})
	errCh := startServerTestApp(t, app)
	defer shutdownServerTestApp(t, app, errCh)

	respBody, status := serverTestHTTPGet(t, "http://"+addr+"/events", errCh)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if !strings.Contains(respBody, "event: message") || !strings.Contains(respBody, "data: ok") {
		t.Fatalf("body = %q", respBody)
	}
	assertNoServerTestMiddlewareError(t, middlewareErr)
	waitForSignal(t, callSeen, "sse middleware")
}

func dispatchTestEcho(t *testing.T, endpoint *Endpoint) {
	t.Helper()
	session := &fakeSession{id: 10, done: make(chan struct{})}
	if err := endpoint.dispatch(core.NewContext(session, core.Message{
		ID:   testMsgEcho,
		Seq:  1,
		Data: []byte("hello"),
	})); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
}

func waitForSignal(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("%s was not called", name)
	}
}

func shutdownEndpointRouter(t *testing.T, endpoint *Endpoint) {
	t.Helper()
	if err := endpoint.router.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown router: %v", err)
	}
}

func startServerTestApp(t *testing.T, app *Server) <-chan error {
	t.Helper()
	errCh := make(chan error, 1)
	go func() {
		errCh <- app.ListenAndServe()
	}()
	return errCh
}

func shutdownServerTestApp(t *testing.T, app *Server, errCh <-chan error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := app.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop")
	}
}

func serverTestHTTPGet(t *testing.T, url string, errCh <-chan error) (string, int) {
	t.Helper()
	client := &http.Client{Timeout: 3 * time.Second}
	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			t.Fatalf("server stopped before request completed: %v", err)
		default:
		}
		resp, err := client.Get(url)
		if err == nil {
			defer resp.Body.Close()
			body, readErr := io.ReadAll(resp.Body)
			if readErr != nil {
				t.Fatal(readErr)
			}
			return string(body), resp.StatusCode
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("GET %s failed: %v", url, lastErr)
	return "", 0
}

func freeServerTestTCPAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func recordServerTestMiddlewareError(ch chan<- error, err error) error {
	select {
	case ch <- err:
	default:
	}
	return err
}

func assertNoServerTestMiddlewareError(t *testing.T, ch <-chan error) {
	t.Helper()
	select {
	case err := <-ch:
		t.Fatal(err)
	default:
	}
}

type fakeTransport struct {
	name string
}

func (t *fakeTransport) Name() string {
	return t.name
}

func (t *fakeTransport) ListenAndServe(context.Context, core.Handler) error {
	return nil
}

func (t *fakeTransport) Shutdown(context.Context) error {
	return nil
}

type fakeSession struct {
	id   uint64
	done chan struct{}
}

func (s *fakeSession) ID() uint64 {
	return s.id
}

func (s *fakeSession) Transport() core.Transport {
	return core.TransportTCP
}

func (s *fakeSession) RemoteAddr() net.Addr {
	return nil
}

func (s *fakeSession) Send(core.Message) error {
	return nil
}

func (s *fakeSession) Close() error {
	close(s.done)
	return nil
}

func (s *fakeSession) Done() <-chan struct{} {
	return s.done
}

type customMiddlewareRunner struct {
	middlewareCount int
}

func (r *customMiddlewareRunner) Name() string {
	return "custom"
}

func (r *customMiddlewareRunner) ListenAndServe(context.Context) error {
	return nil
}

func (r *customMiddlewareRunner) Shutdown(context.Context) error {
	return nil
}

func (r *customMiddlewareRunner) ApplyMiddleware(middlewares ...core.Middleware) {
	r.middlewareCount += len(middlewares)
}

type blockingMiddlewareRunner struct {
	entered chan struct{}
	release chan struct{}
	first   sync.Once
	applied int
}

func newBlockingMiddlewareRunner() *blockingMiddlewareRunner {
	return &blockingMiddlewareRunner{entered: make(chan struct{}), release: make(chan struct{})}
}

func (r *blockingMiddlewareRunner) Name() string { return "blocking-middleware" }
func (r *blockingMiddlewareRunner) ListenAndServe(context.Context) error {
	return nil
}
func (r *blockingMiddlewareRunner) Shutdown(context.Context) error { return nil }
func (r *blockingMiddlewareRunner) ApplyMiddleware(middlewares ...core.Middleware) {
	r.first.Do(func() {
		close(r.entered)
		<-r.release
	})
	r.applied += len(middlewares)
}

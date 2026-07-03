// File: server/server_test.go
// Purpose: Tests high-level server lifecycle behavior.

package server

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func TestListenAndServeRequiresTransport(t *testing.T) {
	app := New()

	err := app.ListenAndServe()

	if !errors.Is(err, ErrNoTransports) {
		t.Fatalf("ListenAndServe error = %v, want %v", err, ErrNoTransports)
	}
}

func TestShutdownAcceptsNilContext(t *testing.T) {
	app := New()

	if err := app.Shutdown(nil); err != nil {
		t.Fatalf("Shutdown(nil) returned error: %v", err)
	}
}

func TestListenAndServeShutsDownRunnersAfterFirstExit(t *testing.T) {
	wantErr := errors.New("listener failed")
	app := New()
	fast := newFakeRunner("fast", wantErr, false)
	blocked := newFakeRunner("blocked", nil, true)
	app.MountRunner(fast)
	app.MountRunner(blocked)

	errCh := make(chan error, 1)
	go func() {
		errCh <- app.ListenAndServe()
	}()

	waitForClosed(t, fast.started, "fast runner did not start")
	waitForClosed(t, blocked.started, "blocked runner did not start")

	select {
	case err := <-errCh:
		if !errors.Is(err, wantErr) {
			t.Fatalf("ListenAndServe error = %v, want %v", err, wantErr)
		}
	case <-time.After(time.Second):
		t.Fatal("ListenAndServe did not return after first runner exited")
	}

	if got := fast.shutdowns.Load(); got == 0 {
		t.Fatal("fast runner was not shut down")
	}
	if got := blocked.shutdowns.Load(); got == 0 {
		t.Fatal("blocked runner was not shut down")
	}
}

func TestShutdownCancelsRunningServer(t *testing.T) {
	app := New()
	runner := newFakeRunner("blocked", nil, true)
	app.MountRunner(runner)

	errCh := make(chan error, 1)
	go func() {
		errCh <- app.ListenAndServe()
	}()
	waitForClosed(t, runner.started, "runner did not start")

	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("ListenAndServe returned error after shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ListenAndServe did not return after Shutdown")
	}
	if got := runner.shutdowns.Load(); got == 0 {
		t.Fatal("runner was not shut down")
	}
}

func TestShutdownAttemptsAllRunnersAndJoinsErrors(t *testing.T) {
	firstErr := errors.New("first shutdown failed")
	secondErr := errors.New("second shutdown failed")
	app := New()
	first := newFakeRunner("first", nil, false)
	first.shutdownErr = firstErr
	second := newFakeRunner("second", nil, false)
	second.shutdownErr = secondErr
	app.MountRunner(first)
	app.MountRunner(second)

	err := app.Shutdown(nil)

	if !errors.Is(err, firstErr) {
		t.Fatalf("shutdown error = %v, want first error %v", err, firstErr)
	}
	if !errors.Is(err, secondErr) {
		t.Fatalf("shutdown error = %v, want second error %v", err, secondErr)
	}
	if got := first.shutdowns.Load(); got != 1 {
		t.Fatalf("first shutdown count = %d, want 1", got)
	}
	if got := second.shutdowns.Load(); got != 1 {
		t.Fatalf("second shutdown count = %d, want 1", got)
	}
}

func TestShutdownReturnsContextErrorWhenRunnerShutdownBlocks(t *testing.T) {
	app := New()
	runner := newFakeRunner("blocked-shutdown", nil, false)
	runner.blockShutdown = true
	app.MountRunner(runner)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	err := app.Shutdown(ctx)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown error = %v, want %v", err, context.DeadlineExceeded)
	}
	if got := runner.shutdowns.Load(); got != 1 {
		t.Fatalf("shutdown count = %d, want 1", got)
	}
}

func TestServerManageShutsResourcesAfterRunners(t *testing.T) {
	var order []string
	app := New()
	app.MountRunner(&managedTestRunner{shutdown: func() {
		order = append(order, "runner")
	}})
	app.Manage(&managedTestResource{shutdown: func() {
		order = append(order, "resource")
	}})

	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	want := []string{"runner", "resource", "runner"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("shutdown order = %v, want %v", order, want)
	}
}

type fakeRunner struct {
	name          string
	err           error
	block         bool
	started       chan struct{}
	startOnce     atomic.Bool
	shutdowns     atomic.Int32
	shutdownErr   error
	blockShutdown bool
}

func newFakeRunner(name string, err error, block bool) *fakeRunner {
	return &fakeRunner{
		name:    name,
		err:     err,
		block:   block,
		started: make(chan struct{}),
	}
}

func (r *fakeRunner) Name() string {
	return r.name
}

func (r *fakeRunner) ListenAndServe(ctx context.Context) error {
	if r.startOnce.CompareAndSwap(false, true) {
		close(r.started)
	}
	if r.block {
		<-ctx.Done()
		return nil
	}
	return r.err
}

func (r *fakeRunner) Shutdown(ctx context.Context) error {
	r.shutdowns.Add(1)
	if r.blockShutdown {
		<-ctx.Done()
		return ctx.Err()
	}
	return r.shutdownErr
}

func waitForClosed(t *testing.T, ch <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal(message)
	}
}

type managedTestRunner struct {
	shutdown func()
}

func (r *managedTestRunner) Name() string {
	return "managed-test"
}

func (r *managedTestRunner) ListenAndServe(context.Context) error {
	return nil
}

func (r *managedTestRunner) Shutdown(context.Context) error {
	if r.shutdown != nil {
		r.shutdown()
	}
	return nil
}

type managedTestResource struct {
	shutdown func()
}

func (r *managedTestResource) Shutdown(context.Context) error {
	if r.shutdown != nil {
		r.shutdown()
	}
	return nil
}

// File: server/server_test.go
// Purpose: Tests high-level server lifecycle behavior.

package server

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saramand9/kpro/transport"
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

func TestNewAppliesDefaultShutdownTimeout(t *testing.T) {
	app := New(Config{Name: "test"})

	if app.config.ShutdownTimeout != DefaultShutdownTimeout {
		t.Fatalf(
			"shutdown timeout = %v, want %v",
			app.config.ShutdownTimeout,
			DefaultShutdownTimeout,
		)
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

func TestListenAndServeBoundsAutomaticShutdown(t *testing.T) {
	app := New(Config{ShutdownTimeout: 10 * time.Millisecond})
	fast := newFakeRunner("fast", nil, false)
	blocked := newFakeRunner("blocked-shutdown", nil, true)
	blocked.blockShutdown = true
	resource := &managedTestResource{}
	app.MountRunner(fast)
	app.MountRunner(blocked)
	app.Manage(resource)

	errCh := make(chan error, 1)
	go func() {
		errCh <- app.ListenAndServe()
	}()

	waitForClosed(t, fast.started, "fast runner did not start")
	waitForClosed(t, blocked.started, "blocked runner did not start")

	select {
	case err := <-errCh:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf(
				"ListenAndServe error = %v, want %v",
				err,
				context.DeadlineExceeded,
			)
		}
	case <-time.After(time.Second):
		t.Fatal("ListenAndServe did not honor automatic shutdown timeout")
	}
	if got := resource.shutdowns.Load(); got != 0 {
		t.Fatalf("resource shutdowns = %d, want 0 while runner shutdown failed", got)
	}
}

func TestListenAndServeDoesNotWaitForeverForUnresponsiveRunner(t *testing.T) {
	app := New(Config{ShutdownTimeout: 10 * time.Millisecond})
	fast := newFakeRunner("fast", nil, false)
	stuck := newUnresponsiveRunner()
	resource := &managedTestResource{}
	app.MountRunner(fast)
	app.MountRunner(stuck)
	app.Manage(resource)

	errCh := make(chan error, 1)
	go func() {
		errCh <- app.ListenAndServe()
	}()

	waitForClosed(t, fast.started, "fast runner did not start")
	waitForClosed(t, stuck.started, "unresponsive runner did not start")

	select {
	case err := <-errCh:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf(
				"ListenAndServe error = %v, want %v",
				err,
				context.DeadlineExceeded,
			)
		}
	case <-time.After(time.Second):
		t.Fatal("ListenAndServe waited forever for unresponsive runner")
	}
	if resource.shutdowns.Load() != 0 {
		t.Fatal("resource shutdown was consumed while a runner was still active")
	}

	close(stuck.release)
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("retry shutdown after runner exit: %v", err)
	}
	if got := resource.shutdowns.Load(); got != 1 {
		t.Fatalf("resource shutdowns = %d, want 1", got)
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
	resource := &managedTestResource{}
	app.Manage(resource)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	err := app.Shutdown(ctx)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown error = %v, want %v", err, context.DeadlineExceeded)
	}
	if got := runner.shutdowns.Load(); got != 1 {
		t.Fatalf("shutdown count = %d, want 1", got)
	}
	if got := resource.shutdowns.Load(); got != 0 {
		t.Fatalf("resource shutdowns = %d, want 0 while runner shutdown failed", got)
	}
}

func TestConcurrentRunnerShutdownCallsAreCoalesced(t *testing.T) {
	runner := newBlockingShutdownRunner()
	app := New()
	runners := []transport.Runner{runner}
	firstResult := make(chan error, 1)
	go func() {
		firstResult <- app.shutdownRunners(context.Background(), runners)
	}()
	waitForClosed(t, runner.entered, "first runner shutdown did not start")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := app.shutdownRunners(ctx, runners); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("coalesced shutdown error = %v, want %v", err, context.DeadlineExceeded)
	}
	if got := runner.calls.Load(); got != 1 {
		t.Fatalf("concurrent runner shutdown calls = %d, want 1", got)
	}

	close(runner.release)
	if err := <-firstResult; err != nil {
		t.Fatalf("first shutdown error = %v", err)
	}
}

func TestManagedResourceShutdownDoesNotHoldLockAcrossCallback(t *testing.T) {
	resource := &blockingManagedResource{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	state := &managedResourceState{resource: resource}
	firstCtx, firstCancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer firstCancel()
	if err := state.shutdown(firstCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first shutdown error = %v, want deadline", err)
	}
	waitForClosed(t, resource.entered, "resource callback did not start")

	secondCtx, secondCancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer secondCancel()
	if err := state.shutdown(secondCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("coalesced shutdown error = %v, want deadline", err)
	}
	close(resource.release)
	if err := state.shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown after callback release: %v", err)
	}
	if got := resource.calls.Load(); got != 1 {
		t.Fatalf("resource shutdown calls = %d, want 1", got)
	}
}

func TestManagedResourceShutdownContainsPanic(t *testing.T) {
	state := &managedResourceState{resource: panicManagedResource{}}
	err := state.shutdown(context.Background())
	var componentErr *ComponentError
	if !errors.As(err, &componentErr) || componentErr.Phase != "shutdown" {
		t.Fatalf("panic shutdown error = %v", err)
	}
}

func TestRunnerShutdownTimeoutStillAttemptsOtherRunners(t *testing.T) {
	stuck := newBlockingShutdownRunner()
	responsive := newFakeRunner("responsive", nil, false)
	app := New()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	err := app.shutdownRunners(ctx, []transport.Runner{stuck, responsive})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error = %v, want %v", err, context.DeadlineExceeded)
	}
	if got := responsive.shutdowns.Load(); got != 1 {
		t.Fatalf("responsive shutdown calls = %d, want 1", got)
	}
	close(stuck.release)
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

func TestServerRetriesManagedResourceAfterShutdownFailure(t *testing.T) {
	wantErr := errors.New("temporary shutdown failure")
	resource := &retryingManagedResource{firstErr: wantErr}
	app := New().Manage(resource)

	if err := app.Shutdown(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("first Shutdown error = %v, want %v", err, wantErr)
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("retry Shutdown error = %v", err)
	}
	if got := resource.attempts.Load(); got != 2 {
		t.Fatalf("resource shutdown attempts = %d, want 2", got)
	}
}

func TestServerWaitReusesOneWaiter(t *testing.T) {
	app := New()
	app.wg.Add(1)
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		err := app.wait(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("wait %d error = %v, want deadline exceeded", i, err)
		}
	}
	app.wg.Done()
	if err := app.wait(context.Background()); err != nil {
		t.Fatal(err)
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
	shutdown  func()
	shutdowns atomic.Int32
}

type blockingManagedResource struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (r *blockingManagedResource) Shutdown(context.Context) error {
	if r.calls.Add(1) == 1 {
		close(r.entered)
	}
	<-r.release
	return nil
}

type panicManagedResource struct{}

func (panicManagedResource) Shutdown(context.Context) error {
	panic("resource crash")
}

type retryingManagedResource struct {
	attempts atomic.Int32
	firstErr error
}

func (r *retryingManagedResource) Shutdown(context.Context) error {
	if r.attempts.Add(1) == 1 {
		return r.firstErr
	}
	return nil
}

type unresponsiveRunner struct {
	started chan struct{}
	release chan struct{}
}

type blockingShutdownRunner struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	calls   atomic.Int32
}

func newBlockingShutdownRunner() *blockingShutdownRunner {
	return &blockingShutdownRunner{entered: make(chan struct{}), release: make(chan struct{})}
}

func (r *blockingShutdownRunner) Name() string { return "blocking-shutdown" }
func (r *blockingShutdownRunner) ListenAndServe(context.Context) error {
	return nil
}
func (r *blockingShutdownRunner) Shutdown(context.Context) error {
	r.calls.Add(1)
	r.once.Do(func() { close(r.entered) })
	<-r.release
	return nil
}

func newUnresponsiveRunner() *unresponsiveRunner {
	return &unresponsiveRunner{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (r *unresponsiveRunner) Name() string {
	return "unresponsive"
}

func (r *unresponsiveRunner) ListenAndServe(context.Context) error {
	close(r.started)
	<-r.release
	return nil
}

func (r *unresponsiveRunner) Shutdown(context.Context) error {
	return nil
}

func (r *managedTestResource) Shutdown(context.Context) error {
	r.shutdowns.Add(1)
	if r.shutdown != nil {
		r.shutdown()
	}
	return nil
}

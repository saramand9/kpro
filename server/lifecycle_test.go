// File: server/lifecycle_test.go
// Purpose: Verifies the public one-shot Server lifecycle contract.

package server

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/transport"
)

func TestNewCheckedValidatesConfigWithoutChangingNewCompatibility(t *testing.T) {
	app := New(Config{ShutdownTimeout: -time.Second})
	if app.config.ShutdownTimeout != DefaultShutdownTimeout {
		t.Fatalf("New() timeout = %v, want compatibility default %v", app.config.ShutdownTimeout, DefaultShutdownTimeout)
	}

	if _, err := NewChecked(Config{ShutdownTimeout: -time.Second}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("NewChecked() negative timeout error = %v, want %v", err, ErrInvalidConfiguration)
	}
	if _, err := NewChecked(Config{Router: core.RouterConfig{
		Backpressure: core.BackpressurePolicy(99),
	}}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("NewChecked() invalid policy error = %v, want %v", err, ErrInvalidConfiguration)
	}
	if _, err := NewChecked(); err != nil {
		t.Fatalf("NewChecked() default error = %v", err)
	}
	if _, err := NewChecked(Config{}, Config{}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("NewChecked() multiple configs error = %v, want %v", err, ErrInvalidConfiguration)
	}
	defer func() {
		if recovered := recover(); !errors.Is(asError(recovered), ErrInvalidConfiguration) {
			t.Fatalf("New() multiple configs panic = %v, want %v", recovered, ErrInvalidConfiguration)
		}
	}()
	_ = New(Config{}, Config{})
}

func asError(value any) error {
	err, _ := value.(error)
	return err
}

func TestServerValidatesAllComponentsBeforeStartingAny(t *testing.T) {
	firstErr := errors.New("first invalid")
	secondErr := errors.New("second invalid")
	first := newValidatingRunner("first", firstErr)
	second := newValidatingRunner("second", secondErr)
	app := New()
	app.MountRunner(first)
	app.MountRunner(second)

	err := app.ListenAndServe()
	if !errors.Is(err, ErrInvalidConfiguration) ||
		!errors.Is(err, firstErr) ||
		!errors.Is(err, secondErr) {
		t.Fatalf("ListenAndServe() validation error = %v", err)
	}
	if !strings.Contains(err.Error(), "first") || !strings.Contains(err.Error(), "second") {
		t.Fatalf("validation error lacks component names: %v", err)
	}
	if channelWasClosed(first.started) || channelWasClosed(second.started) {
		t.Fatal("a component started before validation completed")
	}
	if got := app.State(); got != StateCreated {
		t.Fatalf("state after validation failure = %s, want %s", got, StateCreated)
	}
	if readyErr := app.WaitReady(context.Background()); !errors.Is(readyErr, firstErr) || !errors.Is(readyErr, secondErr) {
		t.Fatalf("WaitReady validation error = %v", readyErr)
	}

	first.validationErr = nil
	second.validationErr = nil
	result := make(chan error, 1)
	go func() { result <- app.ListenAndServe() }()
	waitForClosed(t, first.started, "first validated component did not start")
	waitForClosed(t, second.started, "second validated component did not start")
	if err := app.WaitReady(context.Background()); err != nil {
		t.Fatalf("WaitReady after validation fix = %v", err)
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("ListenAndServe() after validation fix = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ListenAndServe() did not stop")
	}
}

func TestEndpointForwardsValidationAndMessageDescriptor(t *testing.T) {
	wantErr := errors.New("message config invalid")
	component := newValidatingMessageTransport(wantErr)
	app := New()
	endpoint := app.Mount(component)

	descriptor := endpoint.Descriptor()
	if descriptor.Mode != transport.ModeMessage {
		t.Fatalf("descriptor mode = %q, want %q", descriptor.Mode, transport.ModeMessage)
	}
	if descriptor.Name != component.name {
		t.Fatalf("descriptor name = %q, want %q", descriptor.Name, component.name)
	}

	err := app.ListenAndServe()
	if !errors.Is(err, ErrInvalidConfiguration) || !errors.Is(err, wantErr) {
		t.Fatalf("ListenAndServe() validation error = %v", err)
	}
	if channelWasClosed(component.started) {
		t.Fatal("message transport started despite validation failure")
	}

	component.validationErr = nil
	result := make(chan error, 1)
	go func() { result <- app.ListenAndServe() }()
	waitForClosed(t, component.started, "validated message transport did not start")
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("ListenAndServe() after validation fix = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ListenAndServe() did not stop")
	}
}

func TestServerConfigurationFreezesAtStartup(t *testing.T) {
	app := New()
	runner := newFakeRunner("running", nil, true)
	app.MountRunner(runner)
	result := make(chan error, 1)
	go func() { result <- app.ListenAndServe() }()
	waitForClosed(t, runner.started, "runner did not start")

	if _, err := app.MountChecked(&fakeTransport{name: "late-message"}); !errors.Is(err, ErrConfigurationLocked) {
		t.Fatalf("MountChecked() error = %v, want %v", err, ErrConfigurationLocked)
	}
	if _, err := app.MountRunnerChecked(newFakeRunner("late-native", nil, true)); !errors.Is(err, ErrConfigurationLocked) {
		t.Fatalf("MountRunnerChecked() error = %v, want %v", err, ErrConfigurationLocked)
	}
	if err := app.ManageChecked(&managedTestResource{}); !errors.Is(err, ErrConfigurationLocked) {
		t.Fatalf("ManageChecked() error = %v, want %v", err, ErrConfigurationLocked)
	}
	if err := app.UseChecked(func(next core.InvocationHandler) core.InvocationHandler { return next }); !errors.Is(err, ErrConfigurationLocked) {
		t.Fatalf("UseChecked() error = %v, want %v", err, ErrConfigurationLocked)
	}

	legacyCalls := []struct {
		name string
		call func()
	}{
		{"Mount", func() { app.Mount(&fakeTransport{name: "late-message"}) }},
		{"MountRunner", func() { app.MountRunner(newFakeRunner("late-native", nil, true)) }},
		{"Manage", func() { app.Manage(&managedTestResource{}) }},
		{"Use", func() { app.Use(func(next core.InvocationHandler) core.InvocationHandler { return next }) }},
	}
	for _, call := range legacyCalls {
		t.Run(call.name, func(t *testing.T) {
			assertConfigurationLockedPanic(t, call.call)
		})
	}

	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("ListenAndServe() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ListenAndServe() did not stop")
	}
	if _, err := app.MountRunnerChecked(newFakeRunner("after-stop", nil, true)); !errors.Is(err, ErrConfigurationLocked) {
		t.Fatalf("MountRunnerChecked() after stop = %v, want %v", err, ErrConfigurationLocked)
	}
}

func TestCheckedMountRejectsTypedNilComponents(t *testing.T) {
	app := New()
	var messageTransport *validatingMessageTransport
	if _, err := app.MountChecked(messageTransport); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("MountChecked(typed nil) error = %v, want %v", err, ErrInvalidConfiguration)
	}
	var nativeRunner *validatingRunner
	if _, err := app.MountRunnerChecked(nativeRunner); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("MountRunnerChecked(typed nil) error = %v, want %v", err, ErrInvalidConfiguration)
	}
	if err := validateRunners([]transport.Runner{nativeRunner}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("validateRunners(typed nil) error = %v, want %v", err, ErrInvalidConfiguration)
	}
	if got := len(app.runners); got != 0 {
		t.Fatalf("mounted runners = %d, want 0", got)
	}
}

func TestManageCheckedRejectsTypedNilResourceAtomically(t *testing.T) {
	app := New()
	valid := &managedTestResource{}
	var typedNil *managedTestResource
	if err := app.ManageChecked(valid, typedNil); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("ManageChecked(typed nil) error = %v, want %v", err, ErrInvalidConfiguration)
	}
	if got := len(app.resources); got != 0 {
		t.Fatalf("managed resources = %d, want atomic rejection", got)
	}
}

func TestServerExposesNameAndMountedDescriptors(t *testing.T) {
	app := New(Config{Name: "diagnostic-app"})
	endpoint := app.Mount(newValidatingMessageTransport(nil))
	defer func() {
		if err := endpoint.router.Shutdown(context.Background()); err != nil {
			t.Fatalf("router shutdown: %v", err)
		}
	}()
	if got := app.Name(); got != "diagnostic-app" {
		t.Fatalf("Name() = %q, want diagnostic-app", got)
	}
	descriptors := app.Descriptors()
	if len(descriptors) != 1 {
		t.Fatalf("Descriptors() length = %d, want 1", len(descriptors))
	}
	descriptor := descriptors[0]
	if descriptor.Mode != transport.ModeMessage {
		t.Fatalf("descriptor mode = %q, want %q", descriptor.Mode, transport.ModeMessage)
	}
	if !descriptor.Capabilities.Has(transport.CapabilityMiddleware) {
		t.Fatalf("descriptor capabilities = %v, want middleware", descriptor.Capabilities)
	}
}

func TestServerLifecycleRollsBackNoTransportStartup(t *testing.T) {
	app := New()
	if got := app.State(); got != StateCreated {
		t.Fatalf("initial state = %s, want %s", got, StateCreated)
	}
	if err := app.ListenAndServe(); !errors.Is(err, ErrNoTransports) {
		t.Fatalf("ListenAndServe() error = %v, want %v", err, ErrNoTransports)
	}
	if got := app.State(); got != StateCreated {
		t.Fatalf("state after startup failure = %s, want %s", got, StateCreated)
	}
	if readyErr := app.WaitReady(context.Background()); !errors.Is(readyErr, ErrNoTransports) {
		t.Fatalf("WaitReady after empty startup = %v, want %v", readyErr, ErrNoTransports)
	}

	runner := newFakeRunner("later", nil, true)
	app.MountRunner(runner)
	result := make(chan error, 1)
	go func() {
		result <- app.ListenAndServe()
	}()
	waitForClosed(t, runner.started, "runner did not start after rollback")
	if err := app.WaitReady(context.Background()); err != nil {
		t.Fatalf("WaitReady() error = %v", err)
	}
	if got := app.State(); got != StateRunning {
		t.Fatalf("state after successful startup = %s, want %s", got, StateRunning)
	}
	if err := app.ListenAndServe(); !errors.Is(err, ErrServerAlreadyStarted) {
		t.Fatalf("concurrent ListenAndServe() error = %v, want %v", err, ErrServerAlreadyStarted)
	}

	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("running ListenAndServe() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ListenAndServe() did not finish after shutdown")
	}
	if got := app.State(); got != StateStopped {
		t.Fatalf("final state = %s, want %s", got, StateStopped)
	}
	if err := app.ListenAndServe(); !errors.Is(err, ErrServerStopped) {
		t.Fatalf("ListenAndServe() after stop = %v, want %v", err, ErrServerStopped)
	}
}

func TestServerShutdownBeforeStartIsTerminalAndIdempotent(t *testing.T) {
	app := New()
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("first Shutdown() error = %v", err)
	}
	if got := app.State(); got != StateStopped {
		t.Fatalf("state = %s, want %s", got, StateStopped)
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown() error = %v", err)
	}
	if err := app.ListenAndServe(); !errors.Is(err, ErrServerStopped) {
		t.Fatalf("ListenAndServe() error = %v, want %v", err, ErrServerStopped)
	}
}

func TestServerRunnerFailureCompletesLifecycle(t *testing.T) {
	wantErr := errors.New("startup failed")
	app := New()
	app.MountRunner(newFakeRunner("failed", wantErr, false))

	if err := app.ListenAndServe(); !errors.Is(err, wantErr) {
		t.Fatalf("ListenAndServe() error = %v, want %v", err, wantErr)
	}
	if got := app.State(); got != StateStopped {
		t.Fatalf("state = %s, want %s", got, StateStopped)
	}
	if err := app.ListenAndServe(); !errors.Is(err, ErrServerStopped) {
		t.Fatalf("second ListenAndServe() error = %v, want %v", err, ErrServerStopped)
	}
}

func TestServerWaitReadyWaitsForListenerReadiness(t *testing.T) {
	runner := newReadinessRunner("readiness")
	app := New()
	app.MountRunner(runner)
	result := make(chan error, 1)
	go func() { result <- app.ListenAndServe() }()
	waitForClosed(t, runner.started, "readiness runner did not enter ListenAndServe")

	if got := app.State(); got != StateStarting {
		t.Fatalf("state before readiness = %s, want %s", got, StateStarting)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := app.WaitReady(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitReady before listener publish = %v, want deadline", err)
	}

	close(runner.ready)
	if err := app.WaitReady(context.Background()); err != nil {
		t.Fatalf("WaitReady after listener publish: %v", err)
	}
	if got := app.State(); got != StateRunning {
		t.Fatalf("state after readiness = %s, want %s", got, StateRunning)
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := <-result; err != nil {
		t.Fatalf("ListenAndServe: %v", err)
	}
}

func TestRunUsesCallerContextAndReportsComponentProvenance(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runner := newFakeRunner("context-runner", nil, true)
	app := New()
	app.MountRunner(runner)
	result := make(chan error, 1)
	go func() { result <- app.Run(ctx) }()
	waitForClosed(t, runner.started, "context runner did not start")
	cancel()

	err := <-result
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run cancellation error = %v, want context.Canceled", err)
	}
}

func TestStartupFailureReportsComponentAndUnblocksWaitReady(t *testing.T) {
	wantErr := errors.New("bind failed")
	runner := newReadinessRunner("http-public")
	runner.serveErr = wantErr
	app := New()
	app.MountRunner(runner)

	err := app.ListenAndServe()
	if !errors.Is(err, wantErr) {
		t.Fatalf("ListenAndServe error = %v, want %v", err, wantErr)
	}
	var componentErr *ComponentError
	if !errors.As(err, &componentErr) || componentErr.Component != "http-public" || componentErr.Phase != "serve" {
		t.Fatalf("component error = %#v", componentErr)
	}
	if readyErr := app.WaitReady(context.Background()); !errors.Is(readyErr, wantErr) {
		t.Fatalf("WaitReady error = %v, want %v", readyErr, wantErr)
	}
}

func TestStartPinsReadinessGenerationAcrossRetry(t *testing.T) {
	app := New()
	first, err := app.Start(context.Background())
	if err != nil {
		t.Fatalf("first Start() error = %v", err)
	}
	if err := first.WaitReady(context.Background()); !errors.Is(err, ErrNoTransports) {
		t.Fatalf("first WaitReady() error = %v, want %v", err, ErrNoTransports)
	}
	if err := first.Wait(); !errors.Is(err, ErrNoTransports) {
		t.Fatalf("first Wait() error = %v, want %v", err, ErrNoTransports)
	}

	runner := newFakeRunner("retry", nil, true)
	app.MountRunner(runner)
	second, err := app.Start(context.Background())
	if err != nil {
		t.Fatalf("second Start() error = %v", err)
	}
	if err := second.WaitReady(context.Background()); err != nil {
		t.Fatalf("second WaitReady() observed prior generation: %v", err)
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if err := second.Wait(); err != nil {
		t.Fatalf("second Wait() error = %v", err)
	}
}

func TestStartWithCanceledContextDoesNotStartRunner(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := newReadinessRunner("pre-canceled")
	app := New()
	app.MountRunner(runner)

	handle, err := app.Start(ctx)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := handle.WaitReady(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitReady() error = %v, want context.Canceled", err)
	}
	if channelWasClosed(runner.started) {
		t.Fatal("pre-canceled Start launched the runner")
	}
	if err := handle.Wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() error = %v, want context.Canceled", err)
	}
}

func TestRunnerPanicIsReturnedAsComponentError(t *testing.T) {
	runner := &panickingRunner{ready: make(chan struct{})}
	app := New()
	app.MountRunner(runner)

	handle, err := app.Start(context.Background())
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := handle.WaitReady(context.Background()); !errors.Is(err, ErrComponentPanic) {
		t.Fatalf("WaitReady() error = %v, want %v", err, ErrComponentPanic)
	}
	err = handle.Wait()
	if !errors.Is(err, ErrComponentPanic) {
		t.Fatalf("Wait() error = %v, want %v", err, ErrComponentPanic)
	}
	var componentErr *ComponentError
	if !errors.As(err, &componentErr) || componentErr.Component != "panicking" || componentErr.Phase != "serve" {
		t.Fatalf("component error = %#v", componentErr)
	}
}

func TestCleanupFailureIsNotHiddenByCancellation(t *testing.T) {
	cleanupErr := errors.New("cleanup failed")
	ctx, cancel := context.WithCancel(context.Background())
	runner := newFakeRunner("cleanup", nil, true)
	runner.shutdownErr = cleanupErr
	app := New()
	app.MountRunner(runner)

	handle, err := app.Start(ctx)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := handle.WaitReady(context.Background()); err != nil {
		t.Fatalf("WaitReady() error = %v", err)
	}
	cancel()
	err = handle.Wait()
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("Wait() error = %v, want cleanup failure", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("cleanup failure still wrapped cancellation: %v", err)
	}
}

type validatingRunner struct {
	name          string
	validationErr error
	started       chan struct{}
}

type readinessRunner struct {
	name     string
	started  chan struct{}
	ready    chan struct{}
	serveErr error
}

type panickingRunner struct {
	ready chan struct{}
}

func (r *panickingRunner) Name() string                   { return "panicking" }
func (r *panickingRunner) Ready() <-chan struct{}         { return r.ready }
func (r *panickingRunner) Shutdown(context.Context) error { return nil }
func (r *panickingRunner) ListenAndServe(context.Context) error {
	panic("serve crash")
}

func newReadinessRunner(name string) *readinessRunner {
	return &readinessRunner{
		name:    name,
		started: make(chan struct{}),
		ready:   make(chan struct{}),
	}
}

func (r *readinessRunner) Name() string                   { return r.name }
func (r *readinessRunner) Ready() <-chan struct{}         { return r.ready }
func (r *readinessRunner) Shutdown(context.Context) error { return nil }
func (r *readinessRunner) ListenAndServe(ctx context.Context) error {
	close(r.started)
	if r.serveErr != nil {
		return r.serveErr
	}
	<-ctx.Done()
	return nil
}

type validatingMessageTransport struct {
	name          string
	validationErr error
	started       chan struct{}
}

func newValidatingMessageTransport(validationErr error) *validatingMessageTransport {
	return &validatingMessageTransport{
		name:          "validated-message",
		validationErr: validationErr,
		started:       make(chan struct{}),
	}
}

func (s *validatingMessageTransport) Name() string    { return s.name }
func (s *validatingMessageTransport) Validate() error { return s.validationErr }
func (s *validatingMessageTransport) Descriptor() transport.Descriptor {
	return transport.Descriptor{
		Name:     s.name,
		Protocol: core.TransportTCP,
		Mode:     transport.ModeNative,
	}
}
func (s *validatingMessageTransport) Shutdown(context.Context) error { return nil }
func (s *validatingMessageTransport) ListenAndServe(ctx context.Context, _ core.Handler) error {
	close(s.started)
	<-ctx.Done()
	return nil
}

func newValidatingRunner(name string, validationErr error) *validatingRunner {
	return &validatingRunner{
		name:          name,
		validationErr: validationErr,
		started:       make(chan struct{}),
	}
}

func (r *validatingRunner) Name() string    { return r.name }
func (r *validatingRunner) Validate() error { return r.validationErr }
func (r *validatingRunner) Shutdown(context.Context) error {
	return nil
}
func (r *validatingRunner) ListenAndServe(ctx context.Context) error {
	close(r.started)
	<-ctx.Done()
	return nil
}

func channelWasClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func assertConfigurationLockedPanic(t *testing.T, call func()) {
	t.Helper()
	defer func() {
		recovered := recover()
		err, ok := recovered.(error)
		if !ok || !errors.Is(err, ErrConfigurationLocked) {
			t.Fatalf("panic = %v, want %v", recovered, ErrConfigurationLocked)
		}
	}()
	call()
}

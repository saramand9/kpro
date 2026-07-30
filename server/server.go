// File: server/server.go
// Purpose: Implements or tests the high-level KPro server orchestration layer.

package server

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/transport"
)

var (
	// ErrNoTransports is returned when ListenAndServe starts without mounted transports.
	ErrNoTransports = errors.New("no transports registered")
	// ErrInvalidConfiguration reports a failed Config or component startup check.
	ErrInvalidConfiguration = errors.New("invalid server configuration")
	// ErrComponentExitedBeforeReady reports a runner that stopped before
	// publishing listener readiness.
	ErrComponentExitedBeforeReady = errors.New("component exited before readiness")
	// ErrComponentPanic reports a panic recovered at a component lifecycle boundary.
	ErrComponentPanic = errors.New("component lifecycle panic recovered")
)

// DefaultShutdownTimeout bounds automatic cleanup after a runner exits.
const DefaultShutdownTimeout = 5 * time.Second

// MiddlewareTarget can be implemented by custom runners that want app-level
// middleware installed through Server.Use.
type MiddlewareTarget = transport.MiddlewareTarget

// ManagedResource is stopped after mounted protocol runners drain or their
// configured automatic shutdown deadline expires.
type ManagedResource interface {
	// Shutdown releases the resource and honors ctx cancellation.
	Shutdown(context.Context) error
}

var (
	_ MiddlewareTarget             = (*Endpoint)(nil)
	_ transport.Validator          = (*Endpoint)(nil)
	_ transport.CapabilityProvider = (*Endpoint)(nil)
)

// Config controls the high-level server and the router used by message transports.
type Config struct {
	// Name is a human-readable server name used by examples and diagnostics.
	Name string
	// Router configures message transports mounted through TCP, UDP, or WebSocket.
	Router core.RouterConfig
	// ShutdownTimeout bounds automatic runner and resource cleanup after any
	// mounted runner exits. Explicit Shutdown calls use their supplied context.
	ShutdownTimeout time.Duration
}

// Validate checks strict configuration invariants without applying New's
// compatibility defaults. Zero values remain valid and select defaults.
func (c Config) Validate() error {
	var validationErr error
	if c.ShutdownTimeout < 0 {
		validationErr = errors.Join(validationErr, errors.New("shutdown timeout cannot be negative"))
	}
	validationErr = errors.Join(validationErr, c.Router.Validate())
	return validationErr
}

// Server coordinates one or more protocol runners under a shared one-shot
// lifecycle. Configuration freezes when ListenAndServe transitions from
// Created to Starting, or when Shutdown begins before startup. Checked builder
// methods return ErrConfigurationLocked after that point; fluent methods panic
// with the same error so late configuration cannot appear to succeed.
type Server struct {
	config Config

	mu                 sync.Mutex
	runners            []transport.Runner
	middlewares        []core.Middleware
	resources          []*managedResourceState
	runnerRevision     uint64
	configuring        sync.WaitGroup
	configurationApply sync.Mutex

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	waitMu      sync.Mutex
	waitDone    chan struct{}
	waitStarted bool
	state       atomic.Uint32

	readyMu   sync.Mutex
	readiness *readinessAttempt

	runnerShutdownMu      sync.Mutex
	runnerShutdownCurrent *runnerShutdownAttempt
}

type runnerShutdownAttempt struct {
	done chan struct{}
	err  error
}

type managedResourceState struct {
	resource ManagedResource
	mu       sync.Mutex
	stopped  bool
	current  *resourceShutdownAttempt
}

type resourceShutdownAttempt struct {
	done chan struct{}
	err  error
}

type readinessAttempt struct {
	done     chan struct{}
	err      error
	signaled bool
}

type runResult struct {
	done chan struct{}
	err  error
}

// RunHandle identifies one asynchronously started Server generation. Unlike
// Server.WaitReady, it cannot observe a completed readiness result from an
// earlier retry.
type RunHandle struct {
	readiness *readinessAttempt
	result    *runResult
}

// Ready is closed when this run becomes ready or fails during startup.
func (h *RunHandle) Ready() <-chan struct{} {
	if h == nil || h.readiness == nil {
		return nil
	}
	return h.readiness.done
}

// WaitReady waits for this run's readiness result.
func (h *RunHandle) WaitReady(ctx context.Context) error {
	if h == nil || h.readiness == nil {
		return ErrServerStopped
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-h.readiness.done:
		return h.readiness.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Done is closed after serving and bounded cleanup finish.
func (h *RunHandle) Done() <-chan struct{} {
	if h == nil || h.result == nil {
		return nil
	}
	return h.result.done
}

// Wait returns this run's final serving and cleanup result.
func (h *RunHandle) Wait() error {
	if h == nil || h.result == nil {
		return ErrServerStopped
	}
	<-h.result.done
	return h.result.err
}

// New creates a Server with optional configuration.
func New(config ...Config) *Server {
	if len(config) > 1 {
		panic(fmt.Errorf("%w: expected at most one Config, got %d", ErrInvalidConfiguration, len(config)))
	}
	return &Server{config: normalizeServerConfig(config)}
}

// NewChecked creates a Server after strict Config validation. New remains
// permissive and applies compatibility defaults for existing callers.
func NewChecked(config ...Config) (*Server, error) {
	if len(config) > 1 {
		return nil, fmt.Errorf("%w: expected at most one Config, got %d", ErrInvalidConfiguration, len(config))
	}
	if len(config) > 0 {
		if err := config[0].Validate(); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidConfiguration, err)
		}
	}
	return &Server{config: normalizeServerConfig(config)}, nil
}

func normalizeServerConfig(config []Config) Config {
	cfg := Config{
		Name:            "kpro",
		ShutdownTimeout: DefaultShutdownTimeout,
	}
	if len(config) > 0 {
		cfg = config[0]
		if cfg.Name == "" {
			cfg.Name = "kpro"
		}
		if cfg.ShutdownTimeout <= 0 {
			cfg.ShutdownTimeout = DefaultShutdownTimeout
		}
	}
	return cfg
}

// Mount attaches a message transport such as TCP, UDP, or WebSocket. It
// panics with ErrConfigurationLocked when called after startup has claimed the
// lifecycle; use MountChecked when the mutation may race startup.
func (s *Server) Mount(component transport.Server) *Endpoint {
	endpoint, err := s.MountChecked(component)
	if err != nil {
		panic(err)
	}
	return endpoint
}

// MountChecked attaches a message transport while the Server is in Created.
func (s *Server) MountChecked(component transport.Server) (*Endpoint, error) {
	if isNilComponent(component) {
		return nil, fmt.Errorf("%w: nil message transport", ErrInvalidConfiguration)
	}
	endpoint := &Endpoint{
		transport: component,
		router:    core.NewRouter(s.config.Router),
	}
	if err := s.beginConfiguration(); err != nil {
		endpoint.router.Close()
		return nil, err
	}
	defer s.endConfiguration()
	if err := s.finishMount(endpoint); err != nil {
		endpoint.router.Close()
		return nil, err
	}
	return endpoint, nil
}

// MountRunner attaches a native protocol runner such as HTTP, SSE, or gRPC. It
// panics with ErrConfigurationLocked after configuration freezes.
func (s *Server) MountRunner(runner transport.Runner) transport.Runner {
	mounted, err := s.MountRunnerChecked(runner)
	if err != nil {
		panic(err)
	}
	return mounted
}

// MountRunnerChecked attaches a native runner while the Server is in Created.
func (s *Server) MountRunnerChecked(runner transport.Runner) (transport.Runner, error) {
	if isNilComponent(runner) {
		return nil, fmt.Errorf("%w: nil native runner", ErrInvalidConfiguration)
	}
	if err := s.beginConfiguration(); err != nil {
		return nil, err
	}
	defer s.endConfiguration()
	if err := s.finishMount(runner); err != nil {
		return nil, err
	}
	return runner, nil
}

func (s *Server) beginConfiguration() error {
	if !s.configurationApply.TryLock() {
		return ErrConfigurationBusy
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.State() != StateCreated {
		s.configurationApply.Unlock()
		return ErrConfigurationLocked
	}
	// ListenAndServe claims Starting while holding the same mutex, so every
	// positive Add happens before its Wait and no configuration callback can
	// race runner startup.
	s.configuring.Add(1)
	return nil
}

func (s *Server) endConfiguration() {
	s.configuring.Done()
	s.configurationApply.Unlock()
}

func (s *Server) finishMount(runner transport.Runner) error {
	applied := 0
	for {
		s.mu.Lock()
		state := s.State()
		if state != StateCreated && state != StateStarting {
			s.mu.Unlock()
			return ErrConfigurationLocked
		}
		if applied == len(s.middlewares) {
			s.runners = append(s.runners, runner)
			s.runnerRevision++
			s.mu.Unlock()
			return nil
		}
		pending := append([]core.Middleware(nil), s.middlewares[applied:]...)
		applied = len(s.middlewares)
		s.mu.Unlock()

		// Middleware application can compile user-supplied middleware. Keep it
		// outside Server.mu so callbacks may safely use checked configuration APIs.
		applyMiddlewares(runner, pending...)
	}
}

// Manage registers resources that should shut down after mounted runners stop.
// A successfully stopped resource is not called again; a failed stop remains
// eligible for a later Shutdown retry with a fresh context.
func (s *Server) Manage(resources ...ManagedResource) *Server {
	if err := s.ManageChecked(resources...); err != nil {
		panic(err)
	}
	return s
}

// ManageChecked registers managed resources while the Server is in Created.
func (s *Server) ManageChecked(resources ...ManagedResource) error {
	if len(resources) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.State() != StateCreated {
		return ErrConfigurationLocked
	}
	for _, resource := range resources {
		if resource != nil && isNilComponent(resource) {
			return fmt.Errorf("%w: nil managed resource", ErrInvalidConfiguration)
		}
	}
	for _, resource := range resources {
		if resource != nil {
			s.resources = append(s.resources, &managedResourceState{resource: resource})
		}
	}
	return nil
}

// Use installs app-level middleware on existing and future mounted runners. It
// panics with ErrConfigurationLocked after configuration freezes.
func (s *Server) Use(middlewares ...core.Middleware) *Server {
	if err := s.UseChecked(middlewares...); err != nil {
		panic(err)
	}
	return s
}

// UseChecked installs middleware while the Server is in Created.
func (s *Server) UseChecked(middlewares ...core.Middleware) error {
	if len(middlewares) == 0 {
		return nil
	}
	for index, middleware := range middlewares {
		if middleware == nil {
			return fmt.Errorf("%w: middleware %d is nil", ErrInvalidConfiguration, index)
		}
	}
	if err := s.beginConfiguration(); err != nil {
		return err
	}
	defer s.endConfiguration()
	s.mu.Lock()
	if state := s.State(); state != StateCreated && state != StateStarting {
		s.mu.Unlock()
		return ErrConfigurationLocked
	}
	s.middlewares = append(s.middlewares, middlewares...)
	runners := append([]transport.Runner(nil), s.runners...)
	s.mu.Unlock()
	for _, runner := range runners {
		applyMiddlewares(runner, middlewares...)
	}
	return nil
}

func applyMiddlewares(runner transport.Runner, middlewares ...core.Middleware) {
	if len(middlewares) == 0 {
		return
	}
	if target, ok := runner.(MiddlewareTarget); ok {
		target.ApplyMiddleware(middlewares...)
	}
}

func isNilComponent(component any) bool {
	if component == nil {
		return true
	}
	value := reflect.ValueOf(component)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// ListenAndServe starts all mounted runners and stops the rest when one exits.
func (s *Server) ListenAndServe() error {
	return s.ListenAndServeContext(context.Background())
}

// Run starts all mounted runners with ctx as their parent lifetime.
// Canceling ctx begins the same bounded graceful shutdown used when a runner
// exits. Run is an ergonomic alias for ListenAndServeContext.
func (s *Server) Run(ctx context.Context) error {
	return s.ListenAndServeContext(ctx)
}

// Start synchronously claims a new lifecycle generation and runs it in the
// background. Use the returned handle for race-free readiness and completion
// waits, especially when retrying after validation or empty-server failures.
func (s *Server) Start(parent context.Context) (*RunHandle, error) {
	if parent == nil {
		parent = context.Background()
	}
	attempt, err := s.claimRun()
	if err != nil {
		return nil, err
	}
	handle := &RunHandle{
		readiness: attempt,
		result:    &runResult{done: make(chan struct{})},
	}
	go func() {
		handle.result.err = s.listenAndServeClaimed(parent)
		close(handle.result.done)
	}()
	return handle, nil
}

// ListenAndServeContext starts all mounted runners with ctx as their parent
// lifetime and stops the rest when ctx is canceled or one runner exits.
func (s *Server) ListenAndServeContext(parent context.Context) error {
	if parent == nil {
		parent = context.Background()
	}
	if _, err := s.claimRun(); err != nil {
		return err
	}
	return s.listenAndServeClaimed(parent)
}

func (s *Server) claimRun() (*readinessAttempt, error) {
	s.mu.Lock()
	if !s.state.CompareAndSwap(uint32(StateCreated), uint32(StateStarting)) {
		s.mu.Unlock()
		return nil, s.startError()
	}
	attempt := s.prepareReadyAttempt()
	s.mu.Unlock()
	return attempt, nil
}

func (s *Server) listenAndServeClaimed(parent context.Context) error {
	s.configuring.Wait()

	var runners []transport.Runner
	var errCh chan error
	for {
		s.mu.Lock()
		if s.State() != StateStarting {
			s.mu.Unlock()
			return ErrServerStopped
		}
		if len(s.runners) == 0 {
			s.mu.Unlock()
			s.state.CompareAndSwap(uint32(StateStarting), uint32(StateCreated))
			s.signalReady(ErrNoTransports)
			return ErrNoTransports
		}
		runners = append(runners[:0], s.runners...)
		revision := s.runnerRevision
		s.mu.Unlock()

		if err := validateRunners(runners); err != nil {
			if !s.state.CompareAndSwap(uint32(StateStarting), uint32(StateCreated)) {
				return errors.Join(err, ErrServerStopped)
			}
			s.signalReady(err)
			return err
		}

		s.mu.Lock()
		if s.State() != StateStarting {
			s.mu.Unlock()
			return ErrServerStopped
		}
		if revision != s.runnerRevision {
			s.mu.Unlock()
			continue
		}
		if parent.Err() != nil {
			startupErr := context.Cause(parent)
			s.mu.Unlock()
			s.signalReady(startupErr)
			s.mu.Lock()
			s.beginDrainingLocked()
			s.mu.Unlock()
			return s.finishShutdown(startupErr, runners)
		}
		s.ctx, s.cancel = context.WithCancel(parent)
		ctx := s.ctx
		// Register every runner before publishing the running state by unlocking.
		// Shutdown may otherwise observe a zero WaitGroup between this unlock and
		// the first goroutine, close managed resources, and race runner startup.
		s.wg.Add(len(runners))
		errCh = make(chan error, len(runners))
		entered := make([]chan struct{}, len(runners))
		for i, runner := range runners {
			r := runner
			entered[i] = make(chan struct{})
			started := entered[i]
			go func() {
				defer s.wg.Done()
				close(started)
				errCh <- safeRunnerServe(r, ctx)
			}()
		}
		s.mu.Unlock()

		readyEvents := make(chan struct{}, len(runners))
		for i, runner := range runners {
			ready := runnerReadyChannel(runner)
			started := entered[i]
			go func() {
				if ready == nil {
					ready = started
				}
				select {
				case <-ready:
					readyEvents <- struct{}{}
				case <-ctx.Done():
				}
			}()
		}
		var startupErr error
		startupAborted := false
		for readyCount := 0; readyCount < len(runners); {
			if ctx.Err() != nil {
				startupAborted = true
				if parent.Err() != nil {
					startupErr = context.Cause(parent)
				} else {
					startupErr = ErrServerStopped
				}
				break
			}
			select {
			case <-readyEvents:
				if ctx.Err() != nil {
					startupAborted = true
					if parent.Err() != nil {
						startupErr = context.Cause(parent)
					} else {
						startupErr = ErrServerStopped
					}
					break
				}
				select {
				case startupErr = <-errCh:
					if startupErr == nil {
						startupErr = ErrComponentExitedBeforeReady
					}
					readyCount = len(runners)
				default:
					readyCount++
				}
			case startupErr = <-errCh:
				if startupErr == nil {
					startupErr = ErrComponentExitedBeforeReady
				}
				readyCount = len(runners)
			case <-ctx.Done():
				startupAborted = true
				if parent.Err() != nil {
					startupErr = context.Cause(parent)
				} else {
					startupErr = ErrServerStopped
				}
				readyCount = len(runners)
			}
		}
		if startupErr != nil {
			s.signalReady(startupErr)
			s.mu.Lock()
			s.beginDrainingLocked()
			s.cancel()
			s.mu.Unlock()
			if startupAborted && parent.Err() == nil {
				startupErr = nil
			}
			return s.finishShutdown(startupErr, runners)
		}
		if !s.state.CompareAndSwap(uint32(StateStarting), uint32(StateRunning)) {
			startupErr = ErrServerStopped
			s.signalReady(startupErr)
			s.mu.Lock()
			if s.cancel != nil {
				s.cancel()
			}
			s.mu.Unlock()
			if parent.Err() == nil {
				startupErr = nil
			}
			return s.finishShutdown(startupErr, runners)
		}
		s.signalReady(nil)
		break
	}

	var runnerErr error
	select {
	case runnerErr = <-errCh:
	case <-parent.Done():
		runnerErr = context.Cause(parent)
	}
	s.mu.Lock()
	s.beginDrainingLocked()
	s.cancel()
	s.mu.Unlock()
	return s.finishShutdown(runnerErr, runners)
}

func (s *Server) finishShutdown(runnerErr error, runners []transport.Runner) error {
	shutdownCtx, shutdownCancel := context.WithTimeout(
		context.Background(),
		s.config.ShutdownTimeout,
	)
	defer shutdownCancel()
	shutdownErr := s.shutdownRunners(shutdownCtx, runners)
	waitErr := s.wait(shutdownCtx)
	var resourceErr error
	if shutdownErr == nil && waitErr == nil {
		resourceCtx, resourceCancel := context.WithTimeout(
			context.Background(),
			s.config.ShutdownTimeout,
		)
		resourceErr = s.shutdownResources(resourceCtx)
		resourceCancel()
		s.state.Store(uint32(StateStopped))
	}
	cleanupErr := errors.Join(shutdownErr, waitErr, resourceErr)
	if cleanupErr != nil && IsContextTermination(runnerErr) {
		// Cancellation initiated shutdown; once cleanup fails, returning the
		// cancellation wrapper would let common errors.Is checks hide the real
		// operational failure.
		return cleanupErr
	}
	return errors.Join(runnerErr, cleanupErr)
}

// IsContextTermination reports whether every leaf in err is cancellation or a
// deadline. It is safe for entry points to suppress only when no cleanup error
// is joined into the result.
func IsContextTermination(err error) bool {
	if err == nil {
		return false
	}
	if err == context.Canceled || err == context.DeadlineExceeded {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !IsContextTermination(child) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return IsContextTermination(wrapped.Unwrap())
	}
	return false
}

func safeRunnerServe(runner transport.Runner, ctx context.Context) (err error) {
	name := safeComponentName(runner)
	defer func() {
		if recovered := recover(); recovered != nil {
			err = wrapNamedComponentError(
				name,
				"serve",
				fmt.Errorf("%w: %v", ErrComponentPanic, recovered),
			)
			return
		}
		err = wrapNamedComponentError(name, "serve", err)
	}()
	err = runner.ListenAndServe(ctx)
	return err
}

func safeComponentName(component transport.Component) (name string) {
	name = fmt.Sprintf("%T", component)
	if isNilComponent(component) {
		return name
	}
	defer func() {
		_ = recover()
	}()
	if candidate := component.Name(); candidate != "" {
		name = candidate
	}
	return name
}

func runnerReadyChannel(runner transport.Runner) <-chan struct{} {
	if endpoint, ok := runner.(*Endpoint); ok && !isNilComponent(endpoint.transport) {
		if provider, ok := endpoint.transport.(transport.ReadinessProvider); ok {
			return provider.Ready()
		}
		return nil
	}
	if provider, ok := runner.(transport.ReadinessProvider); ok {
		return provider.Ready()
	}
	return nil
}

func validateRunners(runners []transport.Runner) error {
	var validationErr error
	for _, runner := range runners {
		if isNilComponent(runner) {
			validationErr = errors.Join(validationErr, fmt.Errorf(
				"%w: nil component",
				ErrInvalidConfiguration,
			))
			continue
		}
		validator, ok := runner.(transport.Validator)
		if !ok {
			continue
		}
		if err := validator.Validate(); err != nil {
			name := runner.Name()
			if name == "" {
				name = fmt.Sprintf("%T", runner)
			}
			validationErr = errors.Join(validationErr, fmt.Errorf(
				"%w: component %q: %w",
				ErrInvalidConfiguration,
				name,
				err,
			))
		}
	}
	return validationErr
}

// Shutdown asks all mounted runners to stop and waits for the server lifecycle to drain.
func (s *Server) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	s.beginDrainingLocked()
	s.signalReady(ErrServerStopped)
	if s.cancel != nil {
		s.cancel()
	}
	runners := append([]transport.Runner(nil), s.runners...)
	s.mu.Unlock()

	runnerErr := s.shutdownRunners(ctx, runners)
	if waitErr := s.wait(ctx); runnerErr == nil && waitErr == nil {
		resourceErr := s.shutdownResources(ctx)
		s.state.Store(uint32(StateStopped))
		return errors.Join(runnerErr, resourceErr)
	} else {
		return errors.Join(runnerErr, waitErr)
	}
}

// State returns the current observable one-shot Server lifecycle state.
func (s *Server) State() State {
	return State(s.state.Load())
}

// Ready returns a channel closed after every readiness-aware runner has bound
// its listener. Legacy runners are considered ready when ListenAndServe is
// entered. If startup fails first, the channel is also closed and WaitReady
// returns that failure.
func (s *Server) Ready() <-chan struct{} {
	s.readyMu.Lock()
	defer s.readyMu.Unlock()
	return s.readyAttemptLocked().done
}

// WaitReady waits for startup readiness or a startup failure.
func (s *Server) WaitReady(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.readyMu.Lock()
	attempt := s.readyAttemptLocked()
	s.readyMu.Unlock()
	select {
	case <-attempt.done:
		return attempt.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) signalReady(err error) {
	s.readyMu.Lock()
	defer s.readyMu.Unlock()
	attempt := s.readyAttemptLocked()
	if attempt.signaled {
		return
	}
	attempt.err = err
	attempt.signaled = true
	close(attempt.done)
}

func (s *Server) prepareReadyAttempt() *readinessAttempt {
	s.readyMu.Lock()
	defer s.readyMu.Unlock()
	if s.readiness == nil || s.readiness.signaled {
		s.readiness = &readinessAttempt{done: make(chan struct{})}
	}
	return s.readiness
}

func (s *Server) readyAttemptLocked() *readinessAttempt {
	if s.readiness == nil {
		s.readiness = &readinessAttempt{done: make(chan struct{})}
	}
	return s.readiness
}

// Name returns the configured diagnostic server name.
func (s *Server) Name() string {
	return s.config.Name
}

// Descriptors returns mounted transport metadata in mount order.
func (s *Server) Descriptors() []transport.Descriptor {
	s.mu.Lock()
	runners := append([]transport.Runner(nil), s.runners...)
	s.mu.Unlock()
	descriptors := make([]transport.Descriptor, 0, len(runners))
	for _, runner := range runners {
		descriptors = append(descriptors, transport.Describe(runner))
	}
	return descriptors
}

func (s *Server) startError() error {
	switch s.State() {
	case StateStarting, StateRunning:
		return ErrServerAlreadyStarted
	case StateDraining, StateStopped:
		return ErrServerStopped
	default:
		return ErrServerAlreadyStarted
	}
}

func (s *Server) beginDrainingLocked() {
	for {
		state := s.State()
		switch state {
		case StateCreated, StateStarting, StateRunning:
			if s.state.CompareAndSwap(uint32(state), uint32(StateDraining)) {
				return
			}
		case StateDraining, StateStopped:
			return
		default:
			return
		}
	}
}

func (s *Server) wait(ctx context.Context) error {
	s.waitMu.Lock()
	if s.waitDone == nil {
		s.waitDone = make(chan struct{})
	}
	done := s.waitDone
	if !s.waitStarted {
		s.waitStarted = true
		go func() {
			s.wg.Wait()
			close(done)
		}()
	}
	s.waitMu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) shutdownRunners(ctx context.Context, runners []transport.Runner) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.runnerShutdownMu.Lock()
	attempt := s.runnerShutdownCurrent
	if attempt == nil {
		attempt = &runnerShutdownAttempt{done: make(chan struct{})}
		s.runnerShutdownCurrent = attempt
		snapshot := append([]transport.Runner(nil), runners...)
		go func(attempt *runnerShutdownAttempt) {
			results := make(chan error, len(snapshot))
			for _, runner := range snapshot {
				runner := runner
				go func() { results <- safeRunnerShutdown(ctx, runner) }()
			}
			for range snapshot {
				attempt.err = errors.Join(attempt.err, <-results)
			}
			s.runnerShutdownMu.Lock()
			close(attempt.done)
			if s.runnerShutdownCurrent == attempt {
				s.runnerShutdownCurrent = nil
			}
			s.runnerShutdownMu.Unlock()
		}(attempt)
	}
	s.runnerShutdownMu.Unlock()
	select {
	case <-attempt.done:
		return attempt.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func safeRunnerShutdown(ctx context.Context, runner transport.Runner) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("shutdown component %T panic: %v", runner, recovered)
		}
	}()
	return wrapComponentError(runner, "shutdown", runner.Shutdown(ctx))
}

func (s *Server) shutdownResources(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	resources := append([]*managedResourceState(nil), s.resources...)
	s.mu.Unlock()
	var resourceErr error
	for i := len(resources) - 1; i >= 0; i-- {
		resourceErr = errors.Join(resourceErr, resources[i].shutdown(ctx))
	}
	return resourceErr
}

func (r *managedResourceState) shutdown(ctx context.Context) error {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return nil
	}
	attempt := r.current
	if attempt == nil {
		attempt = &resourceShutdownAttempt{done: make(chan struct{})}
		r.current = attempt
		go func() {
			attempt.err = safeResourceShutdown(ctx, r.resource)
			r.mu.Lock()
			if attempt.err == nil {
				r.stopped = true
			}
			if r.current == attempt {
				r.current = nil
			}
			close(attempt.done)
			r.mu.Unlock()
		}()
	}
	r.mu.Unlock()
	select {
	case <-attempt.done:
		return attempt.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func safeResourceShutdown(ctx context.Context, resource ManagedResource) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &ComponentError{
				Component: fmt.Sprintf("%T", resource),
				Phase:     "shutdown",
				Err:       fmt.Errorf("panic: %v", recovered),
			}
		}
	}()
	return wrapNamedComponentError(fmt.Sprintf("%T", resource), "shutdown", resource.Shutdown(ctx))
}

// Endpoint binds a message transport to a core Router.
type Endpoint struct {
	transport transport.Server
	router    *core.Router
}

// Use installs middleware on the endpoint router and returns the endpoint for chaining.
func (e *Endpoint) Use(middlewares ...core.Middleware) *Endpoint {
	e.ApplyMiddleware(middlewares...)
	return e
}

// ApplyMiddleware implements MiddlewareTarget for app-level Server.Use wiring.
func (e *Endpoint) ApplyMiddleware(middlewares ...core.Middleware) {
	e.router.Use(middlewares...)
}

// Handle registers a sync message handler.
func (e *Endpoint) Handle(msgID uint32, h core.Handler) *Endpoint {
	e.router.Handle(msgID, h)
	return e
}

// HandleChecked registers a sync message handler and reports invalid,
// duplicate, or late registration.
func (e *Endpoint) HandleChecked(msgID uint32, h core.Handler) error {
	router, err := e.checkedRouter()
	if err != nil {
		return err
	}
	return router.HandleChecked(msgID, h)
}

// HandleSync registers a per-session serialized message handler.
func (e *Endpoint) HandleSync(msgID uint32, h core.Handler) *Endpoint {
	e.router.HandleSync(msgID, h)
	return e
}

// HandleSyncChecked registers a per-session serialized message handler and
// reports invalid, duplicate, or late registration.
func (e *Endpoint) HandleSyncChecked(msgID uint32, h core.Handler) error {
	router, err := e.checkedRouter()
	if err != nil {
		return err
	}
	return router.HandleSyncChecked(msgID, h)
}

// HandleAsync registers a message handler that runs through async workers.
func (e *Endpoint) HandleAsync(msgID uint32, h core.Handler) *Endpoint {
	e.router.HandleAsync(msgID, h)
	return e
}

// HandleAsyncChecked registers a message handler that runs through async
// workers and reports invalid, duplicate, or late registration.
func (e *Endpoint) HandleAsyncChecked(msgID uint32, h core.Handler) error {
	router, err := e.checkedRouter()
	if err != nil {
		return err
	}
	return router.HandleAsyncChecked(msgID, h)
}

func (e *Endpoint) checkedRouter() (*core.Router, error) {
	if e == nil {
		return nil, fmt.Errorf("%w: endpoint is nil", ErrInvalidConfiguration)
	}
	if e.router == nil {
		return nil, fmt.Errorf("%w: endpoint router is nil", ErrInvalidConfiguration)
	}
	return e.router, nil
}

// Snapshot returns a point-in-time view of this endpoint's Router runtime.
func (e *Endpoint) Snapshot() core.RouterStats {
	return e.router.Snapshot()
}

// Validate forwards optional startup validation to the message transport.
func (e *Endpoint) Validate() error {
	if isNilComponent(e.transport) {
		return errors.New("message transport is nil")
	}
	if validator, ok := e.transport.(transport.Validator); ok {
		return validator.Validate()
	}
	return nil
}

// Descriptor forwards transport metadata while preserving Endpoint's message
// startup contract even when a custom provider advertises the wrong mode.
func (e *Endpoint) Descriptor() transport.Descriptor {
	if isNilComponent(e.transport) {
		return transport.Descriptor{Mode: transport.ModeMessage}
	}
	descriptor := transport.Describe(e.transport)
	descriptor.Mode = transport.ModeMessage
	descriptor.Capabilities |= transport.CapabilityMiddleware
	return descriptor
}

// Name returns the mounted transport name.
func (e *Endpoint) Name() string {
	if isNilComponent(e.transport) {
		return "message"
	}
	return e.transport.Name()
}

// ListenAndServe starts the underlying message transport.
func (e *Endpoint) ListenAndServe(ctx context.Context) error {
	return e.transport.ListenAndServe(ctx, e.dispatch)
}

// Shutdown stops the underlying transport and router.
func (e *Endpoint) Shutdown(ctx context.Context) error {
	transportErr := e.transport.Shutdown(ctx)
	routerErr := e.router.Shutdown(ctx)
	return errors.Join(transportErr, routerErr)
}

func (e *Endpoint) dispatch(ctx core.Context) error {
	// Router rejection is already translated into a bounded, public MsgError
	// response and accounted in RouterStats. Do not feed it back into the
	// transport's direct-handler error policy: closing the session here can
	// discard that queued response and turns normal admission control into an
	// unexpected EOF for clients.
	_ = e.router.Dispatch(ctx)
	return nil
}

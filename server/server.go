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

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/transport"
	"github.com/saramand9/bignet/kpro/transport/grpcx"
	http3x "github.com/saramand9/bignet/kpro/transport/http3"
	"github.com/saramand9/bignet/kpro/transport/httpx"
	"github.com/saramand9/bignet/kpro/transport/kcp"
	quictransport "github.com/saramand9/bignet/kpro/transport/quic"
	"github.com/saramand9/bignet/kpro/transport/sse"
	"github.com/saramand9/bignet/kpro/transport/tcp"
	"github.com/saramand9/bignet/kpro/transport/udp"
	"github.com/saramand9/bignet/kpro/transport/websocket"
)

var (
	// ErrNoTransports is returned when ListenAndServe starts without mounted transports.
	ErrNoTransports = errors.New("no transports registered")
	// ErrInvalidConfiguration reports a failed Config or component startup check.
	ErrInvalidConfiguration = errors.New("invalid server configuration")
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
	_ MiddlewareTarget             = (*httpx.Endpoint)(nil)
	_ MiddlewareTarget             = (*http3x.Endpoint)(nil)
	_ MiddlewareTarget             = (*sse.Endpoint)(nil)
	_ MiddlewareTarget             = (*grpcx.Server)(nil)
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
}

// New creates a Server with optional configuration.
func New(config ...Config) *Server {
	return &Server{config: normalizeServerConfig(config)}
}

// NewChecked creates a Server after strict Config validation. New remains
// permissive and applies compatibility defaults for existing callers.
func NewChecked(config ...Config) (*Server, error) {
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

// TCP mounts a TCP message endpoint at addr.
func (s *Server) TCP(addr string, config ...tcp.Config) *Endpoint {
	cfg := tcp.Config{}
	if len(config) > 0 {
		cfg = config[0]
	}
	cfg.Addr = addr
	return s.Mount(tcp.New(cfg))
}

// KCP mounts a KCP message endpoint at addr.
func (s *Server) KCP(addr string, config ...kcp.Config) *Endpoint {
	cfg := kcp.Config{}
	if len(config) > 0 {
		cfg = config[0]
	}
	cfg.Addr = addr
	return s.Mount(kcp.New(cfg))
}

// QUIC mounts a QUIC message endpoint at addr.
func (s *Server) QUIC(addr string, config quictransport.Config) *Endpoint {
	config.Addr = addr
	return s.Mount(quictransport.New(config))
}

// UDP mounts a UDP packet endpoint at addr.
func (s *Server) UDP(addr string, config ...udp.Config) *Endpoint {
	cfg := udp.Config{}
	if len(config) > 0 {
		cfg = config[0]
	}
	cfg.Addr = addr
	return s.Mount(udp.New(cfg))
}

// WebSocket mounts a WebSocket message endpoint at addr and path.
func (s *Server) WebSocket(addr, path string, config ...websocket.Config) *Endpoint {
	cfg := websocket.Config{}
	if len(config) > 0 {
		cfg = config[0]
	}
	cfg.Addr = addr
	cfg.Path = path
	return s.Mount(websocket.New(cfg))
}

// HTTP mounts a native HTTP endpoint at addr.
func (s *Server) HTTP(addr string, config ...httpx.Config) *httpx.Endpoint {
	cfg := httpx.Config{}
	if len(config) > 0 {
		cfg = config[0]
	}
	cfg.Addr = addr
	endpoint := httpx.New(cfg)
	s.MountRunner(endpoint)
	return endpoint
}

// HTTPS mounts a native HTTPS endpoint at addr using config TLS files.
func (s *Server) HTTPS(addr string, config httpx.Config) *httpx.Endpoint {
	config.Addr = addr
	config.TLS = true
	endpoint := httpx.New(config)
	s.MountRunner(endpoint)
	return endpoint
}

// GRPC mounts a gRPC server at addr.
func (s *Server) GRPC(addr string, options ...grpcx.Option) *grpcx.Server {
	endpoint := grpcx.NewServer(addr, options...)
	s.MountRunner(endpoint)
	return endpoint
}

// HTTP3 mounts a native HTTP/3 endpoint at addr.
func (s *Server) HTTP3(addr string, config http3x.Config) *http3x.Endpoint {
	config.Addr = addr
	endpoint := http3x.New(config)
	s.MountRunner(endpoint)
	return endpoint
}

// SSE mounts a Server-Sent Events endpoint at addr and path.
func (s *Server) SSE(addr, path string, config ...sse.Config) *sse.Endpoint {
	cfg := sse.Config{}
	if len(config) > 0 {
		cfg = config[0]
	}
	cfg.Addr = addr
	cfg.Path = path
	endpoint := sse.New(cfg)
	s.MountRunner(endpoint)
	return endpoint
}

// ListenAndServe starts all mounted runners and stops the rest when one exits.
func (s *Server) ListenAndServe() error {
	s.mu.Lock()
	if !s.state.CompareAndSwap(uint32(StateCreated), uint32(StateStarting)) {
		s.mu.Unlock()
		return s.startError()
	}
	s.mu.Unlock()
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
			return ErrNoTransports
		}
		runners = append(runners[:0], s.runners...)
		revision := s.runnerRevision
		s.mu.Unlock()

		if err := validateRunners(runners); err != nil {
			if !s.state.CompareAndSwap(uint32(StateStarting), uint32(StateCreated)) {
				return errors.Join(err, ErrServerStopped)
			}
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
		s.ctx, s.cancel = context.WithCancel(context.Background())
		ctx := s.ctx
		// Register every runner before publishing the running state by unlocking.
		// Shutdown may otherwise observe a zero WaitGroup between this unlock and
		// the first goroutine, close managed resources, and race runner startup.
		s.wg.Add(len(runners))
		errCh = make(chan error, len(runners))
		s.state.Store(uint32(StateRunning))
		for _, runner := range runners {
			r := runner
			go func() {
				defer s.wg.Done()
				errCh <- r.ListenAndServe(ctx)
			}()
		}
		s.mu.Unlock()
		break
	}

	runnerErr := <-errCh
	s.mu.Lock()
	s.beginDrainingLocked()
	s.cancel()
	s.mu.Unlock()
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
	return errors.Join(runnerErr, shutdownErr, waitErr, resourceErr)
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
	return runner.Shutdown(ctx)
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
	defer r.mu.Unlock()
	if r.stopped {
		return nil
	}
	err := r.resource.Shutdown(ctx)
	if err == nil {
		r.stopped = true
	}
	return err
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

// HandleSync registers a per-session serialized message handler.
func (e *Endpoint) HandleSync(msgID uint32, h core.Handler) *Endpoint {
	e.router.HandleSync(msgID, h)
	return e
}

// HandleAsync registers a message handler that runs through async workers.
func (e *Endpoint) HandleAsync(msgID uint32, h core.Handler) *Endpoint {
	e.router.HandleAsync(msgID, h)
	return e
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
	return e.router.Dispatch(ctx)
}

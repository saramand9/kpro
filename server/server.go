// File: server/server.go
// Purpose: Implements or tests the high-level KPro server orchestration layer.

package server

import (
	"context"
	"errors"
	"sync"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/kprogrpc"
	"github.com/saramand9/bignet/kpro/transport"
	"github.com/saramand9/bignet/kpro/transport/httpx"
	"github.com/saramand9/bignet/kpro/transport/sse"
	"github.com/saramand9/bignet/kpro/transport/tcp"
	"github.com/saramand9/bignet/kpro/transport/udp"
	"github.com/saramand9/bignet/kpro/transport/websocket"
)

// ErrNoTransports is returned when ListenAndServe starts without mounted transports.
var ErrNoTransports = errors.New("no transports registered")

// MiddlewareTarget can be implemented by custom runners that want app-level
// middleware installed through Server.Use.
type MiddlewareTarget interface {
	// ApplyMiddleware installs middleware supplied by Server.Use.
	ApplyMiddleware(...core.Middleware)
}

var (
	_ MiddlewareTarget = (*Endpoint)(nil)
	_ MiddlewareTarget = (*httpx.Endpoint)(nil)
	_ MiddlewareTarget = (*sse.Endpoint)(nil)
	_ MiddlewareTarget = (*kprogrpc.Server)(nil)
)

// Config controls the high-level server and the router used by message transports.
type Config struct {
	// Name is a human-readable server name used by examples and diagnostics.
	Name string
	// Router configures message transports mounted through TCP, UDP, or WebSocket.
	Router core.RouterConfig
}

// Server coordinates one or more protocol runners under a shared lifecycle.
type Server struct {
	config Config

	mu          sync.Mutex
	runners     []transport.Runner
	middlewares []core.Middleware

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New creates a Server with optional configuration.
func New(config ...Config) *Server {
	cfg := Config{Name: "kpro"}
	if len(config) > 0 {
		cfg = config[0]
		if cfg.Name == "" {
			cfg.Name = "kpro"
		}
	}
	return &Server{config: cfg}
}

// Mount attaches a message transport such as TCP, UDP, or WebSocket.
func (s *Server) Mount(transport transport.Server) *Endpoint {
	endpoint := &Endpoint{
		transport: transport,
		router:    core.NewRouter(s.config.Router),
	}
	s.mu.Lock()
	middlewares := append([]core.Middleware(nil), s.middlewares...)
	s.runners = append(s.runners, endpoint)
	s.mu.Unlock()
	endpoint.Use(middlewares...)
	return endpoint
}

// MountRunner attaches a native protocol runner such as HTTP, SSE, or gRPC.
func (s *Server) MountRunner(runner transport.Runner) transport.Runner {
	s.mu.Lock()
	middlewares := append([]core.Middleware(nil), s.middlewares...)
	s.runners = append(s.runners, runner)
	s.mu.Unlock()
	applyMiddlewares(runner, middlewares...)
	return runner
}

// Use installs app-level middleware on existing and future mounted runners.
func (s *Server) Use(middlewares ...core.Middleware) *Server {
	if len(middlewares) == 0 {
		return s
	}
	s.mu.Lock()
	s.middlewares = append(s.middlewares, middlewares...)
	runners := append([]transport.Runner(nil), s.runners...)
	s.mu.Unlock()
	for _, runner := range runners {
		applyMiddlewares(runner, middlewares...)
	}
	return s
}

func applyMiddlewares(runner transport.Runner, middlewares ...core.Middleware) {
	if len(middlewares) == 0 {
		return
	}
	if target, ok := runner.(MiddlewareTarget); ok {
		target.ApplyMiddleware(middlewares...)
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
func (s *Server) GRPC(addr string, options ...kprogrpc.Option) *kprogrpc.Server {
	endpoint := kprogrpc.NewServer(addr, options...)
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
	if len(s.runners) == 0 {
		s.mu.Unlock()
		return ErrNoTransports
	}
	runners := append([]transport.Runner(nil), s.runners...)
	s.ctx, s.cancel = context.WithCancel(context.Background())
	ctx := s.ctx
	s.mu.Unlock()

	errCh := make(chan error, len(runners))
	for _, runner := range runners {
		r := runner
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			errCh <- r.ListenAndServe(ctx)
		}()
	}

	err := <-errCh
	s.cancel()
	_ = s.shutdownRunners(context.Background(), runners)
	s.wg.Wait()
	return err
}

// Shutdown asks all mounted runners to stop and waits for the server lifecycle to drain.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	runners := append([]transport.Runner(nil), s.runners...)
	s.mu.Unlock()

	err := s.shutdownRunners(ctx, runners)
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) shutdownRunners(ctx context.Context, runners []transport.Runner) error {
	for _, runner := range runners {
		if err := runner.Shutdown(ctx); err != nil {
			return err
		}
	}
	return nil
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

// Name returns the mounted transport name.
func (e *Endpoint) Name() string {
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
	if transportErr != nil {
		return transportErr
	}
	return routerErr
}

func (e *Endpoint) dispatch(ctx core.Context) error {
	return e.router.Dispatch(ctx)
}

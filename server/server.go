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

var ErrNoTransports = errors.New("no transports registered")

type Config struct {
	Name   string
	Router core.RouterConfig
}

type Server struct {
	config Config

	mu          sync.Mutex
	runners     []transport.Runner
	middlewares []core.Middleware

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

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

func (s *Server) MountRunner(runner transport.Runner) transport.Runner {
	s.mu.Lock()
	middlewares := append([]core.Middleware(nil), s.middlewares...)
	s.runners = append(s.runners, runner)
	s.mu.Unlock()
	applyMiddlewares(runner, middlewares...)
	return runner
}

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
	switch endpoint := runner.(type) {
	case *Endpoint:
		endpoint.Use(middlewares...)
	case *httpx.Endpoint:
		endpoint.Use(middlewares...)
	case *sse.Endpoint:
		endpoint.Use(middlewares...)
	case *kprogrpc.Server:
		endpoint.Use(middlewares...)
	}
}
func (s *Server) TCP(addr string, config ...tcp.Config) *Endpoint {
	cfg := tcp.Config{}
	if len(config) > 0 {
		cfg = config[0]
	}
	cfg.Addr = addr
	return s.Mount(tcp.New(cfg))
}

func (s *Server) UDP(addr string, config ...udp.Config) *Endpoint {
	cfg := udp.Config{}
	if len(config) > 0 {
		cfg = config[0]
	}
	cfg.Addr = addr
	return s.Mount(udp.New(cfg))
}

func (s *Server) WebSocket(addr, path string, config ...websocket.Config) *Endpoint {
	cfg := websocket.Config{}
	if len(config) > 0 {
		cfg = config[0]
	}
	cfg.Addr = addr
	cfg.Path = path
	return s.Mount(websocket.New(cfg))
}

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

func (s *Server) HTTPS(addr string, config httpx.Config) *httpx.Endpoint {
	config.Addr = addr
	config.TLS = true
	endpoint := httpx.New(config)
	s.MountRunner(endpoint)
	return endpoint
}

func (s *Server) GRPC(addr string, options ...kprogrpc.Option) *kprogrpc.Server {
	endpoint := kprogrpc.NewServer(addr, options...)
	s.MountRunner(endpoint)
	return endpoint
}

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

type Endpoint struct {
	transport transport.Server
	router    *core.Router
}

func (e *Endpoint) Use(middlewares ...core.Middleware) *Endpoint {
	e.router.Use(middlewares...)
	return e
}

func (e *Endpoint) Handle(msgID uint32, h core.Handler) *Endpoint {
	e.router.Handle(msgID, h)
	return e
}

func (e *Endpoint) HandleSync(msgID uint32, h core.Handler) *Endpoint {
	e.router.HandleSync(msgID, h)
	return e
}

func (e *Endpoint) HandleAsync(msgID uint32, h core.Handler) *Endpoint {
	e.router.HandleAsync(msgID, h)
	return e
}

func (e *Endpoint) Name() string {
	return e.transport.Name()
}

func (e *Endpoint) ListenAndServe(ctx context.Context) error {
	return e.transport.ListenAndServe(ctx, e.dispatch)
}

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

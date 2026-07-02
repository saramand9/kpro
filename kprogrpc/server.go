package kprogrpc

import (
	"context"
	"errors"
	"hash/fnv"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/saramand9/bignet/kpro/core"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

const DefaultShutdownTimeout = 5 * time.Second

type Option func(*Server)

type Server struct {
	addr            string
	grpc            *grpc.Server
	serverOptions   []grpc.ServerOption
	middlewares     []core.Middleware
	middlewareValue atomic.Value
	shutdownTimeout time.Duration

	mu sync.Mutex
	ln net.Listener
}

func NewServer(addr string, opts ...Option) *Server {
	s := &Server{
		addr:            addr,
		shutdownTimeout: DefaultShutdownTimeout,
	}
	s.middlewareValue.Store([]core.Middleware{})
	for _, opt := range opts {
		opt(s)
	}
	if s.grpc == nil {
		s.grpc = s.newGRPCServer()
	}
	return s
}

func WithServerOptions(opts ...grpc.ServerOption) Option {
	return func(s *Server) {
		s.serverOptions = append(s.serverOptions, opts...)
	}
}

func WithShutdownTimeout(timeout time.Duration) Option {
	return func(s *Server) {
		if timeout > 0 {
			s.shutdownTimeout = timeout
		}
	}
}

func (s *Server) RegisterService(desc *grpc.ServiceDesc, impl any) {
	s.grpc.RegisterService(desc, impl)
}

func (s *Server) Name() string {
	return string(core.TransportGRPC)
}

func (s *Server) Use(middlewares ...core.Middleware) *Server {
	s.ApplyMiddleware(middlewares...)
	return s
}

func (s *Server) ApplyMiddleware(middlewares ...core.Middleware) {
	s.mu.Lock()
	s.middlewares = append(s.middlewares, middlewares...)
	snapshot := append([]core.Middleware(nil), s.middlewares...)
	s.middlewareValue.Store(snapshot)
	s.mu.Unlock()
}

func (s *Server) Serve() error {
	return s.ListenAndServe(context.Background())
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	return s.ServeListenerContext(ctx, ln)
}

func (s *Server) ServeListener(ln net.Listener) error {
	return s.ServeListenerContext(context.Background(), ln)
}

func (s *Server) ServeListenerContext(ctx context.Context, ln net.Listener) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()

	if done := ctx.Done(); done != nil {
		go func() {
			<-done
			shutdownCtx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
			defer cancel()
			_ = s.Shutdown(shutdownCtx)
		}()
	}

	err := s.grpc.Serve(ln)
	if errors.Is(err, grpc.ErrServerStopped) {
		return nil
	}
	return err
}

func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}

func (s *Server) GRPCServer() *grpc.Server {
	return s.grpc
}

func (s *Server) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.shutdownTimeout)
		defer cancel()
	}

	done := make(chan struct{})
	go func() {
		s.grpc.GracefulStop()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		s.grpc.Stop()
		return ctx.Err()
	}
}

func (s *Server) GracefulStop() {
	s.grpc.GracefulStop()
}

func (s *Server) Stop() {
	s.grpc.Stop()
}

type UnaryInvocationNative struct {
	Context context.Context
	Request any
	Info    *grpc.UnaryServerInfo
}

type StreamInvocationNative struct {
	Server any
	Stream grpc.ServerStream
	Info   *grpc.StreamServerInfo
}

func (s *Server) newGRPCServer() *grpc.Server {
	opts := append([]grpc.ServerOption(nil), s.serverOptions...)
	opts = append(opts,
		grpc.ChainUnaryInterceptor(s.unaryCallInterceptor()),
		grpc.ChainStreamInterceptor(s.streamCallInterceptor()),
	)
	return grpc.NewServer(opts...)
}

func (s *Server) unaryCallInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		middlewares := s.middlewaresSnapshot()
		if len(middlewares) == 0 {
			return handler(ctx, req)
		}

		var resp any
		inv := newUnaryInvocation(ctx, req, info)
		invHandler := core.Chain(middlewares...)(func(core.Invocation) error {
			var err error
			resp, err = handler(ctx, req)
			return err
		})
		err := invHandler(inv)
		return resp, err
	}
}

func (s *Server) streamCallInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		middlewares := s.middlewaresSnapshot()
		if len(middlewares) == 0 {
			return handler(srv, stream)
		}

		inv := newStreamInvocation(srv, stream, info)
		invHandler := core.Chain(middlewares...)(func(core.Invocation) error {
			return handler(srv, stream)
		})
		return invHandler(inv)
	}
}

func (s *Server) middlewaresSnapshot() []core.Middleware {
	middlewares, _ := s.middlewareValue.Load().([]core.Middleware)
	return middlewares
}

func newUnaryInvocation(ctx context.Context, req any, info *grpc.UnaryServerInfo) core.Invocation {
	remote := ""
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		remote = p.Addr.String()
	}
	service, method := splitFullMethod(info.FullMethod)
	return core.NewInvocation(core.InvocationOptions{
		Context:   ctx,
		Transport: core.TransportGRPC,
		Operation: info.FullMethod,
		ConnID:    connIDFromRemote(remote),
		Remote:    remote,
		Headers:   metadataHeaders(ctx),
		Meta: map[string]any{
			"full_method": info.FullMethod,
			"service":     service,
			"method":      method,
		},
		Payload: req,
		Native: UnaryInvocationNative{
			Context: ctx,
			Request: req,
			Info:    info,
		},
		Done: ctx.Done(),
	})
}

func newStreamInvocation(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo) core.Invocation {
	ctx := stream.Context()
	remote := ""
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		remote = p.Addr.String()
	}
	service, method := splitFullMethod(info.FullMethod)
	return core.NewInvocation(core.InvocationOptions{
		Context:   ctx,
		Transport: core.TransportGRPC,
		Operation: info.FullMethod,
		ConnID:    connIDFromRemote(remote),
		Remote:    remote,
		Headers:   metadataHeaders(ctx),
		Meta: map[string]any{
			"full_method":   info.FullMethod,
			"service":       service,
			"method":        method,
			"stream":        true,
			"client_stream": info.IsClientStream,
			"server_stream": info.IsServerStream,
		},
		Native: StreamInvocationNative{
			Server: srv,
			Stream: stream,
			Info:   info,
		},
		Done: ctx.Done(),
	})
}

func metadataHeaders(ctx context.Context) map[string]string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok || len(md) == 0 {
		return nil
	}
	headers := make(map[string]string, len(md))
	for key, values := range md {
		if len(values) > 0 {
			headers[key] = values[0]
		}
	}
	return headers
}

func splitFullMethod(fullMethod string) (string, string) {
	trimmed := strings.TrimPrefix(fullMethod, "/")
	idx := strings.LastIndex(trimmed, "/")
	if idx < 0 {
		return trimmed, ""
	}
	return trimmed[:idx], trimmed[idx+1:]
}

func connIDFromRemote(remote string) uint64 {
	if remote == "" {
		return 1
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(remote))
	id := hash.Sum64()
	if id == 0 {
		return 1
	}
	return id
}

var _ grpc.ServiceRegistrar = (*Server)(nil)

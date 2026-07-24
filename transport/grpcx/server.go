// File: transport/grpcx/server.go
// Purpose: Implements the gRPC integration for KPro.

package grpcx

import (
	"context"
	"errors"
	"hash/fnv"
	"net"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/transport/internal/serveutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"
)

// DefaultShutdownTimeout is the fallback graceful-stop timeout.
const DefaultShutdownTimeout = 5 * time.Second

// Option configures a KPro gRPC server.
type Option func(*Server)

// ErrorMapper converts handler and middleware errors to stable gRPC status
// errors. Mappers must not expose sensitive internal details.
type ErrorMapper func(error) error

// Server wraps google.golang.org/grpc.Server with KPro middleware invocation support.
type Server struct {
	addr            string
	grpc            *grpc.Server
	serverOptions   []grpc.ServerOption
	middlewares     []core.Middleware
	middlewareValue atomic.Value
	connTracker     *connStatsHandler
	shutdownTimeout time.Duration
	errorMapper     ErrorMapper
	ready           chan struct{}
	readyOnce       sync.Once

	mu        sync.Mutex
	ln        net.Listener
	accepting bool
	started   bool
}

// NewServer creates a gRPC server bound to addr when ListenAndServe is called.
func NewServer(addr string, opts ...Option) *Server {
	s := &Server{
		addr:            addr,
		connTracker:     &connStatsHandler{},
		shutdownTimeout: DefaultShutdownTimeout,
		errorMapper:     defaultErrorMapper,
		ready:           make(chan struct{}),
		accepting:       true,
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

// WithServerOptions appends raw grpc.ServerOption values.
func WithServerOptions(opts ...grpc.ServerOption) Option {
	return func(s *Server) {
		s.serverOptions = append(s.serverOptions, opts...)
	}
}

// WithShutdownTimeout overrides the graceful shutdown timeout.
func WithShutdownTimeout(timeout time.Duration) Option {
	return func(s *Server) {
		if timeout != 0 {
			s.shutdownTimeout = timeout
		}
	}
}

// WithErrorMapper overrides the default safe gRPC error translation.
func WithErrorMapper(mapper ErrorMapper) Option {
	return func(s *Server) {
		if mapper != nil {
			s.errorMapper = mapper
		}
	}
}

// RegisterService registers a gRPC service implementation.
func (s *Server) RegisterService(desc *grpc.ServiceDesc, impl any) {
	s.grpc.RegisterService(desc, impl)
}

// Name returns the transport name.
func (s *Server) Name() string {
	return string(core.TransportGRPC)
}

// Ready closes after the listener has been published and gRPC can accept work.
func (s *Server) Ready() <-chan struct{} {
	return s.ready
}

// Use installs middleware on unary and stream gRPC calls.
func (s *Server) Use(middlewares ...core.Middleware) *Server {
	s.ApplyMiddleware(middlewares...)
	return s
}

// ApplyMiddleware implements transport.MiddlewareTarget for app-level middleware.
func (s *Server) ApplyMiddleware(middlewares ...core.Middleware) {
	s.mu.Lock()
	s.middlewares = append(s.middlewares, middlewares...)
	snapshot := append([]core.Middleware(nil), s.middlewares...)
	s.middlewareValue.Store(snapshot)
	s.mu.Unlock()
}

// Serve starts serving with a background context.
func (s *Server) Serve() error {
	return s.ListenAndServe(context.Background())
}

// ListenAndServe listens on the configured address and serves until stopped.
func (s *Server) ListenAndServe(ctx context.Context) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if err := s.claimStart(); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		s.rollbackStart()
		return err
	}
	return s.serveClaimed(ctx, ln)
}

// ServeListener serves gRPC traffic on an existing listener. Ownership remains
// with the caller when validation or the one-shot start claim fails; after the
// claim succeeds, the server owns and closes the listener.
func (s *Server) ServeListener(ln net.Listener) error {
	return s.ServeListenerContext(context.Background(), ln)
}

// ServeListenerContext serves gRPC traffic on an existing listener until ctx
// stops. Ownership remains with the caller when validation or the one-shot
// start claim fails; after the claim succeeds, the server owns and closes the
// listener.
func (s *Server) ServeListenerContext(ctx context.Context, ln net.Listener) error {
	if err := s.validateRuntime(); err != nil {
		return err
	}
	if isNilListener(ln) {
		return errors.New("grpc listener is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.claimStart(); err != nil {
		return err
	}
	return s.serveClaimed(ctx, ln)
}

func (s *Server) serveClaimed(ctx context.Context, ln net.Listener) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if !s.accepting {
		s.mu.Unlock()
		_ = ln.Close()
		return core.ErrClosed
	}
	s.ln = ln
	s.readyOnce.Do(func() { close(s.ready) })
	s.mu.Unlock()

	serveDone := make(chan struct{})
	watcherDone := serveutil.ShutdownOnCancel(ctx, serveDone, func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
		defer cancel()
		_ = s.Shutdown(shutdownCtx)
	})
	defer serveutil.FinishContextWatcher(serveDone, watcherDone)

	err := s.grpc.Serve(ln)
	if errors.Is(err, grpc.ErrServerStopped) {
		return nil
	}
	return err
}

func (s *Server) claimStart() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.accepting {
		return core.ErrClosed
	}
	if s.started {
		return core.ErrAlreadyStarted
	}
	s.started = true
	return nil
}

func (s *Server) rollbackStart() {
	s.mu.Lock()
	if s.accepting && s.ln == nil {
		s.started = false
	}
	s.mu.Unlock()
}

func isNilListener(listener net.Listener) bool {
	if listener == nil {
		return true
	}
	value := reflect.ValueOf(listener)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// Addr returns the active listener address after serving starts.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}

// GRPCServer exposes the underlying grpc.Server.
func (s *Server) GRPCServer() *grpc.Server {
	return s.grpc
}

// Shutdown gracefully stops the underlying gRPC server or forces Stop on ctx expiry.
func (s *Server) Shutdown(ctx context.Context) error {
	s.markNotAccepting()
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

// GracefulStop delegates to the underlying gRPC server.
func (s *Server) GracefulStop() {
	s.markNotAccepting()
	s.grpc.GracefulStop()
}

// Stop immediately stops the underlying gRPC server.
func (s *Server) Stop() {
	s.markNotAccepting()
	s.grpc.Stop()
}

func (s *Server) markNotAccepting() {
	s.mu.Lock()
	s.accepting = false
	s.mu.Unlock()
}

// UnaryInvocationNative is exposed through Invocation.Native for unary calls.
type UnaryInvocationNative struct {
	// Context is the original gRPC unary context.
	Context context.Context
	// Request is the decoded protobuf request object.
	Request any
	// Info contains the gRPC full method and server metadata.
	Info *grpc.UnaryServerInfo
}

// StreamInvocationNative is exposed through Invocation.Native for stream calls.
type StreamInvocationNative struct {
	// Server is the registered service implementation.
	Server any
	// Stream is the original gRPC server stream.
	Stream grpc.ServerStream
	// Info contains the gRPC full method and stream metadata.
	Info *grpc.StreamServerInfo
}

type connContextKey struct{}

type connState struct {
	id        uint64
	remote    string
	done      chan struct{}
	closeOnce sync.Once
}

func (s *connState) Done() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.done
}

func (s *connState) close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		close(s.done)
	})
}

type connStatsHandler struct {
	nextID uint64
}

func (h *connStatsHandler) TagConn(ctx context.Context, info *stats.ConnTagInfo) context.Context {
	state := &connState{
		id:     atomic.AddUint64(&h.nextID, 1),
		remote: grpcRemoteFromAddr(info.RemoteAddr),
		done:   make(chan struct{}),
	}
	return context.WithValue(ctx, connContextKey{}, state)
}

func (h *connStatsHandler) HandleConn(ctx context.Context, stat stats.ConnStats) {
	if _, ok := stat.(*stats.ConnEnd); ok {
		grpcConnStateFromContext(ctx).close()
	}
}

func (h *connStatsHandler) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context {
	return ctx
}

func (h *connStatsHandler) HandleRPC(context.Context, stats.RPCStats) {}

func grpcConnStateFromContext(ctx context.Context) *connState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(connContextKey{}).(*connState)
	return state
}

func grpcRemoteFromAddr(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	return addr.String()
}

func (s *Server) newGRPCServer() *grpc.Server {
	opts := append([]grpc.ServerOption(nil), s.serverOptions...)
	opts = append(opts, grpc.StatsHandler(s.connTracker))
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
			resp, err := handler(ctx, req)
			return resp, s.mapError(err)
		}

		var resp any
		inv := newUnaryInvocation(ctx, req, info)
		invHandler := core.Chain(middlewares...)(func(invocation core.Invocation) error {
			var err error
			resp, err = handler(invocation.Context(), req)
			return err
		})
		err := invHandler(inv)
		return resp, s.mapError(err)
	}
}

func (s *Server) streamCallInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		middlewares := s.middlewaresSnapshot()
		if len(middlewares) == 0 {
			return s.mapError(handler(srv, stream))
		}

		limited := &middlewareServerStream{
			ServerStream: stream,
		}
		inv := newStreamInvocation(srv, stream, info)
		invHandler := core.Chain(middlewares...)(func(invocation core.Invocation) error {
			if value, ok := invocation.Get(core.PacketLimiterKey); ok {
				if limiter, ok := value.(core.PacketLimiter); ok {
					limited.packetLimiter = limiter
				}
			}
			limited.ctx = invocation.Context()
			return handler(srv, limited)
		})
		return s.mapError(invHandler(inv))
	}
}

func (s *Server) mapError(err error) error {
	if err == nil {
		return nil
	}
	if s.errorMapper == nil {
		return defaultErrorMapper(err)
	}
	return s.errorMapper(err)
}

func defaultErrorMapper(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, context.Canceled.Error())
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, context.DeadlineExceeded.Error())
	}
	type grpcStatus interface {
		GRPCStatus() *status.Status
	}
	if explicit, ok := err.(grpcStatus); ok {
		if existing := explicit.GRPCStatus(); existing != nil && existing.Code() != codes.Unknown {
			return existing.Err()
		}
	}
	switch core.ErrorKindOf(err) {
	case core.ErrorKindRateLimited:
		return status.Error(codes.ResourceExhausted, "rate limit exceeded")
	case core.ErrorKindPayloadTooLarge:
		return status.Error(codes.ResourceExhausted, "payload too large")
	case core.ErrorKindOverloaded:
		return status.Error(codes.ResourceExhausted, "service overloaded")
	case core.ErrorKindInternal, core.ErrorKindRecovered:
		return status.Error(codes.Internal, "internal error")
	default:
		return status.Error(codes.Internal, "internal error")
	}
}

func (s *Server) middlewaresSnapshot() []core.Middleware {
	middlewares, _ := s.middlewareValue.Load().([]core.Middleware)
	return middlewares
}

type middlewareServerStream struct {
	grpc.ServerStream
	packetLimiter core.PacketLimiter
	ctx           context.Context
}

func (s *middlewareServerStream) Context() context.Context {
	if s.ctx != nil {
		return s.ctx
	}
	return s.ServerStream.Context()
}

func (s *middlewareServerStream) RecvMsg(msg any) error {
	if err := s.ServerStream.RecvMsg(msg); err != nil {
		return err
	}
	return s.runMessageMiddlewares("recv", msg)
}

func (s *middlewareServerStream) SendMsg(msg any) error {
	if err := s.runMessageMiddlewares("send", msg); err != nil {
		return err
	}
	return s.ServerStream.SendMsg(msg)
}

func (s *middlewareServerStream) runMessageMiddlewares(string, any) error {
	if s.packetLimiter == nil {
		return nil
	}
	return s.packetLimiter()
}

func newUnaryInvocation(ctx context.Context, req any, info *grpc.UnaryServerInfo) core.Invocation {
	remote, connID, connDone := grpcConnectionInfo(ctx)
	service, method := splitFullMethod(info.FullMethod)
	return core.NewInvocation(core.InvocationOptions{
		Context:   ctx,
		Transport: core.TransportGRPC,
		Operation: info.FullMethod,
		ConnID:    connID,
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
		Done:     ctx.Done(),
		ConnDone: connDone,
	})
}

func newStreamInvocation(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo) core.Invocation {
	ctx := stream.Context()
	remote, connID, connDone := grpcConnectionInfo(ctx)
	service, method := splitFullMethod(info.FullMethod)
	return core.NewInvocation(core.InvocationOptions{
		Context:   ctx,
		Transport: core.TransportGRPC,
		Operation: info.FullMethod,
		ConnID:    connID,
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
		Done:     ctx.Done(),
		ConnDone: connDone,
	})
}

func grpcConnectionInfo(ctx context.Context) (string, uint64, <-chan struct{}) {
	if state := grpcConnStateFromContext(ctx); state != nil {
		return state.remote, state.id, state.Done()
	}
	remote := ""
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		remote = p.Addr.String()
	}
	return remote, connIDFromRemote(remote), nil
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

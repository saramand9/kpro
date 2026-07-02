// File: transport/sse/sse.go
// Purpose: Implements the Server-Sent Events transport adapter.

package sse

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/saramand9/bignet/kpro/core"
)

const (
	DefaultAddr            = ":8081"
	DefaultPath            = "/events"
	DefaultShutdownTimeout = 5 * time.Second
)

// Config controls SSE listener, path, TLS, and shutdown settings.
type Config struct {
	// Addr is the HTTP listen address for the SSE endpoint.
	Addr string
	// Path is the SSE route path, for example "/events".
	Path string
	// TLS switches ListenAndServe to ListenAndServeTLS.
	TLS bool
	// CertFile is the TLS certificate path used when TLS is true.
	CertFile string
	// KeyFile is the TLS key path used when TLS is true.
	KeyFile string
	// WriteTimeout is passed to net/http.Server.
	WriteTimeout time.Duration
	// ShutdownTimeout bounds graceful shutdown after the parent context is cancelled.
	ShutdownTimeout time.Duration
}

// Handler writes events to one SSE stream.
type Handler func(*Context) error

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

// Endpoint serves one SSE stream route and wraps it with middleware.
type Endpoint struct {
	config Config
	server *http.Server

	mu          sync.RWMutex
	handler     Handler
	middlewares []core.Middleware

	connMu     sync.Mutex
	conns      map[net.Conn]*connState
	nextConnID uint64
}

// New creates an SSE endpoint with normalized defaults.
func New(config Config) *Endpoint {
	return &Endpoint{config: normalizeConfig(config), conns: make(map[net.Conn]*connState)}
}

// Name returns the transport name.
func (e *Endpoint) Name() string {
	return string(core.TransportSSE)
}

// Use installs middleware on the SSE stream and returns the endpoint.
func (e *Endpoint) Use(middlewares ...core.Middleware) *Endpoint {
	e.ApplyMiddleware(middlewares...)
	return e
}

// ApplyMiddleware implements server.MiddlewareTarget for app-level middleware.
func (e *Endpoint) ApplyMiddleware(middlewares ...core.Middleware) {
	e.mu.Lock()
	e.middlewares = append(e.middlewares, middlewares...)
	e.mu.Unlock()
}

// Handle registers the SSE stream handler.
func (e *Endpoint) Handle(h Handler) *Endpoint {
	e.mu.Lock()
	e.handler = h
	e.mu.Unlock()
	return e
}

// ListenAndServe starts the SSE HTTP server until ctx is cancelled or serving fails.
func (e *Endpoint) ListenAndServe(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc(e.config.Path, e.serve)
	server := &http.Server{
		Addr:         e.config.Addr,
		Handler:      mux,
		WriteTimeout: e.config.WriteTimeout,
		ConnContext:  e.connContext,
		ConnState:    e.trackConnState,
	}
	e.mu.Lock()
	e.server = server
	e.mu.Unlock()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), e.config.ShutdownTimeout)
		defer cancel()
		_ = e.Shutdown(shutdownCtx)
	}()
	if e.config.TLS {
		return ignoreServerClosed(server.ListenAndServeTLS(e.config.CertFile, e.config.KeyFile))
	}
	return ignoreServerClosed(server.ListenAndServe())
}

// Shutdown gracefully stops the SSE HTTP server.
func (e *Endpoint) Shutdown(ctx context.Context) error {
	e.mu.RLock()
	server := e.server
	e.mu.RUnlock()
	if server == nil {
		return nil
	}
	return server.Shutdown(ctx)
}

func (e *Endpoint) connContext(ctx context.Context, conn net.Conn) context.Context {
	state := &connState{
		remote: remoteFromConn(conn),
		done:   make(chan struct{}),
	}
	e.connMu.Lock()
	e.nextConnID++
	state.id = e.nextConnID
	e.conns[conn] = state
	e.connMu.Unlock()
	return context.WithValue(ctx, connContextKey{}, state)
}

func (e *Endpoint) trackConnState(conn net.Conn, state http.ConnState) {
	if state != http.StateClosed && state != http.StateHijacked {
		return
	}
	e.connMu.Lock()
	tracked := e.conns[conn]
	delete(e.conns, conn)
	e.connMu.Unlock()
	tracked.close()
}

func sseConnStateFromContext(ctx context.Context) *connState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(connContextKey{}).(*connState)
	return state
}

func remoteFromConn(conn net.Conn) string {
	if conn == nil || conn.RemoteAddr() == nil {
		return ""
	}
	return conn.RemoteAddr().String()
}

func (e *Endpoint) serve(w http.ResponseWriter, r *http.Request) {
	handler, middlewares := e.snapshot()
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	streamCtx := &Context{
		ResponseWriter: w,
		Request:        r,
		flusher:        flusher,
		middlewares:    middlewares,
		tls:            e.config.TLS,
		connID:         sessionIDFromRemote(r.RemoteAddr),
		remote:         r.RemoteAddr,
	}
	if state := sseConnStateFromContext(r.Context()); state != nil {
		streamCtx.connID = state.id
		streamCtx.remote = state.remote
		streamCtx.connDone = state.Done()
	}
	streamStarted := false
	run := func() error {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		streamStarted = true
		if handler == nil {
			return nil
		}
		return handler(streamCtx)
	}

	var err error
	if len(middlewares) == 0 {
		err = run()
	} else {
		inv := newSSEInvocation(r, e.config.TLS, streamCtx)
		handler := core.Chain(middlewares...)(func(core.Invocation) error {
			return run()
		})
		err = handler(inv)
	}
	if err == nil {
		return
	}
	if streamStarted && streamCtx != nil {
		_ = streamCtx.Send("error", []byte(err.Error()))
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func (e *Endpoint) snapshot() (Handler, []core.Middleware) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	middlewares := append([]core.Middleware(nil), e.middlewares...)
	return e.handler, middlewares
}

func newSSEInvocation(r *http.Request, tls bool, native any) core.Invocation {
	transport := core.TransportSSE
	remote := ""
	connID := sessionIDFromRemote(r.RemoteAddr)
	var connDone <-chan struct{}
	if state := sseConnStateFromContext(r.Context()); state != nil {
		connID = state.id
		remote = state.remote
		connDone = state.Done()
	} else if addr := parseRemoteAddr(r.RemoteAddr); addr != nil {
		remote = addr.String()
	}
	return core.NewInvocation(core.InvocationOptions{
		Context:   r.Context(),
		Transport: transport,
		Operation: sseOperation(r.Method, r.URL.Path),
		ConnID:    connID,
		Remote:    remote,
		Headers:   requestHeaders(r),
		Meta: map[string]any{
			"method": r.Method,
			"path":   r.URL.Path,
			"query":  r.URL.RawQuery,
			"host":   r.Host,
			"tls":    tls,
			"stream": true,
		},
		Native:   native,
		Done:     r.Context().Done(),
		ConnDone: connDone,
	})
}

func sseOperation(method, path string) string {
	if path == "" {
		path = "/"
	}
	return method + " " + path
}

func requestHeaders(r *http.Request) map[string]string {
	headers := make(map[string]string, len(r.Header)+1)
	if r.Host != "" {
		headers["Host"] = r.Host
	}
	for key, values := range r.Header {
		if len(values) > 0 {
			headers[key] = values[0]
		}
	}
	if len(headers) == 0 {
		return nil
	}
	return headers
}

func sessionIDFromRemote(remote string) uint64 {
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

func parseRemoteAddr(remote string) net.Addr {
	if remote == "" {
		return stringAddr("")
	}
	if addr, err := net.ResolveTCPAddr("tcp", remote); err == nil {
		return addr
	}
	return stringAddr(remote)
}

type stringAddr string

func (a stringAddr) Network() string {
	return "tcp"
}

func (a stringAddr) String() string {
	return string(a)
}

type errorsText string

func (e errorsText) Error() string {
	return string(e)
}

// Context wraps the HTTP response state for one SSE stream.
type Context struct {
	http.ResponseWriter
	Request *http.Request
	flusher http.Flusher
	mu      sync.Mutex

	middlewares []core.Middleware
	tls         bool
	connID      uint64
	remote      string
	connDone    <-chan struct{}
	values      map[string]any
}

// Done returns the request cancellation channel.
func (c *Context) Done() <-chan struct{} {
	return c.Request.Context().Done()
}

// Send writes one SSE event and flushes the stream.
func (c *Context) Send(event string, data []byte) error {
	if err := c.applyPacketLimiter(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if event != "" {
		if _, err := fmt.Fprintf(c.ResponseWriter, "event: %s\n", event); err != nil {
			return err
		}
	}
	for _, line := range strings.Split(string(data), "\n") {
		if _, err := fmt.Fprintf(c.ResponseWriter, "data: %s\n", line); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprint(c.ResponseWriter, "\n"); err != nil {
		return err
	}
	c.flusher.Flush()
	return nil
}

func (c *Context) applyPacketLimiter() error {
	value, ok := c.Get(core.PacketLimiterKey)
	if !ok {
		return nil
	}
	limiter, ok := value.(core.PacketLimiter)
	if !ok || limiter == nil {
		return nil
	}
	return limiter()
}

// Set stores a value for middleware and protocol helpers on the stream context.
func (c *Context) Set(key string, value any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.values == nil {
		c.values = make(map[string]any)
	}
	c.values[key] = value
}

// Get retrieves a value stored on the stream context.
func (c *Context) Get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value, ok := c.values[key]
	return value, ok
}
func normalizeConfig(config Config) Config {
	if config.Addr == "" {
		config.Addr = DefaultAddr
	}
	if config.Path == "" {
		config.Path = DefaultPath
	}
	if config.ShutdownTimeout <= 0 {
		config.ShutdownTimeout = DefaultShutdownTimeout
	}
	return config
}

func ignoreServerClosed(err error) error {
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

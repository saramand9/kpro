// File: transport/httpx/http.go
// Purpose: Implements the native HTTP/HTTPS transport adapter.

package httpx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/transport/internal/serveutil"
)

// Handler processes one native HTTP request.
type Handler func(*Context) error

// ErrBodyTooLarge identifies HTTP request bodies rejected by a configured limit.
var ErrBodyTooLarge = errors.New("http request body too large")

// ErrNativeAdapterRequired means an endpoint tagged as a non-TCP HTTP
// transport must be served by its native adapter instead of ListenAndServe.
var ErrNativeAdapterRequired = errors.New("native HTTP adapter required")

// BodyTooLargeError reports the observed and configured request body sizes.
type BodyTooLargeError struct {
	Size  int
	Limit int
}

func (e *BodyTooLargeError) Error() string {
	return fmt.Sprintf("payload too large: %d > %d", e.Size, e.Limit)
}

// Unwrap lets callers classify the error with errors.Is.
func (e *BodyTooLargeError) Unwrap() error {
	return ErrBodyTooLarge
}

type compiledRoutes map[string]map[string]Handler

type connContextKey struct{}

type connState struct {
	id        uint64
	remote    string
	done      <-chan struct{}
	ownedDone chan struct{}
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
		if s.ownedDone != nil {
			close(s.ownedDone)
		}
	})
}

// Endpoint routes native HTTP requests and exposes them to middleware as invocations.
type Endpoint struct {
	config    Config
	rawConfig Config
	transport core.Transport
	server    *http.Server

	mu          sync.RWMutex
	accepting   bool
	started     bool
	routes      map[string]map[string]Handler
	middlewares []core.Middleware
	compiled    atomic.Value

	connMu     sync.Mutex
	conns      map[net.Conn]*connState
	nextConnID uint64
}

// New creates an HTTP endpoint with normalized defaults.
func New(config Config) *Endpoint {
	transport := core.TransportHTTP
	if config.TLS {
		transport = core.TransportHTTPS
	}
	return NewWithTransport(config, transport)
}

// NewWithTransport creates an HTTP handler endpoint whose middleware
// invocations use transport. It is intended for native adapters such as
// HTTP/3; non-HTTP/HTTPS transports must be served by that adapter and cannot
// call this endpoint's ListenAndServe directly.
func NewWithTransport(config Config, transport core.Transport) *Endpoint {
	rawConfig := config
	config = normalizeConfig(config)
	if transport == "" {
		transport = core.TransportHTTP
		if config.TLS {
			transport = core.TransportHTTPS
		}
	}
	switch transport {
	case core.TransportHTTP:
		config.TLS = false
	case core.TransportHTTPS:
		config.TLS = true
	}
	e := &Endpoint{
		config:    config,
		rawConfig: rawConfig,
		transport: transport,
		accepting: true,
		routes:    make(map[string]map[string]Handler),
		conns:     make(map[net.Conn]*connState),
	}
	e.compiled.Store(compiledRoutes{})
	return e
}

// Name returns the native HTTP transport used by the endpoint.
func (e *Endpoint) Name() string {
	switch e.transport {
	case core.TransportHTTP, core.TransportHTTPS, core.TransportHTTP3:
		return string(e.transport)
	}
	if e.config.TLS {
		return string(core.TransportHTTPS)
	}
	return string(core.TransportHTTP)
}

// Use installs middleware on all registered HTTP routes and returns the endpoint.
func (e *Endpoint) Use(middlewares ...core.Middleware) *Endpoint {
	e.ApplyMiddleware(middlewares...)
	return e
}

// ApplyMiddleware implements transport.MiddlewareTarget for app-level middleware.
func (e *Endpoint) ApplyMiddleware(middlewares ...core.Middleware) {
	if len(middlewares) == 0 {
		return
	}
	e.mu.Lock()
	e.middlewares = append(e.middlewares, middlewares...)
	e.rebuildCompiledLocked()
	e.mu.Unlock()
}

// Get registers a GET route.
func (e *Endpoint) Get(path string, h Handler) *Endpoint {
	return e.Handle(http.MethodGet, path, h)
}

// Post registers a POST route.
func (e *Endpoint) Post(path string, h Handler) *Endpoint {
	return e.Handle(http.MethodPost, path, h)
}

// Put registers a PUT route.
func (e *Endpoint) Put(path string, h Handler) *Endpoint {
	return e.Handle(http.MethodPut, path, h)
}

// Delete registers a DELETE route.
func (e *Endpoint) Delete(path string, h Handler) *Endpoint {
	return e.Handle(http.MethodDelete, path, h)
}

// Handle registers a native HTTP route for method and path.
func (e *Endpoint) Handle(method, path string, h Handler) *Endpoint {
	e.mu.Lock()
	if e.routes[path] == nil {
		e.routes[path] = make(map[string]Handler)
	}
	e.routes[path][method] = h
	e.updateCompiledRouteLocked(method, path, h)
	e.mu.Unlock()
	return e
}

// ListenAndServe starts the HTTP server until ctx is cancelled or serving fails.
func (e *Endpoint) ListenAndServe(ctx context.Context) error {
	if err := e.Validate(); err != nil {
		return err
	}
	if e.transport != core.TransportHTTP && e.transport != core.TransportHTTPS {
		return fmt.Errorf("%w: %s", ErrNativeAdapterRequired, e.transport)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	server := e.buildServer()
	e.mu.Lock()
	if !e.accepting {
		e.mu.Unlock()
		return core.ErrClosed
	}
	if e.started {
		e.mu.Unlock()
		return core.ErrAlreadyStarted
	}
	e.started = true
	e.server = server
	e.mu.Unlock()

	serveDone := make(chan struct{})
	watcherDone := serveutil.ShutdownOnCancel(ctx, serveDone, func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), e.config.ShutdownTimeout)
		defer cancel()
		_ = e.Shutdown(shutdownCtx)
	})
	defer serveutil.FinishContextWatcher(serveDone, watcherDone)
	if e.config.TLS {
		return ignoreServerClosed(server.ListenAndServeTLS(e.config.CertFile, e.config.KeyFile))
	}
	return ignoreServerClosed(server.ListenAndServe())
}

// Shutdown gracefully stops the HTTP server.
func (e *Endpoint) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	e.accepting = false
	server := e.server
	e.mu.Unlock()
	if server == nil {
		return nil
	}
	return server.Shutdown(ctx)
}

func (e *Endpoint) buildServer() *http.Server {
	return &http.Server{
		Addr:         e.config.Addr,
		Handler:      http.HandlerFunc(e.serve),
		ReadTimeout:  e.config.ReadTimeout,
		WriteTimeout: e.config.WriteTimeout,
		IdleTimeout:  e.config.IdleTimeout,
		ErrorLog:     errorLogger(e.config.Logger),
		ConnContext:  e.connContext,
		ConnState:    e.trackConnState,
	}
}

func (e *Endpoint) connContext(ctx context.Context, conn net.Conn) context.Context {
	done := make(chan struct{})
	state := &connState{
		id:        atomic.AddUint64(&e.nextConnID, 1),
		remote:    remoteFromConn(conn),
		done:      done,
		ownedDone: done,
	}
	e.connMu.Lock()
	e.conns[conn] = state
	e.connMu.Unlock()
	return context.WithValue(ctx, connContextKey{}, state)
}

// WithConnectionContext attaches connection identity and lifetime to a request
// context for native HTTP adapters such as HTTP/3.
func (e *Endpoint) WithConnectionContext(
	ctx context.Context,
	remote string,
	done <-chan struct{},
) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	state := &connState{
		id:     atomic.AddUint64(&e.nextConnID, 1),
		remote: remote,
		done:   done,
	}
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

func httpConnStateFromContext(ctx context.Context) *connState {
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

// ServeHTTP lets Endpoint be mounted in standard net/http servers and tests.
func (e *Endpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e.serve(w, r)
}
func (e *Endpoint) serve(w http.ResponseWriter, r *http.Request) {
	routes, _ := e.compiled.Load().(compiledRoutes)
	methods := routes[r.URL.Path]
	if methods == nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
		return
	}
	h := methods[r.Method]
	if h == nil {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write([]byte("method not allowed"))
		return
	}
	ctx := &Context{w: w, r: r, maxBody: e.config.MaxBodyBytes}
	if err := h(ctx); err != nil {
		if ctx.wroteHeader {
			return
		}
		status := httpStatusForError(err)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(err.Error()))
	}
}

func httpStatusForError(err error) int {
	if errors.Is(err, ErrBodyTooLarge) {
		return http.StatusRequestEntityTooLarge
	}
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusInternalServerError
}

func (e *Endpoint) lookupHandler(path, method string) Handler {
	routes, _ := e.compiled.Load().(compiledRoutes)
	if routes == nil {
		return nil
	}
	methods := routes[path]
	if methods == nil {
		return nil
	}
	return methods[method]
}

func (e *Endpoint) rebuildCompiledLocked() {
	routes := make(compiledRoutes, len(e.routes))
	for path, methodHandlers := range e.routes {
		methods := make(map[string]Handler, len(methodHandlers))
		for method, h := range methodHandlers {
			methods[method] = e.wrapRoute(method, path, h)
		}
		routes[path] = methods
	}
	e.compiled.Store(routes)
}

func (e *Endpoint) updateCompiledRouteLocked(method, path string, h Handler) {
	current, _ := e.compiled.Load().(compiledRoutes)
	routes := make(compiledRoutes, len(current)+1)
	for currentPath, methods := range current {
		routes[currentPath] = methods
	}
	currentMethods := current[path]
	methods := make(map[string]Handler, len(currentMethods)+1)
	for currentMethod, handler := range currentMethods {
		methods[currentMethod] = handler
	}
	methods[method] = e.wrapRoute(method, path, h)
	routes[path] = methods
	e.compiled.Store(routes)
}

func (e *Endpoint) wrapRoute(method, path string, h Handler) Handler {
	if h == nil || len(e.middlewares) == 0 {
		return h
	}
	operation := httpOperation(method, path)
	middlewares := append([]core.Middleware(nil), e.middlewares...)
	var once sync.Once
	var handler core.InvocationHandler
	return func(ctx *Context) error {
		once.Do(func() {
			handler = core.Chain(middlewares...)(func(inv core.Invocation) error {
				native := inv.Native().(*Context)
				// The common middleware path forwards the original lazy HTTP
				// invocation. Avoid cloning http.Request unless middleware has
				// actually replaced the invocation view (for example tracing).
				if _, unchanged := inv.(*httpInvocation); !unchanged {
					native.r = native.r.WithContext(inv.Context())
				}
				return h(native)
			})
		})
		return handler(newHTTPInvocation(ctx, e.transport, operation))
	}
}

func newHTTPInvocation(ctx *Context, transport core.Transport, operation string) core.Invocation {
	remote := ""
	connID := sessionIDFromRemote(ctx.r.RemoteAddr)
	var connDone <-chan struct{}
	if state := httpConnStateFromContext(ctx.r.Context()); state != nil {
		connID = state.id
		remote = state.remote
		connDone = state.Done()
	} else {
		remote = ctx.r.RemoteAddr
	}
	path := ""
	query := ""
	hasURL := ctx.r.URL != nil
	if ctx.r.URL != nil {
		path = ctx.r.URL.Path
		query = ctx.r.URL.RawQuery
	}
	inv := &httpInvocation{
		ctx:       ctx,
		transport: transport,
		operation: operation,
		connID:    connID,
		remote:    remote,
		connDone:  connDone,
		host:      ctx.r.Host,
		method:    ctx.r.Method,
		path:      path,
		query:     query,
		hasURL:    hasURL,
	}
	inv.payload.ctx = ctx
	return inv
}

type httpInvocation struct {
	ctx       *Context
	transport core.Transport
	operation string
	connID    uint64
	remote    string
	connDone  <-chan struct{}
	host      string
	method    string
	path      string
	query     string
	hasURL    bool
	payload   BodyPayload

	headersReady bool
	headers      map[string]string
	metaReady    bool
	meta         map[string]any
	values       map[string]any
}

var _ core.Invocation = (*httpInvocation)(nil)

func (i *httpInvocation) Context() context.Context {
	return i.ctx.Context()
}

func (i *httpInvocation) Transport() core.Transport {
	return i.transport
}

func (i *httpInvocation) Operation() string {
	return i.operation
}

func (i *httpInvocation) ConnID() uint64 {
	return i.connID
}

func (i *httpInvocation) RemoteAddr() string {
	return i.remote
}

func (i *httpInvocation) Headers() map[string]string {
	if !i.headersReady {
		i.headers = httpHeaders(i.host, i.ctx.r.Header)
		i.headersReady = true
	}
	return i.headers
}

func (i *httpInvocation) Meta() map[string]any {
	if !i.metaReady {
		i.meta = httpMeta(i.method, i.host, i.path, i.query, i.hasURL)
		i.metaReady = true
	}
	return i.meta
}

func (i *httpInvocation) Payload() any {
	return &i.payload
}

func (i *httpInvocation) Native() any {
	return i.ctx
}

func (i *httpInvocation) Done() <-chan struct{} {
	return i.ctx.Context().Done()
}

func (i *httpInvocation) ConnDone() <-chan struct{} {
	return i.connDone
}

func (i *httpInvocation) Set(key string, value any) {
	if i.values == nil {
		i.values = make(map[string]any)
	}
	i.values[key] = value
}

func (i *httpInvocation) Get(key string) (any, bool) {
	value, ok := i.values[key]
	return value, ok
}

func httpHeaders(host string, source http.Header) map[string]string {
	if host == "" && len(source) == 0 {
		return nil
	}
	headers := make(map[string]string, len(source)+1)
	if host != "" {
		headers["Host"] = host
	}
	for key, values := range source {
		if len(values) > 0 {
			headers[key] = values[0]
		}
	}
	if len(headers) == 0 {
		return nil
	}
	return headers
}

func httpMeta(method, host, path, query string, hasURL bool) map[string]any {
	meta := map[string]any{
		"method": method,
		"host":   host,
	}
	if hasURL {
		meta["path"] = path
		meta["query"] = query
	}
	return meta
}

func sessionIDFromRemote(remote string) uint64 {
	if remote == "" {
		return 1
	}
	const (
		fnvOffset64 = 14695981039346656037
		fnvPrime64  = 1099511628211
	)
	id := uint64(fnvOffset64)
	for index := 0; index < len(remote); index++ {
		id ^= uint64(remote[index])
		id *= fnvPrime64
	}
	if id == 0 {
		return 1
	}
	return id
}

func httpOperation(method, path string) string {
	if path == "" {
		path = "/"
	}
	return method + " " + path
}

// BodyPayload is a lazy view of an HTTP request body exposed through
// core.Invocation.Payload. It lets middleware inspect size or install a limit
// without forcing every request body to be read before the handler runs.
type BodyPayload struct {
	ctx *Context
}

// NewBodyPayload creates a lazy body payload view for middleware.
func NewBodyPayload(ctx *Context) *BodyPayload {
	return &BodyPayload{ctx: ctx}
}

// Bytes reads and caches the request body through the native Context.
func (p *BodyPayload) Bytes() ([]byte, error) {
	if p == nil || p.ctx == nil {
		return nil, nil
	}
	return p.ctx.Body()
}

// PayloadSize reports the known body size without forcing a read when possible.
func (p *BodyPayload) PayloadSize() (int, bool) {
	if p == nil || p.ctx == nil || p.ctx.r == nil {
		return 0, true
	}
	if p.ctx.bodyRead {
		return len(p.ctx.body), true
	}
	if p.ctx.r.ContentLength >= 0 {
		return int(p.ctx.r.ContentLength), true
	}
	return 0, false
}

// LimitPayload installs an HTTP max-bytes reader when the body has not been read.
func (p *BodyPayload) LimitPayload(size int) error {
	if p == nil || p.ctx == nil || p.ctx.r == nil || size < 0 {
		return nil
	}
	if payloadSize, ok := p.PayloadSize(); ok && payloadSize > size {
		return &BodyTooLargeError{Size: payloadSize, Limit: size}
	}
	if p.ctx.bodyRead || p.ctx.r.Body == nil {
		return nil
	}
	p.ctx.r.Body = http.MaxBytesReader(p.ctx.w, p.ctx.r.Body, int64(size))
	return nil
}

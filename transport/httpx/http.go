// File: transport/httpx/http.go
// Purpose: Implements the native HTTP/HTTPS transport adapter.

package httpx

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/transport/internal/serveutil"
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

type routeDefinition struct {
	handler     Handler
	middlewares []core.Middleware
}

type compiledRoutes struct {
	handlers map[string]map[string]Handler
	exact    map[string]map[string]Handler
	mux      *http.ServeMux
}

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
	listener  net.Listener

	mu          sync.RWMutex
	accepting   bool
	started     bool
	routes      map[string]map[string]routeDefinition
	middlewares []core.Middleware
	compiled    atomic.Value

	connMu     sync.Mutex
	conns      map[net.Conn]*connState
	nextConnID uint64
	ready      chan struct{}
	readyOnce  sync.Once

	forceCloseOnce sync.Once
	forceCloseErr  error
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
		routes:    make(map[string]map[string]routeDefinition),
		conns:     make(map[net.Conn]*connState),
		ready:     make(chan struct{}),
	}
	e.compiled.Store(&compiledRoutes{
		handlers: make(map[string]map[string]Handler),
		exact:    make(map[string]map[string]Handler),
		mux:      http.NewServeMux(),
	})
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

// Ready is closed once the listener is successfully bound and published.
func (e *Endpoint) Ready() <-chan struct{} {
	if e == nil {
		return nil
	}
	return e.ready
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

// Get registers a GET route. Route middleware runs after endpoint middleware.
func (e *Endpoint) Get(path string, h Handler, middlewares ...core.Middleware) *Endpoint {
	return e.Handle(http.MethodGet, path, h, middlewares...)
}

// Post registers a POST route. Route middleware runs after endpoint middleware.
func (e *Endpoint) Post(path string, h Handler, middlewares ...core.Middleware) *Endpoint {
	return e.Handle(http.MethodPost, path, h, middlewares...)
}

// Put registers a PUT route. Route middleware runs after endpoint middleware.
func (e *Endpoint) Put(path string, h Handler, middlewares ...core.Middleware) *Endpoint {
	return e.Handle(http.MethodPut, path, h, middlewares...)
}

// Delete registers a DELETE route. Route middleware runs after endpoint middleware.
func (e *Endpoint) Delete(path string, h Handler, middlewares ...core.Middleware) *Endpoint {
	return e.Handle(http.MethodDelete, path, h, middlewares...)
}

// Handle registers a native HTTP route for method and a Go 1.22 ServeMux path
// pattern. Invalid or conflicting registrations panic; use HandleChecked when
// route definitions come from configuration or plugins.
func (e *Endpoint) Handle(method, path string, h Handler, middlewares ...core.Middleware) *Endpoint {
	if err := e.HandleChecked(method, path, h, middlewares...); err != nil {
		panic(err)
	}
	return e
}

// HandleChecked registers a native HTTP route and reports invalid or
// conflicting patterns without changing the current route table.
func (e *Endpoint) HandleChecked(method, path string, h Handler, middlewares ...core.Middleware) error {
	if e == nil {
		return errors.New("http endpoint is nil")
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		return errors.New("http route method is required")
	}
	if path == "" || !strings.HasPrefix(path, "/") {
		return fmt.Errorf("http route path %q must start with /", path)
	}
	if h == nil {
		return errors.New("http route handler is nil")
	}
	for index, middleware := range middlewares {
		if middleware == nil {
			return fmt.Errorf("http route middleware %d is nil", index)
		}
	}

	definition := routeDefinition{
		handler:     h,
		middlewares: append([]core.Middleware(nil), middlewares...),
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if methods := e.routes[path]; methods != nil {
		if _, exists := methods[method]; exists {
			return fmt.Errorf("conflicting HTTP route %s %s is already registered", method, path)
		}
	}
	current, _ := e.compiled.Load().(*compiledRoutes)
	handlers := cloneCompiledHandlers(current)
	if handlers[path] == nil {
		handlers[path] = make(map[string]Handler)
	}
	handlers[path][method] = e.wrapRoute(method, path, h, definition.middlewares)
	compiled, err := buildCompiledRoutes(handlers, e.config.MaxBodyBytes)
	if err != nil {
		return err
	}
	if e.routes[path] == nil {
		e.routes[path] = make(map[string]routeDefinition)
	}
	e.routes[path][method] = definition
	e.compiled.Store(compiled)
	return nil
}

// ListenAndServe starts the HTTP server until ctx is cancelled or serving fails.
func (e *Endpoint) ListenAndServe(ctx context.Context) (serveErr error) {
	if err := e.Validate(); err != nil {
		return err
	}
	if e.transport != core.TransportHTTP && e.transport != core.TransportHTTPS {
		return fmt.Errorf("%w: %s", ErrNativeAdapterRequired, e.transport)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := e.claimStart(); err != nil {
		return err
	}
	server := e.buildServer()
	if e.config.TLS {
		certificate, err := tls.LoadX509KeyPair(e.config.CertFile, e.config.KeyFile)
		if err != nil {
			e.rollbackStart()
			return err
		}
		server.TLSConfig = &tls.Config{Certificates: []tls.Certificate{certificate}}
	}
	listener, err := net.Listen("tcp", e.config.Addr)
	if err != nil {
		e.rollbackStart()
		return err
	}
	if !e.publishServer(server, listener) {
		_ = listener.Close()
		return core.ErrClosed
	}
	e.signalReady()

	serveDone := make(chan struct{})
	var shutdownErr error
	watcherDone := serveutil.ShutdownOnCancel(ctx, serveDone, func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), e.config.ShutdownTimeout)
		defer cancel()
		shutdownErr = e.Shutdown(shutdownCtx)
	})
	defer func() {
		serveutil.FinishContextWatcher(serveDone, watcherDone)
		serveErr = errors.Join(serveErr, shutdownErr)
	}()
	if e.config.TLS {
		return ignoreServerClosed(server.ServeTLS(listener, "", ""))
	}
	return ignoreServerClosed(server.Serve(listener))
}

// Shutdown gracefully stops the HTTP server.
func (e *Endpoint) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	e.accepting = false
	server := e.server
	listener := e.listener
	e.mu.Unlock()
	if server == nil {
		if listener != nil {
			return ignoreServerClosed(listener.Close())
		}
		return nil
	}
	shutdownErr := ignoreServerClosed(server.Shutdown(ctx))
	var listenerErr error
	if listener != nil {
		listenerErr = ignoreServerClosed(listener.Close())
	}
	gracefulErr := errors.Join(shutdownErr, listenerErr)
	if gracefulErr == nil {
		return nil
	}
	return errors.Join(gracefulErr, e.forceClose())
}

// forceClose immediately closes the listener and active non-hijacked HTTP
// connections after a graceful shutdown failure. Hijacked connections are
// caller-owned under net/http and remain outside Endpoint shutdown. This is
// intentionally separate from the successful path so a completed drain is
// never converted into a hard stop.
func (e *Endpoint) forceClose() error {
	e.forceCloseOnce.Do(func() {
		e.mu.RLock()
		server := e.server
		listener := e.listener
		e.mu.RUnlock()

		var serverErr error
		if server != nil {
			serverErr = ignoreServerClosed(server.Close())
		}
		var listenerErr error
		if listener != nil {
			listenerErr = ignoreServerClosed(listener.Close())
		}
		e.forceCloseErr = errors.Join(serverErr, listenerErr)
	})
	return e.forceCloseErr
}

func (e *Endpoint) claimStart() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.accepting {
		return core.ErrClosed
	}
	if e.started {
		return core.ErrAlreadyStarted
	}
	e.started = true
	return nil
}

func (e *Endpoint) rollbackStart() {
	e.mu.Lock()
	if e.accepting && e.listener == nil {
		e.started = false
	}
	e.mu.Unlock()
}

func (e *Endpoint) publishServer(server *http.Server, listener net.Listener) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.accepting {
		return false
	}
	e.server = server
	e.listener = listener
	return true
}

func (e *Endpoint) signalReady() {
	if e == nil || e.ready == nil {
		return
	}
	e.readyOnce.Do(func() {
		close(e.ready)
	})
}

func (e *Endpoint) buildServer() *http.Server {
	return &http.Server{
		Addr:              e.config.Addr,
		Handler:           http.HandlerFunc(e.serve),
		ReadHeaderTimeout: e.config.ReadHeaderTimeout,
		ReadTimeout:       e.config.ReadTimeout,
		WriteTimeout:      e.config.WriteTimeout,
		IdleTimeout:       e.config.IdleTimeout,
		MaxHeaderBytes:    e.config.MaxHeaderBytes,
		ErrorLog:          errorLogger(e.config.Logger),
		ConnContext:       e.connContext,
		ConnState:         e.trackConnState,
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
	compiled, _ := e.compiled.Load().(*compiledRoutes)
	if compiled == nil || compiled.mux == nil {
		writeRoutingError(w, http.StatusNotFound)
		return
	}
	if r != nil && r.URL != nil {
		if methods := compiled.exact[r.URL.Path]; methods != nil {
			if h := methods[r.Method]; h != nil {
				serveRoute(w, r, e.config.MaxBodyBytes, h)
				return
			}
		}
	}

	h, pattern := compiled.mux.Handler(r)
	if pattern != "" {
		// ServeHTTP, unlike Handler, installs wildcard values on the request so
		// Context.Param can read them with Request.PathValue.
		compiled.mux.ServeHTTP(w, r)
		return
	}

	probe := newRoutingResponse()
	h.ServeHTTP(probe, r)
	copyHTTPHeader(w.Header(), probe.header)
	switch probe.status {
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		writeRoutingError(w, probe.status)
	default:
		w.WriteHeader(probe.status)
		_, _ = w.Write(probe.body.Bytes())
	}
}

func serveRoute(w http.ResponseWriter, r *http.Request, maxBody int64, h Handler) {
	ctx := &Context{w: w, r: r, maxBody: maxBody}
	if err := h(ctx); err != nil {
		if ctx.wroteHeader {
			return
		}
		status := httpStatusForError(err)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(httpMessageForError(err)))
	}
}

func writeRoutingError(w http.ResponseWriter, status int) {
	w.WriteHeader(status)
	message := "not found"
	if status == http.StatusMethodNotAllowed {
		message = "method not allowed"
	}
	_, _ = w.Write([]byte(message))
}

type routingResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newRoutingResponse() *routingResponse {
	return &routingResponse{header: make(http.Header), status: http.StatusOK}
}

func (w *routingResponse) Header() http.Header {
	return w.header
}

func (w *routingResponse) WriteHeader(status int) {
	w.status = status
}

func (w *routingResponse) Write(body []byte) (int, error) {
	return w.body.Write(body)
}

func copyHTTPHeader(destination, source http.Header) {
	for key, values := range source {
		destination[key] = append([]string(nil), values...)
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
	switch core.ErrorKindOf(err) {
	case core.ErrorKindRateLimited:
		return http.StatusTooManyRequests
	case core.ErrorKindPayloadTooLarge:
		return http.StatusRequestEntityTooLarge
	case core.ErrorKindOverloaded:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

func httpMessageForError(err error) string {
	if errors.Is(err, ErrBodyTooLarge) {
		return core.ErrPayloadTooLarge.Error()
	}
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		return core.ErrPayloadTooLarge.Error()
	}
	switch core.ErrorKindOf(err) {
	case core.ErrorKindRateLimited:
		return core.ErrRateLimited.Error()
	case core.ErrorKindPayloadTooLarge:
		return core.ErrPayloadTooLarge.Error()
	case core.ErrorKindOverloaded:
		return core.ErrOverloaded.Error()
	default:
		return core.ErrInternal.Error()
	}
}

func (e *Endpoint) rebuildCompiledLocked() {
	handlers := make(map[string]map[string]Handler, len(e.routes))
	for path, methodHandlers := range e.routes {
		methods := make(map[string]Handler, len(methodHandlers))
		for method, definition := range methodHandlers {
			methods[method] = e.wrapRoute(method, path, definition.handler, definition.middlewares)
		}
		handlers[path] = methods
	}
	compiled, err := buildCompiledRoutes(handlers, e.config.MaxBodyBytes)
	if err != nil {
		panic(err)
	}
	e.compiled.Store(compiled)
}

func cloneCompiledHandlers(current *compiledRoutes) map[string]map[string]Handler {
	if current == nil {
		return make(map[string]map[string]Handler)
	}
	handlers := make(map[string]map[string]Handler, len(current.handlers))
	for path, currentMethods := range current.handlers {
		methods := make(map[string]Handler, len(currentMethods))
		for method, handler := range currentMethods {
			methods[method] = handler
		}
		handlers[path] = methods
	}
	return handlers
}

func buildCompiledRoutes(handlers map[string]map[string]Handler, maxBody int64) (compiled *compiledRoutes, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			compiled = nil
			err = fmt.Errorf("invalid or conflicting HTTP route: %v", recovered)
		}
	}()

	compiled = &compiledRoutes{
		handlers: handlers,
		exact:    make(map[string]map[string]Handler),
		mux:      http.NewServeMux(),
	}
	paths := make([]string, 0, len(handlers))
	for path := range handlers {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		methods := handlers[path]
		methodNames := make([]string, 0, len(methods))
		for method := range methods {
			methodNames = append(methodNames, method)
		}
		sort.Strings(methodNames)
		for _, method := range methodNames {
			handler := methods[method]
			compiled.mux.Handle(method+" "+path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				serveRoute(w, r, maxBody, handler)
			}))
			if isLiteralRoutePath(path) {
				if compiled.exact[path] == nil {
					compiled.exact[path] = make(map[string]Handler)
				}
				compiled.exact[path][method] = handler
			}
		}
	}
	return compiled, nil
}

func isLiteralRoutePath(path string) bool {
	return strings.HasPrefix(path, "/") && !strings.ContainsAny(path, "{}")
}

func (e *Endpoint) wrapRoute(method, path string, h Handler, routeMiddlewares []core.Middleware) Handler {
	if h == nil || len(e.middlewares)+len(routeMiddlewares) == 0 {
		return h
	}
	operation := httpOperation(method, path)
	middlewares := make([]core.Middleware, 0, len(e.middlewares)+len(routeMiddlewares))
	middlewares = append(middlewares, e.middlewares...)
	middlewares = append(middlewares, routeMiddlewares...)
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

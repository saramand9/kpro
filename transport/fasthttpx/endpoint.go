package fasthttpx

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	fastrouter "github.com/fasthttp/router"
	"github.com/valyala/fasthttp"

	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/transport/internal/serveutil"
)

const (
	componentHTTPName  = "fasthttp"
	componentHTTPSName = "fasthttps"
)

// Handler processes one fasthttp-native request.
type Handler func(*Context) error

// ErrBodyTooLarge identifies request bodies rejected by an endpoint or
// middleware limit.
var ErrBodyTooLarge = errors.New("fasthttp request body too large")

// BodyTooLargeError reports the observed and configured request body sizes.
type BodyTooLargeError struct {
	Size  int64
	Limit int64
}

func (e *BodyTooLargeError) Error() string {
	return fmt.Sprintf("payload too large: %d > %d", e.Size, e.Limit)
}

func (e *BodyTooLargeError) Unwrap() error { return ErrBodyTooLarge }

type routeDefinition struct {
	handler     Handler
	middlewares []core.Middleware
}

type compiledRoutes struct {
	handlers map[string]map[string]Handler
	handler  fasthttp.RequestHandler
}

type connState struct {
	done      chan struct{}
	closeOnce sync.Once
}

func (s *connState) close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() { close(s.done) })
}

// Endpoint is an optional HTTP/1.1-native runner backed by fasthttp.
type Endpoint struct {
	config    Config
	rawConfig Config
	transport core.Transport
	server    *fasthttp.Server
	listener  net.Listener

	shutdownCtx    context.Context
	shutdownCancel context.CancelFunc

	mu          sync.RWMutex
	accepting   bool
	started     bool
	routes      map[string]map[string]routeDefinition
	middlewares []core.Middleware
	compiled    atomic.Value

	contextPool sync.Pool
	connMu      sync.Mutex
	conns       map[net.Conn]*connState
	ready       chan struct{}
	readyOnce   sync.Once

	shutdownMu     sync.Mutex
	forceCloseOnce sync.Once
	forceCloseErr  error
}

// New creates an opt-in HTTP/1.1 endpoint. When TLS is enabled the endpoint
// still advertises only HTTP/1.1; use httpx for HTTP/2.
func New(config Config) *Endpoint {
	normalized := normalizeConfig(config)
	protocol := core.TransportHTTP
	if normalized.TLS {
		protocol = core.TransportHTTPS
	}
	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
	e := &Endpoint{
		config:         normalized,
		rawConfig:      config,
		transport:      protocol,
		shutdownCtx:    shutdownCtx,
		shutdownCancel: shutdownCancel,
		accepting:      true,
		routes:         make(map[string]map[string]routeDefinition),
		conns:          make(map[net.Conn]*connState),
		ready:          make(chan struct{}),
	}
	e.contextPool.New = func() any { return new(Context) }
	compiled, err := e.buildCompiledRoutes(make(map[string]map[string]Handler))
	if err != nil {
		panic(err)
	}
	e.compiled.Store(compiled)
	return e
}

// Name identifies the concrete implementation while Descriptor.Protocol
// remains the standard HTTP or HTTPS protocol family.
func (e *Endpoint) Name() string {
	if e != nil && e.config.TLS {
		return componentHTTPSName
	}
	return componentHTTPName
}

// Ready closes after the TCP listener has been bound and published.
func (e *Endpoint) Ready() <-chan struct{} {
	if e == nil {
		return nil
	}
	return e.ready
}

// Use installs middleware on every registered route.
func (e *Endpoint) Use(middlewares ...core.Middleware) *Endpoint {
	e.ApplyMiddleware(middlewares...)
	return e
}

// ApplyMiddleware implements transport.MiddlewareTarget.
func (e *Endpoint) ApplyMiddleware(middlewares ...core.Middleware) {
	if len(middlewares) == 0 {
		return
	}
	if err := validateRouteMiddlewares(middlewares); err != nil {
		panic(err)
	}
	e.mu.Lock()
	e.middlewares = append(e.middlewares, middlewares...)
	e.rebuildCompiledLocked()
	e.mu.Unlock()
}

func (e *Endpoint) Get(path string, handler Handler, middlewares ...core.Middleware) *Endpoint {
	return e.Handle(fasthttp.MethodGet, path, handler, middlewares...)
}

func (e *Endpoint) Head(path string, handler Handler, middlewares ...core.Middleware) *Endpoint {
	return e.Handle(fasthttp.MethodHead, path, handler, middlewares...)
}

func (e *Endpoint) Post(path string, handler Handler, middlewares ...core.Middleware) *Endpoint {
	return e.Handle(fasthttp.MethodPost, path, handler, middlewares...)
}

func (e *Endpoint) Put(path string, handler Handler, middlewares ...core.Middleware) *Endpoint {
	return e.Handle(fasthttp.MethodPut, path, handler, middlewares...)
}

func (e *Endpoint) Patch(path string, handler Handler, middlewares ...core.Middleware) *Endpoint {
	return e.Handle(fasthttp.MethodPatch, path, handler, middlewares...)
}

func (e *Endpoint) Delete(path string, handler Handler, middlewares ...core.Middleware) *Endpoint {
	return e.Handle(fasthttp.MethodDelete, path, handler, middlewares...)
}

func (e *Endpoint) Options(path string, handler Handler, middlewares ...core.Middleware) *Endpoint {
	return e.Handle(fasthttp.MethodOptions, path, handler, middlewares...)
}

// Handle registers a method and fasthttp/router path pattern. Invalid or
// conflicting routes panic; use HandleChecked for data-driven registration.
func (e *Endpoint) Handle(method, path string, handler Handler, middlewares ...core.Middleware) *Endpoint {
	if err := e.HandleChecked(method, path, handler, middlewares...); err != nil {
		panic(err)
	}
	return e
}

// HandleChecked atomically registers a route without changing the active
// snapshot when validation or conflict detection fails.
func (e *Endpoint) HandleChecked(method, path string, handler Handler, middlewares ...core.Middleware) error {
	if e == nil {
		return errors.New("fasthttp endpoint is nil")
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		return errors.New("fasthttp route method is required")
	}
	if path == "" || !strings.HasPrefix(path, "/") {
		return fmt.Errorf("fasthttp route path %q must start with /", path)
	}
	if handler == nil {
		return errors.New("fasthttp route handler is nil")
	}
	if err := validateRouteMiddlewares(middlewares); err != nil {
		return err
	}

	definition := routeDefinition{
		handler:     handler,
		middlewares: append([]core.Middleware(nil), middlewares...),
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if methods := e.routes[path]; methods != nil {
		if _, exists := methods[method]; exists {
			return fmt.Errorf("conflicting fasthttp route %s %s is already registered", method, path)
		}
	}
	current, _ := e.compiled.Load().(*compiledRoutes)
	handlers := cloneCompiledHandlers(current)
	if handlers[path] == nil {
		handlers[path] = make(map[string]Handler)
	}
	handlers[path][method] = e.wrapRoute(method, path, handler, definition.middlewares)
	compiled, err := e.buildCompiledRoutes(handlers)
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

// ServeFastHTTP lets Endpoint be mounted in a caller-owned fasthttp server and
// provides a direct entry point for tests. In that mode the caller owns server
// lifecycle and connection-state reporting.
func (e *Endpoint) ServeFastHTTP(ctx *fasthttp.RequestCtx) {
	if ctx == nil {
		return
	}
	if e == nil {
		writeFastError(ctx, fasthttp.StatusInternalServerError, core.ErrInternal.Error())
		return
	}
	compiled, _ := e.compiled.Load().(*compiledRoutes)
	if compiled == nil || compiled.handler == nil {
		writeFastError(ctx, fasthttp.StatusNotFound, "not found")
		return
	}
	compiled.handler(ctx)
}

// ListenAndServe starts the HTTP/1.1 server until ctx is cancelled or serving
// fails.
func (e *Endpoint) ListenAndServe(ctx context.Context) (serveErr error) {
	if err := e.Validate(); err != nil {
		return err
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
			return fmt.Errorf("load fasthttp HTTPS certificate: %w", err)
		}
		server.TLSConfig.Certificates = []tls.Certificate{certificate}
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
		return ignoreClosed(server.ServeTLS(listener, "", ""))
	}
	return ignoreClosed(server.Serve(listener))
}

// Shutdown stops admission, cancels endpoint request contexts, and waits for
// active handlers within ctx. A deadline failure force-closes tracked native
// connections.
func (e *Endpoint) Shutdown(ctx context.Context) error {
	if e == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	e.shutdownMu.Lock()
	defer e.shutdownMu.Unlock()
	e.shutdownCancel()
	e.mu.Lock()
	e.accepting = false
	server := e.server
	listener := e.listener
	e.mu.Unlock()
	if server == nil {
		if listener != nil {
			return ignoreClosed(listener.Close())
		}
		return nil
	}
	shutdownErr := ignoreClosed(server.ShutdownWithContext(ctx))
	var listenerErr error
	if listener != nil {
		listenerErr = ignoreClosed(listener.Close())
	}
	gracefulErr := errors.Join(shutdownErr, listenerErr)
	if gracefulErr == nil {
		return nil
	}
	return errors.Join(gracefulErr, e.forceClose())
}

func (e *Endpoint) forceClose() error {
	e.forceCloseOnce.Do(func() {
		e.mu.RLock()
		listener := e.listener
		e.mu.RUnlock()
		var listenerErr error
		if listener != nil {
			listenerErr = ignoreClosed(listener.Close())
		}

		e.connMu.Lock()
		connections := make([]net.Conn, 0, len(e.conns))
		for connection := range e.conns {
			connections = append(connections, connection)
		}
		e.connMu.Unlock()
		var closeErr error
		for _, connection := range connections {
			closeErr = errors.Join(closeErr, ignoreClosed(connection.Close()))
		}
		e.forceCloseErr = errors.Join(listenerErr, closeErr)
	})
	return e.forceCloseErr
}

func (e *Endpoint) buildServer() *fasthttp.Server {
	server := &fasthttp.Server{
		Handler:                      e.ServeFastHTTP,
		ErrorHandler:                 e.handleProtocolError,
		Logger:                       e.config.Logger,
		ConnState:                    e.trackConnState,
		Concurrency:                  e.config.Concurrency,
		ReadBufferSize:               e.config.ReadBufferSize,
		WriteBufferSize:              e.config.WriteBufferSize,
		ReadTimeout:                  e.config.ReadTimeout,
		WriteTimeout:                 e.config.WriteTimeout,
		IdleTimeout:                  e.config.IdleTimeout,
		MaxConnsPerIP:                e.config.MaxConnsPerIP,
		MaxRequestsPerConn:           e.config.MaxRequestsPerConn,
		MaxRequestBodySize:           int(e.config.MaxBodyBytes),
		TCPKeepalive:                 e.config.TCPKeepalive,
		ReduceMemoryUsage:            e.config.ReduceMemoryUsage,
		DisablePreParseMultipartForm: true,
		SecureErrorLogMessage:        true,
		NoDefaultServerHeader:        true,
		CloseOnShutdown:              true,
	}
	if e.config.TLS {
		server.TLSConfig = &tls.Config{NextProtos: []string{"http/1.1"}}
	}
	return server
}

func (e *Endpoint) handleProtocolError(ctx *fasthttp.RequestCtx, err error) {
	var smallBufferErr *fasthttp.ErrSmallBuffer
	switch {
	case errors.Is(err, fasthttp.ErrBodyTooLarge):
		writeFastError(ctx, fasthttp.StatusRequestEntityTooLarge, core.ErrPayloadTooLarge.Error())
	case errors.As(err, &smallBufferErr):
		writeFastError(ctx, fasthttp.StatusRequestHeaderFieldsTooLarge, "request header too large")
	default:
		writeFastError(ctx, fasthttp.StatusBadRequest, "bad request")
	}
}

func (e *Endpoint) handlePanic(ctx *fasthttp.RequestCtx, recovered any) {
	if e.config.Logger != nil {
		e.config.Logger.Printf("fasthttp handler panic: %v\n%s", recovered, debug.Stack())
	} else {
		log.Printf("fasthttp handler panic: %v\n%s", recovered, debug.Stack())
	}
	ctx.Response.Reset()
	writeFastError(ctx, fasthttp.StatusInternalServerError, core.ErrInternal.Error())
}

func (e *Endpoint) serveRoute(native *fasthttp.RequestCtx, handler Handler) {
	ctx := e.contextPool.Get().(*Context)
	ctx.initialize(e, native)
	defer func() {
		ctx.reset()
		e.contextPool.Put(ctx)
	}()
	if err := handler(ctx); err != nil && !ctx.wroteHeader {
		writeFastError(native, httpStatusForError(err), httpMessageForError(err))
	}
}

func (e *Endpoint) buildCompiledRoutes(handlers map[string]map[string]Handler) (compiled *compiledRoutes, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			compiled = nil
			err = fmt.Errorf("invalid or conflicting fasthttp route: %v", recovered)
		}
	}()
	router := fastrouter.New()
	router.RedirectTrailingSlash = false
	router.RedirectFixedPath = false
	router.HandleOPTIONS = false
	router.HandleMethodNotAllowed = true
	router.NotFound = func(ctx *fasthttp.RequestCtx) {
		writeFastError(ctx, fasthttp.StatusNotFound, "not found")
	}
	router.MethodNotAllowed = func(ctx *fasthttp.RequestCtx) {
		writeFastError(ctx, fasthttp.StatusMethodNotAllowed, "method not allowed")
	}
	router.PanicHandler = e.handlePanic

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
			router.Handle(method, path, func(ctx *fasthttp.RequestCtx) {
				e.serveRoute(ctx, handler)
			})
		}
	}
	return &compiledRoutes{handlers: handlers, handler: router.Handler}, nil
}

func (e *Endpoint) rebuildCompiledLocked() {
	handlers := make(map[string]map[string]Handler, len(e.routes))
	for path, methodDefinitions := range e.routes {
		methods := make(map[string]Handler, len(methodDefinitions))
		for method, definition := range methodDefinitions {
			methods[method] = e.wrapRoute(method, path, definition.handler, definition.middlewares)
		}
		handlers[path] = methods
	}
	compiled, err := e.buildCompiledRoutes(handlers)
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

func (e *Endpoint) wrapRoute(method, path string, handler Handler, routeMiddlewares []core.Middleware) Handler {
	if handler == nil || len(e.middlewares)+len(routeMiddlewares) == 0 {
		return handler
	}
	operation := method + " " + path
	middlewares := make([]core.Middleware, 0, len(e.middlewares)+len(routeMiddlewares))
	middlewares = append(middlewares, e.middlewares...)
	middlewares = append(middlewares, routeMiddlewares...)
	var once sync.Once
	var invocationHandler core.InvocationHandler
	return func(ctx *Context) error {
		once.Do(func() {
			invocationHandler = core.Chain(middlewares...)(func(invocation core.Invocation) error {
				native := invocation.Native().(*Context)
				if _, unchanged := invocation.(*httpInvocation); !unchanged {
					native.requestContext = invocation.Context()
				}
				return handler(native)
			})
		})
		return invocationHandler(newHTTPInvocation(ctx, e.transport, operation))
	}
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

func (e *Endpoint) publishServer(server *fasthttp.Server, listener net.Listener) bool {
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
	e.readyOnce.Do(func() { close(e.ready) })
}

func (e *Endpoint) trackConnState(connection net.Conn, state fasthttp.ConnState) {
	switch state {
	case fasthttp.StateNew:
		e.connMu.Lock()
		if e.conns[connection] == nil {
			e.conns[connection] = &connState{done: make(chan struct{})}
		}
		e.connMu.Unlock()
	case fasthttp.StateClosed, fasthttp.StateHijacked:
		e.connMu.Lock()
		tracked := e.conns[connection]
		delete(e.conns, connection)
		e.connMu.Unlock()
		tracked.close()
	}
}

func (e *Endpoint) connectionDone(connection net.Conn) <-chan struct{} {
	if e == nil || connection == nil {
		return nil
	}
	e.connMu.Lock()
	tracked := e.conns[connection]
	e.connMu.Unlock()
	if tracked == nil {
		return nil
	}
	return tracked.done
}

func ignoreClosed(err error) error {
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func writeFastError(ctx *fasthttp.RequestCtx, status int, message string) {
	ctx.SetStatusCode(status)
	ctx.SetContentType("text/plain; charset=utf-8")
	ctx.SetBodyString(message)
}

func httpStatusForError(err error) int {
	if errors.Is(err, ErrBodyTooLarge) {
		return fasthttp.StatusRequestEntityTooLarge
	}
	switch core.ErrorKindOf(err) {
	case core.ErrorKindRateLimited:
		return fasthttp.StatusTooManyRequests
	case core.ErrorKindPayloadTooLarge:
		return fasthttp.StatusRequestEntityTooLarge
	case core.ErrorKindOverloaded:
		return fasthttp.StatusServiceUnavailable
	default:
		return fasthttp.StatusInternalServerError
	}
}

func httpMessageForError(err error) string {
	if errors.Is(err, ErrBodyTooLarge) {
		return core.ErrPayloadTooLarge.Error()
	}
	return core.PublicErrorMessage(err)
}

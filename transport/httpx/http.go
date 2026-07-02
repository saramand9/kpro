package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/saramand9/bignet/kpro/core"
)

const (
	DefaultAddr            = ":8080"
	DefaultShutdownTimeout = 5 * time.Second
)

type Config struct {
	Addr            string
	TLS             bool
	CertFile        string
	KeyFile         string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
	Logger          interface {
		Printf(string, ...interface{})
	}
}

type Handler func(*Context) error

type compiledRoutes map[string]map[string]Handler

type Endpoint struct {
	config Config
	server *http.Server

	mu          sync.RWMutex
	routes      map[string]map[string]Handler
	middlewares []core.Middleware
	compiled    atomic.Value
}

func New(config Config) *Endpoint {
	config = normalizeConfig(config)
	e := &Endpoint{
		config: config,
		routes: make(map[string]map[string]Handler),
	}
	e.compiled.Store(compiledRoutes{})
	return e
}

func (e *Endpoint) Name() string {
	if e.config.TLS {
		return "https"
	}
	return "http"
}

func (e *Endpoint) Use(middlewares ...core.Middleware) *Endpoint {
	e.ApplyMiddleware(middlewares...)
	return e
}

func (e *Endpoint) ApplyMiddleware(middlewares ...core.Middleware) {
	e.mu.Lock()
	e.middlewares = append(e.middlewares, middlewares...)
	e.rebuildCompiledLocked()
	e.mu.Unlock()
}

func (e *Endpoint) Get(path string, h Handler) *Endpoint {
	return e.Handle(http.MethodGet, path, h)
}

func (e *Endpoint) Post(path string, h Handler) *Endpoint {
	return e.Handle(http.MethodPost, path, h)
}

func (e *Endpoint) Put(path string, h Handler) *Endpoint {
	return e.Handle(http.MethodPut, path, h)
}

func (e *Endpoint) Delete(path string, h Handler) *Endpoint {
	return e.Handle(http.MethodDelete, path, h)
}

func (e *Endpoint) Handle(method, path string, h Handler) *Endpoint {
	e.mu.Lock()
	if e.routes[path] == nil {
		e.routes[path] = make(map[string]Handler)
	}
	e.routes[path][method] = h
	e.rebuildCompiledLocked()
	e.mu.Unlock()
	return e
}

func (e *Endpoint) ListenAndServe(ctx context.Context) error {
	server := e.buildServer()
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

func (e *Endpoint) Shutdown(ctx context.Context) error {
	e.mu.RLock()
	server := e.server
	e.mu.RUnlock()
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
	}
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
	e.serveHandler(h)(w, r)
}

func (e *Endpoint) serveHandler(h Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := &Context{w: w, r: r}
		if err := h(ctx); err != nil {
			if !ctx.wroteHeader {
				w.WriteHeader(http.StatusInternalServerError)
			}
			_, _ = w.Write([]byte(err.Error()))
		}
	}
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

func (e *Endpoint) wrapRoute(method, path string, h Handler) Handler {
	if len(e.middlewares) == 0 {
		return h
	}
	operation := httpOperation(method, path)
	middlewares := append([]core.Middleware(nil), e.middlewares...)
	return func(ctx *Context) error {
		inv := newHTTPInvocation(ctx, e.config.TLS, operation, NewBodyPayload(ctx))
		handler := core.Chain(middlewares...)(func(core.Invocation) error {
			return h(ctx)
		})
		return handler(inv)
	}
}

func newHTTPInvocation(ctx *Context, tls bool, operation string, payload any) core.Invocation {
	transport := core.TransportHTTP
	if tls {
		transport = core.TransportHTTPS
	}
	remote := ""
	if addr := parseRemoteAddr(ctx.r.RemoteAddr); addr != nil {
		remote = addr.String()
	}
	return core.NewInvocation(core.InvocationOptions{
		Context:   ctx.r.Context(),
		Transport: transport,
		Operation: operation,
		ConnID:    sessionIDFromRemote(ctx.r.RemoteAddr),
		Remote:    remote,
		Headers:   httpHeaders(ctx.r),
		Meta:      httpMeta(ctx.r),
		Payload:   payload,
		Native:    ctx,
		Done:      ctx.r.Context().Done(),
	})
}

func httpHeaders(r *http.Request) map[string]string {
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

func httpMeta(r *http.Request) map[string]any {
	meta := map[string]any{
		"method": r.Method,
		"host":   r.Host,
	}
	if r.URL != nil {
		meta["path"] = r.URL.Path
		meta["query"] = r.URL.RawQuery
	}
	return meta
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

func NewBodyPayload(ctx *Context) *BodyPayload {
	return &BodyPayload{ctx: ctx}
}

func (p *BodyPayload) Bytes() ([]byte, error) {
	if p == nil || p.ctx == nil {
		return nil, nil
	}
	return p.ctx.Body()
}

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

func (p *BodyPayload) LimitPayload(size int) error {
	if p == nil || p.ctx == nil || p.ctx.r == nil || size < 0 {
		return nil
	}
	if payloadSize, ok := p.PayloadSize(); ok && payloadSize > size {
		return fmt.Errorf("payload too large: %d > %d", payloadSize, size)
	}
	if p.ctx.bodyRead || p.ctx.r.Body == nil {
		return nil
	}
	p.ctx.r.Body = http.MaxBytesReader(p.ctx.w, p.ctx.r.Body, int64(size))
	return nil
}

// Context is backed by net/http. Body caches the request body on first read so
// handlers can call Body and CopyBody in either order.
type Context struct {
	w           http.ResponseWriter
	r           *http.Request
	header      http.Header
	body        []byte
	bodyRead    bool
	status      int
	wroteHeader bool
}

func (c *Context) Header() http.Header {
	if c.header == nil {
		c.header = c.w.Header()
	}
	return c.header
}

func (c *Context) WriteHeader(status int) {
	if c.wroteHeader {
		return
	}
	c.wroteHeader = true
	c.status = status
	c.w.WriteHeader(status)
}

func (c *Context) Write(body []byte) (int, error) {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK)
	}
	return c.w.Write(body)
}

func (c *Context) setContentType(contentType string) {
	if contentType == "" {
		return
	}
	c.w.Header().Set("Content-Type", contentType)
}

func (c *Context) Body() ([]byte, error) {
	if c.bodyRead {
		return c.body, nil
	}
	if c.r.Body == nil {
		c.bodyRead = true
		return nil, nil
	}
	body, err := io.ReadAll(c.r.Body)
	if err != nil {
		return nil, err
	}
	c.body = body
	c.bodyRead = true
	return c.body, nil
}

func (c *Context) Method() string {
	return c.r.Method
}

func (c *Context) MethodBytes() []byte {
	return []byte(c.r.Method)
}

func (c *Context) Path() string {
	if c.r.URL == nil {
		return ""
	}
	return c.r.URL.Path
}

func (c *Context) PathBytes() []byte {
	return []byte(c.Path())
}

func (c *Context) RawQuery() string {
	if c.r.URL == nil {
		return ""
	}
	return c.r.URL.RawQuery
}

func (c *Context) Host() string {
	return c.r.Host
}

func (c *Context) ContentLength() int {
	if c.r.ContentLength > 0 {
		return int(c.r.ContentLength)
	}
	body, err := c.Body()
	if err != nil {
		return 0
	}
	return len(body)
}

func (c *Context) Bytes(status int, contentType string, body []byte) error {
	c.setContentType(contentType)
	c.WriteHeader(status)
	if len(body) == 0 {
		return nil
	}
	_, err := c.w.Write(body)
	return err
}

func (c *Context) Stream(status int, contentType string, reader io.Reader) error {
	c.setContentType(contentType)
	c.WriteHeader(status)
	if reader == nil {
		return nil
	}
	_, err := io.Copy(c.w, reader)
	return err
}

// CopyBody copies the current request body into the response.
// Use Body directly only when the handler consumes the data before returning.
func (c *Context) CopyBody(status int, contentType string) error {
	body, err := c.Body()
	if err != nil {
		return err
	}
	c.setContentType(contentType)
	c.WriteHeader(status)
	if len(body) == 0 {
		return nil
	}
	copied := make([]byte, len(body))
	copy(copied, body)
	_, err = c.w.Write(copied)
	return err
}

func (c *Context) Text(status int, text string) error {
	c.setContentType("text/plain; charset=utf-8")
	c.WriteHeader(status)
	_, err := io.WriteString(c.w, text)
	return err
}

func (c *Context) JSON(status int, value interface{}) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	c.setContentType("application/json; charset=utf-8")
	c.WriteHeader(status)
	_, err = c.w.Write(body)
	return err
}

func (c *Context) Query(key string) string {
	if c.r.URL == nil {
		return ""
	}
	return c.r.URL.Query().Get(key)
}

func normalizeConfig(config Config) Config {
	if config.Addr == "" {
		config.Addr = DefaultAddr
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

func errorLogger(logger interface {
	Printf(string, ...interface{})
}) *log.Logger {
	if logger == nil {
		return nil
	}
	return log.New(&logWriter{logger: logger}, "", 0)
}

type logWriter struct {
	logger interface {
		Printf(string, ...interface{})
	}
}

func (l *logWriter) Write(p []byte) (int, error) {
	l.logger.Printf("%s", string(p))
	return len(p), nil
}

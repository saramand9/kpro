package httpx

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	hertzserver "github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/config"
	"github.com/cloudwego/hertz/pkg/common/hlog"
)

const (
	DefaultAddr            = ":8080"
	DefaultShutdownTimeout = 5 * time.Second
)

func init() {
	hlog.SetLevel(hlog.LevelWarn)
}

type Config struct {
	Addr            string
	TLS             bool
	CertFile        string
	KeyFile         string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
}

type Handler func(*Context) error

type Middleware func(Handler) Handler

type compiledRoutes map[string]map[string]Handler

type Endpoint struct {
	config Config
	server *hertzserver.Hertz

	mu          sync.RWMutex
	routes      map[string]map[string]Handler
	middlewares []Middleware
	compiled    atomic.Value
	contextPool sync.Pool
}

func New(config Config) *Endpoint {
	config = normalizeConfig(config)
	e := &Endpoint{
		config: config,
		routes: make(map[string]map[string]Handler),
	}
	e.compiled.Store(compiledRoutes{})
	e.contextPool.New = func() interface{} {
		return new(Context)
	}
	return e
}

func (e *Endpoint) Name() string {
	if e.config.TLS {
		return "https"
	}
	return "http"
}

func (e *Endpoint) Use(middlewares ...Middleware) *Endpoint {
	e.mu.Lock()
	e.middlewares = append(e.middlewares, middlewares...)
	e.rebuildCompiledLocked()
	e.mu.Unlock()
	return e
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
	server, err := e.buildServer()
	if err != nil {
		return err
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
	return server.Run()
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

func (e *Endpoint) buildServer() (*hertzserver.Hertz, error) {
	options := []config.Option{
		hertzserver.WithHostPorts(e.config.Addr),
		hertzserver.WithHandleMethodNotAllowed(true),
		hertzserver.WithDisablePrintRoute(true),
	}
	if e.config.ReadTimeout > 0 {
		options = append(options, hertzserver.WithReadTimeout(e.config.ReadTimeout))
	}
	if e.config.WriteTimeout > 0 {
		options = append(options, hertzserver.WithWriteTimeout(e.config.WriteTimeout))
	}
	if e.config.IdleTimeout > 0 {
		options = append(options, hertzserver.WithIdleTimeout(e.config.IdleTimeout))
	}
	if e.config.ShutdownTimeout > 0 {
		options = append(options, hertzserver.WithExitWaitTime(e.config.ShutdownTimeout))
	}
	if e.config.TLS {
		cert, err := tls.LoadX509KeyPair(e.config.CertFile, e.config.KeyFile)
		if err != nil {
			return nil, err
		}
		options = append(options, hertzserver.WithTLS(&tls.Config{Certificates: []tls.Certificate{cert}}))
	}

	server := hertzserver.New(options...)
	e.mu.RLock()
	defer e.mu.RUnlock()
	for path, methods := range e.routes {
		for method := range methods {
			server.Handle(method, path, e.servePath(path))
		}
	}
	return server, nil
}

func (e *Endpoint) servePath(path string) hertzapp.HandlerFunc {
	return func(_ context.Context, hctx *hertzapp.RequestContext) {
		method := string(hctx.Request.Header.Method())
		h := e.lookupHandler(path, method)
		if h == nil {
			hctx.String(http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		ctx := e.acquireContext(hctx)
		defer e.releaseContext(ctx)
		if err := h(ctx); err != nil {
			hctx.String(http.StatusInternalServerError, err.Error())
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
			methods[method] = wrapMiddleware(h, e.middlewares)
		}
		routes[path] = methods
	}
	e.compiled.Store(routes)
}

func wrapMiddleware(h Handler, middlewares []Middleware) Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}
	return h
}

func (e *Endpoint) acquireContext(hctx *hertzapp.RequestContext) *Context {
	ctx := e.contextPool.Get().(*Context)
	ctx.hctx = hctx
	ctx.Request = ctx.buildRequest(hctx)
	return ctx
}

func (e *Endpoint) releaseContext(ctx *Context) {
	ctx.hctx = nil
	ctx.Request = nil
	ctx.header = nil
	ctx.status = 0
	ctx.wroteHeader = false
	ctx.request = http.Request{}
	ctx.requestURL = url.URL{}
	e.contextPool.Put(ctx)
}

type Context struct {
	Request *http.Request

	hctx        *hertzapp.RequestContext
	header      http.Header
	status      int
	wroteHeader bool
	request     http.Request
	requestURL  url.URL
}

func (c *Context) buildRequest(hctx *hertzapp.RequestContext) *http.Request {
	uri := hctx.URI()
	c.requestURL.Path = string(uri.Path())
	c.requestURL.RawQuery = string(uri.QueryString())
	c.request.Method = string(hctx.Request.Header.Method())
	c.request.URL = &c.requestURL
	c.request.Host = string(uri.Host())
	c.request.ContentLength = int64(len(hctx.Request.Body()))
	return &c.request
}

func (c *Context) Header() http.Header {
	if c.header == nil {
		c.header = make(http.Header)
	}
	return c.header
}

func (c *Context) WriteHeader(status int) {
	if c.wroteHeader {
		return
	}
	c.wroteHeader = true
	c.status = status
	c.applyHeaders()
	c.hctx.SetStatusCode(status)
}

func (c *Context) Write(body []byte) (int, error) {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK)
	}
	c.hctx.Response.AppendBody(body)
	return len(body), nil
}

func (c *Context) applyHeaders() {
	for key, values := range c.header {
		for _, value := range values {
			c.hctx.Response.Header.Set(key, value)
		}
	}
}

func (c *Context) Body() ([]byte, error) {
	return c.hctx.Body()
}

func (c *Context) Bytes(status int, contentType string, body []byte) error {
	if contentType != "" {
		c.Header().Set("Content-Type", contentType)
	}
	c.WriteHeader(status)
	if len(body) == 0 {
		return nil
	}
	c.hctx.Response.SetBody(body)
	return nil
}

func (c *Context) Stream(status int, contentType string, reader io.Reader) error {
	if contentType != "" {
		c.Header().Set("Content-Type", contentType)
	}
	c.WriteHeader(status)
	if reader == nil {
		return nil
	}
	c.hctx.SetBodyStream(reader, -1)
	return nil
}

func (c *Context) CopyBody(status int, contentType string) error {
	body, err := c.Body()
	if err != nil {
		return err
	}
	if contentType != "" {
		c.Header().Set("Content-Type", contentType)
	}
	c.WriteHeader(status)
	c.hctx.Response.SetBodyRaw(body)
	return nil
}

func (c *Context) Text(status int, text string) error {
	c.Header().Set("Content-Type", "text/plain; charset=utf-8")
	c.WriteHeader(status)
	c.hctx.Response.SetBodyString(text)
	return nil
}

func (c *Context) JSON(status int, value interface{}) error {
	c.applyHeaders()
	c.hctx.JSON(status, value)
	return nil
}

func (c *Context) Query(key string) string {
	return string(c.hctx.QueryArgs().Peek(key))
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

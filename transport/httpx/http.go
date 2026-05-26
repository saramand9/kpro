package httpx

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/valyala/fasthttp"
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
	FastContext     bool
}

type Handler func(*Context) error

type Middleware func(Handler) Handler

type compiledRoutes map[string]map[string]Handler

type Endpoint struct {
	config Config
	server *fasthttp.Server

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
		return server.ListenAndServeTLS(e.config.Addr, e.config.CertFile, e.config.KeyFile)
	}
	return server.ListenAndServe(e.config.Addr)
}

func (e *Endpoint) Shutdown(ctx context.Context) error {
	e.mu.RLock()
	server := e.server
	e.mu.RUnlock()
	if server == nil {
		return nil
	}
	return server.ShutdownWithContext(ctx)
}

func (e *Endpoint) buildServer() *fasthttp.Server {
	return &fasthttp.Server{
		Handler:               e.serve,
		ReadTimeout:           e.config.ReadTimeout,
		WriteTimeout:          e.config.WriteTimeout,
		IdleTimeout:           e.config.IdleTimeout,
		NoDefaultServerHeader: true,
	}
}

func (e *Endpoint) serve(fctx *fasthttp.RequestCtx) {
	routes, _ := e.compiled.Load().(compiledRoutes)
	methods := routes[string(fctx.Path())]
	if methods == nil {
		fctx.SetStatusCode(http.StatusNotFound)
		fctx.SetBodyString("not found")
		return
	}
	h := methods[string(fctx.Method())]
	if h == nil {
		fctx.SetStatusCode(http.StatusMethodNotAllowed)
		fctx.SetBodyString("method not allowed")
		return
	}
	e.serveHandler(h)(fctx)
}

func (e *Endpoint) serveHandler(h Handler) fasthttp.RequestHandler {
	return func(fctx *fasthttp.RequestCtx) {
		ctx := e.acquireContext(fctx)
		defer e.releaseContext(ctx)
		if err := h(ctx); err != nil {
			fctx.SetStatusCode(http.StatusInternalServerError)
			fctx.SetBodyString(err.Error())
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

func (e *Endpoint) acquireContext(fctx *fasthttp.RequestCtx) *Context {
	ctx := e.contextPool.Get().(*Context)
	ctx.fctx = fctx
	if !e.config.FastContext {
		ctx.Request = ctx.buildRequest(fctx)
	}
	return ctx
}

func (e *Endpoint) releaseContext(ctx *Context) {
	ctx.fctx = nil
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

	fctx        *fasthttp.RequestCtx
	header      http.Header
	status      int
	wroteHeader bool
	request     http.Request
	requestURL  url.URL
}

func (c *Context) buildRequest(fctx *fasthttp.RequestCtx) *http.Request {
	uri := fctx.URI()
	c.requestURL.Path = string(uri.Path())
	c.requestURL.RawQuery = string(uri.QueryString())
	c.request.Method = string(fctx.Method())
	c.request.URL = &c.requestURL
	c.request.Host = string(fctx.Host())
	c.request.ContentLength = int64(len(fctx.Request.Body()))
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
	c.fctx.SetStatusCode(status)
}

func (c *Context) Write(body []byte) (int, error) {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK)
	}
	c.fctx.Response.AppendBody(body)
	return len(body), nil
}

func (c *Context) applyHeaders() {
	for key, values := range c.header {
		for _, value := range values {
			c.fctx.Response.Header.Set(key, value)
		}
	}
}

func (c *Context) setContentType(contentType string) {
	if contentType == "" {
		return
	}
	if c.header == nil {
		c.fctx.SetContentType(contentType)
		return
	}
	c.header.Set("Content-Type", contentType)
}

func (c *Context) Body() ([]byte, error) {
	return c.fctx.Request.Body(), nil
}

func (c *Context) Method() string {
	return string(c.MethodBytes())
}

func (c *Context) MethodBytes() []byte {
	return c.fctx.Method()
}

func (c *Context) Path() string {
	return string(c.PathBytes())
}

func (c *Context) PathBytes() []byte {
	return c.fctx.Path()
}

func (c *Context) RawQuery() string {
	return string(c.fctx.URI().QueryString())
}

func (c *Context) Host() string {
	return string(c.fctx.Host())
}

func (c *Context) ContentLength() int {
	return len(c.fctx.Request.Body())
}

func (c *Context) Bytes(status int, contentType string, body []byte) error {
	c.setContentType(contentType)
	c.WriteHeader(status)
	if len(body) == 0 {
		return nil
	}
	c.fctx.SetBody(body)
	return nil
}

func (c *Context) Stream(status int, contentType string, reader io.Reader) error {
	c.setContentType(contentType)
	c.WriteHeader(status)
	if reader == nil {
		return nil
	}
	c.fctx.SetBodyStream(reader, -1)
	return nil
}

func (c *Context) CopyBody(status int, contentType string) error {
	body, err := c.Body()
	if err != nil {
		return err
	}
	c.setContentType(contentType)
	c.WriteHeader(status)
	c.fctx.Response.SetBodyRaw(body)
	return nil
}

func (c *Context) Text(status int, text string) error {
	c.setContentType("text/plain; charset=utf-8")
	c.WriteHeader(status)
	c.fctx.SetBodyString(text)
	return nil
}

func (c *Context) JSON(status int, value interface{}) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	c.setContentType("application/json; charset=utf-8")
	c.WriteHeader(status)
	c.fctx.SetBody(body)
	return nil
}

func (c *Context) Query(key string) string {
	return string(c.fctx.QueryArgs().Peek(key))
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

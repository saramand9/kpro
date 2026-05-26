package httpx

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const (
	DefaultAddr            = ":8080"
	DefaultShutdownTimeout = 5 * time.Second
	copyBufferSize         = 32 * 1024
)

var copyBufferPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, copyBufferSize)
		return &buf
	},
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
	mux    *http.ServeMux
	server *http.Server

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
		mux:    http.NewServeMux(),
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
		e.mux.HandleFunc(path, e.servePath(path))
	}
	e.routes[path][method] = h
	e.rebuildCompiledLocked()
	e.mu.Unlock()
	return e
}

func (e *Endpoint) ListenAndServe(ctx context.Context) error {
	e.server = &http.Server{
		Addr:         e.config.Addr,
		Handler:      e.mux,
		ReadTimeout:  e.config.ReadTimeout,
		WriteTimeout: e.config.WriteTimeout,
		IdleTimeout:  e.config.IdleTimeout,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), e.config.ShutdownTimeout)
		defer cancel()
		_ = e.Shutdown(shutdownCtx)
	}()
	if e.config.TLS {
		return ignoreServerClosed(e.server.ListenAndServeTLS(e.config.CertFile, e.config.KeyFile))
	}
	return ignoreServerClosed(e.server.ListenAndServe())
}

func (e *Endpoint) Shutdown(ctx context.Context) error {
	if e.server == nil {
		return nil
	}
	return e.server.Shutdown(ctx)
}

func (e *Endpoint) servePath(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := e.lookupHandler(path, r.Method)
		if h == nil {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ctx := e.acquireContext(w, r)
		defer e.releaseContext(ctx)
		if err := h(ctx); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
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

func (e *Endpoint) acquireContext(w http.ResponseWriter, r *http.Request) *Context {
	ctx := e.contextPool.Get().(*Context)
	ctx.ResponseWriter = w
	ctx.Request = r
	return ctx
}

func (e *Endpoint) releaseContext(ctx *Context) {
	ctx.ResponseWriter = nil
	ctx.Request = nil
	e.contextPool.Put(ctx)
}

type Context struct {
	http.ResponseWriter
	Request *http.Request
}

func (c *Context) Body() ([]byte, error) {
	if c.Request.Body == nil {
		return nil, nil
	}
	return io.ReadAll(c.Request.Body)
}

func (c *Context) Bytes(status int, contentType string, body []byte) error {
	if contentType != "" {
		c.Header().Set("Content-Type", contentType)
	}
	c.WriteHeader(status)
	if len(body) == 0 {
		return nil
	}
	_, err := c.ResponseWriter.Write(body)
	return err
}

func (c *Context) Stream(status int, contentType string, reader io.Reader) error {
	if contentType != "" {
		c.Header().Set("Content-Type", contentType)
	}
	if reader == nil {
		c.WriteHeader(status)
		return nil
	}
	writer := statusResponseWriter{
		ResponseWriter: c.ResponseWriter,
		status:         status,
	}
	buf := acquireCopyBuffer()
	defer releaseCopyBuffer(buf)
	_, err := io.CopyBuffer(&writer, reader, *buf)
	writer.finish()
	return err
}

func (c *Context) CopyBody(status int, contentType string) error {
	if c.Request.Body == nil {
		return c.Bytes(status, contentType, nil)
	}
	if c.Request.ContentLength >= 0 && c.Request.ContentLength <= copyBufferSize {
		if c.Request.ContentLength == 0 {
			return c.Bytes(status, contentType, nil)
		}
		buf := acquireCopyBuffer()
		defer releaseCopyBuffer(buf)
		body := (*buf)[:int(c.Request.ContentLength)]
		if _, err := io.ReadFull(c.Request.Body, body); err != nil {
			return err
		}
		return c.Bytes(status, contentType, body)
	}
	return c.Stream(status, contentType, c.Request.Body)
}

func (c *Context) Text(status int, text string) error {
	c.Header().Set("Content-Type", "text/plain; charset=utf-8")
	c.WriteHeader(status)
	_, err := io.WriteString(c.ResponseWriter, text)
	return err
}

type statusResponseWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusResponseWriter) Write(p []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(w.status)
	}
	return w.ResponseWriter.Write(p)
}

func (w *statusResponseWriter) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.wrote = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusResponseWriter) finish() {
	if !w.wrote {
		w.WriteHeader(w.status)
	}
}

func acquireCopyBuffer() *[]byte {
	return copyBufferPool.Get().(*[]byte)
}

func releaseCopyBuffer(buf *[]byte) {
	*buf = (*buf)[:cap(*buf)]
	copyBufferPool.Put(buf)
}

func (c *Context) JSON(status int, value interface{}) error {
	c.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.WriteHeader(status)
	return json.NewEncoder(c.ResponseWriter).Encode(value)
}

func (c *Context) Query(key string) string {
	return c.Request.URL.Query().Get(key)
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
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

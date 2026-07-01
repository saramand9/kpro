package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
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

type Middleware func(Handler) Handler

type compiledRoutes map[string]map[string]Handler

type Endpoint struct {
	config Config
	server *http.Server

	mu          sync.RWMutex
	routes      map[string]map[string]Handler
	middlewares []Middleware
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

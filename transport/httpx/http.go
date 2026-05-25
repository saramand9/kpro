package httpx

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
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
}

type Handler func(*Context) error

type Middleware func(Handler) Handler

type Endpoint struct {
	config Config
	mux    *http.ServeMux
	server *http.Server

	mu          sync.RWMutex
	routes      map[string]map[string]Handler
	middlewares []Middleware
}

func New(config Config) *Endpoint {
	config = normalizeConfig(config)
	return &Endpoint{
		config: config,
		mux:    http.NewServeMux(),
		routes: make(map[string]map[string]Handler),
	}
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
		e.mu.RLock()
		h := e.routes[path][r.Method]
		middlewares := append([]Middleware(nil), e.middlewares...)
		e.mu.RUnlock()
		if h == nil {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		for i := len(middlewares) - 1; i >= 0; i-- {
			h = middlewares[i](h)
		}
		if err := h(&Context{ResponseWriter: w, Request: r}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
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

func (c *Context) Text(status int, text string) error {
	c.Header().Set("Content-Type", "text/plain; charset=utf-8")
	c.WriteHeader(status)
	_, err := io.WriteString(c.ResponseWriter, text)
	return err
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

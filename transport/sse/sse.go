// File: transport/sse/sse.go
// Purpose: Implements the Server-Sent Events transport adapter.

package sse

import (
	"context"
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

// Endpoint serves one SSE stream route and wraps it with middleware.
type Endpoint struct {
	config Config
	server *http.Server

	mu          sync.RWMutex
	handler     Handler
	middlewares []core.Middleware
}

// New creates an SSE endpoint with normalized defaults.
func New(config Config) *Endpoint {
	return &Endpoint{config: normalizeConfig(config)}
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
	if addr := parseRemoteAddr(r.RemoteAddr); addr != nil {
		remote = addr.String()
	}
	return core.NewInvocation(core.InvocationOptions{
		Context:   r.Context(),
		Transport: transport,
		Operation: sseOperation(r.Method, r.URL.Path),
		ConnID:    sessionIDFromRemote(r.RemoteAddr),
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
		Native: native,
		Done:   r.Context().Done(),
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
}

// Done returns the request cancellation channel.
func (c *Context) Done() <-chan struct{} {
	return c.Request.Context().Done()
}

// Send writes one SSE event and flushes the stream.
func (c *Context) Send(event string, data []byte) error {
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
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

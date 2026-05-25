package sse

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	DefaultAddr            = ":8081"
	DefaultPath            = "/events"
	DefaultShutdownTimeout = 5 * time.Second
)

type Config struct {
	Addr            string
	Path            string
	TLS             bool
	CertFile        string
	KeyFile         string
	WriteTimeout    time.Duration
	ShutdownTimeout time.Duration
}

type Handler func(*Context) error

type Endpoint struct {
	config  Config
	handler Handler
	server  *http.Server
}

func New(config Config) *Endpoint {
	return &Endpoint{config: normalizeConfig(config)}
}

func (e *Endpoint) Name() string {
	return "sse"
}

func (e *Endpoint) Handle(h Handler) *Endpoint {
	e.handler = h
	return e
}

func (e *Endpoint) ListenAndServe(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc(e.config.Path, e.serve)
	e.server = &http.Server{
		Addr:         e.config.Addr,
		Handler:      mux,
		WriteTimeout: e.config.WriteTimeout,
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

func (e *Endpoint) serve(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	if e.handler == nil {
		return
	}
	ctx := &Context{
		ResponseWriter: w,
		Request:        r,
		flusher:        flusher,
	}
	if err := e.handler(ctx); err != nil {
		_ = ctx.Send("error", []byte(err.Error()))
	}
}

type Context struct {
	http.ResponseWriter
	Request *http.Request
	flusher http.Flusher
	mu      sync.Mutex
}

func (c *Context) Done() <-chan struct{} {
	return c.Request.Context().Done()
}

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

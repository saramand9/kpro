package http3

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	qh3 "github.com/quic-go/quic-go/http3"
	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/transport/httpx"
)

const (
	DefaultAddr            = ":8443"
	DefaultShutdownTimeout = 5 * time.Second
)

type Config struct {
	Addr             string
	CertFile         string
	KeyFile          string
	TLSConfig        *tls.Config
	QUICConfig       *quic.Config
	HandshakeTimeout time.Duration
	IdleTimeout      time.Duration
	ShutdownTimeout  time.Duration
	MaxHeaderBytes   int
}

type Handler = httpx.Handler
type Middleware = core.Middleware
type Context = httpx.Context

type Endpoint struct {
	config Config
	http   *httpx.Endpoint

	mu     sync.RWMutex
	server *qh3.Server
}

func New(config Config) *Endpoint {
	config = normalizeConfig(config)
	httpEndpoint := httpx.New(httpx.Config{
		Addr:            config.Addr,
		TLS:             true,
		CertFile:        config.CertFile,
		KeyFile:         config.KeyFile,
		IdleTimeout:     config.IdleTimeout,
		ShutdownTimeout: config.ShutdownTimeout,
	})
	return &Endpoint{config: config, http: httpEndpoint}
}

func (e *Endpoint) Name() string {
	return "http3"
}

func (e *Endpoint) Use(middlewares ...Middleware) *Endpoint {
	e.http.Use(middlewares...)
	return e
}

func (e *Endpoint) Get(path string, h Handler) *Endpoint {
	e.http.Get(path, h)
	return e
}

func (e *Endpoint) Post(path string, h Handler) *Endpoint {
	e.http.Post(path, h)
	return e
}

func (e *Endpoint) Put(path string, h Handler) *Endpoint {
	e.http.Put(path, h)
	return e
}

func (e *Endpoint) Delete(path string, h Handler) *Endpoint {
	e.http.Delete(path, h)
	return e
}

func (e *Endpoint) Handle(method, path string, h Handler) *Endpoint {
	e.http.Handle(method, path, h)
	return e
}

func (e *Endpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e.http.ServeHTTP(w, r)
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

	if e.config.TLSConfig != nil {
		return ignoreServerClosed(server.ListenAndServe())
	}
	return ignoreServerClosed(server.ListenAndServeTLS(e.config.CertFile, e.config.KeyFile))
}

func (e *Endpoint) Shutdown(ctx context.Context) error {
	e.mu.RLock()
	server := e.server
	e.mu.RUnlock()
	if server == nil {
		return nil
	}

	done := make(chan error, 1)
	go func() {
		done <- server.Close()
	}()
	select {
	case err := <-done:
		return ignoreServerClosed(err)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *Endpoint) buildServer() *qh3.Server {
	return &qh3.Server{
		Addr:           e.config.Addr,
		TLSConfig:      e.config.TLSConfig,
		QUICConfig:     e.buildQUICConfig(),
		Handler:        e.http,
		MaxHeaderBytes: e.config.MaxHeaderBytes,
	}
}

func (e *Endpoint) buildQUICConfig() *quic.Config {
	if e.config.QUICConfig == nil && e.config.HandshakeTimeout <= 0 && e.config.IdleTimeout <= 0 {
		return nil
	}
	cfg := &quic.Config{Allow0RTT: true}
	if e.config.QUICConfig != nil {
		cfg = e.config.QUICConfig.Clone()
	}
	if e.config.HandshakeTimeout > 0 {
		cfg.HandshakeIdleTimeout = e.config.HandshakeTimeout
	}
	if e.config.IdleTimeout > 0 {
		cfg.MaxIdleTimeout = e.config.IdleTimeout
	}
	return cfg
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

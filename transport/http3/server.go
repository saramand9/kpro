package http3

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	qh3 "github.com/quic-go/quic-go/http3"
	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/transport/httpx"
	"github.com/saramand9/bignet/kpro/transport/internal/serveutil"
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
	config    Config
	rawConfig Config
	http      *httpx.Endpoint

	mu        sync.RWMutex
	accepting bool
	started   bool
	server    *qh3.Server
	listener  *quic.EarlyListener
}

func New(config Config) *Endpoint {
	rawConfig := config
	config = cloneOwnedConfig(config)
	config = normalizeConfig(config)
	httpEndpoint := httpx.NewWithTransport(httpx.Config{
		Addr:            config.Addr,
		TLS:             true,
		CertFile:        config.CertFile,
		KeyFile:         config.KeyFile,
		IdleTimeout:     config.IdleTimeout,
		ShutdownTimeout: config.ShutdownTimeout,
	}, core.TransportHTTP3)
	return &Endpoint{config: config, rawConfig: rawConfig, http: httpEndpoint, accepting: true}
}

func (e *Endpoint) Name() string {
	return "http3"
}

func (e *Endpoint) Use(middlewares ...Middleware) *Endpoint {
	e.ApplyMiddleware(middlewares...)
	return e
}

// ApplyMiddleware implements transport.MiddlewareTarget for app-level middleware.
func (e *Endpoint) ApplyMiddleware(middlewares ...core.Middleware) {
	e.http.ApplyMiddleware(middlewares...)
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
	if err := e.Validate(); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := e.claimStart(); err != nil {
		return err
	}
	tlsConfig, err := e.buildTLSConfig()
	if err != nil {
		e.rollbackStart()
		return err
	}
	server := e.buildServer(tlsConfig)
	quicConfig := e.buildQUICConfig()
	if quicConfig == nil {
		quicConfig = &quic.Config{Allow0RTT: true}
	}
	listener, err := quic.ListenAddrEarly(e.config.Addr, qh3.ConfigureTLSConfig(tlsConfig), quicConfig)
	if err != nil {
		e.rollbackStart()
		return err
	}
	if !e.publishServer(server, listener) {
		_ = listener.Close()
		_ = server.Close()
		return core.ErrClosed
	}
	defer e.finishServe(server, listener)

	serveDone := make(chan struct{})
	watcherDone := serveutil.ShutdownOnCancel(ctx, serveDone, func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), e.config.ShutdownTimeout)
		defer cancel()
		_ = e.Shutdown(shutdownCtx)
	})
	defer serveutil.FinishContextWatcher(serveDone, watcherDone)

	return ignoreServerClosed(server.ServeListener(listener))
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

func (e *Endpoint) publishServer(server *qh3.Server, listener *quic.EarlyListener) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.accepting {
		return false
	}
	e.server = server
	e.listener = listener
	return true
}

func (e *Endpoint) finishServe(server *qh3.Server, listener *quic.EarlyListener) {
	e.mu.Lock()
	e.accepting = false
	e.mu.Unlock()
	_ = listener.Close()
	_ = server.Close()
}

func (e *Endpoint) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	e.accepting = false
	server := e.server
	listener := e.listener
	e.mu.Unlock()
	var listenerErr error
	if listener != nil {
		listenerErr = ignoreServerClosed(listener.Close())
	}
	if server == nil {
		return listenerErr
	}

	done := make(chan error, 1)
	go func() {
		done <- server.Close()
	}()
	select {
	case err := <-done:
		return errors.Join(listenerErr, ignoreServerClosed(err))
	case <-ctx.Done():
		return errors.Join(listenerErr, ctx.Err())
	}
}

func (e *Endpoint) buildTLSConfig() (*tls.Config, error) {
	if e.config.TLSConfig != nil {
		return e.config.TLSConfig.Clone(), nil
	}
	certificate, err := tls.LoadX509KeyPair(e.config.CertFile, e.config.KeyFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{certificate}}, nil
}

func (e *Endpoint) buildServer(tlsConfig *tls.Config) *qh3.Server {
	return &qh3.Server{
		Addr:           e.config.Addr,
		TLSConfig:      tlsConfig,
		QUICConfig:     e.buildQUICConfig(),
		Handler:        e.http,
		MaxHeaderBytes: e.config.MaxHeaderBytes,
		ConnContext: func(ctx context.Context, conn quic.Connection) context.Context {
			remote := ""
			if conn.RemoteAddr() != nil {
				remote = conn.RemoteAddr().String()
			}
			return e.http.WithConnectionContext(ctx, remote, conn.Context().Done())
		},
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

func cloneOwnedConfig(config Config) Config {
	if config.TLSConfig != nil {
		tlsConfig := config.TLSConfig.Clone()
		tlsConfig.NextProtos = append([]string(nil), config.TLSConfig.NextProtos...)
		tlsConfig.Certificates = append([]tls.Certificate(nil), config.TLSConfig.Certificates...)
		tlsConfig.CipherSuites = append([]uint16(nil), config.TLSConfig.CipherSuites...)
		tlsConfig.CurvePreferences = append([]tls.CurveID(nil), config.TLSConfig.CurvePreferences...)
		config.TLSConfig = tlsConfig
	}
	if config.QUICConfig != nil {
		quicConfig := config.QUICConfig.Clone()
		quicConfig.Versions = append([]quic.Version(nil), config.QUICConfig.Versions...)
		config.QUICConfig = quicConfig
	}
	return config
}

func ignoreServerClosed(err error) error {
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, quic.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

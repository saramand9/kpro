package http3

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
	qh3 "github.com/quic-go/quic-go/http3"
	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/transport/httpx"
	"github.com/saramand9/kpro/transport/internal/serveutil"
)

const (
	DefaultAddr             = ":8443"
	DefaultHandshakeTimeout = httpx.DefaultReadHeaderTimeout
	DefaultReadTimeout      = httpx.DefaultReadTimeout
	DefaultWriteTimeout     = httpx.DefaultWriteTimeout
	DefaultIdleTimeout      = httpx.DefaultIdleTimeout
	DefaultShutdownTimeout  = 5 * time.Second
	DefaultMaxHeaderBytes   = httpx.DefaultMaxHeaderBytes
	DefaultMaxConnections   = 10000
)

type Config struct {
	Addr             string
	CertFile         string
	KeyFile          string
	TLSConfig        *tls.Config
	QUICConfig       *quic.Config
	HandshakeTimeout time.Duration
	// ReadTimeout bounds reading one HTTP/3 request stream after dispatch.
	ReadTimeout time.Duration
	// WriteTimeout bounds writes for one HTTP/3 response stream.
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
	MaxHeaderBytes  int
	// MaxBodyBytes is enforced by the embedded HTTP request context.
	MaxBodyBytes int64
	// MaxConnections caps active physical HTTP/3 QUIC connections.
	MaxConnections int
	// Allow0RTT opts all routes into replayable HTTP/3 early data. It is off
	// by default. Enable it only when the application enforces idempotency or
	// provides its own replay protection for every accepted request.
	Allow0RTT bool
}

type Handler = httpx.Handler
type Middleware = core.Middleware
type Context = httpx.Context
type RouteGroup = httpx.RouteGroup

type Endpoint struct {
	config    Config
	rawConfig Config
	http      *httpx.Endpoint

	mu           sync.RWMutex
	shutdownMu   sync.Mutex
	accepting    bool
	started      bool
	shuttingDown bool
	server       *qh3.Server
	listener     qh3.QUICListener
	limiter      *connectionLimiter
	ready        chan struct{}
	readyOnce    sync.Once
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
		ReadTimeout:     config.ReadTimeout,
		WriteTimeout:    config.WriteTimeout,
		IdleTimeout:     config.IdleTimeout,
		ShutdownTimeout: config.ShutdownTimeout,
		MaxHeaderBytes:  config.MaxHeaderBytes,
		MaxBodyBytes:    config.MaxBodyBytes,
	}, core.TransportHTTP3)
	return &Endpoint{
		config:    config,
		rawConfig: rawConfig,
		http:      httpEndpoint,
		accepting: true,
		limiter:   &connectionLimiter{limit: int64(config.MaxConnections)},
		ready:     make(chan struct{}),
	}
}

func (e *Endpoint) Name() string {
	return "http3"
}

// Ready is closed once the QUIC listener is successfully bound and published.
func (e *Endpoint) Ready() <-chan struct{} {
	if e == nil {
		return nil
	}
	return e.ready
}

func (e *Endpoint) Use(middlewares ...Middleware) *Endpoint {
	e.ApplyMiddleware(middlewares...)
	return e
}

// ApplyMiddleware implements transport.MiddlewareTarget for app-level middleware.
func (e *Endpoint) ApplyMiddleware(middlewares ...core.Middleware) {
	e.http.ApplyMiddleware(middlewares...)
}

func (e *Endpoint) Get(path string, h Handler, middlewares ...Middleware) *Endpoint {
	e.http.Get(path, h, middlewares...)
	return e
}

func (e *Endpoint) Post(path string, h Handler, middlewares ...Middleware) *Endpoint {
	e.http.Post(path, h, middlewares...)
	return e
}

func (e *Endpoint) Put(path string, h Handler, middlewares ...Middleware) *Endpoint {
	e.http.Put(path, h, middlewares...)
	return e
}

func (e *Endpoint) Delete(path string, h Handler, middlewares ...Middleware) *Endpoint {
	e.http.Delete(path, h, middlewares...)
	return e
}

func (e *Endpoint) Handle(method, path string, h Handler, middlewares ...Middleware) *Endpoint {
	e.http.Handle(method, path, h, middlewares...)
	return e
}

func (e *Endpoint) HandleChecked(method, path string, h Handler, middlewares ...Middleware) error {
	return e.http.HandleChecked(method, path, h, middlewares...)
}

func (e *Endpoint) Group(prefix string, middlewares ...Middleware) *RouteGroup {
	return e.http.Group(prefix, middlewares...)
}

func (e *Endpoint) GroupChecked(prefix string, middlewares ...Middleware) (*RouteGroup, error) {
	return e.http.GroupChecked(prefix, middlewares...)
}

func (e *Endpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e.serveHTTP(w, r)
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
	rawListener, err := quic.ListenAddrEarly(e.config.Addr, qh3.ConfigureTLSConfig(tlsConfig), quicConfig)
	if err != nil {
		e.rollbackStart()
		return err
	}
	var listener qh3.QUICListener = rawListener
	if e.limiter != nil {
		listener = &limitedQUICListener{QUICListener: listener, limiter: e.limiter}
	}
	if !e.publishServer(server, listener) {
		_ = listener.Close()
		_ = server.Close()
		return core.ErrClosed
	}
	e.signalReady()
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

func (e *Endpoint) publishServer(server *qh3.Server, listener qh3.QUICListener) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.accepting {
		return false
	}
	e.server = server
	e.listener = listener
	return true
}

func (e *Endpoint) finishServe(server *qh3.Server, listener qh3.QUICListener) {
	e.mu.Lock()
	e.accepting = false
	shuttingDown := e.shuttingDown
	e.mu.Unlock()
	_ = listener.Close()
	// Server.Shutdown owns active connections while it sends GOAWAY and waits
	// for in-flight requests. An unconditional Close here would race that drain
	// as soon as ServeListener returns after the graceful context is canceled.
	if !shuttingDown {
		_ = server.Close()
	}
}

// Shutdown stops new HTTP/3 work, sends GOAWAY, and waits for active requests
// through quic-go's graceful shutdown until ctx expires. Expiry forces the
// remaining HTTP/3 connections closed and is returned to the caller.
func (e *Endpoint) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.shutdownMu.Lock()
	defer e.shutdownMu.Unlock()

	e.mu.Lock()
	e.accepting = false
	e.shuttingDown = true
	server := e.server
	listener := e.listener
	e.mu.Unlock()

	var listenerErr error
	if listener != nil {
		listenerErr = ignoreServerClosed(listener.Close())
		e.mu.Lock()
		e.listener = nil
		e.mu.Unlock()
	}
	var shutdownErr error
	if server != nil {
		shutdownErr = ignoreServerClosed(server.Shutdown(ctx))
	}
	return errors.Join(listenerErr, shutdownErr)
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
		Handler:        http.HandlerFunc(e.serveHTTP),
		MaxHeaderBytes: e.config.MaxHeaderBytes,
		ConnContext: func(ctx context.Context, conn *quic.Conn) context.Context {
			remote := ""
			if conn.RemoteAddr() != nil {
				remote = conn.RemoteAddr().String()
			}
			return e.http.WithConnectionContext(ctx, remote, conn.Context().Done())
		},
	}
}

func (e *Endpoint) serveHTTP(w http.ResponseWriter, r *http.Request) {
	controller := http.NewResponseController(w)
	if e.config.ReadTimeout > 0 {
		_ = controller.SetReadDeadline(time.Now().Add(e.config.ReadTimeout))
	}
	if e.config.WriteTimeout > 0 {
		_ = controller.SetWriteDeadline(time.Now().Add(e.config.WriteTimeout))
	}
	e.http.ServeHTTP(w, r)
}

func (e *Endpoint) buildQUICConfig() *quic.Config {
	cfg := &quic.Config{}
	if e.config.QUICConfig != nil {
		cfg = e.config.QUICConfig.Clone()
	}
	if e.config.HandshakeTimeout > 0 {
		cfg.HandshakeIdleTimeout = e.config.HandshakeTimeout
	}
	if e.config.IdleTimeout > 0 {
		cfg.MaxIdleTimeout = e.config.IdleTimeout
	}
	// The top-level opt-in is authoritative so a shared low-level QUICConfig
	// cannot accidentally enable replayable HTTP requests.
	cfg.Allow0RTT = e.config.Allow0RTT
	return cfg
}

func normalizeConfig(config Config) Config {
	if config.Addr == "" {
		config.Addr = DefaultAddr
	}
	if config.HandshakeTimeout == 0 {
		config.HandshakeTimeout = DefaultHandshakeTimeout
	}
	if config.ReadTimeout == 0 {
		config.ReadTimeout = DefaultReadTimeout
	}
	if config.WriteTimeout == 0 {
		config.WriteTimeout = DefaultWriteTimeout
	}
	if config.IdleTimeout == 0 {
		config.IdleTimeout = DefaultIdleTimeout
	}
	if config.ShutdownTimeout == 0 {
		config.ShutdownTimeout = DefaultShutdownTimeout
	}
	if config.MaxHeaderBytes == 0 {
		config.MaxHeaderBytes = DefaultMaxHeaderBytes
	}
	if config.MaxBodyBytes == 0 {
		config.MaxBodyBytes = httpx.DefaultMaxBodyBytes
	}
	if config.MaxConnections == 0 {
		config.MaxConnections = DefaultMaxConnections
	}
	return config
}

func (e *Endpoint) signalReady() {
	if e == nil || e.ready == nil {
		return
	}
	e.readyOnce.Do(func() {
		close(e.ready)
	})
}

type connectionLimiter struct {
	limit  int64
	active int64
}

func (l *connectionLimiter) tryAcquire() bool {
	if l == nil || l.limit <= 0 {
		return true
	}
	for {
		active := atomic.LoadInt64(&l.active)
		if active >= l.limit {
			return false
		}
		if atomic.CompareAndSwapInt64(&l.active, active, active+1) {
			return true
		}
	}
}

func (l *connectionLimiter) release() {
	if l == nil || l.limit <= 0 {
		return
	}
	atomic.AddInt64(&l.active, -1)
}

type limitedQUICListener struct {
	qh3.QUICListener
	limiter *connectionLimiter
}

func (l *limitedQUICListener) Accept(ctx context.Context) (*quic.Conn, error) {
	for {
		conn, err := l.QUICListener.Accept(ctx)
		if err != nil {
			return nil, err
		}
		if l.limiter.tryAcquire() {
			go func() {
				<-conn.Context().Done()
				l.limiter.release()
			}()
			return conn, nil
		}
		_ = conn.CloseWithError(0, "HTTP/3 connection limit reached")
	}
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

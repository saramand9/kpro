package quic

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	quicgo "github.com/quic-go/quic-go"
	"github.com/saramand9/bignet/kpro/codec"
	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/transport/internal/serveutil"
	"github.com/saramand9/bignet/kpro/transport/internal/streamutil"
)

const (
	DefaultAddr           = ":8444"
	DefaultALPN           = "kpro-quic"
	DefaultMaxFrameSize   = 64 * 1024
	DefaultMaxStreams     = 10000
	DefaultMaxConnections = DefaultMaxStreams
	DefaultSendQueueSize  = 64
	DefaultReadTimeout    = 90 * time.Second
	DefaultWriteTimeout   = 5 * time.Second
	DefaultHandshakeIdle  = 5 * time.Second
	DefaultIdleTimeout    = 30 * time.Second
)

type Config struct {
	Addr                 string
	Codec                codec.StreamCodec
	MaxFrameSize         uint32
	MaxStreams           int
	MaxConnections       int // Deprecated: use MaxStreams. QUIC sessions are bidirectional streams.
	SendQueueSize        int
	ReadTimeout          time.Duration
	WriteTimeout         time.Duration
	HandshakeIdleTimeout time.Duration
	IdleTimeout          time.Duration
	CertFile             string
	KeyFile              string
	TLSConfig            *tls.Config
	QUICConfig           *quicgo.Config
	NextProtos           []string
}

type Server struct {
	config      Config
	rawConfig   Config
	ln          *quicgo.Listener
	lifecycleMu sync.Mutex
	accepting   bool
	started     bool

	nextID      uint64
	active      int64
	connections sync.Map
	sessions    sync.Map
}

func New(config Config) *Server {
	rawConfig := config
	config = cloneOwnedConfig(config)
	return &Server{config: normalizeConfig(config), rawConfig: rawConfig, accepting: true}
}

func NewServer(config Config) *Server {
	return New(config)
}

func (s *Server) Name() string {
	return "quic"
}

func (s *Server) ListenAndServe(ctx context.Context, handler core.Handler) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.claimStart(); err != nil {
		return err
	}
	tlsConfig, err := buildTLSConfig(s.config)
	if err != nil {
		s.rollbackStart()
		return err
	}
	ln, err := quicgo.ListenAddr(s.config.Addr, tlsConfig, buildQUICConfig(s.config))
	if err != nil {
		s.rollbackStart()
		return err
	}
	if !s.publishListener(ln) {
		_ = ln.Close()
		return core.ErrClosed
	}

	serveDone := make(chan struct{})
	watcherDone := serveutil.ShutdownOnCancel(ctx, serveDone, func() {
		_ = s.Shutdown(context.Background())
	})
	defer serveutil.FinishContextWatcher(serveDone, watcherDone)

	for {
		conn, err := ln.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				_ = s.Shutdown(context.Background())
				return nil
			}
			if !s.isAccepting() {
				return nil
			}
			if errors.Is(err, quicgo.ErrServerClosed) {
				return nil
			}
			return err
		}
		if !s.registerConnection(conn) {
			_ = conn.CloseWithError(0, "server shutting down")
			continue
		}
		go s.acceptStreams(conn, handler)
	}
}

func (s *Server) claimStart() error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if !s.accepting {
		return core.ErrClosed
	}
	if s.started {
		return core.ErrAlreadyStarted
	}
	s.started = true
	return nil
}

func (s *Server) rollbackStart() {
	s.lifecycleMu.Lock()
	if s.accepting && s.ln == nil {
		s.started = false
	}
	s.lifecycleMu.Unlock()
}

func (s *Server) publishListener(listener *quicgo.Listener) bool {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if !s.accepting {
		return false
	}
	s.ln = listener
	return true
}

func (s *Server) Shutdown(context.Context) error {
	s.lifecycleMu.Lock()
	s.accepting = false
	ln := s.ln
	s.lifecycleMu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	s.connections.Range(func(key, _ interface{}) bool {
		if conn, ok := key.(quicgo.Connection); ok {
			_ = conn.CloseWithError(0, "server shutting down")
		}
		return true
	})
	s.sessions.Range(func(_, value interface{}) bool {
		if session, ok := value.(*session); ok {
			_ = session.Close()
		}
		return true
	})
	return nil
}

func (s *Server) acceptStreams(conn quicgo.Connection, handler core.Handler) {
	defer func() {
		_ = conn.CloseWithError(0, "stream accept stopped")
		s.connections.Delete(conn)
	}()
	for {
		stream, err := conn.AcceptStream(conn.Context())
		if err != nil {
			return
		}
		if !s.tryAcquireStreamSlot() {
			stream.CancelRead(0)
			stream.CancelWrite(0)
			continue
		}
		id := atomic.AddUint64(&s.nextID, 1)
		session := &session{
			id:        id,
			server:    s,
			conn:      conn,
			stream:    stream,
			sendQueue: make(chan []byte, s.config.SendQueueSize),
			done:      make(chan struct{}),
		}
		if !s.registerSession(session, handler) {
			stream.CancelRead(0)
			stream.CancelWrite(0)
			s.releaseStreamSlot()
			return
		}
	}
}

func (s *Server) isAccepting() bool {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.accepting
}

func (s *Server) registerConnection(conn quicgo.Connection) bool {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if !s.accepting {
		return false
	}
	s.connections.Store(conn, struct{}{})
	return true
}

func (s *Server) registerSession(session *session, handler core.Handler) bool {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if !s.accepting {
		return false
	}
	s.sessions.Store(session.id, session)
	session.start(handler)
	return true
}

func (s *Server) tryAcquireStreamSlot() bool {
	limit := int64(s.config.MaxStreams)
	if limit <= 0 {
		atomic.AddInt64(&s.active, 1)
		return true
	}
	for {
		active := atomic.LoadInt64(&s.active)
		if active >= limit {
			return false
		}
		if atomic.CompareAndSwapInt64(&s.active, active, active+1) {
			return true
		}
	}
}

type session struct {
	id        uint64
	server    *Server
	conn      quicgo.Connection
	stream    quicgo.Stream
	sendQueue chan []byte
	done      chan struct{}
	closeOnce sync.Once
}

func (s *session) ID() uint64 {
	return s.id
}

func (s *session) Transport() core.Transport {
	return core.TransportQUIC
}

func (s *session) RemoteAddr() net.Addr {
	return s.conn.RemoteAddr()
}

func (s *session) Done() <-chan struct{} {
	return s.done
}

func (s *session) Send(msg core.Message) error {
	select {
	case <-s.done:
		return core.ErrClosed
	default:
	}
	frame, err := s.server.config.Codec.Encode(msg)
	if err != nil {
		return err
	}
	select {
	case s.sendQueue <- frame:
		return nil
	case <-s.done:
		return core.ErrClosed
	default:
		_ = s.Close()
		return core.ErrClosed
	}
}

func (s *session) Close() error {
	s.closeOnce.Do(func() {
		close(s.done)
		s.stream.CancelRead(0)
		_ = s.stream.Close()
		s.server.sessions.Delete(s.id)
		s.server.releaseStreamSlot()
	})
	return nil
}

func (s *Server) releaseStreamSlot() {
	atomic.AddInt64(&s.active, -1)
}

func (s *session) start(handler core.Handler) {
	go s.readLoop(handler)
	go s.writeLoop()
}

func (s *session) readLoop(handler core.Handler) {
	defer s.Close()
	for {
		if s.server.config.ReadTimeout > 0 {
			if err := s.stream.SetReadDeadline(time.Now().Add(s.server.config.ReadTimeout)); err != nil {
				return
			}
		}
		result, err := s.server.config.Codec.Read(s.stream, s.server.config.MaxFrameSize)
		if err != nil {
			if err == io.EOF {
				return
			}
			return
		}
		msg := result.Message
		if msg.ID == core.MsgHeartbeat {
			_ = s.Send(core.Message{ID: core.MsgPong, Seq: msg.Seq})
			continue
		}
		_ = handler(core.AcquireContext(s, msg))
	}
}

func (s *session) writeLoop() {
	for {
		select {
		case data := <-s.sendQueue:
			if s.server.config.WriteTimeout > 0 {
				if err := s.stream.SetWriteDeadline(time.Now().Add(s.server.config.WriteTimeout)); err != nil {
					_ = s.Close()
					return
				}
			}
			if err := streamutil.WriteFull(s.stream, data); err != nil {
				_ = s.Close()
				return
			}
		case <-s.done:
			return
		}
	}
}

func normalizeConfig(config Config) Config {
	if config.Addr == "" {
		config.Addr = DefaultAddr
	}
	if config.Codec == nil {
		config.Codec = codec.KProV1()
	}
	if config.MaxFrameSize == 0 {
		config.MaxFrameSize = DefaultMaxFrameSize
	}
	if config.MaxStreams <= 0 {
		config.MaxStreams = config.MaxConnections
	}
	if config.MaxStreams <= 0 {
		config.MaxStreams = DefaultMaxStreams
	}
	config.MaxConnections = config.MaxStreams
	if config.SendQueueSize <= 0 {
		config.SendQueueSize = DefaultSendQueueSize
	}
	if config.ReadTimeout <= 0 {
		config.ReadTimeout = DefaultReadTimeout
	}
	if config.WriteTimeout <= 0 {
		config.WriteTimeout = DefaultWriteTimeout
	}
	if config.HandshakeIdleTimeout <= 0 {
		config.HandshakeIdleTimeout = DefaultHandshakeIdle
	}
	if config.IdleTimeout <= 0 {
		config.IdleTimeout = DefaultIdleTimeout
	}
	if len(config.NextProtos) == 0 {
		config.NextProtos = []string{DefaultALPN}
	}
	return config
}

func cloneOwnedConfig(config Config) Config {
	config.NextProtos = append([]string(nil), config.NextProtos...)
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
		quicConfig.Versions = append([]quicgo.Version(nil), config.QUICConfig.Versions...)
		config.QUICConfig = quicConfig
	}
	return config
}

func buildTLSConfig(config Config) (*tls.Config, error) {
	if config.TLSConfig != nil {
		tlsConfig := config.TLSConfig.Clone()
		if len(tlsConfig.NextProtos) == 0 {
			tlsConfig.NextProtos = append([]string(nil), config.NextProtos...)
		}
		return tlsConfig, nil
	}
	cert, err := tls.LoadX509KeyPair(config.CertFile, config.KeyFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   append([]string(nil), config.NextProtos...),
	}, nil
}

func buildQUICConfig(config Config) *quicgo.Config {
	cfg := &quicgo.Config{}
	if config.QUICConfig != nil {
		cfg = config.QUICConfig.Clone()
	}
	cfg.HandshakeIdleTimeout = config.HandshakeIdleTimeout
	cfg.MaxIdleTimeout = config.IdleTimeout
	return cfg
}

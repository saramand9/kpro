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
	config Config
	ln     *quicgo.Listener

	nextID   uint64
	active   int64
	sessions sync.Map
}

func New(config Config) *Server {
	return &Server{config: normalizeConfig(config)}
}

func NewServer(config Config) *Server {
	return New(config)
}

func (s *Server) Name() string {
	return "quic"
}

func (s *Server) ListenAndServe(ctx context.Context, handler core.Handler) error {
	tlsConfig, err := buildTLSConfig(s.config)
	if err != nil {
		return err
	}
	ln, err := quicgo.ListenAddr(s.config.Addr, tlsConfig, buildQUICConfig(s.config))
	if err != nil {
		return err
	}
	s.ln = ln

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept(ctx)
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				if errors.Is(err, quicgo.ErrServerClosed) {
					return nil
				}
				return err
			}
		}
		go s.acceptStreams(ctx, conn, handler)
	}
}

func (s *Server) Shutdown(context.Context) error {
	if s.ln != nil {
		_ = s.ln.Close()
	}
	s.sessions.Range(func(_, value interface{}) bool {
		if session, ok := value.(*session); ok {
			_ = session.Close()
		}
		return true
	})
	return nil
}

func (s *Server) acceptStreams(ctx context.Context, conn quicgo.Connection, handler core.Handler) {
	for {
		stream, err := conn.AcceptStream(ctx)
		if err != nil {
			return
		}
		if s.config.MaxStreams > 0 && atomic.LoadInt64(&s.active) >= int64(s.config.MaxStreams) {
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
		atomic.AddInt64(&s.active, 1)
		s.sessions.Store(id, session)
		session.start(handler)
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
		atomic.AddInt64(&s.server.active, -1)
	})
	return nil
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

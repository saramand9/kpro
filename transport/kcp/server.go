package kcp

import (
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/saramand9/bignet/kpro/codec"
	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/transport/internal/streamutil"
	kcpgo "github.com/xtaci/kcp-go"
)

const (
	DefaultAddr           = ":8090"
	DefaultMaxFrameSize   = 64 * 1024
	DefaultMaxConnections = 10000
	DefaultSendQueueSize  = 64
	DefaultReadTimeout    = 90 * time.Second
	DefaultWriteTimeout   = 5 * time.Second
	DefaultNoDelay        = 1
	DefaultInterval       = 20
	DefaultResend         = 2
	DefaultNoCongestion   = 1
	DefaultSendWindow     = 128
	DefaultReceiveWindow  = 512
	DefaultMTU            = 1400
)

type Config struct {
	Addr           string
	Codec          codec.StreamCodec
	MaxFrameSize   uint32
	MaxConnections int
	SendQueueSize  int
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration

	NoDelay       int
	Interval      int
	Resend        int
	NoCongestion  int
	SendWindow    int
	ReceiveWindow int
	MTU           int
	DataShards    int
	ParityShards  int
}

type Server struct {
	config Config
	ln     *kcpgo.Listener

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
	return "kcp"
}

func (s *Server) ListenAndServe(ctx context.Context, handler core.Handler) error {
	ln, err := kcpgo.ListenWithOptions(s.config.Addr, nil, s.config.DataShards, s.config.ParityShards)
	if err != nil {
		return err
	}
	s.ln = ln

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		raw, err := ln.AcceptKCP()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return err
			}
		}
		if s.config.MaxConnections > 0 && atomic.LoadInt64(&s.active) >= int64(s.config.MaxConnections) {
			_ = raw.Close()
			continue
		}
		applySessionConfig(raw, s.config)
		id := atomic.AddUint64(&s.nextID, 1)
		session := &session{
			id:        id,
			server:    s,
			raw:       raw,
			sendQueue: make(chan []byte, s.config.SendQueueSize),
			done:      make(chan struct{}),
		}
		atomic.AddInt64(&s.active, 1)
		s.sessions.Store(id, session)
		session.start(handler)
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

type session struct {
	id        uint64
	server    *Server
	raw       *kcpgo.UDPSession
	sendQueue chan []byte
	done      chan struct{}
	closeOnce sync.Once
}

func (s *session) ID() uint64 {
	return s.id
}

func (s *session) Transport() core.Transport {
	return core.TransportKCP
}

func (s *session) RemoteAddr() net.Addr {
	return s.raw.RemoteAddr()
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
		_ = s.raw.Close()
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
			if err := s.raw.SetReadDeadline(time.Now().Add(s.server.config.ReadTimeout)); err != nil {
				return
			}
		}
		result, err := s.server.config.Codec.Read(s.raw, s.server.config.MaxFrameSize)
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
				if err := s.raw.SetWriteDeadline(time.Now().Add(s.server.config.WriteTimeout)); err != nil {
					_ = s.Close()
					return
				}
			}
			if err := streamutil.WriteFull(s.raw, data); err != nil {
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
	if config.MaxConnections <= 0 {
		config.MaxConnections = DefaultMaxConnections
	}
	if config.SendQueueSize <= 0 {
		config.SendQueueSize = DefaultSendQueueSize
	}
	if config.ReadTimeout <= 0 {
		config.ReadTimeout = DefaultReadTimeout
	}
	if config.WriteTimeout <= 0 {
		config.WriteTimeout = DefaultWriteTimeout
	}
	if config.NoDelay <= 0 {
		config.NoDelay = DefaultNoDelay
	}
	if config.Interval <= 0 {
		config.Interval = DefaultInterval
	}
	if config.Resend <= 0 {
		config.Resend = DefaultResend
	}
	if config.NoCongestion <= 0 {
		config.NoCongestion = DefaultNoCongestion
	}
	if config.SendWindow <= 0 {
		config.SendWindow = DefaultSendWindow
	}
	if config.ReceiveWindow <= 0 {
		config.ReceiveWindow = DefaultReceiveWindow
	}
	if config.MTU <= 0 {
		config.MTU = DefaultMTU
	}
	return config
}

func applySessionConfig(session *kcpgo.UDPSession, config Config) {
	session.SetStreamMode(true)
	session.SetNoDelay(config.NoDelay, config.Interval, config.Resend, config.NoCongestion)
	session.SetWindowSize(config.SendWindow, config.ReceiveWindow)
	_ = session.SetMtu(config.MTU)
}

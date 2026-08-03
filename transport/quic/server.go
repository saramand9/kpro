package quic

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	quicgo "github.com/quic-go/quic-go"
	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
	transportapi "github.com/saramand9/kpro/transport"
	"github.com/saramand9/kpro/transport/internal/dispatchgate"
	"github.com/saramand9/kpro/transport/internal/outbound"
	"github.com/saramand9/kpro/transport/internal/serveutil"
	"github.com/saramand9/kpro/transport/internal/streamutil"
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
	Addr         string
	Codec        codec.StreamCodec
	MaxFrameSize uint32
	MaxStreams   int
	// MaxConnections caps active physical QUIC connections. MaxStreams caps
	// active bidirectional message streams across those connections.
	MaxConnections int
	// SendQueueSize caps per-stream writes across encoding reservations, the
	// queue, and the transport writer.
	SendQueueSize int
	// MaxPendingBytes caps bytes reserved for encoding, queued, or currently
	// being written per stream. Zero derives SendQueueSize * (MaxFrameSize + frame header).
	MaxPendingBytes      int64
	ReadTimeout          time.Duration
	WriteTimeout         time.Duration
	HandshakeIdleTimeout time.Duration
	IdleTimeout          time.Duration
	CertFile             string
	KeyFile              string
	TLSConfig            *tls.Config
	QUICConfig           *quicgo.Config
	NextProtos           []string
	// HandlerErrorPolicy defaults to closing the affected stream session.
	HandlerErrorPolicy transportapi.HandlerErrorPolicy
	// OnHandlerError observes handler failures before the policy is applied.
	OnHandlerError transportapi.HandlerErrorObserver
}

type Server struct {
	config      Config
	rawConfig   Config
	ln          *quicgo.Listener
	lifecycleMu sync.Mutex
	accepting   bool
	started     bool
	dispatch    *dispatchgate.Gate
	acceptCtx   context.Context
	acceptStop  context.CancelFunc

	nextID      uint64
	active      int64
	activeConns int64
	connections sync.Map
	sessions    sync.Map
	ready       chan struct{}
	readyOnce   sync.Once
}

func New(config Config) *Server {
	rawConfig := config
	config = cloneOwnedConfig(config)
	acceptCtx, acceptStop := context.WithCancel(context.Background())
	return &Server{
		config:     normalizeConfig(config),
		rawConfig:  rawConfig,
		accepting:  true,
		dispatch:   dispatchgate.New(),
		acceptCtx:  acceptCtx,
		acceptStop: acceptStop,
		ready:      make(chan struct{}),
	}
}

func NewServer(config Config) *Server {
	return New(config)
}

func (s *Server) Name() string {
	return "quic"
}

// Ready is closed once the QUIC listener is successfully bound and published.
func (s *Server) Ready() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.ready
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
	if handler == nil {
		s.rollbackStart()
		return fmt.Errorf("quic: %w", core.ErrNoHandler)
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
	s.signalReady()

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
			reason := "QUIC connection limit reached"
			if !s.isAccepting() {
				reason = "server shutting down"
			}
			_ = conn.CloseWithError(0, reason)
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

// Quiesce stops connection, stream, and message admission while preserving
// established streams and their outbound writers until Shutdown.
func (s *Server) Quiesce(context.Context) error {
	return s.stopAdmissionAndReads()
}

func (s *Server) Shutdown(context.Context) error {
	stopErr := s.stopAdmissionAndReads()
	s.connections.Range(func(key, _ interface{}) bool {
		if conn, ok := key.(*quicgo.Conn); ok {
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
	return stopErr
}

func (s *Server) stopAdmissionAndReads() error {
	s.lifecycleMu.Lock()
	s.accepting = false
	s.dispatch.Stop()
	ln := s.ln
	s.lifecycleMu.Unlock()
	s.acceptStop()
	var stopErr error
	if ln != nil {
		if err := ln.Close(); err != nil && !errors.Is(err, quicgo.ErrServerClosed) {
			stopErr = errors.Join(stopErr, err)
		}
	}
	now := time.Now()
	s.sessions.Range(func(_, value interface{}) bool {
		if session, ok := value.(*session); ok {
			if err := session.stream.SetReadDeadline(now); err != nil {
				stopErr = errors.Join(stopErr, err)
			}
		}
		return true
	})
	return stopErr
}

func (s *Server) acceptStreams(conn *quicgo.Conn, handler core.Handler) {
	defer func() {
		_ = conn.CloseWithError(0, "stream accept stopped")
		s.connections.Delete(conn)
		s.releaseConnectionSlot()
	}()
	for {
		stream, err := conn.AcceptStream(s.acceptCtx)
		if err != nil {
			if s.dispatch.Stopped() {
				s.rejectStreamsUntilClosed(conn)
			}
			return
		}
		if !s.isAccepting() {
			stream.CancelRead(0)
			stream.CancelWrite(0)
			s.rejectStreamsUntilClosed(conn)
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
			sendQueue: newSessionOutboundQueue(s.config),
			done:      make(chan struct{}),
		}
		if !s.registerSession(session, handler) {
			stream.CancelRead(0)
			stream.CancelWrite(0)
			s.releaseStreamSlot()
			if s.dispatch.Stopped() {
				s.rejectStreamsUntilClosed(conn)
			}
			return
		}
	}
}

func (s *Server) rejectStreamsUntilClosed(conn *quicgo.Conn) {
	for {
		stream, err := conn.AcceptStream(conn.Context())
		if err != nil {
			return
		}
		stream.CancelRead(0)
		stream.CancelWrite(0)
	}
}

func (s *Server) isAccepting() bool {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.accepting
}

func (s *Server) registerConnection(conn *quicgo.Conn) bool {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if !s.accepting {
		return false
	}
	if !s.tryAcquireConnectionSlot() {
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

func (s *Server) tryAcquireConnectionSlot() bool {
	limit := int64(s.config.MaxConnections)
	if limit <= 0 {
		atomic.AddInt64(&s.activeConns, 1)
		return true
	}
	for {
		active := atomic.LoadInt64(&s.activeConns)
		if active >= limit {
			return false
		}
		if atomic.CompareAndSwapInt64(&s.activeConns, active, active+1) {
			return true
		}
	}
}

func (s *Server) releaseConnectionSlot() {
	atomic.AddInt64(&s.activeConns, -1)
}

type session struct {
	id        uint64
	server    *Server
	conn      *quicgo.Conn
	stream    *quicgo.Stream
	sendQueue *outbound.Queue[[]byte]
	done      chan struct{}
	closeOnce sync.Once
}

var _ core.OutboundStateProvider = (*session)(nil)
var _ core.OutboundWritableWaiter = (*session)(nil)
var _ transportapi.Quiescer = (*Server)(nil)

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
	reservation, err := s.sendQueue.Reserve(outbound.EstimateBytes(len(msg.Data), codec.FrameHeaderSize))
	if err != nil {
		return s.outboundError(err)
	}
	defer reservation.Cancel()
	frame, err := s.server.config.Codec.Encode(msg)
	if err != nil {
		return err
	}
	if err := reservation.Commit(frame, int64(len(frame))); err != nil {
		return s.outboundError(err)
	}
	return nil
}

func (s *session) Close() error {
	s.closeOnce.Do(func() {
		if s.sendQueue != nil {
			s.sendQueue.Close()
		}
		close(s.done)
		if s.stream != nil {
			s.stream.CancelRead(0)
			_ = s.stream.Close()
		}
		s.server.sessions.Delete(s.id)
		s.server.releaseStreamSlot()
	})
	return nil
}

func (s *session) OutboundState() core.OutboundState {
	if s == nil || s.sendQueue == nil {
		return core.OutboundState{Closed: true}
	}
	snapshot := s.sendQueue.Snapshot()
	return core.OutboundState{
		Closed:             snapshot.Closed,
		Writable:           snapshot.Writable,
		AboveHighWatermark: snapshot.AboveHighWatermark,
		PendingWrites:      snapshot.PendingFrames,
		MaxPendingWrites:   snapshot.MaxFrames,
		PendingBytes:       snapshot.PendingBytes,
		MaxPendingBytes:    snapshot.MaxBytes,
		WaitingWriters:     snapshot.WaitingWriters,
		MaxWaitingWriters:  snapshot.MaxWaitingWriters,
		LowWatermarkBytes:  snapshot.LowWatermarkBytes,
		HighWatermarkBytes: snapshot.HighWatermarkBytes,
	}
}

func (s *session) WaitOutboundWritable(ctx context.Context) error {
	if s == nil || s.sendQueue == nil {
		return core.ErrClosed
	}
	err := s.sendQueue.WaitWritable(ctx)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, outbound.ErrClosed):
		return core.ErrClosed
	case errors.Is(err, outbound.ErrTooManyWaiters):
		return core.ErrTooManyOutboundWaiters
	default:
		return err
	}
}

func (s *Server) releaseStreamSlot() {
	atomic.AddInt64(&s.active, -1)
}

func (s *session) start(handler core.Handler) {
	go s.readLoop(handler)
	go s.writeLoop()
}

func (s *session) readLoop(handler core.Handler) {
	preserveSession := false
	defer func() {
		if preserveSession {
			return
		}
		// Linearize the close-policy decision with Quiesce, but keep network
		// teardown outside the lifecycle lock.
		s.server.lifecycleMu.Lock()
		closeSession := s.server.accepting
		s.server.lifecycleMu.Unlock()
		if closeSession {
			_ = s.Close()
		}
	}()
	for {
		if s.server.dispatch.Stopped() {
			preserveSession = true
			return
		}
		if s.server.config.ReadTimeout > 0 {
			if err := s.stream.SetReadDeadline(time.Now().Add(s.server.config.ReadTimeout)); err != nil {
				preserveSession = s.server.dispatch.Stopped()
				return
			}
		}
		if s.server.dispatch.Stopped() {
			preserveSession = true
			return
		}
		result, err := s.server.config.Codec.Read(s.stream, s.server.config.MaxFrameSize)
		if err != nil {
			preserveSession = s.server.dispatch.Stopped()
			if err == io.EOF {
				return
			}
			return
		}
		msg := result.Message
		if !s.server.dispatch.Begin() {
			preserveSession = true
			return
		}
		keepReading := func() bool {
			defer s.server.dispatch.End()
			if msg.ID == core.MsgHeartbeat {
				_ = s.Send(core.Message{ID: core.MsgPong, Seq: msg.Seq})
				return true
			}
			err = transportapi.InvokeHandler(handler, s, msg)
			return transportapi.ContinueAfterHandlerError(
				s.server.config.HandlerErrorPolicy,
				s.server.config.OnHandlerError,
				s,
				msg,
				err,
			)
		}()
		if s.server.dispatch.Stopped() {
			preserveSession = true
			return
		}
		if !keepReading {
			return
		}
	}
}

func (s *session) writeLoop() {
	for {
		select {
		case item := <-s.sendQueue.Items():
			data := item.Value
			if s.server.config.WriteTimeout > 0 {
				if err := s.stream.SetWriteDeadline(time.Now().Add(s.server.config.WriteTimeout)); err != nil {
					item.Done()
					_ = s.Close()
					return
				}
			}
			if err := streamutil.WriteFull(s.stream, data); err != nil {
				item.Done()
				_ = s.Close()
				return
			}
			item.Done()
		case <-s.done:
			return
		}
	}
}

func newSessionOutboundQueue(config Config) *outbound.Queue[[]byte] {
	return outbound.New[[]byte](outbound.Config{
		MaxFrames: config.SendQueueSize,
		MaxBytes:  config.MaxPendingBytes,
	})
}

func (s *session) outboundError(err error) error {
	switch err {
	case outbound.ErrClosed:
		return core.ErrClosed
	case outbound.ErrFull:
		_ = s.Close()
		return core.ErrSendQueueFull
	default:
		return err
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
		config.MaxStreams = DefaultMaxStreams
	}
	if config.MaxConnections <= 0 {
		config.MaxConnections = DefaultMaxConnections
	}
	if config.SendQueueSize <= 0 {
		config.SendQueueSize = DefaultSendQueueSize
	}
	if config.MaxPendingBytes <= 0 {
		maxFrameBytes := int64(config.MaxFrameSize) + int64(codec.FrameHeaderSize)
		config.MaxPendingBytes = outbound.ByteLimit(config.SendQueueSize, maxFrameBytes)
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

func (s *Server) signalReady() {
	if s == nil || s.ready == nil {
		return
	}
	s.readyOnce.Do(func() {
		close(s.ready)
	})
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

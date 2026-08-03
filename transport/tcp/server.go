// File: transport/tcp/server.go
// Purpose: Implements the TCP transport adapter for KPro message sessions.

package tcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
	transportapi "github.com/saramand9/kpro/transport"
	"github.com/saramand9/kpro/transport/internal/dispatchgate"
	"github.com/saramand9/kpro/transport/internal/outbound"
	"github.com/saramand9/kpro/transport/internal/serveutil"
	"github.com/saramand9/kpro/transport/internal/streamutil"
)

const (
	DefaultAddr           = ":8088"
	DefaultMaxFrameSize   = 64 * 1024
	DefaultMaxConnections = 10000
	DefaultSendQueueSize  = 64
	DefaultReadTimeout    = 90 * time.Second
	DefaultWriteTimeout   = 5 * time.Second
)

// Config controls TCP listener, codec, queue, and timeout settings.
type Config struct {
	// Addr is the TCP listen address.
	Addr string
	// Codec decodes and encodes stream frames. Nil uses codec.KProV1().
	Codec codec.StreamCodec
	// MaxFrameSize limits decoded frame payload size.
	MaxFrameSize uint32
	// MaxConnections caps active TCP sessions.
	MaxConnections int
	// SendQueueSize caps per-session writes across encoding reservations, the
	// queue, and the transport writer.
	SendQueueSize int
	// MaxPendingBytes caps bytes reserved for encoding, queued, or currently
	// being written per session. Zero derives SendQueueSize * (MaxFrameSize + frame header).
	MaxPendingBytes int64
	// ReadTimeout is applied before each frame read.
	ReadTimeout time.Duration
	// WriteTimeout is applied before each frame write.
	WriteTimeout time.Duration
	// HandlerErrorPolicy defaults to closing the affected session.
	HandlerErrorPolicy transportapi.HandlerErrorPolicy
	// OnHandlerError observes handler failures before the policy is applied.
	OnHandlerError transportapi.HandlerErrorObserver
}

// Server accepts TCP connections and turns frames into core.Message contexts.
type Server struct {
	config      Config
	rawConfig   Config
	ln          net.Listener
	lifecycleMu sync.Mutex
	accepting   bool
	started     bool
	dispatch    *dispatchgate.Gate

	nextID    uint64
	active    int64
	sessions  sync.Map
	ready     chan struct{}
	readyOnce sync.Once
}

// New creates a TCP transport with normalized defaults.
func New(config Config) *Server {
	return &Server{
		config:    normalizeConfig(config),
		rawConfig: config,
		accepting: true,
		dispatch:  dispatchgate.New(),
		ready:     make(chan struct{}),
	}
}

// NewServer is an alias for New.
func NewServer(config Config) *Server {
	return New(config)
}

// Name returns the transport name.
func (s *Server) Name() string {
	return "tcp"
}

// Ready is closed once the TCP listener is successfully bound and published.
func (s *Server) Ready() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.ready
}

// ListenAndServe accepts TCP connections until ctx is cancelled or the listener fails.
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
		return fmt.Errorf("tcp: %w", core.ErrNoHandler)
	}
	ln, err := net.Listen("tcp", s.config.Addr)
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
		raw, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || !s.isAccepting() {
				return nil
			}
			return err
		}
		if !s.tryAcquireConnectionSlot() {
			_ = raw.Close()
			continue
		}
		id := atomic.AddUint64(&s.nextID, 1)
		session := &session{
			id:        id,
			server:    s,
			raw:       raw,
			sendQueue: newSessionOutboundQueue(s.config),
			done:      make(chan struct{}),
		}
		if !s.registerSession(session, handler) {
			_ = raw.Close()
			s.releaseConnectionSlot()
		}
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

func (s *Server) publishListener(listener net.Listener) bool {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if !s.accepting {
		return false
	}
	s.ln = listener
	return true
}

// Quiesce stops connection admission and message reads while preserving
// established sessions and their outbound writers until Shutdown.
func (s *Server) Quiesce(context.Context) error {
	return s.stopAdmissionAndReads()
}

// Shutdown closes the listener and all active TCP sessions.
func (s *Server) Shutdown(context.Context) error {
	stopErr := s.stopAdmissionAndReads()
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
	var stopErr error
	if ln != nil {
		if err := ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			stopErr = errors.Join(stopErr, err)
		}
	}
	now := time.Now()
	s.sessions.Range(func(_, value interface{}) bool {
		if session, ok := value.(*session); ok {
			if err := session.raw.SetReadDeadline(now); err != nil && !errors.Is(err, net.ErrClosed) {
				stopErr = errors.Join(stopErr, err)
			}
		}
		return true
	})
	return stopErr
}

func (s *Server) isAccepting() bool {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.accepting
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

func (s *Server) tryAcquireConnectionSlot() bool {
	limit := int64(s.config.MaxConnections)
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

func (s *Server) releaseConnectionSlot() {
	atomic.AddInt64(&s.active, -1)
}

func (s *Server) signalReady() {
	if s == nil || s.ready == nil {
		return
	}
	s.readyOnce.Do(func() { close(s.ready) })
}

type session struct {
	id        uint64
	server    *Server
	raw       net.Conn
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
	return core.TransportTCP
}

func (s *session) RemoteAddr() net.Addr {
	return s.raw.RemoteAddr()
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
		if s.raw != nil {
			_ = s.raw.Close()
		}
		s.server.sessions.Delete(s.id)
		s.server.releaseConnectionSlot()
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
			if err := s.raw.SetReadDeadline(time.Now().Add(s.server.config.ReadTimeout)); err != nil {
				preserveSession = s.server.dispatch.Stopped()
				return
			}
		}
		if s.server.dispatch.Stopped() {
			preserveSession = true
			return
		}
		result, err := s.server.config.Codec.Read(s.raw, s.server.config.MaxFrameSize)
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
				if err := s.raw.SetWriteDeadline(time.Now().Add(s.server.config.WriteTimeout)); err != nil {
					item.Done()
					_ = s.Close()
					return
				}
			}
			if err := streamutil.WriteFull(s.raw, data); err != nil {
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
	return config
}

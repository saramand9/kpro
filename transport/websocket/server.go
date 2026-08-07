// File: transport/websocket/server.go
// Purpose: Implements the WebSocket transport adapter for KPro message sessions.

package websocket

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
	transportapi "github.com/saramand9/kpro/transport"
	"github.com/saramand9/kpro/transport/internal/dispatchgate"
	"github.com/saramand9/kpro/transport/internal/outbound"
	"github.com/saramand9/kpro/transport/internal/serveutil"
	"github.com/saramand9/kpro/transport/internal/streamutil"
	"github.com/saramand9/kpro/transport/proxyproto"
)

const (
	DefaultAddr                = ":8091"
	DefaultPath                = "/ws"
	DefaultMaxMessage          = 64 * 1024
	DefaultMaxConnections      = 10000
	DefaultSendQueueSize       = 64
	DefaultMaxWriteBatchFrames = 16
	DefaultMaxWriteBatchBytes  = 256 * 1024
	DefaultReadTimeout         = 90 * time.Second
	DefaultWriteTimeout        = 5 * time.Second
	DefaultHandshakeTimeout    = 5 * time.Second

	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xa
)

var (
	ErrBadHandshake           = errors.New("bad websocket handshake")
	ErrBadFrame               = errors.New("bad websocket frame")
	ErrFrameTooLarge          = errors.New("websocket frame too large")
	ErrFragmentedControlFrame = errors.New("websocket control frame must not be fragmented")
	ErrTooManyConnections     = errors.New("websocket connection limit reached")
	ErrNotAccepting           = errors.New("websocket server is not accepting upgrades")
	ErrOriginNotAllowed       = errors.New("websocket origin not allowed")
)

// Config controls WebSocket listener, path, codec, queue, and timeout settings.
type Config struct {
	// Addr is the HTTP listen address used for WebSocket upgrades.
	Addr string
	// ProxyProtocol optionally requires HAProxy PROXY protocol v1/v2 from
	// explicitly trusted L4 proxies. The zero value disables it.
	ProxyProtocol proxyproto.Config
	// Path is the WebSocket upgrade route path.
	Path string
	// Codec decodes and encodes binary WebSocket payloads. Nil uses codec.KProV1().
	Codec codec.PacketCodec
	// MaxMessage limits reassembled WebSocket message size.
	MaxMessage int
	// MaxConnections caps active upgraded WebSocket sessions.
	MaxConnections int
	// SendQueueSize caps per-session writes across encoding reservations, the
	// queue, and the transport writer.
	SendQueueSize int
	// MaxPendingBytes caps frame payload bytes reserved for encoding, queued, or
	// currently being written per session. Zero derives SendQueueSize * MaxMessage.
	MaxPendingBytes int64
	// MaxWriteBatchFrames caps frames gathered by one non-blocking queue drain.
	MaxWriteBatchFrames int
	// MaxWriteBatchBytes caps bytes submitted by one gather-write call. A
	// single oversized admitted frame is still written to ensure progress.
	MaxWriteBatchBytes int64
	// ReadTimeout is applied before each WebSocket frame read.
	ReadTimeout time.Duration
	// WriteTimeout is applied before each WebSocket gather-write batch.
	WriteTimeout time.Duration
	// HandshakeTimeout bounds reading the HTTP upgrade headers.
	HandshakeTimeout time.Duration
	// CheckOrigin validates the HTTP Origin header before an upgrade. Nil uses
	// a same-host policy when Origin is present and permits non-browser clients
	// that omit Origin. A non-nil callback is the complete policy and should
	// trust forwarded host/origin data only when a proxy has sanitized it.
	CheckOrigin func(*http.Request) bool
	// HandlerErrorPolicy defaults to closing the affected session.
	HandlerErrorPolicy transportapi.HandlerErrorPolicy
	// OnHandlerError observes handler failures before the policy is applied.
	OnHandlerError transportapi.HandlerErrorObserver
}

// Server upgrades HTTP requests to WebSocket sessions and dispatches KPro messages.
type Server struct {
	config    Config
	rawConfig Config
	server    *http.Server
	listener  net.Listener
	// shutdownTimeout bounds cancellation-triggered graceful shutdown.
	shutdownTimeout time.Duration

	lifecycleMu   sync.Mutex
	accepting     bool
	started       bool
	dispatch      *dispatchgate.Gate
	outboundStats outbound.Tracker
	upgrades      sync.WaitGroup
	upgrading     sync.Map

	nextID    uint64
	active    int64
	sessions  sync.Map
	ready     chan struct{}
	readyOnce sync.Once
}

// New creates a WebSocket transport with normalized defaults.
func New(config Config) *Server {
	return &Server{
		config:          normalizeConfig(config),
		rawConfig:       config,
		accepting:       true,
		dispatch:        dispatchgate.New(),
		ready:           make(chan struct{}),
		shutdownTimeout: 5 * time.Second,
	}
}

// NewServer is an alias for New.
func NewServer(config Config) *Server {
	return New(config)
}

// Name returns the transport name.
func (s *Server) Name() string {
	return "websocket"
}

// Ready is closed once the HTTP upgrade listener is successfully bound and published.
func (s *Server) Ready() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.ready
}

// ListenAndServe starts the WebSocket HTTP server until ctx is cancelled or serving fails.
func (s *Server) ListenAndServe(ctx context.Context, handler core.Handler) (serveErr error) {
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
		return fmt.Errorf("websocket: %w", core.ErrNoHandler)
	}
	server := s.buildHTTPServer(handler)
	rawListener, err := net.Listen("tcp", s.config.Addr)
	if err != nil {
		s.rollbackStart()
		return err
	}
	listener, err := proxyproto.WrapListener(rawListener, s.config.ProxyProtocol)
	if err != nil {
		_ = rawListener.Close()
		s.rollbackStart()
		return fmt.Errorf("websocket proxy protocol: %w", err)
	}
	if !s.publishServer(server, listener) {
		_ = listener.Close()
		return ErrNotAccepting
	}
	s.signalReady()
	serveDone := make(chan struct{})
	var shutdownErr error
	watcherDone := serveutil.ShutdownOnCancel(ctx, serveDone, func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
		defer cancel()
		shutdownErr = s.Shutdown(shutdownCtx)
	})
	defer func() {
		serveutil.FinishContextWatcher(serveDone, watcherDone)
		serveErr = errors.Join(serveErr, shutdownErr)
	}()
	serveErr = server.Serve(listener)
	if errors.Is(serveErr, http.ErrServerClosed) || errors.Is(serveErr, net.ErrClosed) {
		return nil
	}
	return serveErr
}

func (s *Server) claimStart() error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if !s.accepting {
		return ErrNotAccepting
	}
	if s.started {
		return core.ErrAlreadyStarted
	}
	s.started = true
	return nil
}

func (s *Server) rollbackStart() {
	s.lifecycleMu.Lock()
	if s.accepting && s.listener == nil {
		s.started = false
	}
	s.lifecycleMu.Unlock()
}

func (s *Server) publishServer(server *http.Server, listener net.Listener) bool {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if !s.accepting {
		return false
	}
	s.server = server
	s.listener = listener
	return true
}

func (s *Server) buildHTTPServer(handler core.Handler) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc(s.config.Path, func(w http.ResponseWriter, r *http.Request) {
		s.serveWS(w, r, handler)
	})
	return &http.Server{
		Addr:              s.config.Addr,
		Handler:           mux,
		ReadHeaderTimeout: s.config.HandshakeTimeout,
		ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
			if info, ok := proxyproto.InfoFrom(conn); ok {
				return proxyproto.WithInfo(ctx, info)
			}
			return ctx
		},
	}
}

// Quiesce stops HTTP upgrades and WebSocket message reads while preserving
// established sessions and their outbound writers until Shutdown.
func (s *Server) Quiesce(ctx context.Context) error {
	stopErr := s.stopAdmissionAndReads()
	upgradeErr := waitGroupContext(ctx, &s.upgrades)
	return errors.Join(stopErr, upgradeErr)
}

// Shutdown closes active WebSocket sessions and stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	stopErr := s.stopAdmissionAndReads()
	upgradeErr := waitGroupContext(ctx, &s.upgrades)
	drainErr := s.DrainOutbound(ctx)
	s.sessions.Range(func(_, value interface{}) bool {
		if session, ok := value.(*session); ok {
			_ = session.Close()
		}
		return true
	})
	return errors.Join(stopErr, upgradeErr, drainErr)
}

func (s *Server) DrainOutbound(ctx context.Context) (drainErr error) {
	started := time.Now()
	defer func() {
		s.outboundStats.RecordDrain(time.Since(started), drainErr)
	}()
	if ctx == nil {
		ctx = context.Background()
	}
	sessions := make([]*session, 0)
	s.sessions.Range(func(_, value interface{}) bool {
		if session, ok := value.(*session); ok {
			sessions = append(sessions, session)
		}
		return true
	})
	for _, session := range sessions {
		session.sendQueue.Seal()
	}
	for _, session := range sessions {
		drainErr = errors.Join(drainErr, session.sendQueue.WaitDrained(ctx))
	}
	return drainErr
}

// OutboundSnapshot reports bounded aggregate write and drain statistics.
func (s *Server) OutboundSnapshot() transportapi.OutboundSnapshot {
	if s == nil {
		return transportapi.OutboundSnapshot{}
	}
	return s.outboundStats.Snapshot()
}

func (s *Server) stopAdmissionAndReads() error {
	s.lifecycleMu.Lock()
	s.accepting = false
	s.dispatch.Stop()
	server := s.server
	listener := s.listener
	s.lifecycleMu.Unlock()

	var stopErr error
	if server != nil {
		if err := server.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			stopErr = errors.Join(stopErr, err)
		}
	}
	if listener != nil {
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			stopErr = errors.Join(stopErr, err)
		}
	}
	s.upgrading.Range(func(key, _ interface{}) bool {
		if conn, ok := key.(net.Conn); ok {
			_ = conn.Close()
		}
		return true
	})
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

func (s *Server) signalReady() {
	if s == nil || s.ready == nil {
		return
	}
	s.readyOnce.Do(func() { close(s.ready) })
}

func waitGroupContext(ctx context.Context, group *sync.WaitGroup) error {
	if ctx == nil {
		ctx = context.Background()
	}
	done := make(chan struct{})
	go func() {
		group.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) serveWS(w http.ResponseWriter, r *http.Request, handler core.Handler) {
	if !isUpgradeRequest(r) {
		http.Error(w, ErrBadHandshake.Error(), http.StatusBadRequest)
		return
	}
	key, keyOK := singleHeaderValue(r.Header, "Sec-WebSocket-Key")
	version, versionOK := singleHeaderValue(r.Header, "Sec-WebSocket-Version")
	if !keyOK || !versionOK || !validWebSocketKey(key) || version != "13" {
		http.Error(w, ErrBadHandshake.Error(), http.StatusBadRequest)
		return
	}
	if !s.originAllowed(r) {
		http.Error(w, ErrOriginNotAllowed.Error(), http.StatusForbidden)
		return
	}
	if !s.beginUpgrade() {
		http.Error(w, ErrNotAccepting.Error(), http.StatusServiceUnavailable)
		return
	}
	defer s.upgrades.Done()
	if !s.tryAcquireConnectionSlot() {
		http.Error(w, ErrTooManyConnections.Error(), http.StatusServiceUnavailable)
		return
	}
	sessionOwnsSlot := false
	defer func() {
		if !sessionOwnsSlot {
			s.releaseConnectionSlot()
		}
	}()
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
		return
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return
	}
	s.upgrading.Store(conn, struct{}{})
	defer s.upgrading.Delete(conn)
	// Hijacked connections are no longer owned by net/http. Recheck admission
	// after publishing the connection so Quiesce either observes it in
	// upgrading or this path closes it after the quiesce boundary.
	s.lifecycleMu.Lock()
	accepting := s.accepting
	s.lifecycleMu.Unlock()
	if !accepting {
		_ = conn.Close()
		return
	}
	if s.config.HandshakeTimeout > 0 {
		if err := conn.SetWriteDeadline(time.Now().Add(s.config.HandshakeTimeout)); err != nil {
			_ = conn.Close()
			return
		}
	}
	accept := computeAcceptKey(strings.TrimSpace(key))
	_, err = fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", accept)
	if err != nil {
		_ = conn.Close()
		return
	}
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return
	}

	id := atomic.AddUint64(&s.nextID, 1)
	session := &session{
		id:        id,
		server:    s,
		raw:       conn,
		reader:    rw.Reader,
		sendQueue: newSessionOutboundQueue(s.config, &s.outboundStats),
		done:      make(chan struct{}),
	}
	if !s.registerSession(session, handler) {
		_ = conn.Close()
		return
	}
	sessionOwnsSlot = true
}

func (s *Server) beginUpgrade() bool {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if !s.accepting {
		return false
	}
	s.upgrades.Add(1)
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

type wsFrame struct {
	fin     bool
	opcode  byte
	payload []byte
}

type session struct {
	id        uint64
	server    *Server
	raw       net.Conn
	reader    *bufio.Reader
	sendQueue *outbound.Queue[wsFrame]
	done      chan struct{}
	closeOnce sync.Once
	writeMu   sync.Mutex
	// closeRequested is published before a direct close writer waits for
	// writeMu. The batch writer checks it between byte-bounded submissions so
	// a close cannot be delayed by every remaining sub-batch.
	closeRequested      atomic.Bool
	directCloseComplete atomic.Bool
	// closing is protected by writeMu and prevents a queued data batch from
	// being written after a close control frame.
	closing bool
}

var _ core.OutboundStateProvider = (*session)(nil)
var _ core.OutboundWritableWaiter = (*session)(nil)
var _ core.WriteReceiptSender = (*session)(nil)
var _ core.SessionOutboundDrainer = (*session)(nil)
var _ transportapi.Quiescer = (*Server)(nil)
var _ transportapi.OutboundDrainer = (*Server)(nil)
var _ transportapi.OutboundSnapshotter = (*Server)(nil)

func (s *session) ID() uint64 {
	return s.id
}

func (s *session) Transport() core.Transport {
	return core.TransportWebSocket
}

func (s *session) RemoteAddr() net.Addr {
	return s.raw.RemoteAddr()
}

func (s *session) Done() <-chan struct{} {
	return s.done
}

func (s *session) Send(msg core.Message) error {
	return s.send(msg, nil)
}

func (s *session) SendWithReceipt(msg core.Message) (*core.WriteReceipt, error) {
	receipt := core.NewWriteReceipt()
	if err := s.send(msg, receipt); err != nil {
		return nil, err
	}
	return receipt, nil
}

func (s *session) send(msg core.Message, completion outbound.Completion) error {
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
	packet, err := s.server.config.Codec.EncodePacket(msg)
	if err != nil {
		return err
	}
	if err := reservation.CommitWithCompletion(
		wsFrame{opcode: opBinary, payload: packet},
		int64(len(packet)),
		completion,
	); err != nil {
		return s.outboundError(err)
	}
	return nil
}

func (s *session) Close() error {
	s.closeOnce.Do(func() {
		if s.sendQueue != nil {
			s.sendQueue.CloseWithError(core.ErrClosed)
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

func (s *session) DrainOutbound(ctx context.Context) error {
	if s == nil || s.sendQueue == nil {
		return core.ErrClosed
	}
	s.sendQueue.Seal()
	return s.sendQueue.WaitDrained(ctx)
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

func (s *session) enqueue(frame wsFrame) error {
	select {
	case <-s.done:
		return core.ErrClosed
	default:
	}
	reservation, err := s.sendQueue.Reserve(int64(len(frame.payload)))
	if err != nil {
		return s.outboundError(err)
	}
	if err := reservation.Commit(frame, int64(len(frame.payload))); err != nil {
		return s.outboundError(err)
	}
	return nil
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
	var fragmentOpcode byte
	var fragmentPayload []byte

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
		frame, err := readFrame(s.reader, s.server.config.MaxMessage)
		if err != nil {
			preserveSession = s.server.dispatch.Stopped()
			return
		}
		if !s.server.dispatch.Begin() {
			preserveSession = true
			return
		}
		keepReading := func() bool {
			defer s.server.dispatch.End()
			return s.processFrame(handler, frame, &fragmentOpcode, &fragmentPayload)
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

func (s *session) processFrame(
	handler core.Handler,
	frame wsFrame,
	fragmentOpcode *byte,
	fragmentPayload *[]byte,
) bool {
	switch frame.opcode {
	case opClose:
		if err := validateClosePayload(frame.payload); err != nil {
			_ = s.writeWebSocketFrame(opClose, closePayload(1002, "protocol error"))
			return false
		}
		_ = s.writeWebSocketFrame(opClose, frame.payload)
		return false
	case opPing:
		_ = s.enqueue(wsFrame{opcode: opPong, payload: frame.payload})
		return true
	case opPong:
		return true
	case opText, opBinary:
		if *fragmentOpcode != 0 {
			return false
		}
		if !frame.fin {
			*fragmentOpcode = frame.opcode
			*fragmentPayload = append((*fragmentPayload)[:0], frame.payload...)
			return true
		}
		if frame.opcode == opText && !utf8.Valid(frame.payload) {
			_ = s.writeWebSocketFrame(opClose, closePayload(1007, "invalid text"))
			return false
		}
		if err := s.handleMessage(handler, frame.payload); err != nil {
			return false
		}
		return true
	case opContinuation:
		if *fragmentOpcode == 0 {
			return false
		}
		payload, err := appendFragment(*fragmentPayload, frame.payload, s.server.config.MaxMessage)
		if err != nil {
			return false
		}
		*fragmentPayload = payload
		if !frame.fin {
			return true
		}
		completeOpcode := *fragmentOpcode
		completePayload := *fragmentPayload
		*fragmentOpcode = 0
		*fragmentPayload = nil
		if completeOpcode == opText && !utf8.Valid(completePayload) {
			_ = s.writeWebSocketFrame(opClose, closePayload(1007, "invalid text"))
			return false
		}
		if err := s.handleMessage(handler, completePayload); err != nil {
			return false
		}
		return true
	default:
		return false
	}
}

func (s *session) handleMessage(handler core.Handler, payload []byte) error {
	msg, err := s.server.config.Codec.DecodePacket(payload)
	if err != nil {
		return err
	}
	if msg.ID == core.MsgHeartbeat {
		_ = s.Send(core.Message{ID: core.MsgPong, Seq: msg.Seq})
		return nil
	}
	err = transportapi.InvokeHandler(handler, s, msg)
	if transportapi.ContinueAfterHandlerError(
		s.server.config.HandlerErrorPolicy,
		s.server.config.OnHandlerError,
		s,
		msg,
		err,
	) {
		return nil
	}
	return err
}

func (s *session) writeLoop() {
	writer := newSessionBatchWriter(s.server.config)
	for {
		select {
		case item := <-s.sendQueue.Items():
			closeAfterWrite, err := writer.writeAvailable(s, &item)
			if err != nil {
				_ = s.Close()
				return
			}
			if closeAfterWrite {
				_ = s.Close()
				return
			}
		case <-s.done:
			return
		}
	}
}

func newSessionOutboundQueue(config Config, trackers ...*outbound.Tracker) *outbound.Queue[wsFrame] {
	var tracker *outbound.Tracker
	if len(trackers) > 0 {
		tracker = trackers[0]
	}
	return outbound.NewTracked[wsFrame](outbound.Config{
		MaxFrames: config.SendQueueSize,
		MaxBytes:  config.MaxPendingBytes,
	}, tracker)
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

func (s *session) writeWebSocketFrame(opcode byte, payload []byte) error {
	directClose := opcode == opClose
	if directClose {
		s.closeRequested.Store(true)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if directClose {
		defer s.directCloseComplete.Store(true)
	}
	if s.closing {
		return nil
	}
	if directClose {
		s.closing = true
	}
	if s.server.config.WriteTimeout > 0 {
		if err := s.raw.SetWriteDeadline(time.Now().Add(s.server.config.WriteTimeout)); err != nil {
			return err
		}
	}
	return writeFrame(s.raw, opcode, payload)
}

func readFrame(r io.Reader, maxSize int) (wsFrame, error) {
	var header [2]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return wsFrame{}, err
	}
	fin := header[0]&0x80 != 0
	opcode := header[0] & 0x0f
	if header[0]&0x70 != 0 {
		return wsFrame{}, ErrBadFrame
	}
	switch opcode {
	case opContinuation, opText, opBinary, opClose, opPing, opPong:
	default:
		return wsFrame{}, ErrBadFrame
	}
	masked := header[1]&0x80 != 0
	if !masked {
		return wsFrame{}, ErrBadFrame
	}
	lengthCode := header[1] & 0x7f
	length := uint64(lengthCode)
	switch lengthCode {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return wsFrame{}, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
		if length < 126 {
			return wsFrame{}, ErrBadFrame
		}
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return wsFrame{}, err
		}
		if ext[0]&0x80 != 0 {
			return wsFrame{}, ErrBadFrame
		}
		length = binary.BigEndian.Uint64(ext[:])
		if length <= 0xffff {
			return wsFrame{}, ErrBadFrame
		}
	}
	if opcode >= opClose && (!fin || length > 125) {
		return wsFrame{}, ErrFragmentedControlFrame
	}
	if maxSize > 0 && length > uint64(maxSize) {
		return wsFrame{}, ErrFrameTooLarge
	}
	if length > uint64(^uint(0)>>1) {
		return wsFrame{}, ErrFrameTooLarge
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return wsFrame{}, err
		}
	}
	payload := make([]byte, int(length))
	if length > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return wsFrame{}, err
		}
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return wsFrame{fin: fin, opcode: opcode, payload: payload}, nil
}

func appendFragment(dst, part []byte, maxSize int) ([]byte, error) {
	if maxSize > 0 && len(part) > maxSize-len(dst) {
		return nil, ErrFrameTooLarge
	}
	if len(part) == 0 {
		return dst, nil
	}
	return append(dst, part...), nil
}

func writeFrame(w io.Writer, opcode byte, payload []byte) error {
	header := []byte{0x80 | opcode}
	length := len(payload)
	switch {
	case length < 126:
		header = append(header, byte(length))
	case length <= 0xffff:
		header = append(header, 126, 0, 0)
		binary.BigEndian.PutUint16(header[len(header)-2:], uint16(length))
	default:
		header = append(header, 127, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64(header[len(header)-8:], uint64(length))
	}
	if err := streamutil.WriteFull(w, header); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	return streamutil.WriteFull(w, payload)
}

func isUpgradeRequest(r *http.Request) bool {
	return r != nil && r.Method == http.MethodGet && r.ProtoAtLeast(1, 1) &&
		headerHasToken(r.Header, "Upgrade", "websocket") &&
		headerHasToken(r.Header, "Connection", "upgrade")
}

func singleHeaderValue(header http.Header, name string) (string, bool) {
	values := header.Values(name)
	if len(values) != 1 {
		return "", false
	}
	return strings.TrimSpace(values[0]), true
}

func headerHasToken(header http.Header, name, token string) bool {
	for _, value := range header.Values(name) {
		for _, candidate := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(candidate), token) {
				return true
			}
		}
	}
	return false
}

func validWebSocketKey(key string) bool {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(key))
	return err == nil && len(decoded) == 16
}

func (s *Server) originAllowed(r *http.Request) bool {
	if s.config.CheckOrigin != nil {
		return s.config.CheckOrigin(r)
	}
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	if len(origins) != 1 {
		return false
	}
	origin, err := url.Parse(origins[0])
	if err != nil || origin.Host == "" {
		return false
	}
	return strings.EqualFold(origin.Host, r.Host)
}

func validateClosePayload(payload []byte) error {
	if len(payload) == 0 {
		return nil
	}
	if len(payload) == 1 {
		return ErrBadFrame
	}
	code := binary.BigEndian.Uint16(payload[:2])
	if !validCloseCode(code) || !utf8.Valid(payload[2:]) {
		return ErrBadFrame
	}
	return nil
}

func validCloseCode(code uint16) bool {
	if code >= 3000 && code <= 4999 {
		return true
	}
	return code == 1000 || code == 1001 || code == 1002 || code == 1003 ||
		code == 1007 || code == 1008 || code == 1009 || code == 1010 ||
		code == 1011 || code == 1012 || code == 1013 || code == 1014
}

func closePayload(code uint16, reason string) []byte {
	payload := make([]byte, 2, 2+len(reason))
	binary.BigEndian.PutUint16(payload, code)
	return append(payload, reason...)
}

func computeAcceptKey(key string) string {
	const magic = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	sum := sha1.Sum([]byte(key + magic))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func normalizeConfig(config Config) Config {
	if config.Addr == "" {
		config.Addr = DefaultAddr
	}
	if config.Path == "" {
		config.Path = DefaultPath
	}
	if config.Codec == nil {
		config.Codec = codec.KProV1()
	}
	if config.MaxMessage <= 0 {
		config.MaxMessage = DefaultMaxMessage
	}
	if config.MaxConnections <= 0 {
		config.MaxConnections = DefaultMaxConnections
	}
	if config.SendQueueSize <= 0 {
		config.SendQueueSize = DefaultSendQueueSize
	}
	if config.MaxPendingBytes <= 0 {
		config.MaxPendingBytes = outbound.ByteLimit(config.SendQueueSize, int64(config.MaxMessage))
	}
	if config.MaxWriteBatchFrames <= 0 {
		config.MaxWriteBatchFrames = DefaultMaxWriteBatchFrames
	}
	if config.MaxWriteBatchFrames > config.SendQueueSize {
		config.MaxWriteBatchFrames = config.SendQueueSize
	}
	if config.MaxWriteBatchBytes <= 0 {
		config.MaxWriteBatchBytes = min(int64(DefaultMaxWriteBatchBytes), config.MaxPendingBytes)
	}
	if config.ReadTimeout <= 0 {
		config.ReadTimeout = DefaultReadTimeout
	}
	if config.WriteTimeout <= 0 {
		config.WriteTimeout = DefaultWriteTimeout
	}
	if config.HandshakeTimeout <= 0 {
		config.HandshakeTimeout = DefaultHandshakeTimeout
	}
	return config
}

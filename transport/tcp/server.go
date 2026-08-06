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
	"github.com/saramand9/kpro/transport/internal/bufferpool"
	"github.com/saramand9/kpro/transport/internal/dispatchgate"
	"github.com/saramand9/kpro/transport/internal/outbound"
	"github.com/saramand9/kpro/transport/internal/serveutil"
	"github.com/saramand9/kpro/transport/proxyproto"
)

const (
	DefaultAddr                   = ":8088"
	DefaultMaxFrameSize           = 64 * 1024
	DefaultMaxConnections         = 10000
	DefaultSendQueueSize          = 64
	DefaultMaxWriteBatchFrames    = 32
	DefaultMaxWriteBatchBytes     = 256 * 1024
	DefaultMaxWriteBatchDelay     = 2 * time.Microsecond
	DefaultMaxRetainedBufferBytes = 4 * 1024 * 1024
	DefaultMaxReadBufferSize      = 32 * 1024
	DefaultReadTimeout            = 90 * time.Second
	DefaultWriteTimeout           = 5 * time.Second
	defaultPayloadPoolShards      = 64
	defaultMaxReadBatchFrames     = 16
)

// Config controls TCP listener, codec, queue, and timeout settings.
type Config struct {
	// Addr is the TCP listen address.
	Addr string
	// ProxyProtocol optionally requires HAProxy PROXY protocol v1/v2 from
	// explicitly trusted L4 proxies. The zero value disables it.
	ProxyProtocol proxyproto.Config
	// Codec decodes and encodes stream frames. Nil uses codec.KProV1().
	Codec codec.StreamCodec
	// MaxFrameSize limits decoded inbound frame payload size. Outbound writes
	// are bounded independently by MaxPendingBytes and the selected codec.
	MaxFrameSize uint32
	// MaxConnections caps active TCP sessions.
	MaxConnections int
	// SendQueueSize caps per-session writes across encoding reservations, the
	// queue, and the transport writer.
	SendQueueSize int
	// MaxPendingBytes caps bytes reserved for encoding, queued, or currently
	// being written per session. Zero derives SendQueueSize * (MaxFrameSize + frame header).
	MaxPendingBytes int64
	// MaxWriteBatchFrames caps frames gathered by one non-blocking queue drain.
	MaxWriteBatchFrames int
	// MaxWriteBatchBytes caps bytes submitted by one gather-write call. A
	// single oversized admitted frame is still written to ensure progress.
	MaxWriteBatchBytes int64
	// MaxWriteBatchDelay is the bounded coalescing window used when a writer
	// observes only one queued frame. Non-positive values use the default.
	MaxWriteBatchDelay time.Duration
	// MaxRetainedBufferBytes caps idle inbound and outbound payload memory
	// across the server's sharded size-class pools. Queue and Router limits
	// separately bound active buffers and admitted work.
	MaxRetainedBufferBytes int64
	// MaxReadBufferSize caps per-connection adaptive receive aggregation.
	// Buffers start at 4 KiB and grow only when lower-level reads fill them.
	MaxReadBufferSize int
	// ReadTimeout bounds read inactivity. The transport establishes a deadline
	// before reading and refreshes it by a bounded time window under traffic.
	ReadTimeout time.Duration
	// WriteTimeout bounds an inactive or blocked write. The transport refreshes
	// it per write activity window rather than before every batch.
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
	payloadPool *bufferpool.ShardedPool
	reuseCodec  codec.ReusableStreamCodec
	bufferCodec codec.BufferedStreamCodec

	nextID    uint64
	active    int64
	sessions  sync.Map
	ready     chan struct{}
	readyOnce sync.Once
}

// New creates a TCP transport with normalized defaults.
func New(config Config) *Server {
	normalized := normalizeConfig(config)
	server := &Server{
		config:    normalized,
		rawConfig: config,
		accepting: true,
		dispatch:  dispatchgate.New(),
		payloadPool: bufferpool.NewSharded(bufferpool.Config{
			MaxRetainedBytes: normalized.MaxRetainedBufferBytes,
			MaxPooledSize:    DefaultMaxFrameSize,
		}, defaultPayloadPoolShards),
		ready: make(chan struct{}),
	}
	server.reuseCodec, _ = normalized.Codec.(codec.ReusableStreamCodec)
	server.bufferCodec, _ = normalized.Codec.(codec.BufferedStreamCodec)
	return server
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
	return s.listenAndServe(ctx, handler, nil)
}

// ListenAndServeBatch enables optional same-read message batching while
// preserving the ordinary handler as an exact fallback.
func (s *Server) ListenAndServeBatch(
	ctx context.Context,
	handler core.Handler,
	batchHandler transportapi.BatchHandler,
) error {
	return s.listenAndServe(ctx, handler, batchHandler)
}

func (s *Server) listenAndServe(
	ctx context.Context,
	handler core.Handler,
	batchHandler transportapi.BatchHandler,
) error {
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
	rawListener, err := net.Listen("tcp", s.config.Addr)
	if err != nil {
		s.rollbackStart()
		return err
	}
	ln, err := proxyproto.WrapListener(rawListener, s.config.ProxyProtocol)
	if err != nil {
		_ = rawListener.Close()
		s.rollbackStart()
		return fmt.Errorf("tcp proxy protocol: %w", err)
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
		session.payloadPool = s.payloadPool.Shard(id)
		if !s.registerSessionWithBatch(session, handler, batchHandler) {
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
	return s.registerSessionWithBatch(session, handler, nil)
}

func (s *Server) registerSessionWithBatch(
	session *session,
	handler core.Handler,
	batchHandler transportapi.BatchHandler,
) bool {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if !s.accepting {
		return false
	}
	s.sessions.Store(session.id, session)
	session.start(handler, batchHandler)
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
	id                   uint64
	server               *Server
	raw                  net.Conn
	sendQueue            *outbound.Queue[outboundFrame]
	payloadPool          *bufferpool.Pool
	done                 chan struct{}
	readBuffer           *adaptiveReader
	readPayloadSize      int
	writeDeadlineRefresh time.Time
	closeOnce            sync.Once
	readFrames           [defaultMaxReadBatchFrames]inboundFrame
	readContexts         [defaultMaxReadBatchFrames]core.Context
}

type inboundFrame struct {
	message core.Message
	buffer  []byte
}

func (s *session) outboundPayloadPool() *bufferpool.Pool {
	if s.payloadPool != nil {
		return s.payloadPool
	}
	return s.server.payloadPool.Shard(s.id)
}

func (s *session) ReleaseTransportBuffer(buffer []byte) {
	s.outboundPayloadPool().Put(buffer)
}

var _ core.OutboundStateProvider = (*session)(nil)
var _ core.OutboundWritableWaiter = (*session)(nil)
var _ core.OwnedPayloadSender = (*session)(nil)
var _ transportapi.Quiescer = (*Server)(nil)
var _ transportapi.BatchMessageServer = (*Server)(nil)

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
	headerBytes := int64(codec.FrameHeaderSize)
	if headerSize, ok := segmentedHeaderSize(s.server.config.Codec); ok {
		headerBytes = int64(headerSize)
	}
	reservation, err := s.sendQueue.Reserve(outbound.EstimateBytes(len(msg.Data), headerBytes))
	if err != nil {
		return s.outboundError(err)
	}
	defer reservation.Cancel()
	frame, err := s.encodeOutboundFrame(msg)
	if err != nil {
		return err
	}
	if err := reservation.Commit(frame, frame.wireBytes()); err != nil {
		frame.release()
		return s.outboundError(err)
	}
	return nil
}

// SendOwnedPayload consumes one transport Context reference on every return
// path. Segmented codecs can retain Message.Data directly until frame
// completion; other codecs preserve correctness by falling back to Send.
func (s *session) SendOwnedPayload(msg core.Message, owner *core.BaseContext) error {
	ownerTransferred := false
	defer func() {
		if !ownerTransferred {
			core.ReleaseTransportContext(owner)
		}
	}()
	select {
	case <-s.done:
		return core.ErrClosed
	default:
	}
	if err := validateSegmentedPayloadSize(s.server.config.Codec, len(msg.Data)); err != nil {
		return err
	}
	headerSize, segmented := segmentedHeaderSize(s.server.config.Codec)
	if !segmented {
		return s.Send(msg)
	}
	reservation, err := s.sendQueue.Reserve(
		outbound.EstimateBytes(len(msg.Data), int64(headerSize)),
	)
	if err != nil {
		return s.outboundError(err)
	}
	defer reservation.Cancel()
	frame, err := s.segmentedFrame(msg, msg.Data)
	if err != nil {
		return err
	}
	frame.pool = nil
	frame.owned = owner
	ownerTransferred = true
	if err := reservation.Commit(frame, frame.wireBytes()); err != nil {
		frame.release()
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

func (s *session) start(handler core.Handler, batchHandler transportapi.BatchHandler) {
	go s.readLoop(handler, batchHandler)
	go s.writeLoop()
}

func (s *session) readLoop(handler core.Handler, batchHandler transportapi.BatchHandler) {
	preserveSession := false
	var readDeadlineRefresh time.Time
	if timeout := s.server.config.ReadTimeout; timeout > 0 {
		now := time.Now()
		if err := s.raw.SetReadDeadline(now.Add(timeout)); err != nil {
			return
		}
		readDeadlineRefresh = now.Add(timeout / 2)
	}
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
		if !readDeadlineRefresh.IsZero() {
			now := time.Now()
			if !now.Before(readDeadlineRefresh) {
				if err := s.raw.SetReadDeadline(now.Add(s.server.config.ReadTimeout)); err != nil {
					preserveSession = s.server.dispatch.Stopped()
					return
				}
				readDeadlineRefresh = now.Add(s.server.config.ReadTimeout / 2)
			}
		}
		var result codec.ReadResult
		var readBuffer []byte
		var err error
		if batchHandler == nil {
			result, readBuffer, err = s.readFrame()
		} else {
			var buffered bool
			result, readBuffer, buffered, err = s.readBufferedFrame()
			if err == nil && !buffered {
				result, readBuffer, err = s.readFrame()
			}
		}
		if err != nil {
			preserveSession = s.server.dispatch.Stopped()
			if err == io.EOF {
				return
			}
			return
		}
		frameCount := 1
		s.readFrames[0] = inboundFrame{message: result.Message, buffer: readBuffer}
		var bufferedErr error
		if batchHandler != nil {
			for frameCount < len(s.readFrames) {
				if s.server.dispatch.Stopped() {
					s.releaseInboundFrames(frameCount)
					preserveSession = true
					return
				}
				next, nextBuffer, complete, nextErr := s.readBufferedFrame()
				if nextErr != nil {
					bufferedErr = nextErr
					break
				}
				if !complete {
					break
				}
				s.readFrames[frameCount] = inboundFrame{
					message: next.Message,
					buffer:  nextBuffer,
				}
				frameCount++
			}
		}
		keepReading := s.dispatchInboundFrames(handler, batchHandler, frameCount)
		if s.server.dispatch.Stopped() {
			preserveSession = true
			return
		}
		if !keepReading {
			return
		}
		if bufferedErr != nil {
			return
		}
	}
}

func (s *session) dispatchInboundFrames(
	handler core.Handler,
	batchHandler transportapi.BatchHandler,
	count int,
) bool {
	for start := 0; start < count; {
		if s.readFrames[start].message.ID == core.MsgHeartbeat {
			if !s.dispatchHeartbeat(start) {
				s.releaseInboundFramesRange(start+1, count)
				return false
			}
			start++
			continue
		}

		end := start + 1
		for end < count && s.readFrames[end].message.ID != core.MsgHeartbeat {
			end++
		}
		if batchHandler != nil && end-start > 1 {
			if !s.dispatchInboundBatch(handler, batchHandler, start, end) {
				s.releaseInboundFramesRange(end, count)
				return false
			}
		} else {
			for index := start; index < end; index++ {
				if !s.dispatchInboundOne(handler, index) {
					s.releaseInboundFramesRange(index+1, count)
					return false
				}
			}
		}
		start = end
	}
	return true
}

func (s *session) dispatchHeartbeat(index int) bool {
	frame := s.readFrames[index]
	s.readFrames[index] = inboundFrame{}
	if !s.server.dispatch.Begin() {
		s.ReleaseTransportBuffer(frame.buffer)
		return false
	}
	defer s.server.dispatch.End()
	_ = s.Send(core.Message{ID: core.MsgPong, Seq: frame.message.Seq})
	s.ReleaseTransportBuffer(frame.buffer)
	return true
}

func (s *session) dispatchInboundOne(handler core.Handler, index int) bool {
	frame := s.readFrames[index]
	s.readFrames[index] = inboundFrame{}
	if !s.server.dispatch.Begin() {
		s.ReleaseTransportBuffer(frame.buffer)
		return false
	}
	defer s.server.dispatch.End()
	err := transportapi.InvokeOwnedHandler(handler, s, frame.message, frame.buffer, s)
	return transportapi.ContinueAfterHandlerError(
		s.server.config.HandlerErrorPolicy,
		s.server.config.OnHandlerError,
		s,
		frame.message,
		err,
	)
}

func (s *session) dispatchInboundBatch(
	handler core.Handler,
	batchHandler transportapi.BatchHandler,
	start int,
	end int,
) bool {
	if !s.server.dispatch.Begin() {
		s.releaseInboundFramesRange(start, end)
		return false
	}
	defer s.server.dispatch.End()

	firstMessage := s.readFrames[start].message
	contexts := s.readContexts[:end-start]
	for index := start; index < end; index++ {
		frame := s.readFrames[index]
		contexts[index-start] = core.AcquireOwnedTransportContext(
			s,
			frame.message,
			frame.buffer,
			s,
		)
		s.readFrames[index] = inboundFrame{}
	}
	accepted, batchErr := transportapi.InvokeBatchHandler(batchHandler, contexts)
	if batchErr != nil {
		for index := range contexts {
			core.ReleaseTransportContext(contexts[index])
			contexts[index] = nil
		}
		return transportapi.ContinueAfterHandlerError(
			s.server.config.HandlerErrorPolicy,
			s.server.config.OnHandlerError,
			s,
			firstMessage,
			batchErr,
		)
	}
	if accepted {
		for index := range contexts {
			core.ReleaseTransportContext(contexts[index])
			contexts[index] = nil
		}
		return true
	}

	for index, dispatchContext := range contexts {
		message := dispatchContext.Message()
		err := transportapi.InvokeContextHandler(handler, dispatchContext)
		core.ReleaseTransportContext(dispatchContext)
		contexts[index] = nil
		if !transportapi.ContinueAfterHandlerError(
			s.server.config.HandlerErrorPolicy,
			s.server.config.OnHandlerError,
			s,
			message,
			err,
		) {
			for remaining := index + 1; remaining < len(contexts); remaining++ {
				core.ReleaseTransportContext(contexts[remaining])
				contexts[remaining] = nil
			}
			return false
		}
	}
	return true
}

func (s *session) releaseInboundFrames(count int) {
	s.releaseInboundFramesRange(0, count)
}

func (s *session) releaseInboundFramesRange(start int, end int) {
	for index := start; index < end; index++ {
		s.ReleaseTransportBuffer(s.readFrames[index].buffer)
		s.readFrames[index] = inboundFrame{}
	}
}

func (s *session) readFrame() (codec.ReadResult, []byte, error) {
	if s.readBuffer == nil {
		initialSize := min(4*1024, s.server.config.MaxReadBufferSize)
		s.readBuffer = newAdaptiveReader(s.raw, initialSize, s.server.config.MaxReadBufferSize)
	}
	if s.server.reuseCodec == nil {
		result, err := s.server.config.Codec.Read(s.readBuffer, s.server.config.MaxFrameSize)
		return result, nil, err
	}

	pool := s.outboundPayloadPool()
	scratch := pool.GetUninitialized(s.readPayloadSize)
	result, owned, err := s.server.reuseCodec.ReadInto(
		s.readBuffer,
		s.server.config.MaxFrameSize,
		scratch,
	)
	// If the codec grew scratch, the original pooled buffer is deliberately
	// discarded instead of being returned early: a custom codec may return a
	// subslice, and retaining both until ownership is known avoids alias bugs.
	if err != nil {
		pool.Put(owned)
		return codec.ReadResult{}, nil, err
	}
	if len(result.Message.Data) == 0 {
		pool.Put(owned)
		return result, nil, nil
	}
	s.readPayloadSize = len(result.Message.Data)
	return result, owned, nil
}

func (s *session) readBufferedFrame() (codec.ReadResult, []byte, bool, error) {
	if s.server.bufferCodec == nil || s.readBuffer == nil {
		return codec.ReadResult{}, nil, false, nil
	}
	result, complete, err := s.server.bufferCodec.DecodeBuffered(
		s.readBuffer.bufferedBytes(),
		s.server.config.MaxFrameSize,
	)
	if err != nil || !complete {
		return codec.ReadResult{}, nil, false, err
	}

	var owned []byte
	if payloadSize := len(result.Message.Data); payloadSize > 0 {
		owned = s.outboundPayloadPool().GetUninitialized(payloadSize)
		copy(owned, result.Message.Data)
		result.Message.Data = owned
		s.readPayloadSize = payloadSize
	}
	s.readBuffer.discardBuffered(result.Bytes)
	return result, owned, true, nil
}

func (s *session) writeLoop() {
	writer := newSessionBatchWriter(s.server.config)
	for {
		select {
		case item := <-s.sendQueue.Items():
			if err := writer.writeAvailable(s, &item); err != nil {
				_ = s.Close()
				return
			}
		case <-s.done:
			return
		}
	}
}

func newSessionOutboundQueue(config Config) *outbound.Queue[outboundFrame] {
	return outbound.NewWithFinalizer(outbound.Config{
		MaxFrames: config.SendQueueSize,
		MaxBytes:  config.MaxPendingBytes,
	}, func(frame outboundFrame) {
		frame.release()
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
	if config.MaxWriteBatchFrames <= 0 {
		config.MaxWriteBatchFrames = DefaultMaxWriteBatchFrames
	}
	if config.MaxWriteBatchFrames > config.SendQueueSize {
		config.MaxWriteBatchFrames = config.SendQueueSize
	}
	if config.MaxWriteBatchBytes <= 0 {
		config.MaxWriteBatchBytes = min(int64(DefaultMaxWriteBatchBytes), config.MaxPendingBytes)
	}
	if config.MaxWriteBatchDelay <= 0 {
		config.MaxWriteBatchDelay = DefaultMaxWriteBatchDelay
	}
	if config.MaxRetainedBufferBytes <= 0 {
		config.MaxRetainedBufferBytes = DefaultMaxRetainedBufferBytes
	}
	if config.MaxReadBufferSize <= 0 {
		config.MaxReadBufferSize = DefaultMaxReadBufferSize
	}
	if config.ReadTimeout <= 0 {
		config.ReadTimeout = DefaultReadTimeout
	}
	if config.WriteTimeout <= 0 {
		config.WriteTimeout = DefaultWriteTimeout
	}
	return config
}

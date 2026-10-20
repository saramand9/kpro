// File: transport/udp/server.go
// Purpose: Implements the UDP transport adapter for KPro packet sessions.

package udp

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
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
	"github.com/saramand9/kpro/transport/internal/sessionoutbound"
)

const (
	DefaultAddr               = ":8089"
	DefaultMaxPacketSize      = 2048
	DefaultMaxSessions        = 10000
	DefaultSendQueueSize      = 64
	DefaultSessionIdleTimeout = 90 * time.Second
	DefaultCleanupInterval    = 30 * time.Second
	udpDatagramReadBufferSize = 64 * 1024
)

// Config controls UDP listener, packet codec, sessions, queues, and cleanup.
type Config struct {
	// Addr is the UDP listen address.
	Addr string
	// Codec decodes and encodes packet payloads. Nil uses codec.KProV1().
	Codec codec.PacketCodec
	// MaxPacketSize limits incoming datagram read size.
	MaxPacketSize int
	// MaxSessions caps active virtual peer sessions.
	MaxSessions int
	// SendQueueSize caps per-peer writes across encoding reservations, the
	// queue, and the transport writer.
	SendQueueSize int
	// MaxPendingBytes caps bytes reserved for encoding, queued, or currently
	// being written per peer. Zero derives SendQueueSize * MaxPacketSize.
	MaxPendingBytes int64
	// SessionIdleTimeout closes virtual sessions with no recent packets.
	SessionIdleTimeout time.Duration
	// CleanupInterval controls how often idle UDP sessions are swept.
	CleanupInterval time.Duration
	// HandlerErrorPolicy defaults to closing the affected virtual session.
	HandlerErrorPolicy transportapi.HandlerErrorPolicy
	// OnHandlerError observes handler failures before the policy is applied.
	OnHandlerError transportapi.HandlerErrorObserver
}

// Server maps UDP peers to virtual sessions and dispatches decoded packets.
type Server struct {
	config    Config
	rawConfig Config
	conn      *net.UDPConn
	// clockBase lets atomic activity timestamps retain Go's monotonic clock.
	clockBase time.Time

	mu            sync.Mutex
	sessions      map[string]*packetSession
	closed        bool
	accepting     bool
	started       bool
	dispatch      *dispatchgate.Gate
	outboundStats outbound.Tracker
	ready         chan struct{}
	readyOnce     sync.Once
}

// New creates a UDP transport with normalized defaults.
func New(config Config) *Server {
	return &Server{
		config:    normalizeConfig(config),
		rawConfig: config,
		sessions:  make(map[string]*packetSession),
		clockBase: time.Now(),
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
	return "udp"
}

// Ready is closed once the UDP socket is successfully bound and published.
func (s *Server) Ready() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.ready
}

// ListenAndServe reads UDP packets until ctx is cancelled or the socket fails.
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
		return fmt.Errorf("udp: %w", core.ErrNoHandler)
	}
	addr, err := net.ResolveUDPAddr("udp", s.config.Addr)
	if err != nil {
		s.rollbackStart()
		return err
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		s.rollbackStart()
		return err
	}
	if !s.publishConn(conn) {
		_ = conn.Close()
		return core.ErrClosed
	}
	s.signalReady()

	serveDone := make(chan struct{})
	watcherDone := serveutil.ShutdownOnCancel(ctx, serveDone, func() {
		_ = s.Shutdown(context.Background())
	})
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		s.cleanupLoop(ctx, serveDone)
	}()
	defer func() {
		serveutil.FinishContextWatcher(serveDone, watcherDone)
		<-cleanupDone
	}()

	// Read the complete UDP datagram before enforcing MaxPacketSize. Reading
	// directly into a MaxPacketSize buffer can silently truncate an oversized
	// datagram and make a valid frame prefix indistinguishable from the complete
	// packet on platforms where ReadFromUDP reports no truncation error.
	buf := make([]byte, udpDatagramReadBufferSize)
	for {
		n, remote, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil || !s.isAccepting() {
				return nil
			}
			return err
		}
		if n > s.config.MaxPacketSize {
			continue
		}
		msg, err := s.config.Codec.DecodePacket(buf[:n])
		if err != nil {
			continue
		}
		session, ok := s.getOrCreateSession(remote)
		if !ok {
			continue
		}
		if !s.dispatch.Begin() {
			return nil
		}
		func() {
			defer s.dispatch.End()
			if msg.ID == core.MsgHeartbeat {
				_ = session.Send(core.Message{ID: core.MsgPong, Seq: msg.Seq})
				return
			}
			err = transportapi.InvokeHandler(handler, session, msg)
			keepReading := transportapi.ContinueAfterHandlerError(
				s.config.HandlerErrorPolicy,
				s.config.OnHandlerError,
				session,
				msg,
				err,
			)
			if !keepReading {
				// Linearize the close-policy decision with Quiesce, then close
				// outside the server lock.
				s.mu.Lock()
				closeSession := s.accepting && !s.closed
				s.mu.Unlock()
				if closeSession {
					_ = session.Close()
				}
			}
		}()
	}
}

func (s *Server) claimStart() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !s.accepting {
		return core.ErrClosed
	}
	if s.started {
		return core.ErrAlreadyStarted
	}
	s.started = true
	return nil
}

func (s *Server) rollbackStart() {
	s.mu.Lock()
	if !s.closed && s.accepting && s.conn == nil {
		s.started = false
	}
	s.mu.Unlock()
}

func (s *Server) publishConn(conn *net.UDPConn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !s.accepting {
		return false
	}
	s.conn = conn
	return true
}

func (s *Server) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *Server) isAccepting() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accepting && !s.closed
}

// Quiesce stops datagram reads and virtual-session admission while preserving
// the UDP socket for established peer-session writers until Shutdown.
func (s *Server) Quiesce(context.Context) error {
	return s.stopAdmissionAndReads()
}

// Shutdown drains peer writes before closing the UDP socket and sessions.
func (s *Server) Shutdown(ctx context.Context) error {
	stopErr := s.stopAdmissionAndReads()
	drainErr := s.DrainOutbound(ctx)
	s.mu.Lock()
	s.closed = true
	s.accepting = false
	conn := s.conn
	sessions := make([]*packetSession, 0, len(s.sessions))
	for _, session := range s.sessions {
		sessions = append(sessions, session)
	}
	s.mu.Unlock()

	var closeErr error
	if conn != nil {
		if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			closeErr = err
		}
	}
	for _, session := range sessions {
		_ = session.Close()
	}
	return errors.Join(stopErr, drainErr, closeErr)
}

func (s *Server) DrainOutbound(ctx context.Context) (drainErr error) {
	started := time.Now()
	defer func() {
		s.outboundStats.RecordDrain(time.Since(started), drainErr)
	}()
	s.mu.Lock()
	queues := make([]*outbound.Queue[[]byte], 0, len(s.sessions))
	for _, session := range s.sessions {
		queues = append(queues, session.sendQueue)
	}
	s.mu.Unlock()
	return outbound.DrainQueues(ctx, queues)
}

// OutboundSnapshot reports bounded aggregate write and drain statistics.
func (s *Server) OutboundSnapshot() transportapi.OutboundSnapshot {
	if s == nil {
		return transportapi.OutboundSnapshot{}
	}
	return s.outboundStats.Snapshot()
}

func (s *Server) stopAdmissionAndReads() error {
	s.mu.Lock()
	s.accepting = false
	conn := s.conn
	s.mu.Unlock()
	// Publish the cleanup/session-admission boundary under the same lock used
	// by closeIdleSessions before closing the handler dispatch gate.
	s.dispatch.Stop()
	if conn == nil {
		return nil
	}
	if err := conn.SetReadDeadline(time.Now()); err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}

func (s *Server) signalReady() {
	if s == nil || s.ready == nil {
		return
	}
	s.readyOnce.Do(func() { close(s.ready) })
}

func (s *Server) getOrCreateSession(addr *net.UDPAddr) (*packetSession, bool) {
	key := addr.String()
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !s.accepting {
		return nil, false
	}
	if session := s.sessions[key]; session != nil && !session.isClosed() {
		session.touchAtLocked(now)
		return session, true
	}
	if s.config.MaxSessions > 0 && len(s.sessions) >= s.config.MaxSessions {
		return nil, false
	}

	remote := *addr
	session := &packetSession{
		id:        sessionID(addr),
		key:       key,
		server:    s,
		remote:    &remote,
		sendQueue: newSessionOutboundQueue(s.config, &s.outboundStats),
		done:      make(chan struct{}),
	}
	session.lastActive.Store(s.activityTick(now))
	s.sessions[key] = session
	go session.writeLoop()
	return session, true
}

func (s *Server) udpConn() *net.UDPConn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn
}

func (s *Server) cleanupLoop(ctx context.Context, serveDone <-chan struct{}) {
	ticker := time.NewTicker(s.config.CleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			s.closeIdleSessions(now)
		case <-ctx.Done():
			return
		case <-serveDone:
			return
		}
	}
}

func (s *Server) closeIdleSessions(now time.Time) {
	if s.config.SessionIdleTimeout <= 0 {
		return
	}
	s.mu.Lock()
	if !s.accepting || s.closed {
		s.mu.Unlock()
		return
	}
	for _, session := range s.sessions {
		if session.idleSince(now) >= s.config.SessionIdleTimeout {
			session.closeLocked()
		}
	}
	s.mu.Unlock()
}

func (s *Server) activityTick(now time.Time) int64 {
	return now.Sub(s.clockBase).Nanoseconds()
}

func sessionID(addr *net.UDPAddr) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(addr.String()))
	return h.Sum64()
}

type packetSession struct {
	id     uint64
	key    string
	server *Server
	remote *net.UDPAddr

	sendQueue *outbound.Queue[[]byte]
	done      chan struct{}
	closeOnce sync.Once

	closed     atomic.Bool
	lastActive atomic.Int64
}

var _ core.OutboundStateProvider = (*packetSession)(nil)
var _ core.OutboundWritableWaiter = (*packetSession)(nil)
var _ core.WriteReceiptSender = (*packetSession)(nil)
var _ core.SessionOutboundDrainer = (*packetSession)(nil)
var _ transportapi.Quiescer = (*Server)(nil)
var _ transportapi.OutboundDrainer = (*Server)(nil)
var _ transportapi.OutboundSnapshotter = (*Server)(nil)

func (s *packetSession) ID() uint64 {
	return s.id
}

func (s *packetSession) Transport() core.Transport {
	return core.TransportUDP
}

func (s *packetSession) RemoteAddr() net.Addr {
	return s.remote
}

func (s *packetSession) Done() <-chan struct{} {
	return s.done
}

func (s *packetSession) Send(msg core.Message) error {
	return s.send(msg, nil)
}

func (s *packetSession) SendWithReceipt(msg core.Message) (*core.WriteReceipt, error) {
	receipt := core.NewWriteReceipt()
	if err := s.send(msg, receipt); err != nil {
		return nil, err
	}
	return receipt, nil
}

func (s *packetSession) send(msg core.Message, completion outbound.Completion) error {
	if s.server.udpConn() == nil {
		return core.ErrClosed
	}
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
	s.touch()
	if err := reservation.CommitWithCompletion(packet, int64(len(packet)), completion); err != nil {
		return s.outboundError(err)
	}
	return nil
}

func (s *packetSession) Close() error {
	s.server.mu.Lock()
	s.closeLocked()
	s.server.mu.Unlock()
	return nil
}

func (s *packetSession) DrainOutbound(ctx context.Context) error {
	if s == nil || s.sendQueue == nil {
		return core.ErrClosed
	}
	s.sendQueue.Seal()
	return s.sendQueue.WaitDrained(ctx)
}

func (s *packetSession) OutboundState() core.OutboundState {
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

func (s *packetSession) WaitOutboundWritable(ctx context.Context) error {
	if s == nil || s.sendQueue == nil {
		return core.ErrClosed
	}
	return sessionoutbound.MapWaitError(s.sendQueue.WaitWritable(ctx))
}

// closeLocked closes and removes the session while server.mu serializes it
// with packet activity updates and idle cleanup.
func (s *packetSession) closeLocked() {
	s.closeOnce.Do(func() {
		if s.sendQueue != nil {
			s.sendQueue.CloseWithError(core.ErrClosed)
		}
		s.closed.Store(true)
		close(s.done)
		if s.server.sessions[s.key] == s {
			delete(s.server.sessions, s.key)
		}
	})
}

func (s *packetSession) writeLoop() {
	for {
		select {
		case item := <-s.sendQueue.Items():
			packet := item.Value
			conn := s.server.udpConn()
			if conn == nil {
				item.Complete(core.ErrClosed)
				_ = s.Close()
				return
			}
			written, err := conn.WriteToUDP(packet, s.remote)
			if err != nil {
				item.Complete(err)
				_ = s.Close()
				return
			}
			if written != len(packet) {
				item.Complete(io.ErrShortWrite)
				_ = s.Close()
				return
			}
			item.Done()
		case <-s.done:
			return
		}
	}
}

func newSessionOutboundQueue(config Config, trackers ...*outbound.Tracker) *outbound.Queue[[]byte] {
	var tracker *outbound.Tracker
	if len(trackers) > 0 {
		tracker = trackers[0]
	}
	return outbound.NewTracked[[]byte](outbound.Config{
		MaxFrames: config.SendQueueSize,
		MaxBytes:  config.MaxPendingBytes,
	}, tracker)
}

func (s *packetSession) outboundError(err error) error {
	switch err {
	case outbound.ErrClosed:
		return core.ErrClosed
	case outbound.ErrFull:
		return core.ErrSendQueueFull
	default:
		return err
	}
}

func (s *packetSession) touch() {
	now := time.Now()
	s.server.mu.Lock()
	if !s.closed.Load() && s.server.sessions[s.key] == s {
		s.touchAtLocked(now)
	}
	s.server.mu.Unlock()
}

// touchAtLocked records activity while server.mu protects the decision against
// idle cleanup closing the same session.
func (s *packetSession) touchAtLocked(now time.Time) {
	s.lastActive.Store(s.server.activityTick(now))
}

func (s *packetSession) idleSince(now time.Time) time.Duration {
	if s.closed.Load() {
		return 0
	}
	return time.Duration(s.server.activityTick(now) - s.lastActive.Load())
}

func (s *packetSession) isClosed() bool {
	return s.closed.Load()
}

func normalizeConfig(config Config) Config {
	if config.Addr == "" {
		config.Addr = DefaultAddr
	}
	if config.Codec == nil {
		config.Codec = codec.KProV1()
	}
	if config.MaxPacketSize <= 0 {
		config.MaxPacketSize = DefaultMaxPacketSize
	}
	if config.MaxSessions <= 0 {
		config.MaxSessions = DefaultMaxSessions
	}
	if config.SendQueueSize <= 0 {
		config.SendQueueSize = DefaultSendQueueSize
	}
	if config.MaxPendingBytes <= 0 {
		config.MaxPendingBytes = outbound.ByteLimit(config.SendQueueSize, int64(config.MaxPacketSize))
	}
	if config.SessionIdleTimeout <= 0 {
		config.SessionIdleTimeout = DefaultSessionIdleTimeout
	}
	if config.CleanupInterval <= 0 {
		config.CleanupInterval = DefaultCleanupInterval
	}
	return config
}

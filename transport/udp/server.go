// File: transport/udp/server.go
// Purpose: Implements the UDP transport adapter for KPro packet sessions.

package udp

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
	transportapi "github.com/saramand9/kpro/transport"
	"github.com/saramand9/kpro/transport/internal/serveutil"
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
	// SendQueueSize is the per-peer outbound packet queue length.
	SendQueueSize int
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

	mu        sync.Mutex
	sessions  map[string]*packetSession
	closed    bool
	started   bool
	ready     chan struct{}
	readyOnce sync.Once
}

// New creates a UDP transport with normalized defaults.
func New(config Config) *Server {
	return &Server{
		config:    normalizeConfig(config),
		rawConfig: config,
		sessions:  make(map[string]*packetSession),
		clockBase: time.Now(),
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
			if ctx.Err() != nil || s.isClosed() {
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
		if msg.ID == core.MsgHeartbeat {
			_ = session.Send(core.Message{ID: core.MsgPong, Seq: msg.Seq})
			continue
		}
		err = transportapi.InvokeHandler(handler, session, msg)
		if !transportapi.ContinueAfterHandlerError(
			s.config.HandlerErrorPolicy,
			s.config.OnHandlerError,
			session,
			msg,
			err,
		) {
			_ = session.Close()
		}
	}
}

func (s *Server) claimStart() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
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
	if !s.closed && s.conn == nil {
		s.started = false
	}
	s.mu.Unlock()
}

func (s *Server) publishConn(conn *net.UDPConn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
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

// Shutdown closes the UDP socket and all virtual sessions.
func (s *Server) Shutdown(context.Context) error {
	s.mu.Lock()
	s.closed = true
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
	return closeErr
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
	if s.closed {
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
		sendQueue: make(chan []byte, s.config.SendQueueSize),
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

	sendQueue chan []byte
	done      chan struct{}
	closeOnce sync.Once

	closed     atomic.Bool
	lastActive atomic.Int64
}

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
	if s.server.udpConn() == nil {
		return core.ErrClosed
	}
	packet, err := s.server.config.Codec.EncodePacket(msg)
	if err != nil {
		return err
	}
	s.touch()

	select {
	case <-s.done:
		return core.ErrClosed
	default:
	}
	select {
	case s.sendQueue <- packet:
		return nil
	case <-s.done:
		return core.ErrClosed
	default:
		return core.ErrSendQueueFull
	}
}

func (s *packetSession) Close() error {
	s.server.mu.Lock()
	s.closeLocked()
	s.server.mu.Unlock()
	return nil
}

// closeLocked closes and removes the session while server.mu serializes it
// with packet activity updates and idle cleanup.
func (s *packetSession) closeLocked() {
	s.closeOnce.Do(func() {
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
		case packet := <-s.sendQueue:
			conn := s.server.udpConn()
			if conn == nil {
				_ = s.Close()
				return
			}
			if _, err := conn.WriteToUDP(packet, s.remote); err != nil {
				_ = s.Close()
				return
			}
		case <-s.done:
			return
		}
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
	if config.SessionIdleTimeout <= 0 {
		config.SessionIdleTimeout = DefaultSessionIdleTimeout
	}
	if config.CleanupInterval <= 0 {
		config.CleanupInterval = DefaultCleanupInterval
	}
	return config
}

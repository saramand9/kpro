// File: transport/udp/server.go
// Purpose: Implements the UDP transport adapter for KPro packet sessions.

package udp

import (
	"context"
	"errors"
	"hash/fnv"
	"net"
	"sync"
	"time"

	"github.com/saramand9/bignet/kpro/codec"
	"github.com/saramand9/bignet/kpro/core"
)

const (
	DefaultAddr               = ":8089"
	DefaultMaxPacketSize      = 2048
	DefaultMaxSessions        = 10000
	DefaultSendQueueSize      = 64
	DefaultSessionIdleTimeout = 90 * time.Second
	DefaultCleanupInterval    = 30 * time.Second
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
}

// Server maps UDP peers to virtual sessions and dispatches decoded packets.
type Server struct {
	config Config
	conn   *net.UDPConn

	mu       sync.Mutex
	sessions map[string]*packetSession
	closed   bool
}

// New creates a UDP transport with normalized defaults.
func New(config Config) *Server {
	return &Server{
		config:   normalizeConfig(config),
		sessions: make(map[string]*packetSession),
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

// ListenAndServe reads UDP packets until ctx is cancelled or the socket fails.
func (s *Server) ListenAndServe(ctx context.Context, handler core.Handler) error {
	addr, err := net.ResolveUDPAddr("udp", s.config.Addr)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.conn = conn
	s.closed = false
	s.mu.Unlock()

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	go s.cleanupLoop(ctx)

	buf := make([]byte, s.config.MaxPacketSize)
	for {
		n, remote, err := conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return err
			}
		}
		msg, err := s.config.Codec.DecodePacket(buf[:n])
		if err != nil {
			continue
		}
		session, ok := s.getOrCreateSession(remote)
		if !ok {
			continue
		}
		session.touch()
		if msg.ID == core.MsgHeartbeat {
			_ = session.Send(core.Message{ID: core.MsgPong, Seq: msg.Seq})
			continue
		}
		_ = handler(core.AcquireContext(session, msg))
	}
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

func (s *Server) getOrCreateSession(addr *net.UDPAddr) (*packetSession, bool) {
	key := addr.String()
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, false
	}
	if session := s.sessions[key]; session != nil && !session.isClosed() {
		session.touchAt(now)
		return session, true
	}
	if s.config.MaxSessions > 0 && len(s.sessions) >= s.config.MaxSessions {
		return nil, false
	}

	remote := *addr
	session := &packetSession{
		id:         sessionID(addr),
		key:        key,
		server:     s,
		remote:     &remote,
		sendQueue:  make(chan []byte, s.config.SendQueueSize),
		done:       make(chan struct{}),
		lastActive: now,
	}
	s.sessions[key] = session
	go session.writeLoop()
	return session, true
}

func (s *Server) removeSession(session *packetSession) {
	s.mu.Lock()
	if s.sessions[session.key] == session {
		delete(s.sessions, session.key)
	}
	s.mu.Unlock()
}

func (s *Server) udpConn() *net.UDPConn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn
}

func (s *Server) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(s.config.CleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			s.closeIdleSessions(now)
		case <-ctx.Done():
			return
		}
	}
}

func (s *Server) closeIdleSessions(now time.Time) {
	if s.config.SessionIdleTimeout <= 0 {
		return
	}
	s.mu.Lock()
	sessions := make([]*packetSession, 0, len(s.sessions))
	for _, session := range s.sessions {
		if session.idleSince(now) >= s.config.SessionIdleTimeout {
			sessions = append(sessions, session)
		}
	}
	s.mu.Unlock()

	for _, session := range sessions {
		_ = session.Close()
	}
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

	mu         sync.Mutex
	closed     bool
	lastActive time.Time
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
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		close(s.done)
		s.server.removeSession(s)
	})
	return nil
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
	s.touchAt(time.Now())
}

func (s *packetSession) touchAt(now time.Time) {
	s.mu.Lock()
	s.lastActive = now
	s.mu.Unlock()
}

func (s *packetSession) idleSince(now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0
	}
	return now.Sub(s.lastActive)
}

func (s *packetSession) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
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

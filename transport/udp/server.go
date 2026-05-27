package udp

import (
	"context"
	"hash/fnv"
	"net"
	"sync"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/protocol"
)

const (
	DefaultAddr          = ":8089"
	DefaultMaxPacketSize = 2048
)

type Config struct {
	Addr          string
	Protocol      protocol.PacketProtocol
	MaxPacketSize int
}

type Server struct {
	config Config
	conn   *net.UDPConn

	mu       sync.Mutex
	sessions map[string]uint64
}

func New(config Config) *Server {
	return &Server{
		config:   normalizeConfig(config),
		sessions: make(map[string]uint64),
	}
}

func NewServer(config Config) *Server {
	return New(config)
}

func (s *Server) Name() string {
	return "udp"
}

func (s *Server) ListenAndServe(ctx context.Context, handler core.Handler) error {
	addr, err := net.ResolveUDPAddr("udp", s.config.Addr)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}
	s.conn = conn

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

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
		packet := make([]byte, n)
		copy(packet, buf[:n])
		msg, err := s.config.Protocol.DecodePacket(packet)
		if err != nil {
			continue
		}
		session := &packetSession{
			id:     s.sessionID(remote),
			server: s,
			remote: remote,
		}
		if msg.ID == core.MsgHeartbeat {
			_ = session.Send(core.Message{ID: core.MsgPong, Seq: msg.Seq})
			continue
		}
		_ = handler(core.AcquireContext(session, msg))
	}
}

func (s *Server) Shutdown(context.Context) error {
	if s.conn != nil {
		return s.conn.Close()
	}
	return nil
}

func (s *Server) sessionID(addr *net.UDPAddr) uint64 {
	key := addr.String()
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.sessions[key]; ok {
		return id
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	id := h.Sum64()
	s.sessions[key] = id
	return id
}

type packetSession struct {
	id     uint64
	server *Server
	remote *net.UDPAddr
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

func (s *packetSession) Send(msg core.Message) error {
	if s.server.conn == nil {
		return core.ErrClosed
	}
	packet, err := s.server.config.Protocol.EncodePacket(msg)
	if err != nil {
		return err
	}
	_, err = s.server.conn.WriteToUDP(packet, s.remote)
	return err
}

func (s *packetSession) Close() error {
	return nil
}

func normalizeConfig(config Config) Config {
	if config.Addr == "" {
		config.Addr = DefaultAddr
	}
	if config.Protocol == nil {
		config.Protocol = protocol.KProV1()
	}
	if config.MaxPacketSize <= 0 {
		config.MaxPacketSize = DefaultMaxPacketSize
	}
	return config
}

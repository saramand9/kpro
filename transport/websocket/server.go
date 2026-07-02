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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/protocol"
)

const (
	DefaultAddr          = ":8091"
	DefaultPath          = "/ws"
	DefaultMaxMessage    = 64 * 1024
	DefaultSendQueueSize = 64
	DefaultReadTimeout   = 90 * time.Second
	DefaultWriteTimeout  = 5 * time.Second

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
)

// Config controls WebSocket listener, path, protocol, queue, and timeout settings.
type Config struct {
	// Addr is the HTTP listen address used for WebSocket upgrades.
	Addr string
	// Path is the WebSocket upgrade route path.
	Path string
	// Protocol decodes and encodes binary WebSocket payloads. Nil uses protocol.KProV1().
	Protocol protocol.PacketProtocol
	// MaxMessage limits reassembled WebSocket message size.
	MaxMessage int
	// SendQueueSize is the per-session outbound frame queue length.
	SendQueueSize int
	// ReadTimeout is applied before each WebSocket frame read.
	ReadTimeout time.Duration
	// WriteTimeout is applied before each WebSocket frame write.
	WriteTimeout time.Duration
}

// Server upgrades HTTP requests to WebSocket sessions and dispatches KPro messages.
type Server struct {
	config Config
	server *http.Server

	nextID   uint64
	sessions sync.Map
}

// New creates a WebSocket transport with normalized defaults.
func New(config Config) *Server {
	return &Server{config: normalizeConfig(config)}
}

// NewServer is an alias for New.
func NewServer(config Config) *Server {
	return New(config)
}

// Name returns the transport name.
func (s *Server) Name() string {
	return "websocket"
}

// ListenAndServe starts the WebSocket HTTP server until ctx is cancelled or serving fails.
func (s *Server) ListenAndServe(ctx context.Context, handler core.Handler) error {
	mux := http.NewServeMux()
	mux.HandleFunc(s.config.Path, func(w http.ResponseWriter, r *http.Request) {
		s.serveWS(w, r, handler)
	})
	s.server = &http.Server{Addr: s.config.Addr, Handler: mux}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Shutdown(shutdownCtx)
	}()
	err := s.server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// Shutdown closes active WebSocket sessions and stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.sessions.Range(func(_, value interface{}) bool {
		if session, ok := value.(*session); ok {
			_ = session.Close()
		}
		return true
	})
	if s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

func (s *Server) serveWS(w http.ResponseWriter, r *http.Request, handler core.Handler) {
	if !isUpgradeRequest(r) {
		http.Error(w, ErrBadHandshake.Error(), http.StatusBadRequest)
		return
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" || r.Header.Get("Sec-WebSocket-Version") != "13" {
		http.Error(w, ErrBadHandshake.Error(), http.StatusBadRequest)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
		return
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return
	}
	accept := computeAcceptKey(key)
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
		sendQueue: make(chan wsFrame, s.config.SendQueueSize),
		done:      make(chan struct{}),
	}
	s.sessions.Store(id, session)
	session.start(handler)
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
	sendQueue chan wsFrame
	done      chan struct{}
	closeOnce sync.Once
}

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
	packet, err := s.server.config.Protocol.EncodePacket(msg)
	if err != nil {
		return err
	}
	return s.enqueue(wsFrame{opcode: opBinary, payload: packet})
}

func (s *session) Close() error {
	s.closeOnce.Do(func() {
		close(s.done)
		_ = s.raw.Close()
		s.server.sessions.Delete(s.id)
	})
	return nil
}

func (s *session) start(handler core.Handler) {
	go s.readLoop(handler)
	go s.writeLoop()
}

func (s *session) enqueue(frame wsFrame) error {
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

func (s *session) readLoop(handler core.Handler) {
	defer s.Close()
	var fragmentOpcode byte
	var fragmentPayload []byte

	for {
		if s.server.config.ReadTimeout > 0 {
			if err := s.raw.SetReadDeadline(time.Now().Add(s.server.config.ReadTimeout)); err != nil {
				return
			}
		}
		frame, err := readFrame(s.reader, s.server.config.MaxMessage)
		if err != nil {
			return
		}
		switch frame.opcode {
		case opClose:
			_ = s.enqueue(wsFrame{opcode: opClose})
			return
		case opPing:
			_ = s.enqueue(wsFrame{opcode: opPong, payload: frame.payload})
			continue
		case opPong:
			continue
		case opText, opBinary:
			if fragmentOpcode != 0 {
				return
			}
			if !frame.fin {
				fragmentOpcode = frame.opcode
				fragmentPayload = append(fragmentPayload[:0], frame.payload...)
				continue
			}
			if err := s.handleMessage(handler, frame.payload); err != nil {
				return
			}
		case opContinuation:
			if fragmentOpcode == 0 {
				return
			}
			payload, err := appendFragment(fragmentPayload, frame.payload, s.server.config.MaxMessage)
			if err != nil {
				return
			}
			fragmentPayload = payload
			if !frame.fin {
				continue
			}
			completePayload := fragmentPayload
			fragmentOpcode = 0
			fragmentPayload = nil
			if err := s.handleMessage(handler, completePayload); err != nil {
				return
			}
		default:
			return
		}
	}
}

func (s *session) handleMessage(handler core.Handler, payload []byte) error {
	msg, err := s.server.config.Protocol.DecodePacket(payload)
	if err != nil {
		return err
	}
	if msg.ID == core.MsgHeartbeat {
		_ = s.Send(core.Message{ID: core.MsgPong, Seq: msg.Seq})
		return nil
	}
	_ = handler(core.AcquireContext(s, msg))
	return nil
}

func (s *session) writeLoop() {
	for {
		select {
		case frame := <-s.sendQueue:
			if s.server.config.WriteTimeout > 0 {
				if err := s.raw.SetWriteDeadline(time.Now().Add(s.server.config.WriteTimeout)); err != nil {
					_ = s.Close()
					return
				}
			}
			if err := writeFrame(s.raw, frame.opcode, frame.payload); err != nil {
				_ = s.Close()
				return
			}
			if frame.opcode == opClose {
				_ = s.Close()
				return
			}
		case <-s.done:
			return
		}
	}
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
	masked := header[1]&0x80 != 0
	length := uint64(header[1] & 0x7f)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return wsFrame{}, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return wsFrame{}, err
		}
		length = binary.BigEndian.Uint64(ext[:])
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
	if _, err := w.Write(header); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := w.Write(payload)
	return err
}

func isUpgradeRequest(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
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
	if config.Protocol == nil {
		config.Protocol = protocol.KProV1()
	}
	if config.MaxMessage <= 0 {
		config.MaxMessage = DefaultMaxMessage
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
	return config
}

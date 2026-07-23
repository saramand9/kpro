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

	"github.com/saramand9/bignet/kpro/codec"
	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/transport/internal/serveutil"
	"github.com/saramand9/bignet/kpro/transport/internal/streamutil"
)

const (
	DefaultAddr             = ":8091"
	DefaultPath             = "/ws"
	DefaultMaxMessage       = 64 * 1024
	DefaultMaxConnections   = 10000
	DefaultSendQueueSize    = 64
	DefaultReadTimeout      = 90 * time.Second
	DefaultWriteTimeout     = 5 * time.Second
	DefaultHandshakeTimeout = 5 * time.Second

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
)

// Config controls WebSocket listener, path, codec, queue, and timeout settings.
type Config struct {
	// Addr is the HTTP listen address used for WebSocket upgrades.
	Addr string
	// Path is the WebSocket upgrade route path.
	Path string
	// Codec decodes and encodes binary WebSocket payloads. Nil uses codec.KProV1().
	Codec codec.PacketCodec
	// MaxMessage limits reassembled WebSocket message size.
	MaxMessage int
	// MaxConnections caps active upgraded WebSocket sessions.
	MaxConnections int
	// SendQueueSize is the per-session outbound frame queue length.
	SendQueueSize int
	// ReadTimeout is applied before each WebSocket frame read.
	ReadTimeout time.Duration
	// WriteTimeout is applied before each WebSocket frame write.
	WriteTimeout time.Duration
	// HandshakeTimeout bounds reading the HTTP upgrade headers.
	HandshakeTimeout time.Duration
}

// Server upgrades HTTP requests to WebSocket sessions and dispatches KPro messages.
type Server struct {
	config    Config
	rawConfig Config
	server    *http.Server

	lifecycleMu sync.Mutex
	accepting   bool
	started     bool
	upgrades    sync.WaitGroup
	upgrading   sync.Map

	nextID   uint64
	active   int64
	sessions sync.Map
}

// New creates a WebSocket transport with normalized defaults.
func New(config Config) *Server {
	return &Server{config: normalizeConfig(config), rawConfig: config, accepting: true}
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
	if err := s.Validate(); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	server := s.buildHTTPServer(handler)
	s.lifecycleMu.Lock()
	if !s.accepting {
		s.lifecycleMu.Unlock()
		return ErrNotAccepting
	}
	if s.started {
		s.lifecycleMu.Unlock()
		return core.ErrAlreadyStarted
	}
	s.started = true
	s.server = server
	s.lifecycleMu.Unlock()
	serveDone := make(chan struct{})
	watcherDone := serveutil.ShutdownOnCancel(ctx, serveDone, func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Shutdown(shutdownCtx)
	})
	defer serveutil.FinishContextWatcher(serveDone, watcherDone)
	err := server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
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
	}
}

// Shutdown closes active WebSocket sessions and stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.lifecycleMu.Lock()
	s.accepting = false
	server := s.server
	s.lifecycleMu.Unlock()

	s.upgrading.Range(func(key, _ interface{}) bool {
		if conn, ok := key.(net.Conn); ok {
			_ = conn.Close()
		}
		return true
	})
	s.sessions.Range(func(_, value interface{}) bool {
		if session, ok := value.(*session); ok {
			_ = session.Close()
		}
		return true
	})
	var serverErr error
	if server != nil {
		serverErr = server.Shutdown(ctx)
	}
	upgradeErr := waitGroupContext(ctx, &s.upgrades)
	return errors.Join(serverErr, upgradeErr)
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
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" || r.Header.Get("Sec-WebSocket-Version") != "13" {
		http.Error(w, ErrBadHandshake.Error(), http.StatusBadRequest)
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
	if s.config.HandshakeTimeout > 0 {
		if err := conn.SetWriteDeadline(time.Now().Add(s.config.HandshakeTimeout)); err != nil {
			_ = conn.Close()
			return
		}
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
	select {
	case <-s.done:
		return core.ErrClosed
	default:
	}
	packet, err := s.server.config.Codec.EncodePacket(msg)
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
		s.server.releaseConnectionSlot()
	})
	return nil
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
	msg, err := s.server.config.Codec.DecodePacket(payload)
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
	if !masked {
		return wsFrame{}, ErrBadFrame
	}
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
	if err := streamutil.WriteFull(w, header); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	return streamutil.WriteFull(w, payload)
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

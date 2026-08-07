package websocket

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/transport/transporttest"
)

const websocketContractMaxMessage = 64 * 1024

func TestMessageTransportContract(t *testing.T) {
	transporttest.RunMessageServerContract(t, transporttest.MessageContract{
		New:     newWebSocketContractHarness,
		Timeout: 5 * time.Second,
		Require: transporttest.MessageRequirements{
			Descriptor:       true,
			Readiness:        true,
			SessionLifecycle: true,
			OutboundState:    true,
			WritableWait:     true,
			WriteReceipt:     true,
			SessionDrain:     true,
			Quiesce:          true,
			TransportDrain:   true,
			OutboundSnapshot: true,
		},
	})
}

func newWebSocketContractHarness(t *testing.T) transporttest.MessageHarness {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve WebSocket contract address: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release WebSocket contract address: %v", err)
	}
	packetCodec := codec.KProV1()
	server := New(Config{
		Addr:            addr,
		Path:            "/ws",
		Codec:           packetCodec,
		MaxMessage:      websocketContractMaxMessage,
		SendQueueSize:   4,
		MaxPendingBytes: 256 * 1024,
		ReadTimeout:     time.Second,
		WriteTimeout:    time.Second,
	})
	return transporttest.MessageHarness{
		Server: server,
		Connect: func(ctx context.Context) (transporttest.MessagePeer, error) {
			connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
			if err != nil {
				return nil, err
			}
			peer, err := upgradeWebSocketContractPeer(ctx, connection, addr, packetCodec)
			if err != nil {
				_ = connection.Close()
				return nil, err
			}
			return peer, nil
		},
	}
}

type websocketContractPeer struct {
	connection net.Conn
	reader     *bufio.Reader
	codec      codec.PacketCodec
}

func upgradeWebSocketContractPeer(ctx context.Context, connection net.Conn, addr string, packetCodec codec.PacketCodec) (*websocketContractPeer, error) {
	stop := context.AfterFunc(ctx, func() { _ = connection.SetDeadline(time.Now()) })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}
	request := "GET /ws HTTP/1.1\r\n" +
		"Host: " + addr + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	if _, err := io.WriteString(connection, request); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	reader := bufio.NewReader(connection)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		return nil, fmt.Errorf("websocket upgrade status = %d", response.StatusCode)
	}
	if !headerHasToken(response.Header, "Upgrade", "websocket") || !headerHasToken(response.Header, "Connection", "upgrade") {
		return nil, fmt.Errorf("websocket upgrade response is missing required headers")
	}
	if got, want := response.Header.Get("Sec-WebSocket-Accept"), computeAcceptKey("dGhlIHNhbXBsZSBub25jZQ=="); got != want {
		return nil, fmt.Errorf("Sec-WebSocket-Accept = %q, want %q", got, want)
	}
	if err := connection.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	return &websocketContractPeer{connection: connection, reader: reader, codec: packetCodec}, nil
}

func (p *websocketContractPeer) Send(ctx context.Context, message core.Message) error {
	packet, err := p.codec.EncodePacket(message)
	if err != nil {
		return err
	}
	wire := maskedClientFrame(0x80|opBinary, packet)
	stop := context.AfterFunc(ctx, func() { _ = p.connection.SetWriteDeadline(time.Now()) })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		if err := p.connection.SetWriteDeadline(deadline); err != nil {
			return err
		}
		defer func() { _ = p.connection.SetWriteDeadline(time.Time{}) }()
	}
	for len(wire) > 0 {
		written, err := p.connection.Write(wire)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if written == 0 {
			return io.ErrNoProgress
		}
		wire = wire[written:]
	}
	return nil
}

func (p *websocketContractPeer) Receive(ctx context.Context) (core.Message, error) {
	stop := context.AfterFunc(ctx, func() { _ = p.connection.SetReadDeadline(time.Now()) })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		if err := p.connection.SetReadDeadline(deadline); err != nil {
			return core.Message{}, err
		}
		defer func() { _ = p.connection.SetReadDeadline(time.Time{}) }()
	}
	opcode, payload, err := readWebSocketContractFrame(p.reader, websocketContractMaxMessage)
	if err != nil {
		if ctx.Err() != nil {
			return core.Message{}, ctx.Err()
		}
		return core.Message{}, err
	}
	if opcode != opBinary {
		return core.Message{}, fmt.Errorf("websocket opcode = %#x, want binary", opcode)
	}
	return p.codec.DecodePacket(payload)
}

func (p *websocketContractPeer) Close() error {
	return p.connection.Close()
}

func readWebSocketContractFrame(reader io.Reader, maxPayload int) (byte, []byte, error) {
	var header [2]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return 0, nil, err
	}
	if header[0]&0x80 == 0 || header[0]&0x70 != 0 || header[1]&0x80 != 0 {
		return 0, nil, ErrBadFrame
	}
	opcode := header[0] & 0x0f
	length := uint64(header[1] & 0x7f)
	switch length {
	case 126:
		var extended [2]byte
		if _, err := io.ReadFull(reader, extended[:]); err != nil {
			return 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(extended[:]))
		if length < 126 {
			return 0, nil, ErrBadFrame
		}
	case 127:
		var extended [8]byte
		if _, err := io.ReadFull(reader, extended[:]); err != nil {
			return 0, nil, err
		}
		if extended[0]&0x80 != 0 {
			return 0, nil, ErrBadFrame
		}
		length = binary.BigEndian.Uint64(extended[:])
		if length <= 0xffff {
			return 0, nil, ErrBadFrame
		}
	}
	if length > uint64(maxPayload) {
		return 0, nil, ErrFrameTooLarge
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return 0, nil, err
	}
	return opcode, payload, nil
}

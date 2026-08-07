package udp_test

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/transport/transporttest"
	"github.com/saramand9/kpro/transport/udp"
)

const udpContractMaxPacketSize = 64 * 1024

func TestMessageTransportContract(t *testing.T) {
	transporttest.RunMessageServerContract(t, transporttest.MessageContract{
		New:     newUDPContractHarness,
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

func newUDPContractHarness(t *testing.T) transporttest.MessageHarness {
	t.Helper()
	reserved, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("reserve UDP contract address: %v", err)
	}
	addr := reserved.LocalAddr().String()
	if err := reserved.Close(); err != nil {
		t.Fatalf("release UDP contract address: %v", err)
	}
	packetCodec := codec.KProV1()
	server := udp.New(udp.Config{
		Addr:               addr,
		Codec:              packetCodec,
		MaxPacketSize:      udpContractMaxPacketSize,
		SendQueueSize:      4,
		MaxPendingBytes:    256 * 1024,
		SessionIdleTimeout: time.Minute,
		CleanupInterval:    time.Minute,
	})
	return transporttest.MessageHarness{
		Server: server,
		Connect: func(ctx context.Context) (transporttest.MessagePeer, error) {
			remote, err := net.ResolveUDPAddr("udp", addr)
			if err != nil {
				return nil, err
			}
			connection, err := (&net.Dialer{}).DialContext(ctx, "udp", remote.String())
			if err != nil {
				return nil, err
			}
			return &udpContractPeer{connection: connection, codec: packetCodec}, nil
		},
	}
}

type udpContractPeer struct {
	connection net.Conn
	codec      codec.PacketCodec
}

func (p *udpContractPeer) Send(ctx context.Context, message core.Message) error {
	packet, err := p.codec.EncodePacket(message)
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = p.connection.SetWriteDeadline(time.Now()) })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		if err := p.connection.SetWriteDeadline(deadline); err != nil {
			return err
		}
		defer func() { _ = p.connection.SetWriteDeadline(time.Time{}) }()
	}
	written, err := p.connection.Write(packet)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if written != len(packet) {
		return io.ErrShortWrite
	}
	return nil
}

func (p *udpContractPeer) Receive(ctx context.Context) (core.Message, error) {
	stop := context.AfterFunc(ctx, func() { _ = p.connection.SetReadDeadline(time.Now()) })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		if err := p.connection.SetReadDeadline(deadline); err != nil {
			return core.Message{}, err
		}
		defer func() { _ = p.connection.SetReadDeadline(time.Time{}) }()
	}
	buffer := make([]byte, udpContractMaxPacketSize)
	n, err := p.connection.Read(buffer)
	if err != nil {
		if ctx.Err() != nil {
			return core.Message{}, ctx.Err()
		}
		return core.Message{}, err
	}
	return p.codec.DecodePacket(buffer[:n])
}

func (p *udpContractPeer) Close() error {
	return p.connection.Close()
}

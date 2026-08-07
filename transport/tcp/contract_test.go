package tcp_test

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/transport/tcp"
	"github.com/saramand9/kpro/transport/transporttest"
)

const tcpContractMaxFrameSize = 64 * 1024

func TestMessageTransportContract(t *testing.T) {
	transporttest.RunMessageServerContract(t, transporttest.MessageContract{
		New: newTCPContractHarness,
		Probe: core.Message{
			Flags: 1,
			ID:    0x7fff1001,
			Seq:   41,
			Data:  []byte("tcp-contract-probe"),
		},
		Reply: core.Message{
			Flags: 2,
			ID:    0x7fff1002,
			Seq:   42,
			Data:  []byte("tcp-contract-reply"),
		},
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

func newTCPContractHarness(t *testing.T) transporttest.MessageHarness {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve TCP contract address: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release TCP contract address: %v", err)
	}
	streamCodec := codec.KProV1()
	server := tcp.New(tcp.Config{
		Addr:            addr,
		Codec:           streamCodec,
		MaxFrameSize:    tcpContractMaxFrameSize,
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
			return &tcpContractPeer{connection: connection, codec: streamCodec}, nil
		},
	}
}

type tcpContractPeer struct {
	connection net.Conn
	codec      codec.StreamCodec
}

func (p *tcpContractPeer) Send(ctx context.Context, message core.Message) error {
	encoded, err := p.codec.Encode(message)
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() {
		_ = p.connection.SetWriteDeadline(time.Now())
	})
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		if err := p.connection.SetWriteDeadline(deadline); err != nil {
			return err
		}
		defer func() { _ = p.connection.SetWriteDeadline(time.Time{}) }()
	}
	for len(encoded) > 0 {
		written, err := p.connection.Write(encoded)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if written == 0 {
			return io.ErrNoProgress
		}
		encoded = encoded[written:]
	}
	return nil
}

func (p *tcpContractPeer) Receive(ctx context.Context) (core.Message, error) {
	stop := context.AfterFunc(ctx, func() {
		_ = p.connection.SetReadDeadline(time.Now())
	})
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		if err := p.connection.SetReadDeadline(deadline); err != nil {
			return core.Message{}, err
		}
		defer func() { _ = p.connection.SetReadDeadline(time.Time{}) }()
	}
	result, err := p.codec.Read(p.connection, tcpContractMaxFrameSize)
	if err != nil {
		if ctx.Err() != nil {
			return core.Message{}, ctx.Err()
		}
		return core.Message{}, err
	}
	result.Message.Data = append([]byte(nil), result.Message.Data...)
	return result.Message, nil
}

func (p *tcpContractPeer) Close() error {
	return p.connection.Close()
}

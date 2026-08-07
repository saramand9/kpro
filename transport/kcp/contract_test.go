package kcp_test

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/transport/kcp"
	"github.com/saramand9/kpro/transport/transporttest"
	kcpgo "github.com/xtaci/kcp-go"
)

const kcpContractMaxFrameSize = 64 * 1024

func TestMessageTransportContract(t *testing.T) {
	transporttest.RunMessageServerContract(t, transporttest.MessageContract{
		New:     newKCPContractHarness,
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

func newKCPContractHarness(t *testing.T) transporttest.MessageHarness {
	t.Helper()
	reserved, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("reserve KCP contract address: %v", err)
	}
	addr := reserved.LocalAddr().String()
	if err := reserved.Close(); err != nil {
		t.Fatalf("release KCP contract address: %v", err)
	}
	streamCodec := codec.KProV1()
	server := kcp.New(kcp.Config{
		Addr:            addr,
		Codec:           streamCodec,
		MaxFrameSize:    kcpContractMaxFrameSize,
		SendQueueSize:   4,
		MaxPendingBytes: 256 * 1024,
		ReadTimeout:     time.Second,
		WriteTimeout:    time.Second,
	})
	return transporttest.MessageHarness{
		Server: server,
		Connect: func(context.Context) (transporttest.MessagePeer, error) {
			connection, err := kcpgo.DialWithOptions(addr, nil, 0, 0)
			if err != nil {
				return nil, err
			}
			connection.SetStreamMode(true)
			connection.SetNoDelay(kcp.DefaultNoDelay, kcp.DefaultInterval, kcp.DefaultResend, kcp.DefaultNoCongestion)
			connection.SetWindowSize(kcp.DefaultSendWindow, kcp.DefaultReceiveWindow)
			_ = connection.SetMtu(kcp.DefaultMTU)
			return &kcpContractPeer{connection: connection, codec: streamCodec}, nil
		},
	}
}

type kcpContractPeer struct {
	connection *kcpgo.UDPSession
	codec      codec.StreamCodec
}

func (p *kcpContractPeer) Send(ctx context.Context, message core.Message) error {
	encoded, err := p.codec.Encode(message)
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

func (p *kcpContractPeer) Receive(ctx context.Context) (core.Message, error) {
	stop := context.AfterFunc(ctx, func() { _ = p.connection.SetReadDeadline(time.Now()) })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		if err := p.connection.SetReadDeadline(deadline); err != nil {
			return core.Message{}, err
		}
		defer func() { _ = p.connection.SetReadDeadline(time.Time{}) }()
	}
	result, err := p.codec.Read(p.connection, kcpContractMaxFrameSize)
	if err != nil {
		if ctx.Err() != nil {
			return core.Message{}, ctx.Err()
		}
		return core.Message{}, err
	}
	result.Message.Data = append([]byte(nil), result.Message.Data...)
	return result.Message, nil
}

func (p *kcpContractPeer) Close() error {
	return p.connection.Close()
}

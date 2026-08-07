package quic_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"testing"
	"time"

	quicgo "github.com/quic-go/quic-go"
	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
	kproquic "github.com/saramand9/kpro/transport/quic"
	"github.com/saramand9/kpro/transport/transporttest"
)

const quicContractMaxFrameSize = 64 * 1024

func TestMessageTransportContract(t *testing.T) {
	transporttest.RunMessageServerContract(t, transporttest.MessageContract{
		New:     newQUICContractHarness,
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

func newQUICContractHarness(t *testing.T) transporttest.MessageHarness {
	t.Helper()
	reserved, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("reserve QUIC contract address: %v", err)
	}
	addr := reserved.LocalAddr().String()
	if err := reserved.Close(); err != nil {
		t.Fatalf("release QUIC contract address: %v", err)
	}
	certificate := quicContractCertificate(t)
	streamCodec := codec.KProV1()
	server := kproquic.New(kproquic.Config{
		Addr:            addr,
		Codec:           streamCodec,
		MaxFrameSize:    quicContractMaxFrameSize,
		SendQueueSize:   4,
		MaxPendingBytes: 256 * 1024,
		ReadTimeout:     time.Second,
		WriteTimeout:    time.Second,
		TLSConfig: &tls.Config{
			MinVersion:   tls.VersionTLS13,
			NextProtos:   []string{kproquic.DefaultALPN},
			Certificates: []tls.Certificate{certificate},
		},
	})
	return transporttest.MessageHarness{
		Server: server,
		Connect: func(ctx context.Context) (transporttest.MessagePeer, error) {
			connection, err := quicgo.DialAddr(ctx, addr, &tls.Config{
				MinVersion:         tls.VersionTLS13,
				InsecureSkipVerify: true, // test-only ephemeral certificate
				NextProtos:         []string{kproquic.DefaultALPN},
			}, &quicgo.Config{
				HandshakeIdleTimeout: time.Second,
				MaxIdleTimeout:       10 * time.Second,
			})
			if err != nil {
				return nil, err
			}
			stream, err := connection.OpenStreamSync(ctx)
			if err != nil {
				_ = connection.CloseWithError(0, "contract stream open failed")
				return nil, err
			}
			return &quicContractPeer{connection: connection, stream: stream, codec: streamCodec}, nil
		},
	}
}

type quicContractPeer struct {
	connection *quicgo.Conn
	stream     *quicgo.Stream
	codec      codec.StreamCodec
}

func (p *quicContractPeer) Send(ctx context.Context, message core.Message) error {
	encoded, err := p.codec.Encode(message)
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = p.stream.SetWriteDeadline(time.Now()) })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		if err := p.stream.SetWriteDeadline(deadline); err != nil {
			return err
		}
		defer func() { _ = p.stream.SetWriteDeadline(time.Time{}) }()
	}
	for len(encoded) > 0 {
		written, err := p.stream.Write(encoded)
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

func (p *quicContractPeer) Receive(ctx context.Context) (core.Message, error) {
	stop := context.AfterFunc(ctx, func() { _ = p.stream.SetReadDeadline(time.Now()) })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		if err := p.stream.SetReadDeadline(deadline); err != nil {
			return core.Message{}, err
		}
		defer func() { _ = p.stream.SetReadDeadline(time.Time{}) }()
	}
	result, err := p.codec.Read(p.stream, quicContractMaxFrameSize)
	if err != nil {
		if ctx.Err() != nil {
			return core.Message{}, ctx.Err()
		}
		return core.Message{}, err
	}
	result.Message.Data = append([]byte(nil), result.Message.Data...)
	return result.Message, nil
}

func (p *quicContractPeer) Close() error {
	return errors.Join(p.stream.Close(), p.connection.CloseWithError(0, "contract complete"))
}

func quicContractCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate QUIC contract private key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate QUIC contract certificate serial: %v", err)
	}
	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("create QUIC contract certificate: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: privateKey}
}

package transport_test

import (
	"context"
	"crypto/tls"
	"io"
	"strings"
	"testing"
	"time"

	quicgo "github.com/quic-go/quic-go"
	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/transport/fasthttpx"
	"github.com/saramand9/kpro/transport/grpcx"
	"github.com/saramand9/kpro/transport/http3"
	"github.com/saramand9/kpro/transport/httpx"
	"github.com/saramand9/kpro/transport/kcp"
	"github.com/saramand9/kpro/transport/proxyproto"
	quictransport "github.com/saramand9/kpro/transport/quic"
	"github.com/saramand9/kpro/transport/sse"
	"github.com/saramand9/kpro/transport/tcp"
	"github.com/saramand9/kpro/transport/udp"
	"github.com/saramand9/kpro/transport/websocket"
)

func TestBuiltInValidationReportsActionableTLSAndAddressErrors(t *testing.T) {
	if err := tcp.New(tcp.Config{}).Validate(); err != nil {
		t.Fatalf("default TCP validation: %v", err)
	}
	if err := fasthttpx.New(fasthttpx.Config{}).Validate(); err != nil {
		t.Fatalf("default fasthttp validation: %v", err)
	}
	if err := httpx.New(httpx.Config{TLS: true}).Validate(); err == nil {
		t.Fatal("HTTPS without certificate was accepted")
	}
	if err := http3.New(http3.Config{}).Validate(); err == nil {
		t.Fatal("HTTP/3 without TLS configuration was accepted")
	}
	if err := grpcx.NewServer("").Validate(); err == nil {
		t.Fatal("gRPC without a listen address was accepted")
	}
}

func TestBuiltInValidationRejectsInvalidAddressesAndNegativeLimits(t *testing.T) {
	var streamCodec *typedNilStreamCodec
	var packetCodec *typedNilPacketCodec
	var logger *typedNilLogger
	tests := []struct {
		name     string
		validate func() error
		want     string
	}{
		{name: "tcp address", validate: func() error { return tcp.New(tcp.Config{Addr: "invalid"}).Validate() }, want: "TCP listen address"},
		{name: "tcp limit", validate: func() error { return tcp.New(tcp.Config{MaxConnections: -1}).Validate() }, want: "MaxConnections"},
		{name: "tcp typed nil codec", validate: func() error { return tcp.New(tcp.Config{Codec: streamCodec}).Validate() }, want: "Codec cannot be typed nil"},
		{name: "kcp tuning", validate: func() error { return kcp.New(kcp.Config{MTU: -1}).Validate() }, want: "MTU"},
		{name: "kcp mtu below minimum", validate: func() error { return kcp.New(kcp.Config{MTU: 49}).Validate() }, want: "between 50 and 1500"},
		{name: "kcp mtu above maximum", validate: func() error { return kcp.New(kcp.Config{MTU: 1501}).Validate() }, want: "between 50 and 1500"},
		{name: "kcp incomplete FEC", validate: func() error { return kcp.New(kcp.Config{DataShards: 1}).Validate() }, want: "both be zero or both be positive"},
		{name: "kcp excessive FEC", validate: func() error { return kcp.New(kcp.Config{DataShards: 200, ParityShards: 100}).Validate() }, want: "cannot exceed 256"},
		{name: "kcp overflowing FEC", validate: func() error { return kcp.New(kcp.Config{DataShards: int(^uint(0) >> 1), ParityShards: 1}).Validate() }, want: "cannot exceed 256"},
		{name: "udp limit", validate: func() error { return udp.New(udp.Config{MaxPacketSize: -1}).Validate() }, want: "MaxPacketSize"},
		{name: "udp typed nil codec", validate: func() error { return udp.New(udp.Config{Codec: packetCodec}).Validate() }, want: "Codec cannot be typed nil"},
		{name: "websocket timeout", validate: func() error { return websocket.New(websocket.Config{ReadTimeout: -time.Second}).Validate() }, want: "ReadTimeout"},
		{name: "websocket handshake timeout", validate: func() error { return websocket.New(websocket.Config{HandshakeTimeout: -time.Second}).Validate() }, want: "HandshakeTimeout"},
		{name: "websocket connection limit", validate: func() error { return websocket.New(websocket.Config{MaxConnections: -1}).Validate() }, want: "MaxConnections"},
		{name: "websocket path", validate: func() error { return websocket.New(websocket.Config{Path: "/{"}).Validate() }, want: "unsupported path characters"},
		{name: "http timeout", validate: func() error { return httpx.New(httpx.Config{ShutdownTimeout: -time.Second}).Validate() }, want: "ShutdownTimeout"},
		{name: "http typed nil logger", validate: func() error { return httpx.New(httpx.Config{Logger: logger}).Validate() }, want: "Logger cannot be typed nil"},
		{name: "fasthttp timeout", validate: func() error { return fasthttpx.New(fasthttpx.Config{ShutdownTimeout: -time.Second}).Validate() }, want: "ShutdownTimeout"},
		{name: "fasthttp typed nil logger", validate: func() error { return fasthttpx.New(fasthttpx.Config{Logger: logger}).Validate() }, want: "Logger cannot be typed nil"},
		{name: "sse timeout", validate: func() error { return sse.New(sse.Config{ShutdownTimeout: -time.Second}).Validate() }, want: "ShutdownTimeout"},
		{name: "sse stream limit", validate: func() error { return sse.New(sse.Config{MaxStreams: -1}).Validate() }, want: "MaxStreams"},
		{name: "sse handshake timeout", validate: func() error { return sse.New(sse.Config{HandshakeTimeout: -time.Second}).Validate() }, want: "HandshakeTimeout"},
		{name: "sse path", validate: func() error { return sse.New(sse.Config{Path: "/{"}).Validate() }, want: "unsupported path characters"},
		{name: "grpc timeout", validate: func() error { return grpcx.NewServer(":0", grpcx.WithShutdownTimeout(-time.Second)).Validate() }, want: "ShutdownTimeout"},
		{
			name: "tcp proxy protocol trust",
			validate: func() error {
				return tcp.New(tcp.Config{ProxyProtocol: proxyproto.Config{Enabled: true}}).Validate()
			},
			want: "TrustedProxies",
		},
		{
			name: "websocket proxy protocol trust",
			validate: func() error {
				return websocket.New(websocket.Config{ProxyProtocol: proxyproto.Config{Enabled: true}}).Validate()
			},
			want: "TrustedProxies",
		},
		{
			name: "http proxy protocol trust",
			validate: func() error {
				return httpx.New(httpx.Config{ProxyProtocol: proxyproto.Config{Enabled: true}}).Validate()
			},
			want: "TrustedProxies",
		},
		{
			name: "fasthttp proxy protocol trust",
			validate: func() error {
				return fasthttpx.New(fasthttpx.Config{ProxyProtocol: proxyproto.Config{Enabled: true}}).Validate()
			},
			want: "TrustedProxies",
		},
		{
			name: "sse proxy protocol trust",
			validate: func() error {
				return sse.New(sse.Config{ProxyProtocol: proxyproto.Config{Enabled: true}}).Validate()
			},
			want: "TrustedProxies",
		},
		{
			name: "grpc proxy protocol trust",
			validate: func() error {
				return grpcx.NewServer(":0", grpcx.WithProxyProtocol(proxyproto.Config{Enabled: true})).Validate()
			},
			want: "TrustedProxies",
		},
		{name: "quic limit", validate: func() error { return quictransport.New(quictransport.Config{MaxStreams: -1}).Validate() }, want: "MaxStreams"},
		{
			name: "quic ALPN",
			validate: func() error {
				return quictransport.New(quictransport.Config{TLSConfig: dynamicTLSConfig("")}).Validate()
			},
			want: "must contain 1..255 bytes",
		},
		{
			name: "quic version",
			validate: func() error {
				return quictransport.New(quictransport.Config{
					TLSConfig:  dynamicTLSConfig(),
					QUICConfig: &quicgo.Config{Versions: []quicgo.Version{quicgo.Version(0xdeadbeef)}},
				}).Validate()
			},
			want: "QUICConfig.Versions",
		},
		{name: "http3 limit", validate: func() error { return http3.New(http3.Config{MaxHeaderBytes: -1}).Validate() }, want: "MaxHeaderBytes"},
		{
			name: "http3 version",
			validate: func() error {
				return http3.New(http3.Config{
					TLSConfig:  dynamicTLSConfig(),
					QUICConfig: &quicgo.Config{Versions: []quicgo.Version{quicgo.Version(0xdeadbeef)}},
				}).Validate()
			},
			want: "QUICConfig.Versions",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want text %q", err, test.want)
			}
		})
	}
}

func TestTLSValidationLoadsFilesAndChecksInMemoryCertificateSelection(t *testing.T) {
	tests := []struct {
		name     string
		validate func() error
		want     string
	}{
		{
			name: "https missing files",
			validate: func() error {
				return httpx.New(httpx.Config{TLS: true, CertFile: "missing-cert.pem", KeyFile: "missing-key.pem"}).Validate()
			},
			want: "load HTTPS certificate and key",
		},
		{
			name: "fasthttps missing files",
			validate: func() error {
				return fasthttpx.New(fasthttpx.Config{TLS: true, CertFile: "missing-cert.pem", KeyFile: "missing-key.pem"}).Validate()
			},
			want: "load fasthttp HTTPS certificate and key",
		},
		{
			name: "quic empty TLS config",
			validate: func() error {
				return quictransport.New(quictransport.Config{TLSConfig: &tls.Config{}}).Validate()
			},
			want: "cannot select a certificate",
		},
		{
			name: "http3 empty TLS config",
			validate: func() error {
				return http3.New(http3.Config{TLSConfig: &tls.Config{}}).Validate()
			},
			want: "cannot select a certificate",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want text %q", err, test.want)
			}
		})
	}
}

func TestQUICTransportsOwnMutableConfigSlicesAndPointers(t *testing.T) {
	nextProtos := []string{"kpro-test"}
	versions := []quicgo.Version{quicgo.Version1}
	tlsConfig := dynamicTLSConfig()
	messageServer := quictransport.New(quictransport.Config{
		TLSConfig:  tlsConfig,
		QUICConfig: &quicgo.Config{Versions: versions},
		NextProtos: nextProtos,
	})

	nextProtos[0] = ""
	versions[0] = quicgo.Version(0xdeadbeef)
	tlsConfig.GetCertificate = nil
	if err := messageServer.Validate(); err != nil {
		t.Fatalf("QUIC Validate after caller mutation: %v", err)
	}

	httpVersions := []quicgo.Version{quicgo.Version2}
	httpTLSConfig := dynamicTLSConfig()
	httpServer := http3.New(http3.Config{
		TLSConfig:  httpTLSConfig,
		QUICConfig: &quicgo.Config{Versions: httpVersions},
	})
	httpVersions[0] = quicgo.Version(0xdeadbeef)
	httpTLSConfig.GetCertificate = nil
	if err := httpServer.Validate(); err != nil {
		t.Fatalf("HTTP/3 Validate after caller mutation: %v", err)
	}
}

func dynamicTLSConfig(nextProtos ...string) *tls.Config {
	return &tls.Config{
		NextProtos: nextProtos,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return nil, nil
		},
	}
}

type typedNilStreamCodec struct{}

func (*typedNilStreamCodec) Name() string { return "typed-nil-stream" }

func (*typedNilStreamCodec) Read(io.Reader, uint32) (codec.ReadResult, error) {
	return codec.ReadResult{}, nil
}

func (*typedNilStreamCodec) Encode(core.Message) ([]byte, error) { return nil, nil }

type typedNilPacketCodec struct{}

func (*typedNilPacketCodec) Name() string { return "typed-nil-packet" }

func (*typedNilPacketCodec) DecodePacket([]byte) (core.Message, error) {
	return core.Message{}, nil
}

func (*typedNilPacketCodec) EncodePacket(core.Message) ([]byte, error) { return nil, nil }

type typedNilLogger struct{}

func (*typedNilLogger) Printf(string, ...interface{}) {}

func TestDirectListenAndServeRunsValidationBeforeBinding(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := tcp.New(tcp.Config{Addr: ":0", MaxConnections: -1}).ListenAndServe(ctx, func(core.Context) error {
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "MaxConnections") {
		t.Fatalf("ListenAndServe() error = %v, want preflight MaxConnections error", err)
	}
}

func TestGRPCServeListenerContextValidatesListenerAndRuntimeOptions(t *testing.T) {
	if err := grpcx.NewServer(":0").ServeListenerContext(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "listener is nil") {
		t.Fatalf("ServeListenerContext(nil) error = %v, want nil listener error", err)
	}
	if err := grpcx.NewServer(":0", grpcx.WithShutdownTimeout(-time.Second)).ServeListenerContext(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "ShutdownTimeout") {
		t.Fatalf("ServeListenerContext(negative timeout) error = %v, want ShutdownTimeout error", err)
	}
}

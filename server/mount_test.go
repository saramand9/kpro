// File: server/mount_test.go
// Purpose: Tests explicit protocol construction and generic native mounting.

package server

import (
	"errors"
	"testing"

	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/transport/grpcx"
	http3x "github.com/saramand9/kpro/transport/http3"
	"github.com/saramand9/kpro/transport/httpx"
	"github.com/saramand9/kpro/transport/kcp"
	quictransport "github.com/saramand9/kpro/transport/quic"
	"github.com/saramand9/kpro/transport/sse"
	"github.com/saramand9/kpro/transport/tcp"
	"github.com/saramand9/kpro/transport/udp"
	"github.com/saramand9/kpro/transport/websocket"
)

func TestServerMountsMessageEndpoints(t *testing.T) {
	app := New()

	assertEndpointName(t, app.Mount(tcp.New(tcp.Config{Addr: "127.0.0.1:0"})), core.TransportTCP)
	assertEndpointName(t, app.Mount(kcp.New(kcp.Config{Addr: "127.0.0.1:0"})), core.TransportKCP)
	assertEndpointName(t, app.Mount(quictransport.New(quictransport.Config{Addr: "127.0.0.1:0"})), core.TransportQUIC)
	assertEndpointName(t, app.Mount(udp.New(udp.Config{Addr: "127.0.0.1:0"})), core.TransportUDP)
	assertEndpointName(t, app.Mount(websocket.New(websocket.Config{Addr: "127.0.0.1:0", Path: "/ws"})), core.TransportWebSocket)

	if got, want := len(app.runners), 5; got != want {
		t.Fatalf("runner count = %d, want %d", got, want)
	}
}

func TestServerMountsNativeRunners(t *testing.T) {
	app := New()

	httpEndpoint := httpx.New(httpx.Config{Addr: "127.0.0.1:0"})
	if endpoint := app.MountRunner(httpEndpoint); endpoint == nil {
		t.Fatal("HTTP endpoint is nil")
	}
	httpsEndpoint := httpx.New(httpx.Config{Addr: "127.0.0.1:0", TLS: true, CertFile: "server.crt", KeyFile: "server.key"})
	if endpoint := app.MountRunner(httpsEndpoint); endpoint == nil {
		t.Fatal("HTTPS endpoint is nil")
	}
	sseEndpoint := sse.New(sse.Config{Addr: "127.0.0.1:0", Path: "/events"})
	if endpoint := app.MountRunner(sseEndpoint); endpoint == nil {
		t.Fatal("SSE endpoint is nil")
	}
	grpcEndpoint := grpcx.NewServer("127.0.0.1:0")
	if endpoint := app.MountRunner(grpcEndpoint); endpoint == nil {
		t.Fatal("gRPC endpoint is nil")
	}
	http3Endpoint := http3x.New(http3x.Config{Addr: "127.0.0.1:0", CertFile: "server.crt", KeyFile: "server.key"})
	if endpoint := app.MountRunner(http3Endpoint); endpoint == nil {
		t.Fatal("HTTP/3 endpoint is nil")
	}

	if got, want := len(app.runners), 5; got != want {
		t.Fatalf("runner count = %d, want %d", got, want)
	}
}

func TestMountNativePreservesConcreteRunnerType(t *testing.T) {
	app := New()
	endpoint := MountNative(app, httpx.New(httpx.Config{Addr: "127.0.0.1:0"}))
	endpoint.Get("/health", func(ctx *httpx.Context) error {
		return ctx.Text(200, "ok")
	})
	if got, want := len(app.runners), 1; got != want {
		t.Fatalf("runner count = %d, want %d", got, want)
	}
}

func TestMountNativeCheckedPreservesConcreteRunnerType(t *testing.T) {
	app := New()
	want := httpx.New(httpx.Config{Addr: "127.0.0.1:0"})
	endpoint, err := MountNativeChecked(app, want)
	if err != nil {
		t.Fatalf("MountNativeChecked() error = %v", err)
	}
	if endpoint != want {
		t.Fatal("MountNativeChecked() did not return the mounted concrete endpoint")
	}
	endpoint.Get("/health", func(ctx *httpx.Context) error {
		return ctx.Text(200, "ok")
	})
}

func TestMountNativeCheckedRejectsNilServer(t *testing.T) {
	endpoint, err := MountNativeChecked[*httpx.Endpoint](nil, httpx.New(httpx.Config{}))
	if !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("MountNativeChecked() error = %v, want %v", err, ErrInvalidConfiguration)
	}
	if endpoint != nil {
		t.Fatal("MountNativeChecked() endpoint is not nil after failure")
	}
}

func TestMountNativeCheckedRejectsTypedNilRunner(t *testing.T) {
	app := New()
	var endpoint *httpx.Endpoint
	mounted, err := MountNativeChecked(app, endpoint)
	if !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("MountNativeChecked() error = %v, want %v", err, ErrInvalidConfiguration)
	}
	if mounted != nil {
		t.Fatal("MountNativeChecked() endpoint is not nil after failure")
	}
}

func assertEndpointName(t *testing.T, endpoint *Endpoint, want core.Transport) {
	t.Helper()
	if endpoint == nil {
		t.Fatalf("%s endpoint is nil", want)
	}
	if got := endpoint.Name(); got != string(want) {
		t.Fatalf("endpoint name = %q, want %q", got, want)
	}
}

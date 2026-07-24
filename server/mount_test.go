// File: server/mount_test.go
// Purpose: Tests protocol mount helpers register the expected server runners.

package server

import (
	"testing"

	"github.com/saramand9/kpro/core"
	http3x "github.com/saramand9/kpro/transport/http3"
	"github.com/saramand9/kpro/transport/httpx"
	quictransport "github.com/saramand9/kpro/transport/quic"
)

func TestServerMountsMessageEndpoints(t *testing.T) {
	app := New()

	assertEndpointName(t, app.TCP("127.0.0.1:0"), core.TransportTCP)
	assertEndpointName(t, app.KCP("127.0.0.1:0"), core.TransportKCP)
	assertEndpointName(t, app.QUIC("127.0.0.1:0", quictransport.Config{}), core.TransportQUIC)
	assertEndpointName(t, app.UDP("127.0.0.1:0"), core.TransportUDP)
	assertEndpointName(t, app.WebSocket("127.0.0.1:0", "/ws"), core.TransportWebSocket)

	if got, want := len(app.runners), 5; got != want {
		t.Fatalf("runner count = %d, want %d", got, want)
	}
}

func TestServerMountsNativeRunners(t *testing.T) {
	app := New()

	if endpoint := app.HTTP("127.0.0.1:0"); endpoint == nil {
		t.Fatal("HTTP endpoint is nil")
	}
	if endpoint := app.HTTPS("127.0.0.1:0", httpx.Config{CertFile: "server.crt", KeyFile: "server.key"}); endpoint == nil {
		t.Fatal("HTTPS endpoint is nil")
	}
	if endpoint := app.SSE("127.0.0.1:0", "/events"); endpoint == nil {
		t.Fatal("SSE endpoint is nil")
	}
	if endpoint := app.GRPC("127.0.0.1:0"); endpoint == nil {
		t.Fatal("gRPC endpoint is nil")
	}
	if endpoint := app.HTTP3("127.0.0.1:0", http3x.Config{CertFile: "server.crt", KeyFile: "server.key"}); endpoint == nil {
		t.Fatal("HTTP/3 endpoint is nil")
	}

	if got, want := len(app.runners), 5; got != want {
		t.Fatalf("runner count = %d, want %d", got, want)
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

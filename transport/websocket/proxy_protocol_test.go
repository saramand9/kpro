package websocket

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/transport/proxyproto"
)

func TestProxyProtocolAdvertisesClientAddressAfterUpgrade(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve address: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	type observation struct {
		remote       string
		directRemote string
		version      uint8
		ok           bool
	}
	observed := make(chan observation, 1)
	server := New(Config{
		Addr: addr,
		ProxyProtocol: proxyproto.Config{
			Enabled:        true,
			TrustedProxies: []string{"127.0.0.1"},
		},
	})
	serveCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe(serveCtx, func(ctx core.Context) error {
			info, ok := proxyproto.InfoFrom(ctx.Session())
			observed <- observation{
				remote:       ctx.RemoteAddr(),
				directRemote: info.DirectRemoteAddr,
				version:      info.Header.Version,
				ok:           ok,
			}
			return nil
		})
	}()
	select {
	case <-server.Ready():
	case <-time.After(time.Second):
		cancel()
		t.Fatal("WebSocket server did not become ready")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("ListenAndServe: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("WebSocket server did not stop")
		}
	})

	connection, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer connection.Close()
	request := "PROXY TCP4 203.0.113.13 198.51.100.23 45681 8091\r\n" +
		"GET /ws HTTP/1.1\r\n" +
		"Host: example.test\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	if _, err := fmt.Fprint(connection, request); err != nil {
		t.Fatalf("write upgrade: %v", err)
	}
	reader := bufio.NewReader(connection)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("read upgrade response: %v", err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade status = %d", response.StatusCode)
	}

	packet, err := codec.KProV1().EncodePacket(core.Message{ID: 100, Seq: 1, Data: []byte("hello")})
	if err != nil {
		t.Fatalf("encode message: %v", err)
	}
	if _, err := connection.Write(maskedClientFrame(0x80|opBinary, packet)); err != nil {
		t.Fatalf("write WebSocket frame: %v", err)
	}
	select {
	case got := <-observed:
		if !got.ok || got.version != 1 || got.remote != "203.0.113.13:45681" {
			t.Fatalf("handler observation = %+v", got)
		}
		if !strings.HasPrefix(got.directRemote, "127.0.0.1:") {
			t.Fatalf("direct remote = %q", got.directRemote)
		}
	case <-time.After(time.Second):
		t.Fatal("WebSocket handler did not observe the message")
	}
}

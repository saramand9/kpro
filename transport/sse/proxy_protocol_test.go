package sse

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/saramand9/kpro/transport/proxyproto"
)

func TestProxyProtocolPropagatesClientAddressToStream(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve address: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	type observation struct {
		remote       string
		directRemote string
		ok           bool
	}
	observed := make(chan observation, 1)
	endpoint := New(Config{
		Addr: addr,
		ProxyProtocol: proxyproto.Config{
			Enabled:        true,
			TrustedProxies: []string{"127.0.0.1"},
		},
	}).Handle(func(ctx *Context) error {
		info, ok := proxyproto.FromContext(ctx.Request.Context())
		observed <- observation{
			remote:       ctx.Request.RemoteAddr,
			directRemote: info.DirectRemoteAddr,
			ok:           ok,
		}
		return ctx.Send("remote", []byte(ctx.Request.RemoteAddr))
	})

	serveCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- endpoint.ListenAndServe(serveCtx) }()
	select {
	case <-endpoint.Ready():
	case <-time.After(time.Second):
		cancel()
		t.Fatal("SSE endpoint did not become ready")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("ListenAndServe: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("SSE endpoint did not stop")
		}
	})

	dialer := new(net.Dialer)
	transport := &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network string, address string) (net.Conn, error) {
			connection, err := dialer.DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}
			if _, err := io.WriteString(connection, "PROXY TCP4 203.0.113.12 198.51.100.22 45680 8081\r\n"); err != nil {
				_ = connection.Close()
				return nil, err
			}
			return connection, nil
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	response, err := client.Get("http://" + addr + endpoint.config.Path)
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if !strings.Contains(string(body), "data: 203.0.113.12:45680\n") {
		t.Fatalf("stream body = %q", body)
	}

	select {
	case got := <-observed:
		if !got.ok || got.remote != "203.0.113.12:45680" {
			t.Fatalf("handler observation = %+v", got)
		}
		if !strings.HasPrefix(got.directRemote, "127.0.0.1:") {
			t.Fatalf("direct remote = %q", got.directRemote)
		}
	case <-time.After(time.Second):
		t.Fatal("SSE handler did not observe the request")
	}
}

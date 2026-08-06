package httpx

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/transport/proxyproto"
)

func TestProxyProtocolPropagatesClientAddressAndConnectionInfo(t *testing.T) {
	addr := freeHTTPRateLimitAddr(t)
	endpoint := New(Config{
		Addr: addr,
		ProxyProtocol: proxyproto.Config{
			Enabled:        true,
			TrustedProxies: []string{"127.0.0.1"},
		},
	})
	type observation struct {
		remote       string
		directRemote string
		version      uint8
	}
	observed := make(chan observation, 1)
	endpoint.Use(func(next core.InvocationHandler) core.InvocationHandler {
		return func(invocation core.Invocation) error {
			info, ok := proxyproto.FromContext(invocation.Context())
			if !ok {
				observed <- observation{remote: invocation.RemoteAddr()}
			} else {
				observed <- observation{
					remote:       invocation.RemoteAddr(),
					directRemote: info.DirectRemoteAddr,
					version:      info.Header.Version,
				}
			}
			return next(invocation)
		}
	})
	endpoint.Get("/who", func(ctx *Context) error {
		if _, ok := proxyproto.FromContext(ctx.Request().Context()); !ok {
			return core.ErrInternal
		}
		return ctx.Text(http.StatusOK, ctx.Request().RemoteAddr)
	})

	serveCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- endpoint.ListenAndServe(serveCtx) }()
	select {
	case <-endpoint.Ready():
	case <-time.After(time.Second):
		cancel()
		t.Fatal("HTTP endpoint did not become ready")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("ListenAndServe: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("HTTP endpoint did not stop")
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
			if _, err := io.WriteString(connection, "PROXY TCP4 203.0.113.10 198.51.100.20 45678 8080\r\n"); err != nil {
				_ = connection.Close()
				return nil, err
			}
			return connection, nil
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	response, err := client.Get("http://" + addr + "/who")
	if err != nil {
		t.Fatalf("GET /who: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if got, want := string(body), "203.0.113.10:45678"; got != want {
		t.Fatalf("response body = %q, want %q", got, want)
	}

	select {
	case got := <-observed:
		if got.remote != "203.0.113.10:45678" || got.version != 1 {
			t.Fatalf("middleware observation = %+v", got)
		}
		if !strings.HasPrefix(got.directRemote, "127.0.0.1:") {
			t.Fatalf("direct remote = %q", got.directRemote)
		}
	case <-time.After(time.Second):
		t.Fatal("middleware did not observe the request")
	}
}

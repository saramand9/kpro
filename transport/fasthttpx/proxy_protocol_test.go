package fasthttpx

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/saramand9/kpro/transport/proxyproto"
)

func TestTLSProxyProtocolHeaderPrecedesHandshake(t *testing.T) {
	certificate, key := writeTestSelfSignedCert(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve address: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	endpoint := New(Config{
		Addr:     addr,
		TLS:      true,
		CertFile: certificate,
		KeyFile:  key,
		ProxyProtocol: proxyproto.Config{
			Enabled:        true,
			TrustedProxies: []string{"127.0.0.1"},
		},
	})
	type observation struct {
		remote       string
		directRemote string
		version      uint8
		ok           bool
	}
	observed := make(chan observation, 1)
	endpoint.Get("/who", func(ctx *Context) error {
		info, ok := proxyproto.FromContext(ctx.Context())
		observed <- observation{
			remote:       ctx.Native().RemoteAddr().String(),
			directRemote: info.DirectRemoteAddr,
			version:      info.Header.Version,
			ok:           ok,
		}
		return ctx.Text(http.StatusOK, ctx.Native().RemoteAddr().String())
	})

	serveCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- endpoint.ListenAndServe(serveCtx) }()
	select {
	case <-endpoint.Ready():
	case <-time.After(time.Second):
		cancel()
		t.Fatal("fasthttp endpoint did not become ready")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("ListenAndServe: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("fasthttp endpoint did not stop")
		}
	})

	raw, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer raw.Close()
	if _, err := io.WriteString(raw, "PROXY TCP4 203.0.113.11 198.51.100.21 45679 443\r\n"); err != nil {
		t.Fatalf("write PROXY header: %v", err)
	}
	tlsConnection := tls.Client(raw, &tls.Config{
		InsecureSkipVerify: true, // Test certificate is generated per run.
		ServerName:         "localhost",
	})
	defer tlsConnection.Close()
	if err := tlsConnection.Handshake(); err != nil {
		t.Fatalf("TLS handshake after PROXY header: %v", err)
	}
	request, err := http.NewRequest(http.MethodGet, "https://"+addr+"/who", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Host = "localhost"
	request.Close = true
	if err := request.Write(tlsConnection); err != nil {
		t.Fatalf("write HTTP request: %v", err)
	}
	response, err := http.ReadResponse(bufio.NewReader(tlsConnection), request)
	if err != nil {
		t.Fatalf("read HTTP response: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if got, want := string(body), "203.0.113.11:45679"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}

	select {
	case got := <-observed:
		if !got.ok || got.version != 1 || got.remote != "203.0.113.11:45679" {
			t.Fatalf("handler observation = %+v", got)
		}
		if !strings.HasPrefix(got.directRemote, "127.0.0.1:") {
			t.Fatalf("direct remote = %q", got.directRemote)
		}
	case <-time.After(time.Second):
		t.Fatal("handler did not observe the request")
	}
}

package fasthttpx

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"

	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/transport"
)

func TestConfigDefaultsAndDescriptor(t *testing.T) {
	endpoint := New(Config{})
	if err := endpoint.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if endpoint.config.Addr != DefaultAddr || endpoint.config.MaxBodyBytes != DefaultMaxBodyBytes {
		t.Fatalf("normalized config = %#v", endpoint.config)
	}
	descriptor := endpoint.Descriptor()
	if descriptor.Name != componentHTTPName || descriptor.Protocol != core.TransportHTTP {
		t.Fatalf("descriptor = %#v", descriptor)
	}
	required := transport.CapabilityStream |
		transport.CapabilityRequestResponse |
		transport.CapabilityMiddleware
	if !descriptor.Capabilities.Has(required) || descriptor.Capabilities.Has(transport.CapabilityTLS) {
		t.Fatalf("unexpected HTTP capabilities = %b", descriptor.Capabilities)
	}

	tlsEndpoint := New(Config{TLS: true})
	if tlsEndpoint.Name() != componentHTTPSName || tlsEndpoint.Descriptor().Protocol != core.TransportHTTPS {
		t.Fatalf("TLS descriptor = %#v", tlsEndpoint.Descriptor())
	}
	if !tlsEndpoint.Descriptor().Capabilities.Has(transport.CapabilityTLS) {
		t.Fatalf("TLS capabilities = %b", tlsEndpoint.Descriptor().Capabilities)
	}
	if err := tlsEndpoint.Validate(); err == nil {
		t.Fatal("TLS endpoint without certificate was accepted")
	}
	server := tlsEndpoint.buildServer()
	if server.TLSConfig == nil || len(server.TLSConfig.NextProtos) != 1 || server.TLSConfig.NextProtos[0] != "http/1.1" {
		t.Fatalf("TLS ALPN = %#v", server.TLSConfig)
	}
}

func TestValidationRejectsNegativeLimitsAndTypedNilLogger(t *testing.T) {
	var logger *recordingLogger
	tests := []struct {
		name   string
		config Config
		want   string
	}{
		{name: "address", config: Config{Addr: "invalid"}, want: "listen address"},
		{name: "timeout", config: Config{ShutdownTimeout: -time.Second}, want: "ShutdownTimeout"},
		{name: "body", config: Config{MaxBodyBytes: -1}, want: "MaxBodyBytes"},
		{name: "concurrency", config: Config{Concurrency: -1}, want: "Concurrency"},
		{name: "logger", config: Config{Logger: logger}, want: "Logger cannot be typed nil"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := New(test.config).Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestReadyClosesAfterBindAndWireRoundTrip(t *testing.T) {
	endpoint := New(Config{Addr: "127.0.0.1:0"})
	endpoint.Post("/echo", func(ctx *Context) error {
		return ctx.CopyBody(fasthttp.StatusOK, "application/octet-stream")
	})
	serveErr := make(chan error, 1)
	go func() { serveErr <- endpoint.ListenAndServe(context.Background()) }()
	address := waitForEndpointReady(t, endpoint)

	response, err := http.Post("http://"+address+"/echo", "application/octet-stream", strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil {
		t.Fatalf("read response: %v", readErr)
	}
	if response.Proto != "HTTP/1.1" || response.StatusCode != http.StatusOK || string(body) != "hello" {
		t.Fatalf("response = %s %d/%q", response.Proto, response.StatusCode, body)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := endpoint.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	waitForServeResult(t, serveErr)
}

func TestWireBodyLimitReturnsSafe413(t *testing.T) {
	endpoint := New(Config{Addr: "127.0.0.1:0", MaxBodyBytes: 4})
	endpoint.Post("/echo", func(ctx *Context) error {
		return ctx.CopyBody(fasthttp.StatusOK, "application/octet-stream")
	})
	serveErr := make(chan error, 1)
	go func() { serveErr <- endpoint.ListenAndServe(context.Background()) }()
	address := waitForEndpointReady(t, endpoint)

	response, err := http.Post("http://"+address+"/echo", "text/plain", strings.NewReader("large"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil {
		t.Fatalf("read response: %v", readErr)
	}
	if response.StatusCode != http.StatusRequestEntityTooLarge || string(body) != core.ErrPayloadTooLarge.Error() {
		t.Fatalf("response = %d/%q", response.StatusCode, body)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := endpoint.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	waitForServeResult(t, serveErr)
}

func TestTLSWireNegotiatesHTTP11Only(t *testing.T) {
	certFile, keyFile := writeTestSelfSignedCert(t)
	endpoint := New(Config{
		Addr:     "127.0.0.1:0",
		TLS:      true,
		CertFile: certFile,
		KeyFile:  keyFile,
	})
	endpoint.Get("/health", func(ctx *Context) error {
		return ctx.Text(fasthttp.StatusOK, "ok")
	})
	serveErr := make(chan error, 1)
	go func() { serveErr <- endpoint.ListenAndServe(context.Background()) }()
	address := waitForEndpointReady(t, endpoint)

	clientTransport := &http.Transport{
		ForceAttemptHTTP2: true,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // Test-only self-signed certificate.
		},
	}
	client := &http.Client{Transport: clientTransport}
	response, err := client.Get("https://" + address + "/health")
	if err != nil {
		t.Fatalf("HTTPS GET: %v", err)
	}
	response.Body.Close()
	clientTransport.CloseIdleConnections()
	negotiatedProtocol := ""
	if response.TLS != nil {
		negotiatedProtocol = response.TLS.NegotiatedProtocol
	}
	if response.Proto != "HTTP/1.1" || negotiatedProtocol != "http/1.1" {
		t.Fatalf("TLS response = %s, ALPN %q", response.Proto, negotiatedProtocol)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := endpoint.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	waitForServeResult(t, serveErr)
}

func TestRequestContextClosesDuringGracefulShutdown(t *testing.T) {
	endpoint := New(Config{Addr: "127.0.0.1:0"})
	handlerStarted := make(chan struct{})
	endpoint.Get("/wait", func(ctx *Context) error {
		close(handlerStarted)
		<-ctx.Context().Done()
		return ctx.Text(fasthttp.StatusOK, "stopped")
	})
	serveErr := make(chan error, 1)
	go func() { serveErr <- endpoint.ListenAndServe(context.Background()) }()
	address := waitForEndpointReady(t, endpoint)
	clientResult := make(chan error, 1)
	go func() {
		response, err := http.Get("http://" + address + "/wait")
		if err != nil {
			clientResult <- err
			return
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err == nil && (response.StatusCode != http.StatusOK || string(body) != "stopped") {
			err = errors.New("unexpected shutdown response")
		}
		clientResult <- err
	}()
	select {
	case <-handlerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := endpoint.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if err := <-clientResult; err != nil {
		t.Fatalf("client error = %v", err)
	}
	waitForServeResult(t, serveErr)
}

func TestShutdownDeadlineForceClosesActiveConnection(t *testing.T) {
	endpoint := New(Config{Addr: "127.0.0.1:0"})
	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	endpoint.Get("/block", func(ctx *Context) error {
		close(handlerStarted)
		<-releaseHandler
		return ctx.Text(fasthttp.StatusOK, "late")
	})
	serveErr := make(chan error, 1)
	go func() { serveErr <- endpoint.ListenAndServe(context.Background()) }()
	address := waitForEndpointReady(t, endpoint)
	clientDone := make(chan struct{})
	go func() {
		response, err := http.Get("http://" + address + "/block")
		if err == nil {
			response.Body.Close()
		}
		close(clientDone)
	}()
	select {
	case <-handlerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := endpoint.Shutdown(shutdownCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown() error = %v, want deadline exceeded", err)
	}
	close(releaseHandler)
	select {
	case <-clientDone:
	case <-time.After(2 * time.Second):
		t.Fatal("client connection was not closed")
	}
	waitForServeResult(t, serveErr)
}

func TestConnectionIdentityPersistsAcrossKeepAliveRequests(t *testing.T) {
	type observation struct {
		id   uint64
		done <-chan struct{}
	}
	observations := make(chan observation, 2)
	endpoint := New(Config{Addr: "127.0.0.1:0"})
	endpoint.Use(func(next core.InvocationHandler) core.InvocationHandler {
		return func(invocation core.Invocation) error {
			observations <- observation{
				id:   invocation.ConnID(),
				done: core.InvocationConnDone(invocation),
			}
			return next(invocation)
		}
	})
	endpoint.Get("/identity", func(ctx *Context) error {
		return ctx.Text(fasthttp.StatusOK, "ok")
	})
	serveErr := make(chan error, 1)
	go func() { serveErr <- endpoint.ListenAndServe(context.Background()) }()
	address := waitForEndpointReady(t, endpoint)

	transport := &http.Transport{MaxIdleConnsPerHost: 1}
	client := &http.Client{Transport: transport}
	for requestIndex := 0; requestIndex < 2; requestIndex++ {
		response, err := client.Get("http://" + address + "/identity")
		if err != nil {
			t.Fatalf("GET %d: %v", requestIndex, err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
	}
	first := <-observations
	second := <-observations
	if first.id == 0 || first.id != second.id {
		t.Fatalf("connection ids = %d/%d", first.id, second.id)
	}
	if first.done == nil || first.done != second.done {
		t.Fatal("keep-alive requests did not share a connection lifetime")
	}
	transport.CloseIdleConnections()
	select {
	case <-first.done:
	case <-time.After(2 * time.Second):
		t.Fatal("connection lifetime did not close")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := endpoint.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	waitForServeResult(t, serveErr)
}

func TestSecondStartAndShutdownBeforeStart(t *testing.T) {
	endpoint := New(Config{Addr: "127.0.0.1:0"})
	serveErr := make(chan error, 1)
	go func() { serveErr <- endpoint.ListenAndServe(context.Background()) }()
	waitForEndpointReady(t, endpoint)
	if err := endpoint.ListenAndServe(context.Background()); !errors.Is(err, core.ErrAlreadyStarted) {
		t.Fatalf("second ListenAndServe() error = %v", err)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := endpoint.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	waitForServeResult(t, serveErr)

	closed := New(Config{Addr: "127.0.0.1:0"})
	if err := closed.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown before start = %v", err)
	}
	if err := closed.ListenAndServe(context.Background()); !errors.Is(err, core.ErrClosed) {
		t.Fatalf("ListenAndServe after shutdown = %v", err)
	}
	select {
	case <-closed.Ready():
		t.Fatal("Ready closed without a bound listener")
	default:
	}
}

func TestReadyStaysOpenWhenBindFails(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve listener: %v", err)
	}
	defer listener.Close()
	endpoint := New(Config{Addr: listener.Addr().String()})
	if err := endpoint.ListenAndServe(context.Background()); err == nil {
		t.Fatal("ListenAndServe() error = nil")
	}
	select {
	case <-endpoint.Ready():
		t.Fatal("Ready closed after bind failure")
	default:
	}
}

func waitForEndpointReady(t *testing.T, endpoint *Endpoint) string {
	t.Helper()
	select {
	case <-endpoint.Ready():
	case <-time.After(3 * time.Second):
		t.Fatal("endpoint did not become ready")
	}
	endpoint.mu.RLock()
	listener := endpoint.listener
	endpoint.mu.RUnlock()
	if listener == nil {
		t.Fatal("ready endpoint has no listener")
	}
	return listener.Addr().String()
}

func waitForServeResult(t *testing.T, serveErr <-chan error) {
	t.Helper()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("ListenAndServe() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ListenAndServe did not return")
	}
}

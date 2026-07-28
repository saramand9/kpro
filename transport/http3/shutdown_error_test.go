package http3

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	qh3 "github.com/quic-go/quic-go/http3"
)

func TestListenAndServeReturnsCancellationShutdownDeadline(t *testing.T) {
	certFile, keyFile := http3TestSelfSignedCert(t)
	endpoint := New(Config{
		Addr:             freeHTTP3TestAddr(t),
		CertFile:         certFile,
		KeyFile:          keyFile,
		ShutdownTimeout:  50 * time.Millisecond,
		HandshakeTimeout: time.Second,
	})

	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseHandler)
	endpoint.Get("/block", func(ctx *Context) error {
		close(entered)
		select {
		case <-ctx.Request().Context().Done():
			return ctx.Request().Context().Err()
		case <-release:
			return ctx.Text(http.StatusOK, "released")
		}
	})

	serveCtx, cancelServe := context.WithCancel(context.Background())
	t.Cleanup(cancelServe)
	serveErr := make(chan error, 1)
	go func() { serveErr <- endpoint.ListenAndServe(serveCtx) }()
	select {
	case <-endpoint.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP/3 server did not become ready")
	}

	roundTripper := &qh3.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	t.Cleanup(func() { _ = roundTripper.Close() })
	client := &http.Client{Transport: roundTripper, Timeout: 2 * time.Second}
	requestDone := make(chan struct{}, 1)
	go func() {
		resp, err := client.Get("https://" + endpoint.config.Addr + "/block")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		requestDone <- struct{}{}
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP/3 blocking handler did not start")
	}
	cancelServe()
	select {
	case err := <-serveErr:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("ListenAndServe() error = %v, want deadline exceeded", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ListenAndServe() did not return after shutdown deadline")
	}

	releaseHandler()
	select {
	case <-requestDone:
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP/3 request did not finish after handler release")
	}
}

package http3

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	qh3 "github.com/quic-go/quic-go/http3"
)

func TestHTTP3GetAndPost(t *testing.T) {
	endpoint, baseURL, client, stop := startHTTP3TestServer(t)
	defer stop()

	endpoint.Get("/health", func(ctx *Context) error {
		return ctx.Text(http.StatusOK, "ok\n")
	})
	endpoint.Post("/echo", func(ctx *Context) error {
		return ctx.CopyBody(http.StatusCreated, "text/plain")
	})
	waitForHTTP3Server(t, client, baseURL+"/health")

	resp, err := client.Get(baseURL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	assertHTTP3Response(t, resp, http.StatusOK, "ok\n")

	resp, err = client.Post(baseURL+"/echo", "text/plain", strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("POST /echo: %v", err)
	}
	defer resp.Body.Close()
	assertHTTP3Response(t, resp, http.StatusCreated, "payload")
}

func TestHTTP3RoutingErrors(t *testing.T) {
	endpoint, baseURL, client, stop := startHTTP3TestServer(t)
	defer stop()

	endpoint.Get("/only-get", func(ctx *Context) error {
		return ctx.Text(http.StatusOK, "hit")
	})
	waitForHTTP3Server(t, client, baseURL+"/only-get")

	resp, err := client.Get(baseURL + "/missing")
	if err != nil {
		t.Fatalf("GET /missing: %v", err)
	}
	defer resp.Body.Close()
	assertHTTP3Response(t, resp, http.StatusNotFound, "not found")

	resp, err = client.Post(baseURL+"/only-get", "text/plain", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST /only-get: %v", err)
	}
	defer resp.Body.Close()
	assertHTTP3Response(t, resp, http.StatusMethodNotAllowed, "method not allowed")
}

func startHTTP3TestServer(t *testing.T) (*Endpoint, string, *http.Client, func()) {
	t.Helper()

	certFile, keyFile := http3TestSelfSignedCert(t)
	addr := freeHTTP3TestAddr(t)
	endpoint := New(Config{
		Addr:             addr,
		CertFile:         certFile,
		KeyFile:          keyFile,
		HandshakeTimeout: 2 * time.Second,
		IdleTimeout:      10 * time.Second,
		ShutdownTimeout:  2 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- endpoint.ListenAndServe(ctx)
	}()

	roundTripper := &qh3.RoundTripper{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: roundTripper, Timeout: 3 * time.Second}
	stop := func() {
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutdownCancel()
		_ = endpoint.Shutdown(shutdownCtx)
		_ = roundTripper.Close()
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("HTTP/3 server stopped with error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("HTTP/3 server did not stop")
		}
	}
	return endpoint, "https://" + addr, client, stop
}

func waitForHTTP3Server(t *testing.T, client *http.Client, url string) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			return
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("HTTP/3 server did not become ready: %v", lastErr)
}

func assertHTTP3Response(t *testing.T, resp *http.Response, status int, body string) {
	t.Helper()

	if resp.StatusCode != status {
		t.Fatalf("status = %d, want %d", resp.StatusCode, status)
	}
	if !strings.HasPrefix(resp.Proto, "HTTP/3") {
		t.Fatalf("response protocol = %q, want HTTP/3", resp.Proto)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if string(got) != body {
		t.Fatalf("body = %q, want %q", string(got), body)
	}
}

func freeHTTP3TestAddr(t *testing.T) string {
	t.Helper()

	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp temp addr: %v", err)
	}
	defer conn.Close()
	return conn.LocalAddr().String()
}

func http3TestSelfSignedCert(t *testing.T) (string, string) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate serial: %v", err)
	}
	tpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tpl, &tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}

	dir := t.TempDir()
	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")
	writeHTTP3TestPEM(t, certFile, 0644, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	writeHTTP3TestPEM(t, keyFile, 0600, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return certFile, keyFile
}

func writeHTTP3TestPEM(t *testing.T, path string, perm os.FileMode, block *pem.Block) {
	t.Helper()

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		t.Fatalf("open pem file: %v", err)
	}
	defer file.Close()
	if err := pem.Encode(file, block); err != nil {
		t.Fatalf("write pem file: %v", err)
	}
}

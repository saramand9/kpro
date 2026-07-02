// File: test/http_sse_file_compare_test.go
// Purpose: Exercises KPro protocol and transport behavior through integration tests.

package test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saramand9/bignet/kpro/transport/httpx"
	"github.com/saramand9/bignet/kpro/transport/sse"
)

func TestHTTPFileTrace(t *testing.T) {
	runHTTPFileTrace(t, false)
}

func TestHTTPSFileTrace(t *testing.T) {
	runHTTPFileTrace(t, true)
}

func TestSSEFileTrace(t *testing.T) {
	trace := newTCPTrace(t)
	clientRequests := newJSONTraceLog(t, filepath.Join(trace.dir, "client_sent.ndjson"))
	serverRequests := newJSONTraceLog(t, filepath.Join(trace.dir, "server_received.ndjson"))
	serverEvents := newJSONTraceLog(t, filepath.Join(trace.dir, "server_events.ndjson"))
	clientEvents := newJSONTraceLog(t, filepath.Join(trace.dir, "client_received.ndjson"))

	addr := "127.0.0.1:" + freeTCPPort(t)
	endpoint := sse.New(sse.Config{Addr: addr, Path: "/events"})
	endpoint.Handle(func(ctx *sse.Context) error {
		if err := serverRequests.append(sseRequestRecord{Method: ctx.Request.Method, Path: ctx.Request.URL.Path}); err != nil {
			return err
		}
		events := []string{"one", "two", "three"}
		for _, event := range events {
			if err := serverEvents.append(sseEventRecord{Event: "message", Data: event}); err != nil {
				return err
			}
			if err := ctx.Send("message", []byte(event)); err != nil {
				return err
			}
		}
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- endpoint.ListenAndServe(ctx)
	}()
	defer func() {
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutdownCancel()
		_ = endpoint.Shutdown(shutdownCtx)
		select {
		case <-errCh:
		case <-time.After(2 * time.Second):
			t.Fatal("sse trace server did not stop")
		}
	}()
	time.Sleep(50 * time.Millisecond)

	if err := clientRequests.append(sseRequestRecord{Method: http.MethodGet, Path: "/events"}); err != nil {
		t.Fatalf("append SSE client request: %v", err)
	}
	resp, err := http.Get("http://" + addr + "/events")
	if err != nil {
		t.Fatalf("get SSE events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected SSE status: %s", resp.Status)
	}
	readSSEEvents(t, resp.Body, clientEvents, 3)

	assertTraceFileEqual(t, clientRequests.path, serverRequests.path)
	assertTraceFileEqual(t, serverEvents.path, clientEvents.path)
}

func runHTTPFileTrace(t *testing.T, tlsEnabled bool) {
	t.Helper()

	trace := newTCPTrace(t)
	clientRequests := newJSONTraceLog(t, filepath.Join(trace.dir, "client_sent.ndjson"))
	serverRequests := newJSONTraceLog(t, filepath.Join(trace.dir, "server_received.ndjson"))
	serverResponses := newJSONTraceLog(t, filepath.Join(trace.dir, "server_replied.ndjson"))
	clientResponses := newJSONTraceLog(t, filepath.Join(trace.dir, "client_received.ndjson"))

	addr := "127.0.0.1:" + freeTCPPort(t)
	config := httpx.Config{Addr: addr, ShutdownTimeout: 500 * time.Millisecond}
	cleanup := func() {}
	scheme := "http"
	client := &http.Client{Timeout: 3 * time.Second}
	if tlsEnabled {
		certFile, keyFile, certCleanup, err := testSelfSignedCert(t)
		if err != nil {
			t.Fatalf("generate test cert: %v", err)
		}
		cleanup = certCleanup
		config.TLS = true
		config.CertFile = certFile
		config.KeyFile = keyFile
		scheme = "https"
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}
	defer cleanup()

	endpoint := httpx.New(config)
	endpoint.Get("/health", func(ctx *httpx.Context) error {
		if err := serverRequests.append(httpTraceRecord{Method: ctx.Method(), Path: ctx.Path()}); err != nil {
			return err
		}
		record := httpTraceRecord{Method: ctx.Method(), Path: ctx.Path(), Status: http.StatusOK, Response: "ok\n"}
		if err := serverResponses.append(record); err != nil {
			return err
		}
		return ctx.Text(http.StatusOK, record.Response)
	})
	endpoint.Post("/echo", func(ctx *httpx.Context) error {
		body, err := ctx.Body()
		if err != nil {
			return err
		}
		if err := serverRequests.append(httpTraceRecord{Method: ctx.Method(), Path: ctx.Path(), Payload: string(body)}); err != nil {
			return err
		}
		record := httpTraceRecord{Method: ctx.Method(), Path: ctx.Path(), Payload: string(body), Status: http.StatusOK, Response: string(body)}
		if err := serverResponses.append(record); err != nil {
			return err
		}
		return ctx.Text(http.StatusOK, record.Response)
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- endpoint.ListenAndServe(ctx)
	}()
	defer func() {
		cancel()
		client.CloseIdleConnections()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutdownCancel()
		_ = endpoint.Shutdown(shutdownCtx)
		select {
		case <-errCh:
		case <-time.After(2 * time.Second):
			t.Fatal("http trace server did not stop")
		}
	}()
	time.Sleep(50 * time.Millisecond)

	base := scheme + "://" + addr
	requests := []httpTraceRecord{
		{Method: http.MethodGet, Path: "/health"},
		{Method: http.MethodPost, Path: "/echo", Payload: "hello " + scheme},
	}
	for _, req := range requests {
		if err := clientRequests.append(req); err != nil {
			t.Fatalf("append HTTP client request: %v", err)
		}
		respRecord := doHTTPTraceRequest(t, client, base, req)
		if err := clientResponses.append(respRecord); err != nil {
			t.Fatalf("append HTTP client response: %v", err)
		}
	}

	assertTraceFileEqual(t, clientRequests.path, serverRequests.path)
	assertTraceFileEqual(t, serverResponses.path, clientResponses.path)
}

type httpTraceRecord struct {
	Index    int    `json:"index"`
	Method   string `json:"method"`
	Path     string `json:"path"`
	Payload  string `json:"payload,omitempty"`
	Status   int    `json:"status,omitempty"`
	Response string `json:"response,omitempty"`
}

type sseRequestRecord struct {
	Index  int    `json:"index"`
	Method string `json:"method"`
	Path   string `json:"path"`
}

type sseEventRecord struct {
	Index int    `json:"index"`
	Event string `json:"event"`
	Data  string `json:"data"`
}

type jsonTraceLog struct {
	mu    sync.Mutex
	path  string
	index int
}

func newJSONTraceLog(t *testing.T, path string) *jsonTraceLog {
	t.Helper()
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatalf("create json trace file %s: %v", path, err)
	}
	return &jsonTraceLog{path: path}
}

func (l *jsonTraceLog) append(record interface{}) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.index++
	switch record := record.(type) {
	case httpTraceRecord:
		record.Index = l.index
		return appendJSONLine(l.path, record)
	case sseRequestRecord:
		record.Index = l.index
		return appendJSONLine(l.path, record)
	case sseEventRecord:
		record.Index = l.index
		return appendJSONLine(l.path, record)
	default:
		return appendJSONLine(l.path, record)
	}
}

func appendJSONLine(path string, record interface{}) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer file.Close()
	line, err := json.Marshal(record)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	_, err = file.Write(line)
	return err
}

func doHTTPTraceRequest(t *testing.T, client *http.Client, base string, req httpTraceRecord) httpTraceRecord {
	t.Helper()

	var body io.Reader
	if req.Payload != "" {
		body = bytes.NewBufferString(req.Payload)
	}
	httpReq, err := http.NewRequest(req.Method, base+req.Path, body)
	if err != nil {
		t.Fatalf("create HTTP request: %v", err)
	}
	if req.Payload != "" {
		httpReq.Header.Set("Content-Type", "text/plain")
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		t.Fatalf("do HTTP request: %v", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read HTTP response: %v", err)
	}
	return httpTraceRecord{
		Method:   req.Method,
		Path:     req.Path,
		Payload:  req.Payload,
		Status:   resp.StatusCode,
		Response: string(responseBody),
	}
}

func readSSEEvents(t *testing.T, body io.Reader, log *jsonTraceLog, want int) {
	t.Helper()
	scanner := bufio.NewScanner(body)
	currentEvent := ""
	seen := 0
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			currentEvent = ""
		case strings.HasPrefix(line, "event: "):
			currentEvent = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			if err := log.append(sseEventRecord{Event: currentEvent, Data: strings.TrimPrefix(line, "data: ")}); err != nil {
				t.Fatalf("append SSE client event: %v", err)
			}
			seen++
			if seen >= want {
				return
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan SSE response: %v", err)
	}
	if seen != want {
		t.Fatalf("read %d SSE events, want %d", seen, want)
	}
}

func testSelfSignedCert(t *testing.T) (string, string, func(), error) {
	t.Helper()

	dir, err := os.MkdirTemp("", "kpro-test-https-*")
	if err != nil {
		return "", "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		cleanup()
		return "", "", nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		cleanup()
		return "", "", nil, err
	}
	tpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tpl, &tpl, &key.PublicKey, key)
	if err != nil {
		cleanup()
		return "", "", nil, err
	}

	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")
	if err := writeTestPEM(certFile, 0644, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		cleanup()
		return "", "", nil, err
	}
	if err := writeTestPEM(keyFile, 0600, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}); err != nil {
		cleanup()
		return "", "", nil, err
	}
	return certFile, keyFile, cleanup, nil
}

func writeTestPEM(path string, perm os.FileMode, block *pem.Block) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer file.Close()
	return pem.Encode(file, block)
}

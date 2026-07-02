// File: test/http_wire_crc_test.go
// Purpose: Exercises KPro protocol and transport behavior through integration tests.

package test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/transport/httpx"
)

const strictHTTPExchangeCount = 1000

const (
	strictHTTPHost = "127.0.0.1"
)

func TestHTTPWireCRCTraceStrict1000(t *testing.T) {
	t.Run("get_health", func(t *testing.T) {
		requests := strictHTTPRequests(http.MethodGet, "/health", "health", strictHTTPExchangeCount)
		logs := runHTTPWireCRCScenario(t, func(endpoint *httpx.Endpoint, logs strictHTTPLogs) {
			endpoint.Get("/health", func(ctx *httpx.Context) error {
				req := strictHTTPRequestFromContext(t, ctx)
				if err := logs.serverReceived.appendRequest(req); err != nil {
					return err
				}
				body := []byte("ok:" + ctx.Query("seq"))
				resp := strictHTTPResponse{Status: http.StatusOK, ContentType: "text/plain; charset=utf-8", Body: body}
				if err := logs.serverSent.appendResponse(resp); err != nil {
					return err
				}
				return ctx.Text(resp.Status, string(body))
			})
		}, requests, writeStrictHTTPRequestsNormal)

		assertHTTPExchangesEqual(t, logs.clientSent, logs.serverReceived)
		assertHTTPExchangesEqual(t, logs.serverSent, logs.clientReceived)
		assertHTTPResponsesMatchRequests(t, requests, logs.clientReceived.snapshot(), http.StatusOK, func(req strictHTTPRequest) []byte {
			return []byte("ok:" + strictHTTPQueryValue(req.Target, "seq"))
		})
	})

	t.Run("post_echo", func(t *testing.T) {
		requests := strictHTTPRequests(http.MethodPost, "/echo", "echo", strictHTTPExchangeCount)
		logs := runHTTPWireCRCScenario(t, func(endpoint *httpx.Endpoint, logs strictHTTPLogs) {
			endpoint.Post("/echo", func(ctx *httpx.Context) error {
				req := strictHTTPRequestFromContext(t, ctx)
				if err := logs.serverReceived.appendRequest(req); err != nil {
					return err
				}
				body, err := ctx.Body()
				if err != nil {
					return err
				}
				resp := strictHTTPResponse{Status: http.StatusOK, ContentType: "text/plain", Body: append([]byte(nil), body...)}
				if err := logs.serverSent.appendResponse(resp); err != nil {
					return err
				}
				return ctx.CopyBody(resp.Status, resp.ContentType)
			})
		}, requests, writeStrictHTTPRequestsNormal)

		assertHTTPExchangesEqual(t, logs.clientSent, logs.serverReceived)
		assertHTTPExchangesEqual(t, logs.serverSent, logs.clientReceived)
		assertHTTPResponsesEchoRequests(t, requests, logs.clientReceived.snapshot(), http.StatusOK)
	})

	t.Run("put_bytes_created", func(t *testing.T) {
		requests := strictHTTPRequests(http.MethodPut, "/bytes", "bytes", strictHTTPExchangeCount)
		logs := runHTTPWireCRCScenario(t, func(endpoint *httpx.Endpoint, logs strictHTTPLogs) {
			endpoint.Put("/bytes", func(ctx *httpx.Context) error {
				req := strictHTTPRequestFromContext(t, ctx)
				if err := logs.serverReceived.appendRequest(req); err != nil {
					return err
				}
				body, err := ctx.Body()
				if err != nil {
					return err
				}
				resp := strictHTTPResponse{Status: http.StatusCreated, ContentType: "application/octet-stream", Body: append([]byte(nil), body...)}
				if err := logs.serverSent.appendResponse(resp); err != nil {
					return err
				}
				return ctx.Bytes(resp.Status, resp.ContentType, body)
			})
		}, requests, writeStrictHTTPRequestsNormal)

		assertHTTPExchangesEqual(t, logs.clientSent, logs.serverReceived)
		assertHTTPExchangesEqual(t, logs.serverSent, logs.clientReceived)
		assertHTTPResponsesEchoRequests(t, requests, logs.clientReceived.snapshot(), http.StatusCreated)
	})

	t.Run("json_response", func(t *testing.T) {
		requests := strictHTTPRequests(http.MethodGet, "/json", "json", strictHTTPExchangeCount)
		logs := runHTTPWireCRCScenario(t, func(endpoint *httpx.Endpoint, logs strictHTTPLogs) {
			endpoint.Get("/json", func(ctx *httpx.Context) error {
				req := strictHTTPRequestFromContext(t, ctx)
				if err := logs.serverReceived.appendRequest(req); err != nil {
					return err
				}
				body := []byte(fmt.Sprintf(`{"seq":"%s"}`, ctx.Query("seq")))
				resp := strictHTTPResponse{Status: http.StatusOK, ContentType: "application/json; charset=utf-8", Body: body}
				if err := logs.serverSent.appendResponse(resp); err != nil {
					return err
				}
				return ctx.JSON(resp.Status, struct {
					Seq string `json:"seq"`
				}{Seq: ctx.Query("seq")})
			})
		}, requests, writeStrictHTTPRequestsNormal)

		assertHTTPExchangesEqual(t, logs.clientSent, logs.serverReceived)
		assertHTTPExchangesEqual(t, logs.serverSent, logs.clientReceived)
		assertHTTPResponsesMatchRequests(t, requests, logs.clientReceived.snapshot(), http.StatusOK, func(req strictHTTPRequest) []byte {
			return []byte(fmt.Sprintf(`{"seq":"%s"}`, strictHTTPQueryValue(req.Target, "seq")))
		})
	})

	t.Run("middleware_header", func(t *testing.T) {
		requests := strictHTTPRequests(http.MethodGet, "/middleware", "middleware", strictHTTPExchangeCount)
		logs := runHTTPWireCRCScenario(t, func(endpoint *httpx.Endpoint, logs strictHTTPLogs) {
			endpoint.Use(func(next core.InvocationHandler) core.InvocationHandler {
				return func(inv core.Invocation) error {
					ctx := inv.Native().(*httpx.Context)
					ctx.Header().Set("X-Strict-Middleware", "hit")
					return next(inv)
				}
			})
			endpoint.Get("/middleware", func(ctx *httpx.Context) error {
				req := strictHTTPRequestFromContext(t, ctx)
				if err := logs.serverReceived.appendRequest(req); err != nil {
					return err
				}
				body := []byte("middleware:" + ctx.Query("seq"))
				resp := strictHTTPResponse{
					Status:      http.StatusAccepted,
					ContentType: "text/plain; charset=utf-8",
					Headers:     map[string]string{"X-Strict-Middleware": "hit"},
					Body:        body,
				}
				if err := logs.serverSent.appendResponse(resp); err != nil {
					return err
				}
				return ctx.Text(resp.Status, string(body))
			})
		}, requests, writeStrictHTTPRequestsNormal)

		assertHTTPExchangesEqual(t, logs.clientSent, logs.serverReceived)
		assertHTTPExchangesEqual(t, logs.serverSent, logs.clientReceived)
		assertHTTPResponsesMatchRequests(t, requests, logs.clientReceived.snapshot(), http.StatusAccepted, func(req strictHTTPRequest) []byte {
			return []byte("middleware:" + strictHTTPQueryValue(req.Target, "seq"))
		})
	})

	t.Run("handler_error_500", func(t *testing.T) {
		requests := strictHTTPRequests(http.MethodGet, "/handler-error", "handler-error", strictHTTPExchangeCount)
		logs := runHTTPWireCRCScenario(t, func(endpoint *httpx.Endpoint, logs strictHTTPLogs) {
			endpoint.Get("/handler-error", func(ctx *httpx.Context) error {
				req := strictHTTPRequestFromContext(t, ctx)
				if err := logs.serverReceived.appendRequest(req); err != nil {
					return err
				}
				text := "handler failed:" + ctx.Query("seq")
				resp := strictHTTPResponse{Status: http.StatusInternalServerError, ContentType: "text/plain; charset=utf-8", Body: []byte(text)}
				if err := logs.serverSent.appendResponse(resp); err != nil {
					return err
				}
				return fmt.Errorf("%s", text)
			})
		}, requests, writeStrictHTTPRequestsNormal)

		assertHTTPExchangesEqual(t, logs.clientSent, logs.serverReceived)
		assertHTTPExchangesEqual(t, logs.serverSent, logs.clientReceived)
		assertHTTPResponsesMatchRequests(t, requests, logs.clientReceived.snapshot(), http.StatusInternalServerError, func(req strictHTTPRequest) []byte {
			return []byte("handler failed:" + strictHTTPQueryValue(req.Target, "seq"))
		})
	})

	t.Run("boundary_payloads", func(t *testing.T) {
		requests := strictHTTPBoundaryRequests("/boundary", strictHTTPExchangeCount)
		logs := runHTTPWireCRCInterleavedScenario(t, func(endpoint *httpx.Endpoint, logs strictHTTPLogs) {
			endpoint.Post("/boundary", func(ctx *httpx.Context) error {
				req := strictHTTPRequestFromContext(t, ctx)
				if err := logs.serverReceived.appendRequest(req); err != nil {
					return err
				}
				body, err := ctx.Body()
				if err != nil {
					return err
				}
				resp := strictHTTPResponse{Status: http.StatusOK, ContentType: "application/octet-stream", Body: append([]byte(nil), body...)}
				if err := logs.serverSent.appendResponse(resp); err != nil {
					return err
				}
				return ctx.Bytes(resp.Status, resp.ContentType, body)
			})
		}, requests)

		assertHTTPExchangesEqual(t, logs.clientSent, logs.serverReceived)
		assertHTTPExchangesEqual(t, logs.serverSent, logs.clientReceived)
		assertHTTPResponsesEchoRequests(t, requests, logs.clientReceived.snapshot(), http.StatusOK)
	})

	t.Run("multi_connection_echo", func(t *testing.T) {
		requests := strictHTTPRequests(http.MethodPost, "/multi", "multi", strictHTTPExchangeCount)
		logs := runHTTPWireCRCMultiConnectionScenario(t, 4, func(endpoint *httpx.Endpoint, logs strictHTTPLogs) {
			endpoint.Post("/multi", func(ctx *httpx.Context) error {
				req := strictHTTPRequestFromContext(t, ctx)
				if err := logs.serverReceived.appendRequest(req); err != nil {
					return err
				}
				body, err := ctx.Body()
				if err != nil {
					return err
				}
				resp := strictHTTPResponse{Status: http.StatusOK, ContentType: "text/plain", Body: append([]byte(nil), body...)}
				if err := logs.serverSent.appendResponse(resp); err != nil {
					return err
				}
				return ctx.CopyBody(resp.Status, resp.ContentType)
			})
		}, requests)

		assertHTTPExchangeSetEqual(t, logs.clientSent, logs.serverReceived)
		assertHTTPExchangeSetEqual(t, logs.serverSent, logs.clientReceived)
		assertHTTPResponseSetEchoRequests(t, requests, logs.clientReceived.snapshot(), http.StatusOK)
	})

	t.Run("coalesced_write_echo", func(t *testing.T) {
		requests := strictHTTPRequests(http.MethodPost, "/coalesced", "coalesced", strictHTTPExchangeCount)
		logs := runHTTPWireCRCScenario(t, func(endpoint *httpx.Endpoint, logs strictHTTPLogs) {
			endpoint.Post("/coalesced", func(ctx *httpx.Context) error {
				req := strictHTTPRequestFromContext(t, ctx)
				if err := logs.serverReceived.appendRequest(req); err != nil {
					return err
				}
				body, err := ctx.Body()
				if err != nil {
					return err
				}
				resp := strictHTTPResponse{Status: http.StatusOK, ContentType: "text/plain", Body: append([]byte(nil), body...)}
				if err := logs.serverSent.appendResponse(resp); err != nil {
					return err
				}
				return ctx.CopyBody(resp.Status, resp.ContentType)
			})
		}, requests, writeStrictHTTPRequestsCoalesced)

		assertHTTPExchangesEqual(t, logs.clientSent, logs.serverReceived)
		assertHTTPExchangesEqual(t, logs.serverSent, logs.clientReceived)
		assertHTTPResponsesEchoRequests(t, requests, logs.clientReceived.snapshot(), http.StatusOK)
	})

	t.Run("fragmented_write_echo", func(t *testing.T) {
		requests := strictHTTPRequests(http.MethodPost, "/fragmented", "fragmented", strictHTTPExchangeCount)
		logs := runHTTPWireCRCScenario(t, func(endpoint *httpx.Endpoint, logs strictHTTPLogs) {
			endpoint.Post("/fragmented", func(ctx *httpx.Context) error {
				req := strictHTTPRequestFromContext(t, ctx)
				if err := logs.serverReceived.appendRequest(req); err != nil {
					return err
				}
				body, err := ctx.Body()
				if err != nil {
					return err
				}
				resp := strictHTTPResponse{Status: http.StatusOK, ContentType: "text/plain", Body: append([]byte(nil), body...)}
				if err := logs.serverSent.appendResponse(resp); err != nil {
					return err
				}
				return ctx.CopyBody(resp.Status, resp.ContentType)
			})
		}, requests, writeStrictHTTPRequestsFragmented)

		assertHTTPExchangesEqual(t, logs.clientSent, logs.serverReceived)
		assertHTTPExchangesEqual(t, logs.serverSent, logs.clientReceived)
		assertHTTPResponsesEchoRequests(t, requests, logs.clientReceived.snapshot(), http.StatusOK)
	})

	t.Run("not_found", func(t *testing.T) {
		requests := strictHTTPRequests(http.MethodGet, "/missing", "missing", strictHTTPExchangeCount)
		logs := runHTTPWireCRCScenario(t, func(endpoint *httpx.Endpoint, _ strictHTTPLogs) {
			endpoint.Get("/exists", func(ctx *httpx.Context) error {
				return ctx.Text(http.StatusOK, "exists")
			})
		}, requests, writeStrictHTTPRequestsNormal)

		assertHTTPLogCount(t, logs.clientSent, strictHTTPExchangeCount)
		assertHTTPLogCount(t, logs.serverReceived, 0)
		assertHTTPLogCount(t, logs.serverSent, 0)
		assertHTTPResponsesBody(t, logs.clientReceived.snapshot(), http.StatusNotFound, []byte("not found"))
	})

	t.Run("method_not_allowed", func(t *testing.T) {
		requests := strictHTTPRequests(http.MethodPost, "/only-get", "method", strictHTTPExchangeCount)
		logs := runHTTPWireCRCScenario(t, func(endpoint *httpx.Endpoint, _ strictHTTPLogs) {
			endpoint.Get("/only-get", func(ctx *httpx.Context) error {
				return ctx.Text(http.StatusOK, "ok")
			})
		}, requests, writeStrictHTTPRequestsNormal)

		assertHTTPLogCount(t, logs.clientSent, strictHTTPExchangeCount)
		assertHTTPLogCount(t, logs.serverReceived, 0)
		assertHTTPLogCount(t, logs.serverSent, 0)
		assertHTTPResponsesBody(t, logs.clientReceived.snapshot(), http.StatusMethodNotAllowed, []byte("method not allowed"))
	})
}

func TestHTTPWireProtocolFaultStrict1000(t *testing.T) {
	cases := []struct {
		name  string
		build func(int, string) []byte
	}{
		{
			name: "bad_method_token",
			build: func(i int, host string) []byte {
				return []byte(fmt.Sprintf("B AD /fault?seq=%d HTTP/1.1\r\nHost: %s\r\nContent-Length: 0\r\n\r\n", i, host))
			},
		},
		{
			name: "invalid_header_line",
			build: func(i int, host string) []byte {
				return []byte(fmt.Sprintf("GET /fault?seq=%d HTTP/1.1\r\nHost: %s\r\nBroken-Header\r\nContent-Length: 0\r\n\r\n", i, host))
			},
		},
		{
			name: "invalid_content_length",
			build: func(i int, host string) []byte {
				return []byte(fmt.Sprintf("POST /fault?seq=%d HTTP/1.1\r\nHost: %s\r\nContent-Length: nope\r\n\r\nbody", i, host))
			},
		},
		{
			name: "partial_header_close",
			build: func(i int, host string) []byte {
				return []byte(fmt.Sprintf("GET /fault?seq=%d HTTP/1.1\r\nHost: %s\r\n", i, host))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs, addr, stop := startStrictHTTPServer(t, func(endpoint *httpx.Endpoint, logs strictHTTPLogs) {
				endpoint.Get("/fault", func(ctx *httpx.Context) error {
					req := strictHTTPRequestFromContext(t, ctx)
					if err := logs.serverReceived.appendRequest(req); err != nil {
						return err
					}
					resp := strictHTTPResponse{Status: http.StatusOK, ContentType: "text/plain; charset=utf-8", Body: []byte("unexpected")}
					if err := logs.serverSent.appendResponse(resp); err != nil {
						return err
					}
					return ctx.Text(resp.Status, string(resp.Body))
				})
			})
			defer stop()

			rawLog := newStrictHTTPRawLog(t, "client_malformed_"+tc.name+".ndjson")
			host := strictHTTPHostHeader(addr)
			for i := 0; i < strictHTTPExchangeCount; i++ {
				conn := dialTCP(t, addr)
				raw := tc.build(i+1, host)
				if err := rawLog.appendRaw(raw); err != nil {
					t.Fatalf("append malformed HTTP request: %v", err)
				}
				if err := writeFullForTest(conn, raw); err != nil {
					_ = conn.Close()
					t.Fatalf("write malformed HTTP request: %v", err)
				}
				_ = conn.(*net.TCPConn).CloseWrite()
				readOptionalStrictHTTPFaultResponse(conn)
				_ = conn.Close()
			}
			waitForStrictHTTPRawCount(t, rawLog, strictHTTPExchangeCount)
			assertHTTPLogCount(t, logs.serverReceived, 0)
			assertHTTPLogCount(t, logs.serverSent, 0)
		})
	}
}

type strictHTTPRequest struct {
	Method string
	Target string
	Host   string
	Body   []byte
}

type strictHTTPResponse struct {
	Status      int
	ContentType string
	Headers     map[string]string
	Body        []byte
}

type strictHTTPLogs struct {
	clientSent     *strictHTTPLog
	serverReceived *strictHTTPLog
	serverSent     *strictHTTPLog
	clientReceived *strictHTTPLog
}

type strictHTTPLog struct {
	mu      sync.Mutex
	path    string
	records []strictHTTPRecord
}

type strictHTTPRawLog struct {
	mu      sync.Mutex
	path    string
	records []strictHTTPRawRecord
}

type strictHTTPRecord struct {
	Index        int    `json:"index"`
	Kind         string `json:"kind"`
	Method       string `json:"method,omitempty"`
	Target       string `json:"target,omitempty"`
	Host         string `json:"host,omitempty"`
	Status       int    `json:"status,omitempty"`
	ContentType  string `json:"content_type,omitempty"`
	BodyLen      int    `json:"body_len"`
	BodyCRC32    string `json:"body_crc32"`
	MessageLen   int    `json:"message_len"`
	MessageCRC32 string `json:"message_crc32"`
	MessageHex   string `json:"message_hex"`
}

type strictHTTPRawRecord struct {
	Index      int    `json:"index"`
	MessageLen int    `json:"message_len"`
	CRC32      string `json:"crc32"`
	MessageHex string `json:"message_hex"`
}

type strictHTTPDiscardLogger struct{}

func (strictHTTPDiscardLogger) Printf(string, ...interface{}) {}

func runHTTPWireCRCScenario(
	t *testing.T,
	register func(*httpx.Endpoint, strictHTTPLogs),
	requests []strictHTTPRequest,
	writer func(*testing.T, net.Conn, *strictHTTPLog, []strictHTTPRequest),
) strictHTTPLogs {
	t.Helper()
	if len(requests) < strictHTTPExchangeCount {
		t.Fatalf("strict HTTP scenario has %d requests, want at least %d", len(requests), strictHTTPExchangeCount)
	}

	logs, addr, stop := startStrictHTTPServer(t, register)
	defer stop()

	conn := dialTCP(t, addr)
	defer conn.Close()
	reader := bufio.NewReader(conn)

	writer(t, conn, logs.clientSent, requests)
	for range requests {
		resp := readStrictHTTPResponse(t, reader)
		if err := logs.clientReceived.appendResponse(resp); err != nil {
			t.Fatalf("append HTTP client received response: %v", err)
		}
	}
	return logs
}

func runHTTPWireCRCMultiConnectionScenario(
	t *testing.T,
	connections int,
	register func(*httpx.Endpoint, strictHTTPLogs),
	requests []strictHTTPRequest,
) strictHTTPLogs {
	t.Helper()
	if len(requests) < strictHTTPExchangeCount {
		t.Fatalf("strict HTTP multi-connection scenario has %d requests, want at least %d", len(requests), strictHTTPExchangeCount)
	}

	logs, addr, stop := startStrictHTTPServer(t, register)
	defer stop()

	conns := make([]net.Conn, connections)
	readers := make([]*bufio.Reader, connections)
	for i := range conns {
		conns[i] = dialTCP(t, addr)
		defer conns[i].Close()
		readers[i] = bufio.NewReader(conns[i])
	}

	batches := make([][]strictHTTPRequest, connections)
	for i, req := range requests {
		batches[i%connections] = append(batches[i%connections], req)
	}
	for i, conn := range conns {
		writeStrictHTTPRequestsNormal(t, conn, logs.clientSent, batches[i])
	}
	for i := range conns {
		for range batches[i] {
			resp := readStrictHTTPResponse(t, readers[i])
			if err := logs.clientReceived.appendResponse(resp); err != nil {
				t.Fatalf("append HTTP multi client response: %v", err)
			}
		}
	}
	return logs
}

func runHTTPWireCRCInterleavedScenario(
	t *testing.T,
	register func(*httpx.Endpoint, strictHTTPLogs),
	requests []strictHTTPRequest,
) strictHTTPLogs {
	t.Helper()
	if len(requests) < strictHTTPExchangeCount {
		t.Fatalf("strict HTTP interleaved scenario has %d requests, want at least %d", len(requests), strictHTTPExchangeCount)
	}

	logs, addr, stop := startStrictHTTPServer(t, register)
	defer stop()

	conn := dialTCP(t, addr)
	defer conn.Close()
	reader := bufio.NewReader(conn)

	for _, req := range requests {
		writeStrictHTTPRequestsNormal(t, conn, logs.clientSent, []strictHTTPRequest{req})
		resp := readStrictHTTPResponse(t, reader)
		if err := logs.clientReceived.appendResponse(resp); err != nil {
			t.Fatalf("append HTTP interleaved client response: %v", err)
		}
	}
	return logs
}

func startStrictHTTPServer(t *testing.T, register func(*httpx.Endpoint, strictHTTPLogs)) (strictHTTPLogs, string, func()) {
	t.Helper()
	trace := newStrictHTTPTrace(t)
	logs := strictHTTPLogs{
		clientSent:     trace.log("client_sent.ndjson"),
		serverReceived: trace.log("server_received.ndjson"),
		serverSent:     trace.log("server_sent.ndjson"),
		clientReceived: trace.log("client_received.ndjson"),
	}

	addr := "127.0.0.1:" + freeTCPPort(t)
	endpoint := httpx.New(httpx.Config{
		Addr:            addr,
		ReadTimeout:     10 * time.Second,
		WriteTimeout:    10 * time.Second,
		IdleTimeout:     10 * time.Second,
		ShutdownTimeout: 500 * time.Millisecond,
		Logger:          strictHTTPDiscardLogger{},
	})
	register(endpoint, logs)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- endpoint.ListenAndServe(ctx)
	}()
	waitForStrictHTTPServer(t, addr)

	stop := func() {
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutdownCancel()
		_ = endpoint.Shutdown(shutdownCtx)
		select {
		case <-errCh:
		case <-time.After(2 * time.Second):
			t.Fatal("strict HTTP server did not stop")
		}
	}
	return logs, addr, stop
}

type strictHTTPTrace struct {
	t   *testing.T
	dir string
}

func newStrictHTTPTrace(t *testing.T) *strictHTTPTrace {
	t.Helper()
	dir, err := os.MkdirTemp("", "kpro-http-wire-crc-*")
	if err != nil {
		t.Fatalf("create strict HTTP trace dir: %v", err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("preserved strict HTTP trace dir: %s", dir)
			return
		}
		_ = os.RemoveAll(dir)
	})
	return &strictHTTPTrace{t: t, dir: dir}
}

func (tr *strictHTTPTrace) log(name string) *strictHTTPLog {
	tr.t.Helper()
	path := filepath.Join(tr.dir, name)
	if err := os.WriteFile(path, nil, 0644); err != nil {
		tr.t.Fatalf("create strict HTTP log %s: %v", path, err)
	}
	return &strictHTTPLog{path: path}
}

func newStrictHTTPRawLog(t *testing.T, name string) *strictHTTPRawLog {
	t.Helper()
	dir, err := os.MkdirTemp("", "kpro-http-wire-raw-*")
	if err != nil {
		t.Fatalf("create strict HTTP raw dir: %v", err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("preserved strict HTTP raw dir: %s", dir)
			return
		}
		_ = os.RemoveAll(dir)
	})
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatalf("create strict HTTP raw log %s: %v", path, err)
	}
	return &strictHTTPRawLog{path: path}
}

func (l *strictHTTPLog) appendRequest(req strictHTTPRequest) error {
	return l.appendRecord(strictHTTPRequestRecord(req))
}

func (l *strictHTTPLog) appendResponse(resp strictHTTPResponse) error {
	return l.appendRecord(strictHTTPResponseRecord(resp))
}

func (l *strictHTTPLog) appendRecord(record strictHTTPRecord) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	record.Index = len(l.records) + 1
	l.records = append(l.records, record)

	file, err := os.OpenFile(l.path, os.O_WRONLY|os.O_APPEND, 0644)
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

func (l *strictHTTPRawLog) appendRaw(data []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	copied := append([]byte(nil), data...)
	record := strictHTTPRawRecord{
		Index:      len(l.records) + 1,
		MessageLen: len(copied),
		CRC32:      strictCRC32(copied),
		MessageHex: hex.EncodeToString(copied),
	}
	l.records = append(l.records, record)

	file, err := os.OpenFile(l.path, os.O_WRONLY|os.O_APPEND, 0644)
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

func (l *strictHTTPLog) snapshot() []strictHTTPRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]strictHTTPRecord(nil), l.records...)
}

func (l *strictHTTPLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.records)
}

func (l *strictHTTPRawLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.records)
}

func writeStrictHTTPRequestsNormal(t *testing.T, conn net.Conn, log *strictHTTPLog, requests []strictHTTPRequest) {
	t.Helper()
	for _, req := range requests {
		data := canonicalHTTPRequest(req)
		if err := log.appendRequest(req); err != nil {
			t.Fatalf("append HTTP client request: %v", err)
		}
		if err := writeFullForTest(conn, data); err != nil {
			t.Fatalf("write HTTP request: %v", err)
		}
	}
}

func writeStrictHTTPRequestsCoalesced(t *testing.T, conn net.Conn, log *strictHTTPLog, requests []strictHTTPRequest) {
	t.Helper()
	var batch bytes.Buffer
	for _, req := range requests {
		data := canonicalHTTPRequest(req)
		if err := log.appendRequest(req); err != nil {
			t.Fatalf("append coalesced HTTP client request: %v", err)
		}
		if _, err := batch.Write(data); err != nil {
			t.Fatalf("buffer coalesced HTTP request: %v", err)
		}
	}
	if err := writeFullForTest(conn, batch.Bytes()); err != nil {
		t.Fatalf("write coalesced HTTP requests: %v", err)
	}
}

func writeStrictHTTPRequestsFragmented(t *testing.T, conn net.Conn, log *strictHTTPLog, requests []strictHTTPRequest) {
	t.Helper()
	for _, req := range requests {
		data := canonicalHTTPRequest(req)
		if err := log.appendRequest(req); err != nil {
			t.Fatalf("append fragmented HTTP client request: %v", err)
		}
		cuts := []int{1, 7, len(data) / 2, len(data)}
		start := 0
		for _, end := range cuts {
			if end > len(data) {
				end = len(data)
			}
			if end <= start {
				continue
			}
			if err := writeFullForTest(conn, data[start:end]); err != nil {
				t.Fatalf("write fragmented HTTP request: %v", err)
			}
			start = end
		}
	}
}

func readStrictHTTPResponse(t testing.TB, reader *bufio.Reader) strictHTTPResponse {
	t.Helper()
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("read strict HTTP response: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read strict HTTP response body: %v", err)
	}
	headers := map[string]string{}
	if value := resp.Header.Get("X-Strict-Middleware"); value != "" {
		headers["X-Strict-Middleware"] = value
	}
	return strictHTTPResponse{
		Status:      resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Headers:     headers,
		Body:        body,
	}
}

func readOptionalStrictHTTPFaultResponse(conn net.Conn) {
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

func strictHTTPRequestFromContext(t *testing.T, ctx *httpx.Context) strictHTTPRequest {
	t.Helper()
	body, err := ctx.Body()
	if err != nil {
		t.Fatalf("read strict HTTP request body: %v", err)
	}
	target := ctx.Path()
	if rawQuery := ctx.RawQuery(); rawQuery != "" {
		target += "?" + rawQuery
	}
	return strictHTTPRequest{
		Method: ctx.Method(),
		Target: target,
		Host:   ctx.Host(),
		Body:   append([]byte(nil), body...),
	}
}

func waitForStrictHTTPServer(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("strict HTTP server did not start on %s: %v", addr, lastErr)
}

func strictHTTPRequests(method, path, label string, count int) []strictHTTPRequest {
	requests := make([]strictHTTPRequest, count)
	for i := range requests {
		seq := i + 1
		target := path + "?seq=" + strconv.Itoa(seq)
		var body []byte
		if method == http.MethodPost || method == http.MethodPut {
			body = []byte(fmt.Sprintf("%s:%04d:%08x", label, seq, uint64(seq)*2654435761))
		}
		requests[i] = strictHTTPRequest{Method: method, Target: target, Body: body}
	}
	return requests
}

func strictHTTPBoundaryRequests(path string, count int) []strictHTTPRequest {
	sizes := []int{0, 1, 64, 1024, 4096, 64 * 1024}
	requests := make([]strictHTTPRequest, count)
	for i := range requests {
		size := sizes[i%len(sizes)]
		body := make([]byte, size)
		for j := range body {
			body[j] = byte('a' + (i+j)%26)
		}
		requests[i] = strictHTTPRequest{Method: http.MethodPost, Target: path + "?seq=" + strconv.Itoa(i+1), Body: body}
	}
	return requests
}

func strictHTTPRequestRecord(req strictHTTPRequest) strictHTTPRecord {
	data := canonicalHTTPRequest(req)
	return strictHTTPRecord{
		Kind:         "request",
		Method:       req.Method,
		Target:       req.Target,
		Host:         req.Host,
		BodyLen:      len(req.Body),
		BodyCRC32:    strictCRC32(req.Body),
		MessageLen:   len(data),
		MessageCRC32: strictCRC32(data),
		MessageHex:   hex.EncodeToString(data),
	}
}

func strictHTTPResponseRecord(resp strictHTTPResponse) strictHTTPRecord {
	data := canonicalHTTPResponse(resp)
	return strictHTTPRecord{
		Kind:         "response",
		Status:       resp.Status,
		ContentType:  resp.ContentType,
		BodyLen:      len(resp.Body),
		BodyCRC32:    strictCRC32(resp.Body),
		MessageLen:   len(data),
		MessageCRC32: strictCRC32(data),
		MessageHex:   hex.EncodeToString(data),
	}
}

func canonicalHTTPRequest(req strictHTTPRequest) []byte {
	host := req.Host
	if host == "" {
		host = "127.0.0.1"
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "%s %s HTTP/1.1\r\n", req.Method, req.Target)
	fmt.Fprintf(&buf, "Host: %s\r\n", host)
	fmt.Fprintf(&buf, "Content-Length: %d\r\n", len(req.Body))
	buf.WriteString("\r\n")
	buf.Write(req.Body)
	return buf.Bytes()
}

func canonicalHTTPResponse(resp strictHTTPResponse) []byte {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "HTTP/1.1 %d\r\n", resp.Status)
	if resp.ContentType != "" {
		fmt.Fprintf(&buf, "Content-Type: %s\r\n", resp.ContentType)
	}
	if value := resp.Headers["X-Strict-Middleware"]; value != "" {
		fmt.Fprintf(&buf, "X-Strict-Middleware: %s\r\n", value)
	}
	fmt.Fprintf(&buf, "Content-Length: %d\r\n", len(resp.Body))
	buf.WriteString("\r\n")
	buf.Write(resp.Body)
	return buf.Bytes()
}

func strictCRC32(data []byte) string {
	return fmt.Sprintf("%08x", crc32.ChecksumIEEE(data))
}

func strictHTTPHostHeader(addr string) string {
	if strings.HasPrefix(addr, "127.0.0.1:") {
		return addr
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return strictHTTPHost
	}
	return addr
}

func assertHTTPExchangesEqual(t *testing.T, wantLog, gotLog *strictHTTPLog) {
	t.Helper()
	want := wantLog.snapshot()
	got := gotLog.snapshot()
	if len(want) != strictHTTPExchangeCount || len(got) != strictHTTPExchangeCount {
		t.Fatalf("HTTP log counts differ: want=%d got=%d minimum=%d", len(want), len(got), strictHTTPExchangeCount)
	}
	for i := range want {
		if want[i].MessageLen != got[i].MessageLen ||
			want[i].MessageCRC32 != got[i].MessageCRC32 ||
			want[i].MessageHex != got[i].MessageHex {
			t.Fatalf("HTTP exchange %d differs\nwant len=%d crc=%s\ngot  len=%d crc=%s",
				i+1, want[i].MessageLen, want[i].MessageCRC32, got[i].MessageLen, got[i].MessageCRC32)
		}
	}
}

func assertHTTPExchangeSetEqual(t *testing.T, wantLog, gotLog *strictHTTPLog) {
	t.Helper()
	want := wantLog.snapshot()
	got := gotLog.snapshot()
	if len(want) != strictHTTPExchangeCount || len(got) != strictHTTPExchangeCount {
		t.Fatalf("HTTP log counts differ: want=%d got=%d minimum=%d", len(want), len(got), strictHTTPExchangeCount)
	}
	counts := make(map[string]int, len(want))
	for _, record := range want {
		counts[record.MessageHex]++
	}
	for _, record := range got {
		if counts[record.MessageHex] == 0 {
			t.Fatalf("unexpected HTTP exchange kind=%s crc=%s", record.Kind, record.MessageCRC32)
		}
		counts[record.MessageHex]--
	}
	for key, count := range counts {
		if count != 0 {
			t.Fatalf("missing HTTP exchange %s count=%d", key[:minInt(len(key), 32)], count)
		}
	}
}

func assertHTTPResponsesEchoRequests(t *testing.T, requests []strictHTTPRequest, got []strictHTTPRecord, status int) {
	t.Helper()
	assertHTTPResponsesMatchRequests(t, requests, got, status, func(req strictHTTPRequest) []byte {
		return req.Body
	})
}

func assertHTTPResponseSetEchoRequests(t *testing.T, requests []strictHTTPRequest, got []strictHTTPRecord, status int) {
	t.Helper()
	want := make(map[string]strictHTTPRequest, len(requests))
	for _, req := range requests {
		want[strictHTTPQueryValue(req.Target, "seq")] = req
	}
	if len(got) != len(want) {
		t.Fatalf("HTTP response count=%d, want %d", len(got), len(want))
	}
	for _, record := range got {
		body := strictHTTPRecordBody(record)
		var matchedKey string
		for key, req := range want {
			if bytes.Equal(body, req.Body) {
				matchedKey = key
				break
			}
		}
		if matchedKey == "" {
			t.Fatalf("unexpected HTTP echo response body crc=%s", record.BodyCRC32)
		}
		if record.Status != status {
			t.Fatalf("HTTP echo response status=%d, want %d", record.Status, status)
		}
		delete(want, matchedKey)
	}
	if len(want) > 0 {
		t.Fatalf("missing %d HTTP echo responses", len(want))
	}
}

func assertHTTPResponsesMatchRequests(t *testing.T, requests []strictHTTPRequest, got []strictHTTPRecord, status int, bodyFor func(strictHTTPRequest) []byte) {
	t.Helper()
	if len(got) != len(requests) {
		t.Fatalf("HTTP response count=%d, want %d", len(got), len(requests))
	}
	for i, req := range requests {
		wantBody := bodyFor(req)
		if got[i].Status != status ||
			got[i].BodyLen != len(wantBody) ||
			got[i].BodyCRC32 != strictCRC32(wantBody) ||
			!bytes.Equal(strictHTTPRecordBody(got[i]), wantBody) {
			t.Fatalf("HTTP response %d status=%d body_len=%d body_crc=%s, want status=%d body_len=%d body_crc=%s",
				i+1, got[i].Status, got[i].BodyLen, got[i].BodyCRC32, status, len(wantBody), strictCRC32(wantBody))
		}
	}
}

func assertHTTPResponsesBody(t *testing.T, got []strictHTTPRecord, status int, body []byte) {
	t.Helper()
	if len(got) != strictHTTPExchangeCount {
		t.Fatalf("HTTP response count=%d, want %d", len(got), strictHTTPExchangeCount)
	}
	for i, record := range got {
		if record.Status != status ||
			record.BodyLen != len(body) ||
			record.BodyCRC32 != strictCRC32(body) ||
			!bytes.Equal(strictHTTPRecordBody(record), body) {
			t.Fatalf("HTTP response %d status=%d body_len=%d body_crc=%s, want status=%d body_len=%d body_crc=%s",
				i+1, record.Status, record.BodyLen, record.BodyCRC32, status, len(body), strictCRC32(body))
		}
	}
}

func assertHTTPLogCount(t *testing.T, log *strictHTTPLog, count int) {
	t.Helper()
	if got := log.count(); got != count {
		t.Fatalf("HTTP log %s count=%d, want %d", log.path, got, count)
	}
}

func waitForStrictHTTPRawCount(t *testing.T, log *strictHTTPRawLog, count int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if log.count() >= count {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("strict HTTP raw log %s count=%d, want %d", log.path, log.count(), count)
}

func strictHTTPRecordBody(record strictHTTPRecord) []byte {
	data, err := hex.DecodeString(record.MessageHex)
	if err != nil {
		return nil
	}
	sep := []byte("\r\n\r\n")
	idx := bytes.Index(data, sep)
	if idx < 0 {
		return nil
	}
	return data[idx+len(sep):]
}

func strictHTTPQueryValue(target, key string) string {
	idx := strings.IndexByte(target, '?')
	if idx < 0 {
		return ""
	}
	for _, part := range strings.Split(target[idx+1:], "&") {
		name, value, ok := strings.Cut(part, "=")
		if ok && name == key {
			return value
		}
	}
	return ""
}

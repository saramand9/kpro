package compare

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/saramand9/bignet/kpro/transport/httpx"
)

func BenchmarkKProHTTP_Echo(b *testing.B) {
	for _, size := range benchmarkPayloadSizes {
		b.Run(payloadName(size), func(b *testing.B) {
			benchmarkKProHTTPEcho(b, size)
		})
	}
}

func BenchmarkKProHTTP_Echo_Concurrent(b *testing.B) {
	for _, size := range benchmarkPayloadSizes {
		for _, workers := range benchmarkConcurrentWorkers {
			b.Run(concurrentPayloadName(size, workers), func(b *testing.B) {
				benchmarkKProHTTPEchoConcurrent(b, size, workers)
			})
		}
	}
}

func benchmarkKProHTTPEcho(b *testing.B, payloadSize int) {
	addr, stop, client := startKProHTTPBenchmarkServer(b)
	defer stop()

	payload := bytes.Repeat([]byte("x"), payloadSize)
	url := "http://" + addr + "/echo"

	b.SetBytes(int64(payloadSize))
	b.ReportAllocs()
	started := startBenchmarkTimer(b)
	for i := 0; i < b.N; i++ {
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			b.Fatalf("create KPro HTTP request: %v", err)
		}
		req.Header.Set("Content-Type", "text/plain")
		resp, err := client.Do(req)
		if err != nil {
			b.Fatalf("do KPro HTTP request: %v", err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			b.Fatalf("read KPro HTTP response: %v", err)
		}
		if resp.StatusCode != http.StatusOK || len(body) != len(payload) {
			b.Fatalf("unexpected KPro HTTP response: status=%d len=%d", resp.StatusCode, len(body))
		}
	}
	reportBenchmarkMetrics(b, started, payloadSize)
}

func benchmarkKProHTTPEchoConcurrent(b *testing.B, payloadSize, workers int) {
	addr, stop, client := startKProHTTPBenchmarkServer(b)
	defer stop()

	payload := bytes.Repeat([]byte("x"), payloadSize)
	url := "http://" + addr + "/echo"
	benchmarkHTTPPostEchoConcurrent(b, client, url, payload, workers)
}

func startKProHTTPBenchmarkServer(b *testing.B) (string, func(), *http.Client) {
	b.Helper()

	addr := "127.0.0.1:" + freeTCPPort(b)
	endpoint := httpx.New(httpx.Config{
		Addr:            addr,
		ReadTimeout:     10 * time.Second,
		WriteTimeout:    10 * time.Second,
		IdleTimeout:     30 * time.Second,
		ShutdownTimeout: 500 * time.Millisecond,
	})
	endpoint.Post("/echo", func(ctx *httpx.Context) error {
		return ctx.CopyBody(http.StatusOK, "text/plain; charset=utf-8")
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- endpoint.ListenAndServe(ctx)
	}()
	waitForTCPServer(b, addr)

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100,
		},
	}
	stop := func() {
		cancel()
		client.CloseIdleConnections()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer shutdownCancel()
		_ = endpoint.Shutdown(shutdownCtx)
		select {
		case <-errCh:
		case <-time.After(3 * time.Second):
			b.Fatal("KPro HTTP benchmark server did not stop")
		}
	}
	return addr, stop, client
}

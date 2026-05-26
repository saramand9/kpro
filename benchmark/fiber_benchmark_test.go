package benchmark

import (
	"bytes"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
)

func BenchmarkFiberHTTP_Echo(b *testing.B) {
	for _, size := range benchmarkPayloadSizes {
		b.Run(payloadName(size), func(b *testing.B) {
			benchmarkFiberHTTPEcho(b, size)
		})
	}
}

func BenchmarkFiberHTTP_Echo_Concurrent(b *testing.B) {
	for _, size := range benchmarkPayloadSizes {
		for _, workers := range benchmarkConcurrentWorkers {
			b.Run(concurrentPayloadName(size, workers), func(b *testing.B) {
				benchmarkFiberHTTPEchoConcurrent(b, size, workers)
			})
		}
	}
}

func benchmarkFiberHTTPEcho(b *testing.B, payloadSize int) {
	addr, stop, client := startFiberHTTPBenchmarkServer(b)
	defer stop()

	payload := bytes.Repeat([]byte("x"), payloadSize)
	url := "http://" + addr + "/echo"

	b.SetBytes(int64(payloadSize))
	b.ReportAllocs()
	started := startBenchmarkTimer(b)
	for i := 0; i < b.N; i++ {
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			b.Fatalf("create Fiber HTTP request: %v", err)
		}
		req.Header.Set("Content-Type", "text/plain")
		resp, err := client.Do(req)
		if err != nil {
			b.Fatalf("do Fiber HTTP request: %v", err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			b.Fatalf("read Fiber HTTP response: %v", err)
		}
		if resp.StatusCode != http.StatusOK || len(body) != len(payload) {
			b.Fatalf("unexpected Fiber HTTP response: status=%d len=%d", resp.StatusCode, len(body))
		}
	}
	reportBenchmarkMetrics(b, started, payloadSize)
}

func benchmarkFiberHTTPEchoConcurrent(b *testing.B, payloadSize, workers int) {
	addr, stop, client := startFiberHTTPBenchmarkServer(b)
	defer stop()

	payload := bytes.Repeat([]byte("x"), payloadSize)
	url := "http://" + addr + "/echo"
	benchmarkHTTPPostEchoConcurrent(b, client, url, payload, workers)
}

func startFiberHTTPBenchmarkServer(b *testing.B) (string, func(), *http.Client) {
	b.Helper()

	addr := "127.0.0.1:" + freeTCPPort(b)
	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
	})
	app.Post("/echo", func(c *fiber.Ctx) error {
		return c.Send(c.Body())
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- app.Listen(addr)
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
		client.CloseIdleConnections()
		_ = app.ShutdownWithTimeout(3 * time.Second)
		select {
		case <-errCh:
		case <-time.After(3 * time.Second):
			b.Fatal("Fiber HTTP benchmark server did not stop")
		}
	}
	return addr, stop, client
}

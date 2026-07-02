// File: benchmark/compare/helpers_test.go
// Purpose: Benchmarks KPro transports and comparison baselines.

package compare

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/protocol"
)

var benchmarkPayloadSizes = []int{64, 1024}
var benchmarkConcurrentWorkers = []int{16, 64}

func benchmarkHTTPPostEchoConcurrent(b *testing.B, client *http.Client, url string, payload []byte, workers int) {
	b.Helper()
	if workers <= 0 {
		b.Fatal("workers must be positive")
	}

	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()

	var next uint64
	var failed atomic.Bool
	var firstErr atomic.Value
	storeErr := func(format string, args ...interface{}) {
		if failed.CompareAndSwap(false, true) {
			firstErr.Store(fmt.Sprintf(format, args...))
		}
	}

	started := startBenchmarkTimer(b)
	var wg sync.WaitGroup
	wg.Add(workers)
	for worker := 0; worker < workers; worker++ {
		go func() {
			defer wg.Done()
			for {
				if failed.Load() {
					return
				}
				i := int(atomic.AddUint64(&next, 1)) - 1
				if i >= b.N {
					return
				}
				req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
				if err != nil {
					storeErr("create HTTP request: %v", err)
					return
				}
				req.Header.Set("Content-Type", "text/plain")
				resp, err := client.Do(req)
				if err != nil {
					storeErr("do HTTP request: %v", err)
					return
				}
				body, err := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if err != nil {
					storeErr("read HTTP response: %v", err)
					return
				}
				if resp.StatusCode != http.StatusOK || len(body) != len(payload) {
					storeErr("unexpected HTTP response: status=%d len=%d", resp.StatusCode, len(body))
					return
				}
			}
		}()
	}
	wg.Wait()
	reportBenchmarkMetrics(b, started, len(payload))

	if value := firstErr.Load(); value != nil {
		b.Fatal(value.(string))
	}
}

func startBenchmarkTimer(b *testing.B) time.Time {
	b.Helper()
	b.ResetTimer()
	return time.Now()
}

func reportBenchmarkMetrics(b *testing.B, started time.Time, bytesPerOp int) {
	b.Helper()
	elapsed := time.Since(started)
	b.StopTimer()
	if b.N <= 0 {
		return
	}
	if elapsed <= 0 {
		elapsed = time.Nanosecond
	}

	opsPerSecond := float64(b.N) / elapsed.Seconds()
	b.ReportMetric(float64(elapsed.Nanoseconds())/float64(b.N), "ns/op")
	b.ReportMetric(float64(bytesPerOp)*opsPerSecond/1e6, "MB/s")
	b.ReportMetric(opsPerSecond, "qps")
}

func startBenchmarkMessageTransport(
	b *testing.B,
	addr string,
	router *core.Router,
	listen func(context.Context, core.Handler) error,
	shutdown func(context.Context) error,
) (string, func()) {
	b.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- listen(ctx, router.Dispatch)
	}()
	waitForTCPServer(b, addr)

	stop := func() {
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer shutdownCancel()
		_ = shutdown(shutdownCtx)
		_ = router.Shutdown(shutdownCtx)
		select {
		case <-errCh:
		case <-time.After(3 * time.Second):
			b.Fatal("benchmark server did not stop")
		}
	}
	return addr, stop
}

func dialTCPBenchmark(b *testing.B, addr string) net.Conn {
	b.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			return conn
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	b.Fatalf("dial TCP %s: %v", addr, lastErr)
	return nil
}

func writeBenchmarkMessage(b *testing.B, conn net.Conn, msg core.Message) {
	b.Helper()
	frame, err := protocol.KProV1().Encode(msg)
	if err != nil {
		b.Fatalf("encode message: %v", err)
	}
	if _, err := conn.Write(frame); err != nil {
		b.Fatalf("write message: %v", err)
	}
}

func readBenchmarkMessage(b *testing.B, conn net.Conn) core.Message {
	b.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	result, err := protocol.KProV1().Read(conn, 64*1024)
	if err != nil {
		b.Fatalf("read message: %v", err)
	}
	return result.Message
}

func freeTCPPort(tb testing.TB) string {
	tb.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("listen free TCP port: %v", err)
	}
	defer ln.Close()
	return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
}

func waitForTCPServer(tb testing.TB, addr string) {
	tb.Helper()
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
	tb.Fatalf("TCP server did not start on %s: %v", addr, lastErr)
}

func payloadName(size int) string {
	return fmt.Sprintf("%dB", size)
}

func concurrentPayloadName(size, workers int) string {
	return fmt.Sprintf("%dB/%dc", size, workers)
}

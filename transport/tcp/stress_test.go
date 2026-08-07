package tcp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
)

const (
	defaultTCPFaultFanout = 128
	tcpStressEnv          = "KPRO_TCP_STRESS_CONNECTIONS"
	tcpSoakDurationEnv    = "KPRO_TCP_SOAK_DURATION"
	tcpSoakClientsEnv     = "KPRO_TCP_SOAK_CLIENTS"
	tcpSoakPayloadEnv     = "KPRO_TCP_SOAK_PAYLOAD_BYTES"
)

func TestTCPFaultShutdownConnectionFanout(t *testing.T) {
	runTCPShutdownFanout(t, defaultTCPFaultFanout)
}

func TestTCPStressShutdownThousandsOfConnections(t *testing.T) {
	raw, enabled := os.LookupEnv(tcpStressEnv)
	if !enabled {
		t.Skipf("set %s=1000 to run the real-socket stress case", tcpStressEnv)
	}
	connectionCount := parsePositiveTestInt(t, tcpStressEnv, raw)
	if connectionCount < 1000 {
		t.Fatalf("%s = %d, want at least 1000", tcpStressEnv, connectionCount)
	}
	runTCPShutdownFanout(t, connectionCount)
}

func TestTCPSoakEchoStability(t *testing.T) {
	rawDuration, enabled := os.LookupEnv(tcpSoakDurationEnv)
	if !enabled {
		t.Skipf("set %s=2m to run the real-socket soak case", tcpSoakDurationEnv)
	}
	duration, err := time.ParseDuration(rawDuration)
	if err != nil || duration <= 0 {
		t.Fatalf("%s = %q, want a positive Go duration", tcpSoakDurationEnv, rawDuration)
	}
	clientCount := positiveTestIntFromEnv(t, tcpSoakClientsEnv, 32)
	payloadBytes := positiveTestIntFromEnv(t, tcpSoakPayloadEnv, 256)

	baselineGoroutines := runtime.NumGoroutine()
	var handled atomic.Uint64
	server, addr, stopServer := startTCPTestServer(t, Config{
		Addr:                   freeTCPTestAddr(t),
		Codec:                  codec.KProV1(),
		MaxConnections:         clientCount,
		SendQueueSize:          16,
		MaxRetainedBufferBytes: 1 << 20,
		ReadTimeout:            duration + 10*time.Second,
		WriteTimeout:           2 * time.Second,
	}, func(ctx core.Context) error {
		handled.Add(1)
		message := ctx.Message()
		return ctx.Reply(message.ID, message.Data)
	})
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			stopServer()
		}
	})

	connections := dialTCPFaultClients(t, addr, clientCount)
	defer closeTCPFaultClients(connections)
	payload := bytes.Repeat([]byte("k"), payloadBytes)
	frames := make([][]byte, clientCount)
	wants := make([]core.Message, clientCount)
	for index, connection := range connections {
		want := core.Message{ID: 450, Seq: uint64(index + 1), Data: payload}
		frame, encodeErr := codec.KProV1().Encode(want)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		frames[index] = frame
		wants[index] = want
		if roundTripErr := tcpFaultRoundTrip(connection, frame, want); roundTripErr != nil {
			t.Fatalf("warm-up client %d: %v", index, roundTripErr)
		}
	}
	waitForTCPActiveCountWithin(t, server, int64(clientCount), 5*time.Second)
	runtime.GC()
	runtime.GC()
	var warm runtime.MemStats
	runtime.ReadMemStats(&warm)

	started := time.Now()
	stopAt := started.Add(duration)
	var completed atomic.Uint64
	errorsByClient := make(chan error, clientCount)
	var clients sync.WaitGroup
	clients.Add(clientCount)
	for index, connection := range connections {
		index := index
		connection := connection
		go func() {
			defer clients.Done()
			if deadlineErr := connection.SetDeadline(stopAt.Add(5 * time.Second)); deadlineErr != nil {
				errorsByClient <- fmt.Errorf("client %d set deadline: %w", index, deadlineErr)
				return
			}
			for time.Now().Before(stopAt) {
				if roundTripErr := tcpFaultRoundTrip(connection, frames[index], wants[index]); roundTripErr != nil {
					errorsByClient <- fmt.Errorf("client %d round trip: %w", index, roundTripErr)
					return
				}
				completed.Add(1)
			}
		}()
	}
	clients.Wait()
	close(errorsByClient)
	for clientErr := range errorsByClient {
		if clientErr != nil {
			t.Error(clientErr)
		}
	}
	closeTCPFaultClients(connections)
	stopServer()
	stopped = true
	waitForTCPActiveCountWithin(t, server, 0, 5*time.Second)

	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > baselineGoroutines+8 && time.Now().Before(deadline) {
		runtime.GC()
		time.Sleep(25 * time.Millisecond)
	}
	runtime.GC()
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	elapsed := time.Since(started)
	completedCount := completed.Load()
	handledCount := handled.Load()
	if completedCount == 0 {
		t.Fatal("soak completed no round trips")
	}
	if handledCount != completedCount+uint64(clientCount) {
		t.Fatalf("handled messages = %d, want %d including warm-up", handledCount, completedCount+uint64(clientCount))
	}
	throughput := float64(completedCount) / elapsed.Seconds()
	t.Logf(
		"tcp soak: duration=%s clients=%d payload_bytes=%d completed=%d throughput=%.0f msg/s heap_alloc_before=%d heap_alloc_after=%d heap_objects_before=%d heap_objects_after=%d goroutines_baseline=%d goroutines_after=%d",
		elapsed.Round(time.Millisecond),
		clientCount,
		payloadBytes,
		completedCount,
		throughput,
		warm.HeapAlloc,
		after.HeapAlloc,
		warm.HeapObjects,
		after.HeapObjects,
		baselineGoroutines,
		runtime.NumGoroutine(),
	)

	if got := runtime.NumGoroutine(); got > baselineGoroutines+8 {
		t.Fatalf("goroutines after soak = %d, baseline = %d", got, baselineGoroutines)
	}
	allowedHeapGrowth := uint64(16<<20) + uint64(clientCount*64*1024)
	if after.HeapAlloc > warm.HeapAlloc+allowedHeapGrowth {
		t.Fatalf(
			"post-GC heap growth = %d bytes, guard = %d bytes",
			after.HeapAlloc-warm.HeapAlloc,
			allowedHeapGrowth,
		)
	}
	allowedObjectGrowth := uint64(10_000 + clientCount*128)
	if after.HeapObjects > warm.HeapObjects+allowedObjectGrowth {
		t.Fatalf(
			"post-GC heap object growth = %d, guard = %d",
			after.HeapObjects-warm.HeapObjects,
			allowedObjectGrowth,
		)
	}
}

func runTCPShutdownFanout(t *testing.T, connectionCount int) {
	t.Helper()
	server, addr, stop := startTCPTestServer(t, Config{
		Addr:           freeTCPTestAddr(t),
		Codec:          codec.KProV1(),
		MaxConnections: connectionCount,
		ReadTimeout:    30 * time.Second,
		WriteTimeout:   time.Second,
	}, func(core.Context) error { return nil })
	defer stop()

	connections := dialTCPFaultClients(t, addr, connectionCount)
	defer closeTCPFaultClients(connections)
	waitForTCPActiveCountWithin(t, server, int64(connectionCount), 10*time.Second)
	started := time.Now()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	err := server.Shutdown(shutdownCtx)
	cancelShutdown()
	if err != nil {
		t.Fatalf("Shutdown() with %d connections error = %v", connectionCount, err)
	}
	waitForTCPActiveCountWithin(t, server, 0, 5*time.Second)
	for index, connection := range connections {
		if deadlineErr := connection.SetReadDeadline(time.Now().Add(2 * time.Second)); deadlineErr != nil {
			t.Fatalf("client %d SetReadDeadline: %v", index, deadlineErr)
		}
		var one [1]byte
		if count, readErr := connection.Read(one[:]); readErr == nil {
			t.Fatalf("client %d read %d bytes after server shutdown", index, count)
		} else if networkErr, ok := readErr.(net.Error); ok && networkErr.Timeout() {
			t.Fatalf("client %d remained open after server shutdown", index)
		}
	}
	t.Logf("tcp shutdown fanout: connections=%d elapsed=%s", connectionCount, time.Since(started).Round(time.Millisecond))
}

func dialTCPFaultClients(t *testing.T, address string, connectionCount int) []net.Conn {
	t.Helper()
	connections := make([]net.Conn, connectionCount)
	jobs := make(chan int)
	errorsByIndex := make([]error, connectionCount)
	workerCount := min(connectionCount, 32)
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for index := range jobs {
				connection, err := dialTCPFaultClient(address, 10*time.Second)
				if err != nil {
					errorsByIndex[index] = err
					continue
				}
				connections[index] = connection
			}
		}()
	}
	for index := range connectionCount {
		jobs <- index
	}
	close(jobs)
	workers.Wait()
	for index, err := range errorsByIndex {
		if err != nil {
			closeTCPFaultClients(connections)
			t.Fatalf("dial client %d/%d: %v", index+1, connectionCount, err)
		}
	}
	return connections
}

func dialTCPFaultClient(address string, timeout time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", address, time.Second)
		if err == nil {
			return connection, nil
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	return nil, fmt.Errorf("dial %s within %s: %w", address, timeout, lastErr)
}

func closeTCPFaultClients(connections []net.Conn) {
	for _, connection := range connections {
		if connection != nil {
			_ = connection.Close()
		}
	}
}

func tcpFaultRoundTrip(connection net.Conn, frame []byte, want core.Message) error {
	remaining := frame
	for len(remaining) > 0 {
		written, err := connection.Write(remaining)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		remaining = remaining[written:]
	}
	result, err := codec.KProV1().Read(connection, DefaultMaxFrameSize)
	if err != nil {
		return err
	}
	if result.Message.ID != want.ID || result.Message.Seq != want.Seq || !bytes.Equal(result.Message.Data, want.Data) {
		return fmt.Errorf("response = %+v, want ID=%d Seq=%d payload_bytes=%d", result.Message, want.ID, want.Seq, len(want.Data))
	}
	return nil
}

func positiveTestIntFromEnv(t *testing.T, name string, fallback int) int {
	t.Helper()
	raw, ok := os.LookupEnv(name)
	if !ok {
		return fallback
	}
	return parsePositiveTestInt(t, name, raw)
}

func parsePositiveTestInt(t *testing.T, name, raw string) int {
	t.Helper()
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		t.Fatalf("%s = %q, want a positive integer", name, raw)
	}
	return value
}

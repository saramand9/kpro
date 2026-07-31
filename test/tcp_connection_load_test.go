// File: test/tcp_connection_load_test.go
// Purpose: Provides an opt-in TCP connection lifecycle and round-trip load check.

package test

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/transport/tcp"
)

const msgTCPConnectionLoad uint32 = 911

func TestTCPConnectionLoad(t *testing.T) {
	if os.Getenv("KPRO_TCP_CONNECTION_LOAD_TEST") != "1" {
		t.Skip("set KPRO_TCP_CONNECTION_LOAD_TEST=1 to run the TCP connection load check")
	}

	connections := envInt("KPRO_TCP_LOAD_CONNECTIONS", 1000)
	messagesPerConnection := envInt("KPRO_TCP_LOAD_MESSAGES", 10)
	timeout := envDuration("KPRO_TCP_LOAD_TIMEOUT", 2*time.Minute)
	if connections <= 0 || messagesPerConnection <= 0 || timeout <= 0 {
		t.Fatalf(
			"invalid load dimensions: connections=%d messages=%d timeout=%s",
			connections,
			messagesPerConnection,
			timeout,
		)
	}

	runtime.GC()
	goroutinesBefore := runtime.NumGoroutine()
	var memoryBefore runtime.MemStats
	runtime.ReadMemStats(&memoryBefore)

	addr := "127.0.0.1:" + freeTCPPort(t)
	var handled atomic.Uint64
	transport := tcp.New(tcp.Config{
		Addr:           addr,
		Codec:          codec.KProV1(),
		MaxConnections: connections,
		SendQueueSize:  16,
		ReadTimeout:    3 * timeout,
		WriteTimeout:   timeout,
	})
	serveCtx, cancelServe := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- transport.ListenAndServe(serveCtx, func(ctx core.Context) error {
			handled.Add(1)
			return ctx.Session().Send(ctx.Message())
		})
	}()

	select {
	case <-transport.Ready():
	case err := <-errCh:
		cancelServe()
		t.Fatalf("start TCP connection load server: %v", err)
	case <-time.After(minDuration(timeout, 5*time.Second)):
		cancelServe()
		t.Fatal("TCP connection load server did not become ready")
	}

	serverStopped := false
	defer func() {
		cancelServe()
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), minDuration(timeout, 10*time.Second))
		defer cancelShutdown()
		if err := transport.Shutdown(shutdownCtx); err != nil {
			t.Errorf("shutdown TCP connection load server: %v", err)
		}
		if !serverStopped {
			select {
			case err := <-errCh:
				if err != nil {
					t.Errorf("TCP connection load server stopped with error: %v", err)
				}
			case <-shutdownCtx.Done():
				t.Error("TCP connection load server did not stop")
			}
		}
	}()

	firstStarted := time.Now()
	firstWave, err := openTCPConnectionLoadWave(addr, connections, messagesPerConnection, timeout)
	if err != nil {
		t.Fatal(err)
	}
	goroutinesAtCapacity := runtime.NumGoroutine()
	t.Logf(
		"first TCP connection wave passed: connections=%d messages_per_connection=%d total_round_trips=%d elapsed=%s",
		connections,
		messagesPerConnection,
		uint64(connections)*uint64(messagesPerConnection),
		time.Since(firstStarted).Truncate(time.Millisecond),
	)

	closeTCPConnectionLoadWave(firstWave)

	recycleStarted := time.Now()
	recycledWave, err := openTCPConnectionLoadWave(addr, connections, 1, timeout)
	if err != nil {
		t.Fatalf("connection-slot recycle wave: %v", err)
	}
	t.Logf(
		"connection-slot recycle wave passed: connections=%d elapsed=%s",
		connections,
		time.Since(recycleStarted).Truncate(time.Millisecond),
	)
	closeTCPConnectionLoadWave(recycledWave)

	cancelServe()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), minDuration(timeout, 10*time.Second))
	defer cancelShutdown()
	if err := transport.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown TCP connection load server: %v", err)
	}
	select {
	case err := <-errCh:
		serverStopped = true
		if err != nil {
			t.Fatalf("TCP connection load server stopped with error: %v", err)
		}
	case <-shutdownCtx.Done():
		t.Fatal("TCP connection load server did not stop")
	}

	runtime.GC()
	var memoryAfter runtime.MemStats
	runtime.ReadMemStats(&memoryAfter)
	wantHandled := uint64(connections) * uint64(messagesPerConnection+1)
	if got := handled.Load(); got < wantHandled {
		t.Fatalf("handled messages=%d, want at least %d", got, wantHandled)
	}
	t.Logf(
		"TCP connection lifecycle observations: handled=%d goroutines_before=%d goroutines_at_capacity=%d goroutines_after=%d heap_before=%d heap_after=%d",
		handled.Load(),
		goroutinesBefore,
		goroutinesAtCapacity,
		runtime.NumGoroutine(),
		memoryBefore.HeapAlloc,
		memoryAfter.HeapAlloc,
	)
}

type tcpConnectionLoadResult struct {
	index int
	conn  net.Conn
	err   error
}

func openTCPConnectionLoadWave(addr string, connections, messagesPerConnection int, timeout time.Duration) ([]net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	results := make(chan tcpConnectionLoadResult, connections)
	var wg sync.WaitGroup
	for index := 0; index < connections; index++ {
		index := index
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := openVerifiedTCPConnection(ctx, addr, index, messagesPerConnection)
			results <- tcpConnectionLoadResult{index: index, conn: conn, err: err}
		}()
	}
	wg.Wait()
	close(results)

	opened := make([]net.Conn, connections)
	var firstErr error
	for result := range results {
		if result.err != nil && firstErr == nil {
			firstErr = result.err
		}
		opened[result.index] = result.conn
	}
	if firstErr != nil {
		closeTCPConnectionLoadWave(opened)
		return nil, firstErr
	}
	return opened, nil
}

func openVerifiedTCPConnection(ctx context.Context, addr string, clientID, messages int) (net.Conn, error) {
	var lastErr error
	dialer := net.Dialer{}
	for {
		if err := ctx.Err(); err != nil {
			if lastErr == nil {
				lastErr = err
			}
			return nil, fmt.Errorf("client %d establish verified connection: %w", clientID, lastErr)
		}

		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err == nil {
			deadline := time.Now().Add(10 * time.Second)
			if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
				deadline = contextDeadline
			}
			err = conn.SetDeadline(deadline)
			if err == nil {
				err = runTCPConnectionLoadRoundTrips(conn, clientID, messages)
			}
			if err == nil {
				err = conn.SetDeadline(time.Time{})
				if err == nil {
					return conn, nil
				}
			}
			_ = conn.Close()
		}
		lastErr = err

		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func runTCPConnectionLoadRoundTrips(conn net.Conn, clientID, messages int) error {
	streamCodec := codec.KProV1()
	for messageIndex := 0; messageIndex < messages; messageIndex++ {
		payload := make([]byte, 8)
		binary.BigEndian.PutUint32(payload[0:4], uint32(clientID))
		binary.BigEndian.PutUint32(payload[4:8], uint32(messageIndex))
		sequence := uint64(uint32(clientID))<<32 | uint64(uint32(messageIndex)+1)
		message := core.Message{
			ID:   msgTCPConnectionLoad,
			Seq:  sequence,
			Data: payload,
		}
		frame, err := streamCodec.Encode(message)
		if err != nil {
			return fmt.Errorf("encode message %d: %w", messageIndex, err)
		}
		if err := writeAllTCPOrderStress(conn, frame); err != nil {
			return fmt.Errorf("write message %d: %w", messageIndex, err)
		}
		result, err := streamCodec.Read(conn, 64*1024)
		if err != nil {
			return fmt.Errorf("read message %d: %w", messageIndex, err)
		}
		if err := compareTCPConnectionLoadMessage(result.Message, message); err != nil {
			return fmt.Errorf("message %d: %w", messageIndex, err)
		}
	}
	return nil
}

func compareTCPConnectionLoadMessage(got, want core.Message) error {
	if got.ID != want.ID || got.Seq != want.Seq {
		return fmt.Errorf("response id/seq=%d/%d, want %d/%d", got.ID, got.Seq, want.ID, want.Seq)
	}
	if len(got.Data) != len(want.Data) {
		return fmt.Errorf("response payload length=%d, want %d", len(got.Data), len(want.Data))
	}
	for index := range want.Data {
		if got.Data[index] != want.Data[index] {
			return errors.New("response payload mismatch")
		}
	}
	return nil
}

func closeTCPConnectionLoadWave(connections []net.Conn) {
	for _, conn := range connections {
		if conn != nil {
			_ = conn.Close()
		}
	}
}

func envDuration(name string, fallback time.Duration) time.Duration {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func minDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}

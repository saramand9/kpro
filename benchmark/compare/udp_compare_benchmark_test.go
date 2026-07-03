// File: benchmark/compare/udp_compare_benchmark_test.go
// Purpose: Benchmarks KPro transports and comparison baselines.

package compare

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/panjf2000/gnet/v2"
	"github.com/saramand9/bignet/kpro/codec"
	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/transport/udp"
)

func BenchmarkKProUDP_Echo(b *testing.B) {
	for _, size := range benchmarkPayloadSizes {
		b.Run(payloadName(size), func(b *testing.B) {
			addr, stop := startKProUDPCompareServer(b)
			defer stop()
			benchmarkUDPCompareClient(b, addr, size)
		})
	}
}

func BenchmarkGNetUDP_Echo(b *testing.B) {
	for _, size := range benchmarkPayloadSizes {
		b.Run(payloadName(size), func(b *testing.B) {
			addr, stop := startGNetUDPCompareServer(b)
			defer stop()
			benchmarkUDPCompareClient(b, addr, size)
		})
	}
}

func benchmarkUDPCompareClient(b *testing.B, addr string, payloadSize int) {
	b.Helper()

	remote, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		b.Fatalf("resolve UDP addr: %v", err)
	}
	conn, err := net.DialUDP("udp", nil, remote)
	if err != nil {
		b.Fatalf("dial UDP addr: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadBuffer(4 * 1024 * 1024)
	_ = conn.SetWriteBuffer(4 * 1024 * 1024)

	payload := bytes.Repeat([]byte("x"), payloadSize)
	msg := core.Message{ID: benchmarkMsgEcho, Data: payload}
	buf := make([]byte, 64*1024)

	b.SetBytes(int64(payloadSize))
	b.ReportAllocs()
	started := startBenchmarkTimer(b)
	for i := 0; i < b.N; i++ {
		msg.Seq = uint64(i + 1)
		if err := udpCompareEchoOnce(conn, msg, len(payload), buf); err != nil {
			b.Fatalf("udp echo: %v", err)
		}
	}
	reportBenchmarkMetrics(b, started, payloadSize)
}

func udpCompareEchoOnce(conn *net.UDPConn, msg core.Message, wantLen int, buf []byte) error {
	packet, err := codec.KProV1().EncodePacket(msg)
	if err != nil {
		return fmt.Errorf("encode packet: %w", err)
	}
	n, err := conn.Write(packet)
	if err != nil {
		return fmt.Errorf("write packet: %w", err)
	}
	if n != len(packet) {
		return fmt.Errorf("short packet write: n=%d len=%d", n, len(packet))
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err = conn.Read(buf)
	if err != nil {
		return fmt.Errorf("read packet: %w", err)
	}
	got, err := codec.KProV1().DecodePacket(buf[:n])
	if err != nil {
		return fmt.Errorf("decode packet: %w", err)
	}
	if got.Seq != msg.Seq || got.ID != msg.ID || len(got.Data) != wantLen {
		return fmt.Errorf("unexpected response: id=%d seq=%d len=%d", got.ID, got.Seq, len(got.Data))
	}
	return nil
}

func startKProUDPCompareServer(b *testing.B) (string, func()) {
	b.Helper()

	addr := "127.0.0.1:" + freeUDPPort(b)
	router := benchmarkUDPEchoRouter()
	transport := udp.New(udp.Config{
		Addr:          addr,
		Codec:         codec.KProV1(),
		MaxPacketSize: 64 * 1024,
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- transport.ListenAndServe(ctx, router.Dispatch)
	}()
	waitForUDPEchoServer(b, addr)

	stop := func() {
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer shutdownCancel()
		_ = transport.Shutdown(shutdownCtx)
		_ = router.Shutdown(shutdownCtx)
		select {
		case <-errCh:
		case <-time.After(3 * time.Second):
			b.Fatal("KPro UDP benchmark server did not stop")
		}
	}
	return addr, stop
}

func startGNetUDPCompareServer(b *testing.B) (string, func()) {
	b.Helper()

	addr := "127.0.0.1:" + freeUDPPort(b)
	protoAddr := "udp://" + addr
	server := &gnetUDPCompareEchoServer{}
	errCh := make(chan error, 1)
	go func() {
		errCh <- gnet.Run(
			server,
			protoAddr,
			gnet.WithMulticore(true),
			gnet.WithReadBufferCap(64*1024),
			gnet.WithWriteBufferCap(64*1024),
			gnet.WithLogger(gnetDiscardLogger{}),
		)
	}()
	waitForUDPEchoServer(b, addr)

	stop := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if server.engine.Validate() == nil {
			_ = server.engine.Stop(ctx)
		} else {
			_ = gnet.Stop(ctx, protoAddr)
		}
		select {
		case err := <-errCh:
			if err != nil {
				b.Fatalf("gnet UDP benchmark server stopped with error: %v", err)
			}
		case <-time.After(3 * time.Second):
			b.Fatal("gnet UDP benchmark server did not stop")
		}
	}
	return addr, stop
}

type gnetUDPCompareEchoServer struct {
	gnet.BuiltinEventEngine
	engine gnet.Engine
}

type gnetDiscardLogger struct{}

func (gnetDiscardLogger) Debugf(string, ...any) {}
func (gnetDiscardLogger) Infof(string, ...any)  {}
func (gnetDiscardLogger) Warnf(string, ...any)  {}
func (gnetDiscardLogger) Errorf(string, ...any) {}
func (gnetDiscardLogger) Fatalf(string, ...any) {}

func (s *gnetUDPCompareEchoServer) OnBoot(engine gnet.Engine) gnet.Action {
	s.engine = engine
	return gnet.None
}

func (s *gnetUDPCompareEchoServer) OnTraffic(conn gnet.Conn) gnet.Action {
	packet, err := conn.Next(-1)
	if err != nil {
		return gnet.Close
	}
	msg, err := codec.KProV1().DecodePacket(packet)
	if err != nil {
		return gnet.None
	}
	if msg.ID == core.MsgHeartbeat {
		msg.ID = core.MsgPong
		msg.Data = nil
	}
	reply, err := codec.KProV1().EncodePacket(msg)
	if err != nil {
		return gnet.None
	}
	if _, err := conn.Write(reply); err != nil {
		return gnet.Close
	}
	return gnet.None
}

func benchmarkUDPEchoRouter() *core.Router {
	router := core.NewRouter(core.RouterConfig{
		SyncQueueSize:         4096,
		AsyncWorkers:          1,
		AsyncQueueSize:        4096,
		MaxInflightPerSession: 4096,
		SessionIdleTimeout:    time.Minute,
	})
	router.HandleSync(benchmarkMsgEcho, func(ctx core.Context) error {
		msg := ctx.Message()
		return ctx.Reply(msg.ID, msg.Data)
	})
	return router
}

func freeUDPPort(tb testing.TB) string {
	tb.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		tb.Fatalf("listen free UDP port: %v", err)
	}
	defer conn.Close()
	return fmt.Sprintf("%d", conn.LocalAddr().(*net.UDPAddr).Port)
}

func waitForUDPEchoServer(tb testing.TB, addr string) {
	tb.Helper()
	remote, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		tb.Fatalf("resolve UDP addr %s: %v", addr, err)
	}

	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialUDP("udp", nil, remote)
		if err != nil {
			lastErr = err
			time.Sleep(20 * time.Millisecond)
			continue
		}
		packet, err := codec.KProV1().EncodePacket(core.Message{ID: core.MsgHeartbeat, Seq: 1})
		if err == nil {
			_, err = conn.Write(packet)
		}
		if err == nil {
			_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			var buf [128]byte
			var n int
			n, err = conn.Read(buf[:])
			if err == nil {
				msg, decodeErr := codec.KProV1().DecodePacket(buf[:n])
				if decodeErr == nil && msg.ID == core.MsgPong && msg.Seq == 1 {
					_ = conn.Close()
					return
				}
				err = decodeErr
			}
		}
		lastErr = err
		_ = conn.Close()
		time.Sleep(20 * time.Millisecond)
	}
	tb.Fatalf("UDP echo server did not start on %s: %v", addr, lastErr)
}

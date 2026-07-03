// File: benchmark/compare/tcp_compare_benchmark_test.go
// Purpose: Benchmarks KPro transports and comparison baselines.

package compare

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/panjf2000/gnet/v2"
	"github.com/panjf2000/gnet/v2/pkg/logging"
	"github.com/saramand9/bignet/kpro/codec"
	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/transport/tcp"
)

var tcpCompareConnections = []int{1, 16, 64}

type tcpCompareMode struct {
	name string
	mode core.DispatchMode
}

var tcpCompareModes = []tcpCompareMode{
	{name: "sync", mode: core.DispatchSync},
	{name: "async", mode: core.DispatchAsync},
}

func BenchmarkKProTCP_Echo_Modes(b *testing.B) {
	for _, mode := range tcpCompareModes {
		for _, size := range benchmarkPayloadSizes {
			for _, conns := range tcpCompareConnections {
				b.Run(tcpCompareName(mode.name, size, conns), func(b *testing.B) {
					addr, stop := startKProTCPCompareServer(b, mode.mode)
					defer stop()
					benchmarkTCPCompareClient(b, addr, size, conns)
				})
			}
		}
	}
}

func BenchmarkGNetTCP_Echo_Modes(b *testing.B) {
	for _, mode := range tcpCompareModes {
		for _, size := range benchmarkPayloadSizes {
			for _, conns := range tcpCompareConnections {
				b.Run(tcpCompareName(mode.name, size, conns), func(b *testing.B) {
					addr, stop := startGNetTCPCompareServer(b, mode.mode)
					defer stop()
					benchmarkTCPCompareClient(b, addr, size, conns)
				})
			}
		}
	}
}

func tcpCompareName(mode string, payloadSize, conns int) string {
	return fmt.Sprintf("%s/%dB/%dc", mode, payloadSize, conns)
}

func benchmarkTCPCompareClient(b *testing.B, addr string, payloadSize, conns int) {
	b.Helper()
	if conns <= 0 {
		b.Fatal("connections must be positive")
	}

	connections := make([]net.Conn, 0, conns)
	for i := 0; i < conns; i++ {
		conn := dialTCPBenchmark(b, addr)
		connections = append(connections, conn)
	}
	defer func() {
		for _, conn := range connections {
			_ = conn.Close()
		}
	}()

	payload := make([]byte, payloadSize)
	for i := range payload {
		payload[i] = 'x'
	}

	b.SetBytes(int64(payloadSize))
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
	wg.Add(len(connections))
	for _, conn := range connections {
		conn := conn
		go func() {
			defer wg.Done()
			msg := core.Message{ID: benchmarkMsgEcho, Data: payload}
			for {
				if failed.Load() {
					return
				}
				i := atomic.AddUint64(&next, 1)
				if int(i) > b.N {
					return
				}
				msg.Seq = i
				if err := tcpCompareEchoOnce(conn, msg, len(payload)); err != nil {
					storeErr("tcp echo: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	reportBenchmarkMetrics(b, started, payloadSize)

	if value := firstErr.Load(); value != nil {
		b.Fatal(value.(string))
	}
}

func tcpCompareEchoOnce(conn net.Conn, msg core.Message, wantLen int) error {
	frame, err := codec.KProV1().Encode(msg)
	if err != nil {
		return fmt.Errorf("encode message: %w", err)
	}
	if _, err := conn.Write(frame); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	result, err := codec.KProV1().Read(conn, 64*1024)
	if err != nil {
		return fmt.Errorf("read message: %w", err)
	}
	got := result.Message
	if got.Seq != msg.Seq || got.ID != msg.ID || len(got.Data) != wantLen {
		return fmt.Errorf("unexpected response: id=%d seq=%d len=%d", got.ID, got.Seq, len(got.Data))
	}
	return nil
}

func startKProTCPCompareServer(b *testing.B, mode core.DispatchMode) (string, func()) {
	b.Helper()

	addr := "127.0.0.1:" + freeTCPPort(b)
	router := core.NewRouter(core.RouterConfig{
		SyncQueueSize:         4096,
		AsyncWorkers:          runtime.GOMAXPROCS(0) * 4,
		AsyncQueueSize:        65536,
		MaxInflightPerSession: 4096,
		SessionIdleTimeout:    time.Minute,
	})
	handler := func(ctx core.Context) error {
		msg := ctx.Message()
		return ctx.Reply(msg.ID, msg.Data)
	}
	if mode == core.DispatchAsync {
		router.HandleAsync(benchmarkMsgEcho, handler)
	} else {
		router.HandleSync(benchmarkMsgEcho, handler)
	}

	transport := tcp.New(tcp.Config{
		Addr:          addr,
		Codec:         codec.KProV1(),
		SendQueueSize: 1024,
		ReadTimeout:   10 * time.Second,
		WriteTimeout:  10 * time.Second,
	})
	return startBenchmarkMessageTransport(b, addr, router, func(ctx context.Context, h core.Handler) error {
		return transport.ListenAndServe(ctx, h)
	}, func(ctx context.Context) error {
		return transport.Shutdown(ctx)
	})
}

func startGNetTCPCompareServer(b *testing.B, mode core.DispatchMode) (string, func()) {
	b.Helper()

	addr := "127.0.0.1:" + freeTCPPort(b)
	protoAddr := "tcp://" + addr
	server := newGNetCompareEchoServer(mode)
	errCh := make(chan error, 1)
	go func() {
		errCh <- gnet.Run(
			server,
			protoAddr,
			gnet.WithMulticore(true),
			gnet.WithTCPNoDelay(gnet.TCPNoDelay),
			gnet.WithReadBufferCap(64*1024),
			gnet.WithWriteBufferCap(64*1024),
			gnet.WithLogLevel(logging.ErrorLevel),
		)
	}()
	waitForTCPServer(b, addr)

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
				b.Fatalf("gnet benchmark server stopped with error: %v", err)
			}
		case <-time.After(3 * time.Second):
			b.Fatal("gnet benchmark server did not stop")
		}
		server.stopWorkers()
	}
	return addr, stop
}

type gnetCompareEchoServer struct {
	gnet.BuiltinEventEngine
	mode   core.DispatchMode
	engine gnet.Engine
	jobs   chan gnetCompareJob
	done   chan struct{}
	wg     sync.WaitGroup
}

type gnetCompareJob struct {
	conn  gnet.Conn
	frame []byte
}

func newGNetCompareEchoServer(mode core.DispatchMode) *gnetCompareEchoServer {
	return &gnetCompareEchoServer{
		mode: mode,
		jobs: make(chan gnetCompareJob, 65536),
		done: make(chan struct{}),
	}
}

func (s *gnetCompareEchoServer) OnBoot(engine gnet.Engine) gnet.Action {
	s.engine = engine
	if s.mode == core.DispatchAsync {
		workers := runtime.GOMAXPROCS(0) * 4
		s.wg.Add(workers)
		for i := 0; i < workers; i++ {
			go s.worker()
		}
	}
	return gnet.None
}

func (s *gnetCompareEchoServer) OnTraffic(conn gnet.Conn) gnet.Action {
	for {
		frame, action := gnetReadKProFrame(conn)
		if action != gnet.None || frame == nil {
			return action
		}
		if s.mode == core.DispatchAsync {
			stable := append([]byte(nil), frame...)
			select {
			case s.jobs <- gnetCompareJob{conn: conn, frame: stable}:
			default:
				return gnet.Close
			}
			continue
		}
		if !gnetWriteKProEcho(conn, frame, false) {
			return gnet.Close
		}
	}
}

func (s *gnetCompareEchoServer) OnShutdown(gnet.Engine) {
	s.stopWorkers()
}

func (s *gnetCompareEchoServer) worker() {
	defer s.wg.Done()
	for {
		select {
		case job := <-s.jobs:
			_ = gnetWriteKProEcho(job.conn, job.frame, true)
		case <-s.done:
			return
		}
	}
}

func (s *gnetCompareEchoServer) stopWorkers() {
	select {
	case <-s.done:
		return
	default:
		close(s.done)
		s.wg.Wait()
	}
}

func gnetReadKProFrame(conn gnet.Conn) ([]byte, gnet.Action) {
	if conn.InboundBuffered() < codec.FrameHeaderSize {
		return nil, gnet.None
	}
	header, err := conn.Peek(codec.FrameHeaderSize)
	if err != nil {
		if err == io.ErrShortBuffer {
			return nil, gnet.None
		}
		return nil, gnet.Close
	}
	if binary.BigEndian.Uint16(header[0:2]) != codec.FrameMagic || header[2] != codec.FrameVersion {
		return nil, gnet.Close
	}
	bodyLen := binary.BigEndian.Uint32(header[16:20])
	if bodyLen > 64*1024 {
		return nil, gnet.Close
	}
	frameLen := codec.FrameHeaderSize + int(bodyLen)
	if conn.InboundBuffered() < frameLen {
		return nil, gnet.None
	}
	frame, err := conn.Next(frameLen)
	if err != nil {
		if err == io.ErrShortBuffer {
			return nil, gnet.None
		}
		return nil, gnet.Close
	}
	return frame, gnet.None
}

func gnetWriteKProEcho(conn gnet.Conn, frame []byte, async bool) bool {
	msg, err := codec.KProV1().DecodePacket(frame)
	if err != nil {
		return false
	}
	if msg.ID == core.MsgHeartbeat {
		msg.ID = core.MsgPong
		msg.Data = nil
	}
	reply, err := codec.KProV1().EncodePacket(msg)
	if err != nil {
		return false
	}
	if async {
		return conn.AsyncWrite(reply, nil) == nil
	}
	if _, err := conn.Write(reply); err != nil {
		return false
	}
	return conn.Flush() == nil
}

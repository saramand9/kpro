package benchmark

import (
	"context"
	"encoding/binary"
	"io"
	"testing"
	"time"

	"github.com/panjf2000/gnet/v2"
	"github.com/panjf2000/gnet/v2/pkg/logging"
	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/protocol"
)

func BenchmarkGNetTCP_Echo(b *testing.B) {
	for _, size := range benchmarkPayloadSizes {
		b.Run(payloadName(size), func(b *testing.B) {
			benchmarkGNetTCPEcho(b, size)
		})
	}
}

func benchmarkGNetTCPEcho(b *testing.B, payloadSize int) {
	addr, stop := startGNetTCPBenchmarkServer(b)
	defer stop()

	conn := dialTCPBenchmark(b, addr)
	defer conn.Close()

	payload := make([]byte, payloadSize)
	for i := range payload {
		payload[i] = 'x'
	}
	msg := core.Message{ID: core.MsgEcho, Data: payload}

	b.SetBytes(int64(payloadSize))
	b.ReportAllocs()
	started := startBenchmarkTimer(b)
	for i := 0; i < b.N; i++ {
		msg.Seq = uint64(i + 1)
		writeBenchmarkMessage(b, conn, msg)
		got := readBenchmarkMessage(b, conn)
		if got.Seq != msg.Seq || got.ID != msg.ID || len(got.Data) != len(payload) {
			b.Fatalf("unexpected gnet TCP response: id=%d seq=%d len=%d", got.ID, got.Seq, len(got.Data))
		}
	}
	reportBenchmarkMetrics(b, started, payloadSize)
}

func startGNetTCPBenchmarkServer(b *testing.B) (string, func()) {
	b.Helper()

	addr := "127.0.0.1:" + freeTCPPort(b)
	protoAddr := "tcp://" + addr
	server := &gnetKProEchoServer{}
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
	}
	return addr, stop
}

type gnetKProEchoServer struct {
	gnet.BuiltinEventEngine
	engine gnet.Engine
}

func (s *gnetKProEchoServer) OnBoot(engine gnet.Engine) gnet.Action {
	s.engine = engine
	return gnet.None
}

func (s *gnetKProEchoServer) OnTraffic(conn gnet.Conn) gnet.Action {
	for {
		if conn.InboundBuffered() < protocol.FrameHeaderSize {
			return gnet.None
		}
		header, err := conn.Peek(protocol.FrameHeaderSize)
		if err != nil {
			if err == io.ErrShortBuffer {
				return gnet.None
			}
			return gnet.Close
		}
		if binary.BigEndian.Uint16(header[0:2]) != protocol.FrameMagic || header[2] != protocol.FrameVersion {
			return gnet.Close
		}
		bodyLen := binary.BigEndian.Uint32(header[16:20])
		if bodyLen > 64*1024 {
			return gnet.Close
		}
		frameLen := protocol.FrameHeaderSize + int(bodyLen)
		if conn.InboundBuffered() < frameLen {
			return gnet.None
		}
		frame, err := conn.Next(frameLen)
		if err != nil {
			if err == io.ErrShortBuffer {
				return gnet.None
			}
			return gnet.Close
		}
		msg, err := protocol.KProV1().DecodePacket(frame)
		if err != nil {
			return gnet.Close
		}
		if msg.ID == core.MsgHeartbeat {
			msg.ID = core.MsgPong
			msg.Data = nil
		}
		reply, err := protocol.KProV1().EncodePacket(msg)
		if err != nil {
			return gnet.Close
		}
		if _, err := conn.Write(reply); err != nil {
			return gnet.Close
		}
		if err := conn.Flush(); err != nil {
			return gnet.Close
		}
	}
}

package test

import (
	"context"
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/protocol"
	"github.com/saramand9/bignet/kpro/transport/udp"
)

const strictUDPMaxPacketSize = 64 * 1024

const (
	msgStrictUDPSyncEcho       uint32 = 800
	msgStrictUDPAsyncEcho      uint32 = 801
	msgStrictUDPMixedSync      uint32 = 802
	msgStrictUDPMixedAsync     uint32 = 803
	msgStrictUDPQueueFull      uint32 = 804
	msgStrictUDPAsyncQueueFull uint32 = 805
	msgStrictUDPInflight       uint32 = 806
	msgStrictUDPMultiClient    uint32 = 807
	msgStrictUDPBoundary       uint32 = 808
	msgStrictUDPFlags          uint32 = 809
	msgStrictUDPRouterClosed   uint32 = 810
	msgStrictUDPUnknown        uint32 = 899
)

func TestUDPWireCRCTraceStrict1000(t *testing.T) {
	t.Run("sync_echo", func(t *testing.T) {
		messages := strictMessages(msgStrictUDPSyncEcho, "udp-sync", strictWireMessageCount)
		logs := runUDPWireCRCScenario(t, core.RouterConfig{
			SyncQueueSize:         strictWireMessageCount + 16,
			AsyncWorkers:          4,
			AsyncQueueSize:        strictWireMessageCount + 16,
			MaxInflightPerSession: strictWireMessageCount + 16,
		}, func(router *core.Router, _ *strictWireControl) {
			router.HandleSync(msgStrictUDPSyncEcho, echoStrictMessage)
		}, messages, nil)

		assertWireFrameSetEqual(t, logs.clientSent, logs.serverReceived)
		assertWireFrameSetEqual(t, logs.serverSent, logs.clientReceived)
		assertStrictEchoResponseSet(t, messages, logs.clientReceived.snapshot())
	})

	t.Run("async_echo", func(t *testing.T) {
		messages := strictMessages(msgStrictUDPAsyncEcho, "udp-async", strictWireMessageCount)
		logs := runUDPWireCRCScenario(t, core.RouterConfig{
			SyncQueueSize:         strictWireMessageCount + 16,
			AsyncWorkers:          16,
			AsyncQueueSize:        strictWireMessageCount + 16,
			MaxInflightPerSession: strictWireMessageCount + 16,
		}, func(router *core.Router, _ *strictWireControl) {
			router.HandleAsync(msgStrictUDPAsyncEcho, func(ctx core.Context) error {
				if ctx.Message().Seq%2 == 1 {
					time.Sleep(100 * time.Microsecond)
				}
				return echoStrictMessage(ctx)
			})
		}, messages, nil)

		assertWireFrameSetEqual(t, logs.clientSent, logs.serverReceived)
		assertWireFrameSetEqual(t, logs.serverSent, logs.clientReceived)
		assertStrictEchoResponseSet(t, messages, logs.clientReceived.snapshot())
	})

	t.Run("mixed_sync_async_echo", func(t *testing.T) {
		messages := make([]core.Message, strictWireMessageCount)
		for i := range messages {
			if i%2 == 0 {
				messages[i] = strictMessage(msgStrictUDPMixedSync, uint64(i+1), "udp-mixed-sync")
				continue
			}
			messages[i] = strictMessage(msgStrictUDPMixedAsync, uint64(i+1), "udp-mixed-async")
		}
		logs := runUDPWireCRCScenario(t, core.RouterConfig{
			SyncQueueSize:         strictWireMessageCount + 16,
			AsyncWorkers:          16,
			AsyncQueueSize:        strictWireMessageCount + 16,
			MaxInflightPerSession: strictWireMessageCount + 16,
		}, func(router *core.Router, _ *strictWireControl) {
			router.HandleSync(msgStrictUDPMixedSync, echoStrictMessage)
			router.HandleAsync(msgStrictUDPMixedAsync, echoStrictMessage)
		}, messages, nil)

		assertWireFrameSetEqual(t, logs.clientSent, logs.serverReceived)
		assertWireFrameSetEqual(t, logs.serverSent, logs.clientReceived)
		assertStrictEchoResponseSet(t, messages, logs.clientReceived.snapshot())
	})

	t.Run("no_handler_errors", func(t *testing.T) {
		messages := strictMessages(msgStrictUDPUnknown, "udp-unknown", strictWireMessageCount)
		logs := runUDPWireCRCScenario(t, core.RouterConfig{
			SyncQueueSize:         strictWireMessageCount + 16,
			AsyncWorkers:          4,
			AsyncQueueSize:        strictWireMessageCount + 16,
			MaxInflightPerSession: strictWireMessageCount + 16,
		}, func(_ *core.Router, _ *strictWireControl) {}, messages, nil)

		assertWireFrameSetEqual(t, logs.clientSent, logs.serverReceived)
		assertWireFrameSetEqual(t, logs.serverSent, logs.clientReceived)
		assertStrictErrorResponses(t, messages, logs.clientReceived.snapshot(), core.ErrNoHandler.Error())
	})

	t.Run("heartbeat_pong", func(t *testing.T) {
		messages := strictMessages(core.MsgHeartbeat, "udp-heartbeat", strictWireMessageCount)
		logs := runUDPWireCRCScenario(t, core.RouterConfig{
			SyncQueueSize:         strictWireMessageCount + 16,
			AsyncWorkers:          4,
			AsyncQueueSize:        strictWireMessageCount + 16,
			MaxInflightPerSession: strictWireMessageCount + 16,
		}, func(_ *core.Router, _ *strictWireControl) {}, messages, nil)

		assertWireFrameSetEqual(t, logs.clientSent, logs.serverReceived)
		assertWireFrameSetEqual(t, logs.serverSent, logs.clientReceived)
		assertStrictPongResponses(t, messages, logs.clientReceived.snapshot())
	})

	t.Run("sync_queue_full_errors", func(t *testing.T) {
		messages := strictMessages(msgStrictUDPQueueFull, "udp-queue-full", strictWireMessageCount)
		releaseFirst := make(chan struct{})
		var releaseOnce sync.Once
		release := func() {
			releaseOnce.Do(func() {
				close(releaseFirst)
			})
		}
		defer release()

		logs := runUDPWireCRCScenario(t, core.RouterConfig{
			SyncQueueSize:         1,
			AsyncWorkers:          4,
			AsyncQueueSize:        strictWireMessageCount + 16,
			MaxInflightPerSession: strictWireMessageCount + 16,
		}, func(router *core.Router, _ *strictWireControl) {
			router.HandleSync(msgStrictUDPQueueFull, func(ctx core.Context) error {
				if ctx.Message().Seq == 1 {
					<-releaseFirst
				}
				return echoStrictMessage(ctx)
			})
		}, messages, func(logs strictWireLogs) {
			waitForStrictWireCount(t, logs.serverReceived, strictWireMessageCount)
			release()
		})

		assertWireFrameSetEqual(t, logs.clientSent, logs.serverReceived)
		assertWireFrameSetEqual(t, logs.serverSent, logs.clientReceived)
		assertStrictQueueFullResponsesFor(t, messages, logs.clientReceived.snapshot(), msgStrictUDPQueueFull, 2)
	})

	t.Run("async_queue_full_errors", func(t *testing.T) {
		messages := strictMessages(msgStrictUDPAsyncQueueFull, "udp-async-queue-full", strictWireMessageCount)
		releaseFirst := make(chan struct{})
		firstStarted := make(chan struct{})
		var releaseOnce sync.Once
		var firstOnce sync.Once
		release := func() {
			releaseOnce.Do(func() {
				close(releaseFirst)
			})
		}
		defer release()

		logs := runUDPWireCRCScenario(t, core.RouterConfig{
			SyncQueueSize:         strictWireMessageCount + 16,
			AsyncWorkers:          1,
			AsyncQueueSize:        1,
			MaxInflightPerSession: strictWireMessageCount + 16,
		}, func(router *core.Router, _ *strictWireControl) {
			router.HandleAsync(msgStrictUDPAsyncQueueFull, func(ctx core.Context) error {
				if ctx.Message().Seq == 1 {
					firstOnce.Do(func() {
						close(firstStarted)
					})
					<-releaseFirst
				}
				return echoStrictMessage(ctx)
			})
		}, messages, func(logs strictWireLogs) {
			waitForClosed(t, firstStarted, "first UDP async queue-full handler did not start")
			waitForStrictWireCount(t, logs.serverReceived, strictWireMessageCount)
			release()
		})

		assertWireFrameSetEqual(t, logs.clientSent, logs.serverReceived)
		assertWireFrameSetEqual(t, logs.serverSent, logs.clientReceived)
		assertStrictQueueFullResponsesFor(t, messages, logs.clientReceived.snapshot(), msgStrictUDPAsyncQueueFull, 2)
	})

	t.Run("max_inflight_errors", func(t *testing.T) {
		messages := strictMessages(msgStrictUDPInflight, "udp-inflight", strictWireMessageCount)
		releaseFirst := make(chan struct{})
		firstStarted := make(chan struct{})
		var releaseOnce sync.Once
		release := func() {
			releaseOnce.Do(func() {
				close(releaseFirst)
			})
		}
		defer release()

		logs := runUDPWireCRCScenario(t, core.RouterConfig{
			SyncQueueSize:         strictWireMessageCount + 16,
			AsyncWorkers:          4,
			AsyncQueueSize:        strictWireMessageCount + 16,
			MaxInflightPerSession: 1,
		}, func(router *core.Router, _ *strictWireControl) {
			router.HandleSync(msgStrictUDPInflight, func(ctx core.Context) error {
				if ctx.Message().Seq == 1 {
					close(firstStarted)
					<-releaseFirst
				}
				return echoStrictMessage(ctx)
			})
		}, messages, func(logs strictWireLogs) {
			waitForClosed(t, firstStarted, "first UDP inflight handler did not start")
			waitForStrictWireCount(t, logs.serverReceived, strictWireMessageCount)
			release()
		})

		assertWireFrameSetEqual(t, logs.clientSent, logs.serverReceived)
		assertWireFrameSetEqual(t, logs.serverSent, logs.clientReceived)
		assertStrictLimitedErrorResponses(t, messages, logs.clientReceived.snapshot(), msgStrictUDPInflight, core.ErrTooManyInflight.Error(), 1)
	})

	t.Run("router_closed_errors", func(t *testing.T) {
		messages := strictMessages(msgStrictUDPRouterClosed, "udp-router-closed", strictWireMessageCount)
		logs := runUDPWireCRCScenario(t, core.RouterConfig{
			SyncQueueSize:         strictWireMessageCount + 16,
			AsyncWorkers:          4,
			AsyncQueueSize:        strictWireMessageCount + 16,
			MaxInflightPerSession: strictWireMessageCount + 16,
		}, func(router *core.Router, ctl *strictWireControl) {
			router.HandleSync(msgStrictUDPRouterClosed, echoStrictMessage)
			ctl.beforeSend = router.Close
		}, messages, nil)

		assertWireFrameSetEqual(t, logs.clientSent, logs.serverReceived)
		assertWireFrameSetEqual(t, logs.serverSent, logs.clientReceived)
		assertStrictErrorResponses(t, messages, logs.clientReceived.snapshot(), core.ErrRouterClosed.Error())
	})

	t.Run("multi_client_echo", func(t *testing.T) {
		messages := strictMessages(msgStrictUDPMultiClient, "udp-multi-client", strictWireMessageCount)
		logs := runUDPWireCRCMultiClientScenario(t, 4, core.RouterConfig{
			SyncQueueSize:         strictWireMessageCount + 16,
			AsyncWorkers:          8,
			AsyncQueueSize:        strictWireMessageCount + 16,
			MaxInflightPerSession: strictWireMessageCount + 16,
		}, func(router *core.Router, _ *strictWireControl) {
			router.HandleSync(msgStrictUDPMultiClient, echoStrictMessage)
		}, messages)

		assertWireFrameSetEqual(t, logs.clientSent, logs.serverReceived)
		assertWireFrameSetEqual(t, logs.serverSent, logs.clientReceived)
		assertStrictEchoResponseSet(t, messages, logs.clientReceived.snapshot())
	})

	t.Run("boundary_payloads", func(t *testing.T) {
		messages := strictUDPBoundaryMessages(msgStrictUDPBoundary, strictWireMessageCount)
		logs := runUDPWireCRCInterleavedScenario(t, core.RouterConfig{
			SyncQueueSize:         strictWireMessageCount + 16,
			AsyncWorkers:          4,
			AsyncQueueSize:        strictWireMessageCount + 16,
			MaxInflightPerSession: strictWireMessageCount + 16,
		}, func(router *core.Router, _ *strictWireControl) {
			router.HandleSync(msgStrictUDPBoundary, echoStrictMessage)
		}, messages)

		assertWireFrameSetEqual(t, logs.clientSent, logs.serverReceived)
		assertWireFrameSetEqual(t, logs.serverSent, logs.clientReceived)
		assertStrictEchoResponseSet(t, messages, logs.clientReceived.snapshot())
	})

	t.Run("flags_echo", func(t *testing.T) {
		messages := strictFlagMessages(msgStrictUDPFlags, strictWireMessageCount)
		logs := runUDPWireCRCInterleavedScenario(t, core.RouterConfig{
			SyncQueueSize:         strictWireMessageCount + 16,
			AsyncWorkers:          4,
			AsyncQueueSize:        strictWireMessageCount + 16,
			MaxInflightPerSession: strictWireMessageCount + 16,
		}, func(router *core.Router, _ *strictWireControl) {
			router.HandleSync(msgStrictUDPFlags, func(ctx core.Context) error {
				msg := ctx.Message()
				return ctx.Session().Send(core.Message{Flags: msg.Flags, ID: msg.ID, Seq: msg.Seq, Data: msg.Data})
			})
		}, messages)

		assertWireFrameSetEqual(t, logs.clientSent, logs.serverReceived)
		assertWireFrameSetEqual(t, logs.serverSent, logs.clientReceived)
		assertStrictEchoResponseSet(t, messages, logs.clientReceived.snapshot())
	})
}

func TestUDPWireProtocolFaultStrict1000(t *testing.T) {
	cases := []struct {
		name  string
		build func(int) []byte
	}{
		{
			name: "bad_magic",
			build: func(i int) []byte {
				frame := strictFaultFrame(t, i)
				frame[0], frame[1] = 0, 0
				return frame
			},
		},
		{
			name: "bad_version",
			build: func(i int) []byte {
				frame := strictFaultFrame(t, i)
				frame[2] = protocol.FrameVersion + 1
				return frame
			},
		},
		{
			name: "declared_length_too_large",
			build: func(i int) []byte {
				frame := strictFaultFrame(t, i)
				binary.BigEndian.PutUint32(frame[16:20], strictUDPMaxPacketSize+1)
				return frame[:protocol.FrameHeaderSize]
			},
		},
		{
			name: "short_header",
			build: func(i int) []byte {
				frame := strictFaultFrame(t, i)
				return frame[:1+i%(protocol.FrameHeaderSize-1)]
			},
		},
		{
			name: "partial_body",
			build: func(i int) []byte {
				frame := strictFaultFrame(t, i)
				return frame[:len(frame)-1]
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs, addr, _, stop := startStrictUDPServer(t, core.RouterConfig{
				SyncQueueSize:         strictWireMessageCount + 16,
				AsyncWorkers:          4,
				AsyncQueueSize:        strictWireMessageCount + 16,
				MaxInflightPerSession: strictWireMessageCount + 16,
			}, func(router *core.Router, _ *strictWireControl) {
				router.HandleSync(msgStrictUDPSyncEcho, echoStrictMessage)
			})
			defer stop()

			client := dialStrictUDPClient(t, addr)
			defer client.Close()
			rawLog := newStrictRawLog(t, "client_malformed_udp_"+tc.name+".ndjson")
			for i := 0; i < strictWireMessageCount; i++ {
				frame := tc.build(i + 1)
				if err := rawLog.appendRaw(frame); err != nil {
					t.Fatalf("append malformed UDP datagram: %v", err)
				}
				if _, err := client.Write(frame); err != nil {
					t.Fatalf("write malformed UDP datagram: %v", err)
				}
				pauseStrictUDPBurst(i)
			}
			waitForStrictRawCount(t, rawLog, strictWireMessageCount)
			assertNoStrictUDPResponse(t, client)
			assertStrictWireLogCount(t, logs.serverReceived, 0)
			assertStrictWireLogCount(t, logs.serverSent, 0)
			assertStrictWireLogCount(t, logs.clientReceived, 0)
		})
	}
}

type recordingPacketProtocol struct {
	serverReceived *strictWireLog
	serverSent     *strictWireLog
}

func (p *recordingPacketProtocol) Name() string {
	return protocol.KProV1().Name()
}

func (p *recordingPacketProtocol) DecodePacket(data []byte) (core.Message, error) {
	msg, err := protocol.KProV1().DecodePacket(data)
	if err != nil {
		return core.Message{}, err
	}
	if err := p.serverReceived.appendFrame(msg, data); err != nil {
		return core.Message{}, err
	}
	return msg, nil
}

func (p *recordingPacketProtocol) EncodePacket(msg core.Message) ([]byte, error) {
	frame, err := protocol.KProV1().EncodePacket(msg)
	if err != nil {
		return nil, err
	}
	if err := p.serverSent.appendFrame(msg, frame); err != nil {
		return nil, err
	}
	return frame, nil
}

func runUDPWireCRCScenario(
	t *testing.T,
	routerConfig core.RouterConfig,
	register func(*core.Router, *strictWireControl),
	messages []core.Message,
	afterSend func(strictWireLogs),
) strictWireLogs {
	t.Helper()
	if len(messages) < strictWireMessageCount {
		t.Fatalf("strict UDP scenario has %d messages, want at least %d", len(messages), strictWireMessageCount)
	}

	logs, addr, control, stop := startStrictUDPServer(t, routerConfig, register)
	defer stop()

	client := dialStrictUDPClient(t, addr)
	defer client.Close()

	if control.beforeSend != nil {
		control.beforeSend()
	}
	for i, msg := range messages {
		writeStrictUDPMessage(t, client, logs.clientSent, msg)
		pauseStrictUDPBurst(i)
	}
	if afterSend != nil {
		afterSend(logs)
	}
	for range messages {
		readStrictUDPMessage(t, client, logs.clientReceived)
	}
	return logs
}

func runUDPWireCRCInterleavedScenario(
	t *testing.T,
	routerConfig core.RouterConfig,
	register func(*core.Router, *strictWireControl),
	messages []core.Message,
) strictWireLogs {
	t.Helper()
	if len(messages) < strictWireMessageCount {
		t.Fatalf("strict UDP interleaved scenario has %d messages, want at least %d", len(messages), strictWireMessageCount)
	}

	logs, addr, control, stop := startStrictUDPServer(t, routerConfig, register)
	defer stop()

	client := dialStrictUDPClient(t, addr)
	defer client.Close()

	if control.beforeSend != nil {
		control.beforeSend()
	}
	for _, msg := range messages {
		writeStrictUDPMessage(t, client, logs.clientSent, msg)
		readStrictUDPMessage(t, client, logs.clientReceived)
	}
	return logs
}

func runUDPWireCRCMultiClientScenario(
	t *testing.T,
	clients int,
	routerConfig core.RouterConfig,
	register func(*core.Router, *strictWireControl),
	messages []core.Message,
) strictWireLogs {
	t.Helper()
	if len(messages) < strictWireMessageCount {
		t.Fatalf("strict UDP multi-client scenario has %d messages, want at least %d", len(messages), strictWireMessageCount)
	}
	if clients <= 0 {
		t.Fatal("clients must be positive")
	}

	logs, addr, control, stop := startStrictUDPServer(t, routerConfig, register)
	defer stop()

	conns := make([]*net.UDPConn, clients)
	for i := range conns {
		conns[i] = dialStrictUDPClient(t, addr)
		defer conns[i].Close()
	}

	if control.beforeSend != nil {
		control.beforeSend()
	}
	for i, msg := range messages {
		conn := conns[i%clients]
		writeStrictUDPMessage(t, conn, logs.clientSent, msg)
		readStrictUDPMessage(t, conn, logs.clientReceived)
	}
	return logs
}

func startStrictUDPServer(
	t *testing.T,
	routerConfig core.RouterConfig,
	register func(*core.Router, *strictWireControl),
) (strictWireLogs, string, *strictWireControl, func()) {
	t.Helper()

	trace := newStrictWireTrace(t)
	logs := strictWireLogs{
		clientSent:     trace.log("client_sent.ndjson"),
		serverReceived: trace.log("server_received.ndjson"),
		serverSent:     trace.log("server_sent.ndjson"),
		clientReceived: trace.log("client_received.ndjson"),
	}
	wireProtocol := &recordingPacketProtocol{
		serverReceived: logs.serverReceived,
		serverSent:     logs.serverSent,
	}

	addr := "127.0.0.1:" + freeUDPPort(t)
	router := core.NewRouter(routerConfig)
	control := &strictWireControl{}
	register(router, control)
	transport := udp.New(udp.Config{
		Addr:          addr,
		Protocol:      wireProtocol,
		MaxPacketSize: strictUDPMaxPacketSize,
		SendQueueSize: strictWireMessageCount + 16,
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- transport.ListenAndServe(ctx, router.Dispatch)
	}()

	select {
	case err := <-errCh:
		t.Fatalf("strict UDP server exited early: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	stop := func() {
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer shutdownCancel()
		_ = transport.Shutdown(shutdownCtx)
		_ = router.Shutdown(shutdownCtx)
		select {
		case <-errCh:
		case <-time.After(3 * time.Second):
			t.Fatalf("strict UDP server did not stop")
		}
	}
	return logs, addr, control, stop
}

func dialStrictUDPClient(t *testing.T, addr string) *net.UDPConn {
	t.Helper()
	remote, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		t.Fatalf("resolve strict UDP addr: %v", err)
	}
	conn, err := net.DialUDP("udp", nil, remote)
	if err != nil {
		t.Fatalf("dial strict UDP addr: %v", err)
	}
	_ = conn.SetReadBuffer(4 * 1024 * 1024)
	_ = conn.SetWriteBuffer(4 * 1024 * 1024)
	return conn
}

func writeStrictUDPMessage(t *testing.T, conn *net.UDPConn, log *strictWireLog, msg core.Message) {
	t.Helper()
	packet, err := protocol.KProV1().EncodePacket(msg)
	if err != nil {
		t.Fatalf("encode strict UDP datagram: %v", err)
	}
	if err := log.appendFrame(msg, packet); err != nil {
		t.Fatalf("append client sent strict UDP datagram: %v", err)
	}
	n, err := conn.Write(packet)
	if err != nil {
		t.Fatalf("write strict UDP datagram: %v", err)
	}
	if n != len(packet) {
		t.Fatalf("short strict UDP datagram write: n=%d len=%d", n, len(packet))
	}
}

func readStrictUDPMessage(t *testing.T, conn *net.UDPConn, log *strictWireLog) core.Message {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, strictUDPMaxPacketSize)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read strict UDP datagram: %v", err)
	}
	msg, err := protocol.KProV1().DecodePacket(buf[:n])
	if err != nil {
		t.Fatalf("decode strict UDP datagram: %v", err)
	}
	if err := log.appendFrame(msg, buf[:n]); err != nil {
		t.Fatalf("append client received strict UDP datagram: %v", err)
	}
	return msg
}

func strictUDPBoundaryMessages(msgID uint32, count int) []core.Message {
	sizes := []int{0, 1, 64, 1024, 4096, 60 * 1024}
	messages := make([]core.Message, count)
	for i := range messages {
		size := sizes[i%len(sizes)]
		payload := make([]byte, size)
		for j := range payload {
			payload[j] = byte('a' + (i+j)%26)
		}
		messages[i] = core.Message{ID: msgID, Seq: uint64(i + 1), Data: payload}
	}
	return messages
}

func pauseStrictUDPBurst(i int) {
	if (i+1)%32 == 0 {
		time.Sleep(100 * time.Microsecond)
	}
}

func assertNoStrictUDPResponse(t *testing.T, conn *net.UDPConn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	var buf [1]byte
	n, err := conn.Read(buf[:])
	if n > 0 || err == nil {
		t.Fatalf("received unexpected malformed-UDP response: n=%d err=%v", n, err)
	}
}

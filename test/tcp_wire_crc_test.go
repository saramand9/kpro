// File: test/tcp_wire_crc_test.go
// Purpose: Exercises KPro protocol and transport behavior through integration tests.

package test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/saramand9/bignet/kpro/codec"
	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/transport/tcp"
)

const strictWireMessageCount = 1000

const (
	msgStrictSyncEcho       uint32 = 700
	msgStrictAsyncEcho      uint32 = 701
	msgStrictMixedSync      uint32 = 702
	msgStrictMixedAsync     uint32 = 703
	msgStrictQueueFull      uint32 = 704
	msgStrictAsyncQueueFull uint32 = 705
	msgStrictInflight       uint32 = 706
	msgStrictMultiConn      uint32 = 707
	msgStrictBoundary       uint32 = 708
	msgStrictFlags          uint32 = 709
	msgStrictCoalesced      uint32 = 710
	msgStrictFragmented     uint32 = 711
	msgStrictHalfClose      uint32 = 712
	msgStrictRouterClosed   uint32 = 713
	msgStrictSmallSendQueue uint32 = 714
	msgStrictUnknown        uint32 = 799
)

func TestTCPWireCRCTraceStrict1000(t *testing.T) {
	t.Run("sync_echo", func(t *testing.T) {
		messages := strictMessages(msgStrictSyncEcho, "sync", strictWireMessageCount)
		logs := runTCPWireCRCScenario(t, core.RouterConfig{
			SyncQueueSize:         strictWireMessageCount + 16,
			AsyncWorkers:          4,
			AsyncQueueSize:        strictWireMessageCount + 16,
			MaxInflightPerSession: strictWireMessageCount + 16,
		}, func(router *core.Router, _ *strictWireControl) {
			router.HandleSync(msgStrictSyncEcho, echoStrictMessage)
		}, messages, nil)

		assertWireFramesEqual(t, logs.clientSent, logs.serverReceived)
		assertWireFramesEqual(t, logs.serverSent, logs.clientReceived)
		assertStrictEchoResponses(t, messages, logs.clientReceived.snapshot())
	})

	t.Run("async_echo", func(t *testing.T) {
		messages := strictMessages(msgStrictAsyncEcho, "async", strictWireMessageCount)
		logs := runTCPWireCRCScenario(t, core.RouterConfig{
			SyncQueueSize:         strictWireMessageCount + 16,
			AsyncWorkers:          16,
			AsyncQueueSize:        strictWireMessageCount + 16,
			MaxInflightPerSession: strictWireMessageCount + 16,
		}, func(router *core.Router, _ *strictWireControl) {
			router.HandleAsync(msgStrictAsyncEcho, func(ctx core.Context) error {
				if ctx.Message().Seq%2 == 1 {
					time.Sleep(100 * time.Microsecond)
				}
				return echoStrictMessage(ctx)
			})
		}, messages, nil)

		assertWireFramesEqual(t, logs.clientSent, logs.serverReceived)
		assertWireFrameSetEqual(t, logs.serverSent, logs.clientReceived)
		assertStrictEchoResponseSet(t, messages, logs.clientReceived.snapshot())
	})

	t.Run("mixed_sync_async_echo", func(t *testing.T) {
		messages := make([]core.Message, strictWireMessageCount)
		for i := range messages {
			if i%2 == 0 {
				messages[i] = strictMessage(msgStrictMixedSync, uint64(i+1), "mixed-sync")
				continue
			}
			messages[i] = strictMessage(msgStrictMixedAsync, uint64(i+1), "mixed-async")
		}
		logs := runTCPWireCRCScenario(t, core.RouterConfig{
			SyncQueueSize:         strictWireMessageCount + 16,
			AsyncWorkers:          16,
			AsyncQueueSize:        strictWireMessageCount + 16,
			MaxInflightPerSession: strictWireMessageCount + 16,
		}, func(router *core.Router, _ *strictWireControl) {
			router.HandleSync(msgStrictMixedSync, echoStrictMessage)
			router.HandleAsync(msgStrictMixedAsync, echoStrictMessage)
		}, messages, nil)

		assertWireFramesEqual(t, logs.clientSent, logs.serverReceived)
		assertWireFrameSetEqual(t, logs.serverSent, logs.clientReceived)
		assertStrictEchoResponseSet(t, messages, logs.clientReceived.snapshot())
	})

	t.Run("no_handler_errors", func(t *testing.T) {
		messages := strictMessages(msgStrictUnknown, "unknown", strictWireMessageCount)
		logs := runTCPWireCRCScenario(t, core.RouterConfig{
			SyncQueueSize:         strictWireMessageCount + 16,
			AsyncWorkers:          4,
			AsyncQueueSize:        strictWireMessageCount + 16,
			MaxInflightPerSession: strictWireMessageCount + 16,
		}, func(_ *core.Router, _ *strictWireControl) {}, messages, nil)

		assertWireFramesEqual(t, logs.clientSent, logs.serverReceived)
		assertWireFramesEqual(t, logs.serverSent, logs.clientReceived)
		assertStrictErrorResponses(t, messages, logs.clientReceived.snapshot(), core.ErrNoHandler.Error())
	})

	t.Run("heartbeat_pong", func(t *testing.T) {
		messages := strictMessages(core.MsgHeartbeat, "heartbeat", strictWireMessageCount)
		logs := runTCPWireCRCScenario(t, core.RouterConfig{
			SyncQueueSize:         strictWireMessageCount + 16,
			AsyncWorkers:          4,
			AsyncQueueSize:        strictWireMessageCount + 16,
			MaxInflightPerSession: strictWireMessageCount + 16,
		}, func(_ *core.Router, _ *strictWireControl) {}, messages, nil)

		assertWireFramesEqual(t, logs.clientSent, logs.serverReceived)
		assertWireFramesEqual(t, logs.serverSent, logs.clientReceived)
		assertStrictPongResponses(t, messages, logs.clientReceived.snapshot())
	})

	t.Run("sync_queue_full_errors", func(t *testing.T) {
		messages := strictMessages(msgStrictQueueFull, "queue-full", strictWireMessageCount)
		releaseFirst := make(chan struct{})
		var releaseOnce sync.Once
		release := func() {
			releaseOnce.Do(func() {
				close(releaseFirst)
			})
		}
		defer release()

		logs := runTCPWireCRCScenario(t, core.RouterConfig{
			SyncQueueSize:         1,
			AsyncWorkers:          4,
			AsyncQueueSize:        strictWireMessageCount + 16,
			MaxInflightPerSession: strictWireMessageCount + 16,
		}, func(router *core.Router, _ *strictWireControl) {
			router.HandleSync(msgStrictQueueFull, func(ctx core.Context) error {
				if ctx.Message().Seq == 1 {
					<-releaseFirst
				}
				return echoStrictMessage(ctx)
			})
		}, messages, func(logs strictWireLogs) {
			waitForStrictWireCount(t, logs.serverReceived, strictWireMessageCount)
			release()
		})

		assertWireFramesEqual(t, logs.clientSent, logs.serverReceived)
		assertWireFramesEqual(t, logs.serverSent, logs.clientReceived)
		assertStrictQueueFullResponses(t, messages, logs.clientReceived.snapshot())
	})

	t.Run("async_queue_full_errors", func(t *testing.T) {
		messages := strictMessages(msgStrictAsyncQueueFull, "async-queue-full", strictWireMessageCount)
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

		logs := runTCPWireCRCScenario(t, core.RouterConfig{
			SyncQueueSize:         strictWireMessageCount + 16,
			AsyncWorkers:          1,
			AsyncQueueSize:        1,
			MaxInflightPerSession: strictWireMessageCount + 16,
		}, func(router *core.Router, _ *strictWireControl) {
			router.HandleAsync(msgStrictAsyncQueueFull, func(ctx core.Context) error {
				if ctx.Message().Seq == 1 {
					firstOnce.Do(func() {
						close(firstStarted)
					})
					<-releaseFirst
				}
				return echoStrictMessage(ctx)
			})
		}, messages, func(logs strictWireLogs) {
			waitForClosed(t, firstStarted, "first async queue-full handler did not start")
			waitForStrictWireCount(t, logs.serverReceived, strictWireMessageCount)
			release()
		})

		assertWireFramesEqual(t, logs.clientSent, logs.serverReceived)
		assertWireFrameSetEqual(t, logs.serverSent, logs.clientReceived)
		assertStrictQueueFullResponsesFor(t, messages, logs.clientReceived.snapshot(), msgStrictAsyncQueueFull, 2)
	})

	t.Run("max_inflight_errors", func(t *testing.T) {
		messages := strictMessages(msgStrictInflight, "inflight", strictWireMessageCount)
		releaseFirst := make(chan struct{})
		firstStarted := make(chan struct{})
		var releaseOnce sync.Once
		release := func() {
			releaseOnce.Do(func() {
				close(releaseFirst)
			})
		}
		defer release()

		logs := runTCPWireCRCScenario(t, core.RouterConfig{
			SyncQueueSize:         strictWireMessageCount + 16,
			AsyncWorkers:          4,
			AsyncQueueSize:        strictWireMessageCount + 16,
			MaxInflightPerSession: 1,
		}, func(router *core.Router, _ *strictWireControl) {
			router.HandleSync(msgStrictInflight, func(ctx core.Context) error {
				if ctx.Message().Seq == 1 {
					close(firstStarted)
					<-releaseFirst
				}
				return echoStrictMessage(ctx)
			})
		}, messages, func(logs strictWireLogs) {
			waitForClosed(t, firstStarted, "first inflight handler did not start")
			waitForStrictWireCount(t, logs.serverReceived, strictWireMessageCount)
			release()
		})

		assertWireFramesEqual(t, logs.clientSent, logs.serverReceived)
		assertWireFramesEqual(t, logs.serverSent, logs.clientReceived)
		assertStrictLimitedErrorResponses(t, messages, logs.clientReceived.snapshot(), msgStrictInflight, core.ErrTooManyInflight.Error(), 1)
	})

	t.Run("router_closed_errors", func(t *testing.T) {
		messages := strictMessages(msgStrictRouterClosed, "router-closed", strictWireMessageCount)
		logs := runTCPWireCRCScenario(t, core.RouterConfig{
			SyncQueueSize:         strictWireMessageCount + 16,
			AsyncWorkers:          4,
			AsyncQueueSize:        strictWireMessageCount + 16,
			MaxInflightPerSession: strictWireMessageCount + 16,
		}, func(router *core.Router, ctl *strictWireControl) {
			router.HandleSync(msgStrictRouterClosed, echoStrictMessage)
			ctl.beforeSend = router.Close
		}, messages, nil)

		assertWireFramesEqual(t, logs.clientSent, logs.serverReceived)
		assertWireFramesEqual(t, logs.serverSent, logs.clientReceived)
		assertStrictErrorResponses(t, messages, logs.clientReceived.snapshot(), core.ErrRouterClosed.Error())
	})

	t.Run("multi_connection_echo", func(t *testing.T) {
		messages := strictMessages(msgStrictMultiConn, "multi", strictWireMessageCount)
		logs := runTCPWireCRCMultiConnectionScenario(t, 4, core.RouterConfig{
			SyncQueueSize:         strictWireMessageCount + 16,
			AsyncWorkers:          8,
			AsyncQueueSize:        strictWireMessageCount + 16,
			MaxInflightPerSession: strictWireMessageCount + 16,
		}, func(router *core.Router, _ *strictWireControl) {
			router.HandleSync(msgStrictMultiConn, echoStrictMessage)
		}, messages)

		assertWireFrameSetEqual(t, logs.clientSent, logs.serverReceived)
		assertWireFrameSetEqual(t, logs.serverSent, logs.clientReceived)
		assertStrictEchoResponseSet(t, messages, logs.clientReceived.snapshot())
	})

	t.Run("boundary_payloads", func(t *testing.T) {
		messages := strictBoundaryMessages(msgStrictBoundary, strictWireMessageCount)
		logs := runTCPWireCRCScenario(t, core.RouterConfig{
			SyncQueueSize:         strictWireMessageCount + 16,
			AsyncWorkers:          4,
			AsyncQueueSize:        strictWireMessageCount + 16,
			MaxInflightPerSession: strictWireMessageCount + 16,
		}, func(router *core.Router, _ *strictWireControl) {
			router.HandleSync(msgStrictBoundary, echoStrictMessage)
		}, messages, nil)

		assertWireFramesEqual(t, logs.clientSent, logs.serverReceived)
		assertWireFramesEqual(t, logs.serverSent, logs.clientReceived)
		assertStrictEchoResponses(t, messages, logs.clientReceived.snapshot())
	})

	t.Run("flags_echo", func(t *testing.T) {
		messages := strictFlagMessages(msgStrictFlags, strictWireMessageCount)
		logs := runTCPWireCRCScenario(t, core.RouterConfig{
			SyncQueueSize:         strictWireMessageCount + 16,
			AsyncWorkers:          4,
			AsyncQueueSize:        strictWireMessageCount + 16,
			MaxInflightPerSession: strictWireMessageCount + 16,
		}, func(router *core.Router, _ *strictWireControl) {
			router.HandleSync(msgStrictFlags, func(ctx core.Context) error {
				msg := ctx.Message()
				return ctx.Session().Send(core.Message{Flags: msg.Flags, ID: msg.ID, Seq: msg.Seq, Data: msg.Data})
			})
		}, messages, nil)

		assertWireFramesEqual(t, logs.clientSent, logs.serverReceived)
		assertWireFramesEqual(t, logs.serverSent, logs.clientReceived)
		assertStrictEchoResponses(t, messages, logs.clientReceived.snapshot())
	})

	t.Run("coalesced_write_echo", func(t *testing.T) {
		messages := strictMessages(msgStrictCoalesced, "coalesced", strictWireMessageCount)
		logs := runTCPWireCRCCustomWriteScenario(t, core.RouterConfig{
			SyncQueueSize:         strictWireMessageCount + 16,
			AsyncWorkers:          4,
			AsyncQueueSize:        strictWireMessageCount + 16,
			MaxInflightPerSession: strictWireMessageCount + 16,
		}, func(router *core.Router, _ *strictWireControl) {
			router.HandleSync(msgStrictCoalesced, echoStrictMessage)
		}, messages, writeStrictWireMessagesCoalesced, nil)

		assertWireFramesEqual(t, logs.clientSent, logs.serverReceived)
		assertWireFramesEqual(t, logs.serverSent, logs.clientReceived)
		assertStrictEchoResponses(t, messages, logs.clientReceived.snapshot())
	})

	t.Run("fragmented_write_echo", func(t *testing.T) {
		messages := strictMessages(msgStrictFragmented, "fragmented", strictWireMessageCount)
		logs := runTCPWireCRCCustomWriteScenario(t, core.RouterConfig{
			SyncQueueSize:         strictWireMessageCount + 16,
			AsyncWorkers:          4,
			AsyncQueueSize:        strictWireMessageCount + 16,
			MaxInflightPerSession: strictWireMessageCount + 16,
		}, func(router *core.Router, _ *strictWireControl) {
			router.HandleSync(msgStrictFragmented, echoStrictMessage)
		}, messages, writeStrictWireMessagesFragmented, nil)

		assertWireFramesEqual(t, logs.clientSent, logs.serverReceived)
		assertWireFramesEqual(t, logs.serverSent, logs.clientReceived)
		assertStrictEchoResponses(t, messages, logs.clientReceived.snapshot())
	})

	t.Run("client_half_close_after_round_trip", func(t *testing.T) {
		messages := strictMessages(msgStrictHalfClose, "half-close", strictWireMessageCount)
		logs := runTCPWireCRCHalfCloseAfterRoundTripScenario(t, core.RouterConfig{
			SyncQueueSize:         strictWireMessageCount + 16,
			AsyncWorkers:          4,
			AsyncQueueSize:        strictWireMessageCount + 16,
			MaxInflightPerSession: strictWireMessageCount + 16,
		}, func(router *core.Router, _ *strictWireControl) {
			router.HandleSync(msgStrictHalfClose, echoStrictMessage)
		}, messages)

		assertWireFramesEqual(t, logs.clientSent, logs.serverReceived)
		assertWireFramesEqual(t, logs.serverSent, logs.clientReceived)
		assertStrictEchoResponses(t, messages, logs.clientReceived.snapshot())
	})

	t.Run("small_send_queue_paced_round_trip", func(t *testing.T) {
		messages := strictMessages(msgStrictSmallSendQueue, "small-send-queue", strictWireMessageCount)
		logs := runTCPWireCRCInterleavedScenario(t, core.RouterConfig{
			SyncQueueSize:         16,
			AsyncWorkers:          4,
			AsyncQueueSize:        16,
			MaxInflightPerSession: 16,
		}, func(router *core.Router, _ *strictWireControl) {
			router.HandleSync(msgStrictSmallSendQueue, echoStrictMessage)
		}, messages, 1)

		assertWireFramesEqual(t, logs.clientSent, logs.serverReceived)
		assertWireFramesEqual(t, logs.serverSent, logs.clientReceived)
		assertStrictEchoResponses(t, messages, logs.clientReceived.snapshot())
	})
}

func TestTCPWireProtocolFaultStrict1000(t *testing.T) {
	cases := []struct {
		name       string
		build      func(int) []byte
		closeWrite bool
		closeConn  bool
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
				frame[2] = codec.FrameVersion + 1
				return frame
			},
		},
		{
			name: "declared_length_too_large",
			build: func(i int) []byte {
				frame := strictFaultFrame(t, i)
				binary.BigEndian.PutUint32(frame[16:20], 64*1024+1)
				return frame[:codec.FrameHeaderSize]
			},
		},
		{
			name: "short_header",
			build: func(i int) []byte {
				frame := strictFaultFrame(t, i)
				return frame[:1+i%(codec.FrameHeaderSize-1)]
			},
			closeWrite: true,
		},
		{
			name: "partial_body",
			build: func(i int) []byte {
				frame := strictFaultFrame(t, i)
				return frame[:len(frame)-1]
			},
			closeWrite: true,
		},
		{
			name: "abrupt_close_partial_frame",
			build: func(i int) []byte {
				frame := strictFaultFrame(t, i)
				return frame[:len(frame)/2]
			},
			closeConn: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs, addr, _, stop := startStrictWireServer(t, core.RouterConfig{
				SyncQueueSize:         strictWireMessageCount + 16,
				AsyncWorkers:          4,
				AsyncQueueSize:        strictWireMessageCount + 16,
				MaxInflightPerSession: strictWireMessageCount + 16,
			}, func(router *core.Router, _ *strictWireControl) {
				router.HandleSync(msgStrictSyncEcho, echoStrictMessage)
			}, strictWireMessageCount+16)
			defer stop()

			rawLog := newStrictRawLog(t, "client_malformed_"+tc.name+".ndjson")
			for i := 0; i < strictWireMessageCount; i++ {
				conn := dialTCP(t, addr)
				frame := tc.build(i + 1)
				if err := rawLog.appendRaw(frame); err != nil {
					t.Fatalf("append malformed client frame: %v", err)
				}
				if err := writeFullForTest(conn, frame); err != nil {
					_ = conn.Close()
					t.Fatalf("write malformed frame: %v", err)
				}
				if tc.closeConn {
					_ = conn.Close()
					continue
				}
				if tc.closeWrite {
					if tcpConn, ok := conn.(*net.TCPConn); ok {
						_ = tcpConn.CloseWrite()
					}
				}
				assertNoStrictWireResponse(t, conn)
				_ = conn.Close()
			}
			waitForStrictRawCount(t, rawLog, strictWireMessageCount)
			assertStrictWireLogCount(t, logs.serverReceived, 0)
			assertStrictWireLogCount(t, logs.serverSent, 0)
			assertStrictWireLogCount(t, logs.clientReceived, 0)
		})
	}
}

func echoStrictMessage(ctx core.Context) error {
	msg := ctx.Message()
	return ctx.Reply(msg.ID, msg.Data)
}

type strictWireControl struct {
	beforeSend func()
}

type strictWireLogs struct {
	clientSent     *strictWireLog
	serverReceived *strictWireLog
	serverSent     *strictWireLog
	clientReceived *strictWireLog
}

func runTCPWireCRCScenario(
	t *testing.T,
	routerConfig core.RouterConfig,
	register func(*core.Router, *strictWireControl),
	messages []core.Message,
	afterSend func(strictWireLogs),
) strictWireLogs {
	t.Helper()
	if len(messages) < strictWireMessageCount {
		t.Fatalf("strict wire scenario has %d messages, want at least %d", len(messages), strictWireMessageCount)
	}

	logs, addr, control, stop := startStrictWireServer(t, routerConfig, register, len(messages)+16)
	defer stop()

	conn := dialTCP(t, addr)
	defer conn.Close()

	if control.beforeSend != nil {
		control.beforeSend()
	}
	for _, msg := range messages {
		writeStrictWireMessage(t, conn, logs.clientSent, msg)
	}
	if afterSend != nil {
		afterSend(logs)
	}
	for range messages {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		readStrictWireMessage(t, conn, logs.clientReceived)
	}

	return logs
}

func startStrictWireServer(
	t *testing.T,
	routerConfig core.RouterConfig,
	register func(*core.Router, *strictWireControl),
	sendQueueSize int,
) (strictWireLogs, string, *strictWireControl, func()) {
	t.Helper()

	trace := newStrictWireTrace(t)
	logs := strictWireLogs{
		clientSent:     trace.log("client_sent.ndjson"),
		serverReceived: trace.log("server_received.ndjson"),
		serverSent:     trace.log("server_sent.ndjson"),
		clientReceived: trace.log("client_received.ndjson"),
	}
	wireCodec := &recordingStreamCodec{
		serverReceived: logs.serverReceived,
		serverSent:     logs.serverSent,
	}

	addr := "127.0.0.1:" + freeTCPPort(t)
	router := core.NewRouter(routerConfig)
	control := &strictWireControl{}
	register(router, control)
	transport := tcp.New(tcp.Config{
		Addr:          addr,
		Codec:         wireCodec,
		SendQueueSize: sendQueueSize,
		ReadTimeout:   10 * time.Second,
		WriteTimeout:  10 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- transport.ListenAndServe(ctx, router.Dispatch)
	}()

	stop := func() {
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer shutdownCancel()
		_ = transport.Shutdown(shutdownCtx)
		_ = router.Shutdown(shutdownCtx)
		select {
		case <-errCh:
		case <-time.After(3 * time.Second):
			t.Fatalf("strict wire TCP server did not stop")
		}
	}
	return logs, addr, control, stop
}

type strictWireMessageWriter func(*testing.T, net.Conn, *strictWireLog, []core.Message)
type strictWireConnHook func(net.Conn)

func runTCPWireCRCCustomWriteScenario(
	t *testing.T,
	routerConfig core.RouterConfig,
	register func(*core.Router, *strictWireControl),
	messages []core.Message,
	writer strictWireMessageWriter,
	afterWrite strictWireConnHook,
) strictWireLogs {
	t.Helper()
	if len(messages) < strictWireMessageCount {
		t.Fatalf("strict wire custom scenario has %d messages, want at least %d", len(messages), strictWireMessageCount)
	}

	logs, addr, control, stop := startStrictWireServer(t, routerConfig, register, len(messages)+16)
	defer stop()

	conn := dialTCP(t, addr)
	defer conn.Close()

	if control.beforeSend != nil {
		control.beforeSend()
	}
	writer(t, conn, logs.clientSent, messages)
	if afterWrite != nil {
		afterWrite(conn)
	}
	for range messages {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		readStrictWireMessage(t, conn, logs.clientReceived)
	}
	return logs
}

func runTCPWireCRCMultiConnectionScenario(
	t *testing.T,
	connections int,
	routerConfig core.RouterConfig,
	register func(*core.Router, *strictWireControl),
	messages []core.Message,
) strictWireLogs {
	t.Helper()
	if len(messages) < strictWireMessageCount {
		t.Fatalf("strict wire multi-connection scenario has %d messages, want at least %d", len(messages), strictWireMessageCount)
	}
	if connections <= 0 {
		t.Fatal("connections must be positive")
	}

	logs, addr, control, stop := startStrictWireServer(t, routerConfig, register, len(messages)+16)
	defer stop()

	conns := make([]net.Conn, connections)
	for i := range conns {
		conns[i] = dialTCP(t, addr)
		defer conns[i].Close()
	}
	if control.beforeSend != nil {
		control.beforeSend()
	}

	batches := make([][]core.Message, connections)
	for i, msg := range messages {
		batches[i%connections] = append(batches[i%connections], msg)
	}
	for i, conn := range conns {
		for _, msg := range batches[i] {
			writeStrictWireMessage(t, conn, logs.clientSent, msg)
		}
	}
	for i, conn := range conns {
		for range batches[i] {
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			readStrictWireMessage(t, conn, logs.clientReceived)
		}
	}
	return logs
}

func runTCPWireCRCInterleavedScenario(
	t *testing.T,
	routerConfig core.RouterConfig,
	register func(*core.Router, *strictWireControl),
	messages []core.Message,
	sendQueueSize int,
) strictWireLogs {
	t.Helper()
	if len(messages) < strictWireMessageCount {
		t.Fatalf("strict wire interleaved scenario has %d messages, want at least %d", len(messages), strictWireMessageCount)
	}

	logs, addr, control, stop := startStrictWireServer(t, routerConfig, register, sendQueueSize)
	defer stop()

	conn := dialTCP(t, addr)
	defer conn.Close()

	if control.beforeSend != nil {
		control.beforeSend()
	}
	for _, msg := range messages {
		writeStrictWireMessage(t, conn, logs.clientSent, msg)
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		readStrictWireMessage(t, conn, logs.clientReceived)
	}
	return logs
}

func runTCPWireCRCHalfCloseAfterRoundTripScenario(
	t *testing.T,
	routerConfig core.RouterConfig,
	register func(*core.Router, *strictWireControl),
	messages []core.Message,
) strictWireLogs {
	t.Helper()
	if len(messages) < strictWireMessageCount {
		t.Fatalf("strict wire half-close scenario has %d messages, want at least %d", len(messages), strictWireMessageCount)
	}

	logs, addr, control, stop := startStrictWireServer(t, routerConfig, register, len(messages)+16)
	defer stop()

	conn := dialTCP(t, addr)
	defer conn.Close()

	if control.beforeSend != nil {
		control.beforeSend()
	}
	for _, msg := range messages {
		writeStrictWireMessage(t, conn, logs.clientSent, msg)
	}
	for range messages {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		readStrictWireMessage(t, conn, logs.clientReceived)
	}
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		if err := tcpConn.CloseWrite(); err != nil {
			t.Fatalf("half-close client write side after round trip: %v", err)
		}
		assertNoStrictWireResponse(t, conn)
	}
	return logs
}

type recordingStreamCodec struct {
	serverReceived *strictWireLog
	serverSent     *strictWireLog
}

func (p *recordingStreamCodec) Name() string {
	return codec.KProV1().Name()
}

func (p *recordingStreamCodec) Read(r io.Reader, maxBodySize uint32) (codec.ReadResult, error) {
	frame, msg, err := readStrictWireFrame(r, maxBodySize)
	if err != nil {
		return codec.ReadResult{}, err
	}
	if err := p.serverReceived.appendFrame(msg, frame); err != nil {
		return codec.ReadResult{}, err
	}
	return codec.ReadResult{Message: msg, Bytes: len(frame)}, nil
}

func (p *recordingStreamCodec) Encode(msg core.Message) ([]byte, error) {
	frame, err := codec.KProV1().Encode(msg)
	if err != nil {
		return nil, err
	}
	if err := p.serverSent.appendFrame(msg, frame); err != nil {
		return nil, err
	}
	return frame, nil
}

type strictWireTrace struct {
	t   *testing.T
	dir string
}

type strictWireLog struct {
	mu      sync.Mutex
	path    string
	records []strictWireRecord
}

type strictRawLog struct {
	mu      sync.Mutex
	path    string
	records []strictRawRecord
}

type strictWireRecord struct {
	Index      int    `json:"index"`
	Seq        uint64 `json:"seq"`
	MsgID      uint32 `json:"msg_id"`
	Flags      uint8  `json:"flags"`
	PayloadLen int    `json:"payload_len"`
	FrameLen   int    `json:"frame_len"`
	CRC32      string `json:"crc32"`
	FrameHex   string `json:"frame_hex"`
}

type strictRawRecord struct {
	Index    int    `json:"index"`
	FrameLen int    `json:"frame_len"`
	CRC32    string `json:"crc32"`
	FrameHex string `json:"frame_hex"`
}

func newStrictWireTrace(t *testing.T) *strictWireTrace {
	t.Helper()
	dir, err := os.MkdirTemp("", "kpro-tcp-wire-crc-*")
	if err != nil {
		t.Fatalf("create strict wire trace dir: %v", err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("preserved strict wire trace dir: %s", dir)
			return
		}
		_ = os.RemoveAll(dir)
	})
	return &strictWireTrace{t: t, dir: dir}
}

func (tr *strictWireTrace) log(name string) *strictWireLog {
	tr.t.Helper()
	path := filepath.Join(tr.dir, name)
	if err := os.WriteFile(path, nil, 0644); err != nil {
		tr.t.Fatalf("create strict wire log %s: %v", path, err)
	}
	return &strictWireLog{path: path}
}

func newStrictRawLog(t *testing.T, name string) *strictRawLog {
	t.Helper()
	dir, err := os.MkdirTemp("", "kpro-tcp-wire-raw-*")
	if err != nil {
		t.Fatalf("create strict raw trace dir: %v", err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("preserved strict raw trace dir: %s", dir)
			return
		}
		_ = os.RemoveAll(dir)
	})
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatalf("create strict raw log %s: %v", path, err)
	}
	return &strictRawLog{path: path}
}

func (l *strictWireLog) appendFrame(msg core.Message, frame []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	copied := append([]byte(nil), frame...)
	record := strictWireRecord{
		Index:      len(l.records) + 1,
		Seq:        msg.Seq,
		MsgID:      msg.ID,
		Flags:      msg.Flags,
		PayloadLen: len(msg.Data),
		FrameLen:   len(copied),
		CRC32:      fmt.Sprintf("%08x", crc32.ChecksumIEEE(copied)),
		FrameHex:   hex.EncodeToString(copied),
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

func (l *strictRawLog) appendRaw(frame []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	copied := append([]byte(nil), frame...)
	record := strictRawRecord{
		Index:    len(l.records) + 1,
		FrameLen: len(copied),
		CRC32:    fmt.Sprintf("%08x", crc32.ChecksumIEEE(copied)),
		FrameHex: hex.EncodeToString(copied),
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

func (l *strictWireLog) snapshot() []strictWireRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]strictWireRecord(nil), l.records...)
}

func (l *strictWireLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.records)
}

func (l *strictRawLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.records)
}

func writeStrictWireMessage(t *testing.T, conn net.Conn, log *strictWireLog, msg core.Message) {
	t.Helper()
	frame, err := codec.KProV1().Encode(msg)
	if err != nil {
		t.Fatalf("encode strict wire message: %v", err)
	}
	if err := log.appendFrame(msg, frame); err != nil {
		t.Fatalf("append client sent strict wire frame: %v", err)
	}
	if err := writeFullForTest(conn, frame); err != nil {
		t.Fatalf("write strict wire frame: %v", err)
	}
}

func writeStrictWireMessagesNormal(t *testing.T, conn net.Conn, log *strictWireLog, messages []core.Message) {
	t.Helper()
	for _, msg := range messages {
		writeStrictWireMessage(t, conn, log, msg)
	}
}

func writeStrictWireMessagesCoalesced(t *testing.T, conn net.Conn, log *strictWireLog, messages []core.Message) {
	t.Helper()
	var batch bytes.Buffer
	for _, msg := range messages {
		frame, err := codec.KProV1().Encode(msg)
		if err != nil {
			t.Fatalf("encode coalesced strict wire message: %v", err)
		}
		if err := log.appendFrame(msg, frame); err != nil {
			t.Fatalf("append coalesced client sent strict wire frame: %v", err)
		}
		if _, err := batch.Write(frame); err != nil {
			t.Fatalf("buffer coalesced strict wire frame: %v", err)
		}
	}
	if err := writeFullForTest(conn, batch.Bytes()); err != nil {
		t.Fatalf("write coalesced strict wire frames: %v", err)
	}
}

func writeStrictWireMessagesFragmented(t *testing.T, conn net.Conn, log *strictWireLog, messages []core.Message) {
	t.Helper()
	for _, msg := range messages {
		frame, err := codec.KProV1().Encode(msg)
		if err != nil {
			t.Fatalf("encode fragmented strict wire message: %v", err)
		}
		if err := log.appendFrame(msg, frame); err != nil {
			t.Fatalf("append fragmented client sent strict wire frame: %v", err)
		}
		cuts := []int{1, 5, codec.FrameHeaderSize, len(frame)}
		start := 0
		for _, end := range cuts {
			if end > len(frame) {
				end = len(frame)
			}
			if end <= start {
				continue
			}
			if err := writeFullForTest(conn, frame[start:end]); err != nil {
				t.Fatalf("write fragmented strict wire frame: %v", err)
			}
			start = end
		}
	}
}

func readStrictWireMessage(t *testing.T, conn net.Conn, log *strictWireLog) core.Message {
	t.Helper()
	frame, msg, err := readStrictWireFrame(conn, 64*1024)
	if err != nil {
		t.Fatalf("read strict wire frame: %v", err)
	}
	if err := log.appendFrame(msg, frame); err != nil {
		t.Fatalf("append client received strict wire frame: %v", err)
	}
	return msg
}

func readStrictWireFrame(r io.Reader, maxBodySize uint32) ([]byte, core.Message, error) {
	var header [codec.FrameHeaderSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, core.Message{}, err
	}
	bodyLen := binary.BigEndian.Uint32(header[16:20])
	if bodyLen > maxBodySize {
		return nil, core.Message{}, codec.ErrFrameTooLarge
	}
	frame := make([]byte, codec.FrameHeaderSize+int(bodyLen))
	copy(frame, header[:])
	if bodyLen > 0 {
		if _, err := io.ReadFull(r, frame[codec.FrameHeaderSize:]); err != nil {
			return nil, core.Message{}, err
		}
	}
	msg, err := codec.KProV1().DecodePacket(frame)
	if err != nil {
		return nil, core.Message{}, err
	}
	return frame, msg, nil
}

func writeFullForTest(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

func strictMessages(msgID uint32, label string, count int) []core.Message {
	messages := make([]core.Message, count)
	for i := range messages {
		messages[i] = strictMessage(msgID, uint64(i+1), label)
	}
	return messages
}

func strictMessage(msgID uint32, seq uint64, label string) core.Message {
	return core.Message{
		ID:   msgID,
		Seq:  seq,
		Data: []byte(fmt.Sprintf("%s:%04d:%08x", label, seq, seq*2654435761)),
	}
}

func strictFaultFrame(t *testing.T, seq int) []byte {
	t.Helper()
	frame, err := codec.KProV1().Encode(strictMessage(msgStrictSyncEcho, uint64(seq), "fault"))
	if err != nil {
		t.Fatalf("encode strict fault frame: %v", err)
	}
	return frame
}

func strictBoundaryMessages(msgID uint32, count int) []core.Message {
	sizes := []int{0, 1, 64, 1024, 4096, 64 * 1024}
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

func strictFlagMessages(msgID uint32, count int) []core.Message {
	messages := strictMessages(msgID, "flags", count)
	for i := range messages {
		messages[i].Flags = uint8(i%255 + 1)
	}
	return messages
}

func assertWireFramesEqual(t *testing.T, wantLog, gotLog *strictWireLog) {
	t.Helper()
	want := wantLog.snapshot()
	got := gotLog.snapshot()
	if len(want) != strictWireMessageCount || len(got) != strictWireMessageCount {
		t.Fatalf("wire log counts differ: want=%d got=%d minimum=%d", len(want), len(got), strictWireMessageCount)
	}
	if len(want) != len(got) {
		t.Fatalf("wire log lengths differ: want=%d got=%d", len(want), len(got))
	}
	for i := range want {
		if want[i].FrameLen != got[i].FrameLen ||
			want[i].CRC32 != got[i].CRC32 ||
			want[i].FrameHex != got[i].FrameHex {
			t.Fatalf("wire frame %d differs\nwant len=%d crc=%s\ngot  len=%d crc=%s", i+1, want[i].FrameLen, want[i].CRC32, got[i].FrameLen, got[i].CRC32)
		}
	}
}

func assertWireFrameSetEqual(t *testing.T, wantLog, gotLog *strictWireLog) {
	t.Helper()
	want := wantLog.snapshot()
	got := gotLog.snapshot()
	if len(want) != strictWireMessageCount || len(got) != strictWireMessageCount {
		t.Fatalf("wire log counts differ: want=%d got=%d minimum=%d", len(want), len(got), strictWireMessageCount)
	}
	wantCounts := make(map[string]int, len(want))
	for _, record := range want {
		wantCounts[record.FrameHex]++
	}
	for _, record := range got {
		if wantCounts[record.FrameHex] == 0 {
			t.Fatalf("unexpected wire frame seq=%d id=%d crc=%s", record.Seq, record.MsgID, record.CRC32)
		}
		wantCounts[record.FrameHex]--
	}
	for frameHex, count := range wantCounts {
		if count != 0 {
			t.Fatalf("missing wire frame %s count=%d", frameHex[:minInt(len(frameHex), 32)], count)
		}
	}
}

func assertStrictEchoResponses(t *testing.T, sent []core.Message, got []strictWireRecord) {
	t.Helper()
	if len(got) != len(sent) {
		t.Fatalf("response count=%d, want %d", len(got), len(sent))
	}
	for i, msg := range sent {
		if got[i].Seq != msg.Seq ||
			got[i].MsgID != msg.ID ||
			got[i].Flags != msg.Flags ||
			got[i].PayloadLen != len(msg.Data) ||
			!strictWirePayloadEqual(got[i], msg.Data) {
			t.Fatalf("response %d = id=%d seq=%d flags=%d payload_len=%d, want id=%d seq=%d flags=%d payload_len=%d",
				i+1, got[i].MsgID, got[i].Seq, got[i].Flags, got[i].PayloadLen, msg.ID, msg.Seq, msg.Flags, len(msg.Data))
		}
	}
}

func assertStrictEchoResponseSet(t *testing.T, sent []core.Message, got []strictWireRecord) {
	t.Helper()
	want := make(map[uint64]core.Message, len(sent))
	for _, msg := range sent {
		want[msg.Seq] = msg
	}
	if len(got) != len(want) {
		t.Fatalf("response count=%d, want %d", len(got), len(want))
	}
	for _, record := range got {
		msg, ok := want[record.Seq]
		if !ok {
			t.Fatalf("unexpected response seq=%d", record.Seq)
		}
		if record.MsgID != msg.ID ||
			record.Flags != msg.Flags ||
			record.PayloadLen != len(msg.Data) ||
			!strictWirePayloadEqual(record, msg.Data) {
			t.Fatalf("response seq=%d = id=%d flags=%d payload_len=%d, want id=%d flags=%d payload_len=%d",
				record.Seq, record.MsgID, record.Flags, record.PayloadLen, msg.ID, msg.Flags, len(msg.Data))
		}
		delete(want, record.Seq)
	}
	if len(want) > 0 {
		t.Fatalf("missing %d responses", len(want))
	}
}

func assertStrictErrorResponses(t *testing.T, sent []core.Message, got []strictWireRecord, text string) {
	t.Helper()
	if len(got) != len(sent) {
		t.Fatalf("error response count=%d, want %d", len(got), len(sent))
	}
	wantPayloadLen := len([]byte(text))
	for i, msg := range sent {
		if got[i].Seq != msg.Seq ||
			got[i].MsgID != core.MsgError ||
			got[i].PayloadLen != wantPayloadLen ||
			!strictWirePayloadEqual(got[i], []byte(text)) {
			t.Fatalf("error response %d = id=%d seq=%d payload_len=%d, want id=%d seq=%d payload_len=%d",
				i+1, got[i].MsgID, got[i].Seq, got[i].PayloadLen, core.MsgError, msg.Seq, wantPayloadLen)
		}
	}
}

func assertStrictPongResponses(t *testing.T, sent []core.Message, got []strictWireRecord) {
	t.Helper()
	if len(got) != len(sent) {
		t.Fatalf("pong response count=%d, want %d", len(got), len(sent))
	}
	for i, msg := range sent {
		if got[i].Seq != msg.Seq || got[i].MsgID != core.MsgPong || got[i].PayloadLen != 0 {
			t.Fatalf("pong response %d = id=%d seq=%d payload_len=%d, want id=%d seq=%d payload_len=0",
				i+1, got[i].MsgID, got[i].Seq, got[i].PayloadLen, core.MsgPong, msg.Seq)
		}
	}
}

func assertStrictQueueFullResponses(t *testing.T, sent []core.Message, got []strictWireRecord) {
	t.Helper()
	assertStrictQueueFullResponsesFor(t, sent, got, msgStrictQueueFull, 2)
}

func assertStrictQueueFullResponsesFor(t *testing.T, sent []core.Message, got []strictWireRecord, echoMsgID uint32, wantEchoes int) {
	t.Helper()
	if len(got) != len(sent) {
		t.Fatalf("queue-full response count=%d, want %d", len(got), len(sent))
	}
	wantBySeq := make(map[uint64]core.Message, len(sent))
	for _, msg := range sent {
		wantBySeq[msg.Seq] = msg
	}
	var echoes int
	var queueFullErrors int
	for _, record := range got {
		msg, ok := wantBySeq[record.Seq]
		if !ok {
			t.Fatalf("queue-full response has unexpected seq=%d", record.Seq)
		}
		switch record.MsgID {
		case echoMsgID:
			if record.PayloadLen != len(msg.Data) || !strictWirePayloadEqual(record, msg.Data) {
				t.Fatalf("queue-full echo seq=%d payload mismatch", record.Seq)
			}
			echoes++
		case core.MsgError:
			wantPayload := []byte(core.ErrDispatchQueueFull.Error())
			if record.PayloadLen != len(wantPayload) || !strictWirePayloadEqual(record, wantPayload) {
				t.Fatalf("queue-full error seq=%d has payload_len=%d", record.Seq, record.PayloadLen)
			}
			queueFullErrors++
		default:
			t.Fatalf("unexpected queue-full response id=%d seq=%d", record.MsgID, record.Seq)
		}
	}
	if echoes != wantEchoes {
		t.Fatalf("queue-full scenario echoed %d messages, want %d", echoes, wantEchoes)
	}
	if queueFullErrors != len(sent)-wantEchoes {
		t.Fatalf("queue-full scenario produced %d errors, want %d", queueFullErrors, len(sent)-wantEchoes)
	}
	if echoes+queueFullErrors != len(sent) {
		t.Fatalf("queue-full responses do not add up: echoes=%d errors=%d total=%d", echoes, queueFullErrors, len(sent))
	}
}

func assertStrictLimitedErrorResponses(t *testing.T, sent []core.Message, got []strictWireRecord, echoMsgID uint32, errorText string, wantEchoes int) {
	t.Helper()
	if len(got) != len(sent) {
		t.Fatalf("limited-error response count=%d, want %d", len(got), len(sent))
	}
	wantBySeq := make(map[uint64]core.Message, len(sent))
	for _, msg := range sent {
		wantBySeq[msg.Seq] = msg
	}
	var echoes int
	var errors int
	errorPayload := []byte(errorText)
	for _, record := range got {
		msg, ok := wantBySeq[record.Seq]
		if !ok {
			t.Fatalf("limited-error response has unexpected seq=%d", record.Seq)
		}
		switch record.MsgID {
		case echoMsgID:
			if record.PayloadLen != len(msg.Data) || !strictWirePayloadEqual(record, msg.Data) {
				t.Fatalf("limited-error echo seq=%d payload mismatch", record.Seq)
			}
			echoes++
		case core.MsgError:
			if record.PayloadLen != len(errorPayload) || !strictWirePayloadEqual(record, errorPayload) {
				t.Fatalf("limited-error seq=%d has payload_len=%d", record.Seq, record.PayloadLen)
			}
			errors++
		default:
			t.Fatalf("unexpected limited-error response id=%d seq=%d", record.MsgID, record.Seq)
		}
	}
	if echoes != wantEchoes {
		t.Fatalf("limited-error scenario echoed %d messages, want %d", echoes, wantEchoes)
	}
	if errors != len(sent)-wantEchoes {
		t.Fatalf("limited-error scenario produced %d errors, want %d", errors, len(sent)-wantEchoes)
	}
}

func waitForStrictWireCount(t *testing.T, log *strictWireLog, count int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if log.count() >= count {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("strict wire log %s count=%d, want %d", log.path, log.count(), count)
}

func waitForStrictRawCount(t *testing.T, log *strictRawLog, count int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if log.count() >= count {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("strict raw log %s count=%d, want %d", log.path, log.count(), count)
}

func assertStrictWireLogCount(t *testing.T, log *strictWireLog, count int) {
	t.Helper()
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if log.count() == count {
			time.Sleep(10 * time.Millisecond)
			if log.count() == count {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := log.count(); got != count {
		t.Fatalf("strict wire log %s count=%d, want %d", log.path, got, count)
	}
}

func assertNoStrictWireResponse(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	var buf [1]byte
	n, err := conn.Read(buf[:])
	if n > 0 || err == nil {
		t.Fatalf("received unexpected malformed-frame response: n=%d err=%v", n, err)
	}
}

func strictWireRecordPayload(record strictWireRecord) []byte {
	frame, err := hex.DecodeString(record.FrameHex)
	if err != nil || len(frame) < codec.FrameHeaderSize {
		return nil
	}
	return frame[codec.FrameHeaderSize:]
}

func strictWirePayloadEqual(record strictWireRecord, data []byte) bool {
	return bytes.Equal(strictWireRecordPayload(record), data)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

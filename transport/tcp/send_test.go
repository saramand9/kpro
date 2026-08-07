package tcp

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
)

type blockingStreamCodec struct {
	codec.StreamCodec
	started chan struct{}
	release chan struct{}
}

type panickingStreamCodec struct{ codec.StreamCodec }

func (panickingStreamCodec) Encode(core.Message) ([]byte, error) {
	panic("encode panic")
}

func (c *blockingStreamCodec) Encode(msg core.Message) ([]byte, error) {
	close(c.started)
	<-c.release
	return c.StreamCodec.Encode(msg)
}

func TestSendAfterCloseNeverQueues(t *testing.T) {
	for attempt := 0; attempt < 128; attempt++ {
		server := New(Config{SendQueueSize: 1})
		done := make(chan struct{})
		close(done)
		session := &session{server: server, done: done, sendQueue: newSessionOutboundQueue(server.config)}
		if err := session.Send(core.Message{}); !errors.Is(err, core.ErrClosed) {
			t.Fatalf("attempt %d: Send() error = %v, want %v", attempt, err, core.ErrClosed)
		}
		if snapshot := session.sendQueue.Snapshot(); snapshot.PendingFrames != 0 || snapshot.PendingBytes != 0 {
			t.Fatalf("attempt %d: outbound snapshot = %+v", attempt, snapshot)
		}
	}
}

func TestShutdownDrainsAcceptedWriteAndCompletesReceipt(t *testing.T) {
	server := New(Config{SendQueueSize: 1, WriteTimeout: time.Second})
	serverConn, peer := net.Pipe()
	defer peer.Close()
	atomic.StoreInt64(&server.active, 1)
	session := &session{
		id:        1,
		server:    server,
		raw:       serverConn,
		sendQueue: newSessionOutboundQueue(server.config),
		done:      make(chan struct{}),
	}
	server.sessions.Store(session.id, session)
	go session.writeLoop()

	want := core.Message{ID: 42, Seq: 7, Data: []byte("drain me")}
	receipt, supported, err := core.SessionSendWithReceipt(session, want)
	if err != nil || !supported || receipt == nil {
		t.Fatalf("SessionSendWithReceipt() = (%p, %v, %v)", receipt, supported, err)
	}
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- server.Shutdown(context.Background()) }()

	select {
	case <-receipt.Done():
		t.Fatal("write receipt completed before the peer accepted bytes")
	case err := <-shutdownDone:
		t.Fatalf("Shutdown() returned before outbound write completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	result, err := server.config.Codec.Read(peer, server.config.MaxFrameSize)
	if err != nil {
		t.Fatalf("read drained frame: %v", err)
	}
	if result.Message.ID != want.ID || result.Message.Seq != want.Seq || string(result.Message.Data) != string(want.Data) {
		t.Fatalf("drained message = %+v, want %+v", result.Message, want)
	}
	if err := receipt.Wait(context.Background()); err != nil {
		t.Fatalf("write receipt error = %v", err)
	}
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("Shutdown() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Shutdown() did not finish after peer accepted the write")
	}
}

func TestServerOutboundSnapshotPersistsAfterSessionClose(t *testing.T) {
	server := New(Config{SendQueueSize: 1, WriteTimeout: time.Second})
	serverConn, peer := net.Pipe()
	defer peer.Close()
	atomic.StoreInt64(&server.active, 1)
	session := &session{
		id:        1,
		server:    server,
		raw:       serverConn,
		sendQueue: newSessionOutboundQueue(server.config, &server.outboundStats),
		done:      make(chan struct{}),
	}
	server.sessions.Store(session.id, session)
	go session.writeLoop()

	want := core.Message{ID: 43, Seq: 8, Data: []byte("observable")}
	receipt, err := session.SendWithReceipt(want)
	if err != nil {
		t.Fatal(err)
	}
	result, err := server.config.Codec.Read(peer, server.config.MaxFrameSize)
	if err != nil {
		t.Fatal(err)
	}
	if result.Message.ID != want.ID || result.Message.Seq != want.Seq || string(result.Message.Data) != string(want.Data) {
		t.Fatalf("received message = %+v, want %+v", result.Message, want)
	}
	if err := receipt.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot := server.OutboundSnapshot()
	if snapshot.Sessions != 1 || snapshot.PendingWrites != 0 || snapshot.PendingBytes != 0 ||
		snapshot.AcceptedWrites != 1 || snapshot.SucceededWrites != 1 || snapshot.FailedWrites != 0 || snapshot.DroppedWrites != 0 {
		t.Fatalf("live outbound snapshot = %+v", snapshot)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot = server.OutboundSnapshot()
	if snapshot.Sessions != 0 || snapshot.AcceptedWrites != 1 || snapshot.SucceededWrites != 1 {
		t.Fatalf("closed outbound snapshot = %+v", snapshot)
	}
	if err := server.DrainOutbound(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot = server.OutboundSnapshot()
	if snapshot.DrainSucceeded != 1 || snapshot.DrainDeadline != 0 || snapshot.DrainCanceled != 0 || snapshot.DrainFailed != 0 {
		t.Fatalf("drain outbound snapshot = %+v", snapshot)
	}
}

func TestSendQueueFullReturnsOverloadBeforeClosedState(t *testing.T) {
	server := New(Config{SendQueueSize: 1})
	queue := newSessionOutboundQueue(server.config)
	reservation, err := queue.Reserve(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := reservation.Commit(outboundFrame{payload: []byte{0}}, 1); err != nil {
		t.Fatal(err)
	}
	session := &session{server: server, sendQueue: queue, done: make(chan struct{})}
	session.closeOnce.Do(func() {})
	err = session.Send(core.Message{})
	if !errors.Is(err, core.ErrSendQueueFull) {
		t.Fatalf("Send() error = %v, want %v", err, core.ErrSendQueueFull)
	}
	if errors.Is(err, core.ErrClosed) {
		t.Fatalf("Send() error = %v, must report overload rather than prior closed state", err)
	}
}

func TestSendCannotCommitAfterConcurrentClose(t *testing.T) {
	blocking := &blockingStreamCodec{
		StreamCodec: codec.KProV1(),
		started:     make(chan struct{}),
		release:     make(chan struct{}),
	}
	server := New(Config{Codec: blocking, SendQueueSize: 1})
	atomic.StoreInt64(&server.active, 1)
	session := &session{
		id:        1,
		server:    server,
		sendQueue: newSessionOutboundQueue(server.config),
		done:      make(chan struct{}),
	}
	server.sessions.Store(session.id, session)

	errCh := make(chan error, 1)
	go func() { errCh <- session.Send(core.Message{}) }()
	<-blocking.started
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	close(blocking.release)
	if err := <-errCh; !errors.Is(err, core.ErrClosed) {
		t.Fatalf("Send() error = %v, want %v", err, core.ErrClosed)
	}
	if snapshot := session.sendQueue.Snapshot(); !snapshot.Closed || snapshot.PendingFrames != 0 || snapshot.PendingBytes != 0 {
		t.Fatalf("closed outbound snapshot = %+v", snapshot)
	}
}

func TestSendReleasesReservationWhenCodecPanics(t *testing.T) {
	server := New(Config{Codec: panickingStreamCodec{StreamCodec: codec.KProV1()}, SendQueueSize: 1})
	session := &session{
		server:    server,
		sendQueue: newSessionOutboundQueue(server.config),
		done:      make(chan struct{}),
	}
	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatal("Send() did not propagate codec panic")
			}
		}()
		_ = session.Send(core.Message{})
	}()
	if snapshot := session.sendQueue.Snapshot(); snapshot.PendingFrames != 0 || snapshot.PendingBytes != 0 {
		t.Fatalf("outbound capacity leaked after codec panic: %+v", snapshot)
	}
}

func TestSendEnforcesPendingByteLimit(t *testing.T) {
	server := New(Config{SendQueueSize: 2, MaxPendingBytes: 25})
	atomic.StoreInt64(&server.active, 1)
	session := &session{
		id:        1,
		server:    server,
		sendQueue: newSessionOutboundQueue(server.config),
		done:      make(chan struct{}),
	}
	if err := session.Send(core.Message{}); err != nil {
		t.Fatal(err)
	}
	state, ok := core.SessionOutboundState(session)
	if !ok {
		t.Fatal("TCP session does not expose outbound state")
	}
	if state.PendingWrites != 1 || state.PendingBytes != codec.FrameHeaderSize || !state.AboveHighWatermark || state.Writable {
		t.Fatalf("outbound state = %+v", state)
	}
	if err := session.Send(core.Message{}); !errors.Is(err, core.ErrSendQueueFull) {
		t.Fatalf("second Send() error = %v, want %v", err, core.ErrSendQueueFull)
	}
}

func TestPendingByteConfigDefaultsAndValidation(t *testing.T) {
	server := New(Config{MaxFrameSize: 100, SendQueueSize: 2})
	want := int64(2 * (100 + codec.FrameHeaderSize))
	if server.config.MaxPendingBytes != want {
		t.Fatalf("MaxPendingBytes = %d, want %d", server.config.MaxPendingBytes, want)
	}
	if err := New(Config{MaxPendingBytes: -1}).Validate(); err == nil || !strings.Contains(err.Error(), "MaxPendingBytes") {
		t.Fatalf("negative MaxPendingBytes validation error = %v", err)
	}
	if server.config.MaxWriteBatchFrames != 2 {
		t.Fatalf("MaxWriteBatchFrames = %d, want SendQueueSize cap 2", server.config.MaxWriteBatchFrames)
	}
	if server.config.MaxWriteBatchBytes != want {
		t.Fatalf("MaxWriteBatchBytes = %d, want pending-byte cap %d", server.config.MaxWriteBatchBytes, want)
	}
	if server.config.MaxRetainedBufferBytes != DefaultMaxRetainedBufferBytes {
		t.Fatalf("MaxRetainedBufferBytes = %d, want %d", server.config.MaxRetainedBufferBytes, DefaultMaxRetainedBufferBytes)
	}
	if server.config.MaxReadBufferSize != DefaultMaxReadBufferSize {
		t.Fatalf("MaxReadBufferSize = %d, want %d", server.config.MaxReadBufferSize, DefaultMaxReadBufferSize)
	}
	for _, test := range []struct {
		name   string
		config Config
	}{
		{name: "MaxWriteBatchFrames", config: Config{MaxWriteBatchFrames: -1}},
		{name: "MaxWriteBatchBytes", config: Config{MaxWriteBatchBytes: -1}},
		{name: "MaxRetainedBufferBytes", config: Config{MaxRetainedBufferBytes: -1}},
		{name: "MaxReadBufferSize", config: Config{MaxReadBufferSize: -1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := New(test.config).Validate(); err == nil || !strings.Contains(err.Error(), test.name) {
				t.Fatalf("negative %s validation error = %v", test.name, err)
			}
		})
	}
}

func TestWaitOutboundWritableCapabilityAndBound(t *testing.T) {
	server := New(Config{SendQueueSize: 1})
	session := &session{
		server:    server,
		sendQueue: newSessionOutboundQueue(server.config),
		done:      make(chan struct{}),
	}
	if supported, err := core.SessionWaitOutboundWritable(nil, session); !supported || err != nil {
		t.Fatalf("immediate SessionWaitOutboundWritable() = (%v, %v)", supported, err)
	}
	reservation, err := session.sendQueue.Reserve(1)
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.Cancel()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- session.WaitOutboundWritable(ctx) }()
	waitForOutboundWaiters(t, session.OutboundState, 1)
	state := session.OutboundState()
	if state.WaitingWriters != 1 || state.MaxWaitingWriters != 1 {
		t.Fatalf("outbound waiter state = %+v", state)
	}
	if err := session.WaitOutboundWritable(context.Background()); !errors.Is(err, core.ErrTooManyOutboundWaiters) {
		t.Fatalf("overflow WaitOutboundWritable() error = %v", err)
	}
	if session.OutboundState().Closed {
		t.Fatal("waiter overload closed TCP session")
	}
	cancel()
	if err := waitOutboundResult(t, result); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled WaitOutboundWritable() error = %v", err)
	}
	session.sendQueue.Close()
	if supported, err := core.SessionWaitOutboundWritable(context.Background(), session); !supported || !errors.Is(err, core.ErrClosed) {
		t.Fatalf("closed SessionWaitOutboundWritable() = (%v, %v)", supported, err)
	}
}

func waitForOutboundWaiters(t *testing.T, state func() core.OutboundState, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if state().WaitingWriters == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("waiting writers = %d, want %d", state().WaitingWriters, want)
}

func waitOutboundResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for outbound result")
		return nil
	}
}

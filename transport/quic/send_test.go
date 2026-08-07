package quic

import (
	"context"
	"errors"
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

func TestSendWithReceiptCompletesOnWriterResult(t *testing.T) {
	server := New(Config{SendQueueSize: 1})
	session := &session{server: server, sendQueue: newSessionOutboundQueue(server.config), done: make(chan struct{})}
	receipt, supported, err := core.SessionSendWithReceipt(session, core.Message{ID: 1, Data: []byte("quic")})
	if err != nil || !supported || receipt == nil {
		t.Fatalf("SessionSendWithReceipt() = (%p, %v, %v)", receipt, supported, err)
	}
	select {
	case <-receipt.Done():
		t.Fatal("receipt completed at queue admission")
	default:
	}
	item := <-session.sendQueue.Items()
	item.Done()
	if err := receipt.Wait(context.Background()); err != nil {
		t.Fatalf("receipt error = %v", err)
	}
	if supported, err := core.SessionDrainOutbound(nil, session); !supported || err != nil {
		t.Fatalf("SessionDrainOutbound() = (%v, %v)", supported, err)
	}
	if receipt, err := session.SendWithReceipt(core.Message{}); receipt != nil || !errors.Is(err, core.ErrClosed) {
		t.Fatalf("SendWithReceipt() after drain = (%p, %v)", receipt, err)
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
	if snapshot := session.sendQueue.Snapshot(); snapshot.PendingFrames != 1 || snapshot.PendingBytes != codec.FrameHeaderSize || !snapshot.AboveHighWatermark {
		t.Fatalf("outbound snapshot = %+v", snapshot)
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
}

func TestWaitOutboundWritableCapabilityAndBound(t *testing.T) {
	server := New(Config{SendQueueSize: 1})
	session := &session{server: server, sendQueue: newSessionOutboundQueue(server.config), done: make(chan struct{})}
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
	if state := session.OutboundState(); state.WaitingWriters != 1 || state.MaxWaitingWriters != 1 {
		t.Fatalf("outbound waiter state = %+v", state)
	}
	if err := session.WaitOutboundWritable(context.Background()); !errors.Is(err, core.ErrTooManyOutboundWaiters) {
		t.Fatalf("overflow WaitOutboundWritable() error = %v", err)
	}
	if session.OutboundState().Closed {
		t.Fatal("waiter overload closed QUIC session")
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

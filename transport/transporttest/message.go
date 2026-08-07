package transporttest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/transport"
)

const defaultTimeout = 3 * time.Second

// MessagePeer is the protocol-native peer used to exercise a MessageServer.
// Implementations must honor context cancellation and deadlines. Send and
// Receive may be called concurrently.
type MessagePeer interface {
	Send(context.Context, core.Message) error
	Receive(context.Context) (core.Message, error)
	Close() error
}

// MessageHarness contains one fresh server and a function that connects a
// fresh protocol-native peer. Connect is called only after Ready closes when
// the server implements transport.ReadinessProvider. Otherwise it must retry
// startup races until ctx finishes.
type MessageHarness struct {
	Server  transport.MessageServer
	Connect func(context.Context) (MessagePeer, error)
}

// MessageRequirements makes optional transport and session capabilities
// mandatory for a particular implementation. Optional capabilities are still
// tested whenever they are present, even when not required here.
type MessageRequirements struct {
	Descriptor       bool
	Readiness        bool
	SessionLifecycle bool
	OutboundState    bool
	WritableWait     bool
	WriteReceipt     bool
	SessionDrain     bool
	Quiesce          bool
	TransportDrain   bool
	OutboundSnapshot bool
}

// MessageContract configures RunMessageServerContract.
//
// New must return a new, never-started server on every call. Probe is sent by
// the peer to establish a session; Reply is sent by that server-side session.
// AssertMessage can replace the default comparison of Flags, ID, Seq, and Data
// when a protocol deliberately maps those fields differently.
type MessageContract struct {
	New           func(*testing.T) MessageHarness
	Probe         core.Message
	Reply         core.Message
	Timeout       time.Duration
	Require       MessageRequirements
	AssertMessage func(*testing.T, core.Message, core.Message)
}

// RunMessageServerContract runs the public lifecycle, message, session,
// graceful-drain, and aggregate-observability contract against a transport.
// It creates a fresh harness for every subtest and may be called from an
// ordinary Test function.
func RunMessageServerContract(t *testing.T, contract MessageContract) {
	t.Helper()
	contract = normalizeMessageContract(t, contract)

	t.Run("descriptor_and_lifecycle", func(t *testing.T) {
		running := startMessageHarness(t, contract)
		server := running.harness.Server
		if server.Name() == "" {
			t.Fatal("MessageServer.Name() is empty")
		}
		if validator, ok := server.(transport.Validator); ok {
			if err := validator.Validate(); err != nil {
				t.Fatalf("Validator.Validate() error = %v", err)
			}
		}
		provider, hasDescriptor := server.(transport.CapabilityProvider)
		requireCapability(t, "transport.CapabilityProvider", hasDescriptor, contract.Require.Descriptor)
		if hasDescriptor {
			descriptor := provider.Descriptor()
			if err := descriptor.Validate(); err != nil {
				t.Fatalf("Descriptor().Validate() error = %v", err)
			}
			if descriptor.Name != server.Name() {
				t.Fatalf("Descriptor().Name = %q, MessageServer.Name() = %q", descriptor.Name, server.Name())
			}
			if descriptor.Mode != transport.ModeMessage {
				t.Fatalf("Descriptor().Mode = %q, want %q", descriptor.Mode, transport.ModeMessage)
			}
		}
		shutdownErr, serveErr := running.stop()
		if shutdownErr != nil {
			t.Fatalf("Shutdown() error = %v", shutdownErr)
		}
		if serveErr != nil {
			t.Fatalf("ListenAndServe() after Shutdown error = %v", serveErr)
		}
		ctx, cancel := context.WithTimeout(context.Background(), contract.Timeout)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			t.Fatalf("second Shutdown() error = %v", err)
		}
	})

	t.Run("message_and_session", func(t *testing.T) {
		running := startMessageHarness(t, contract)
		observation := establishSession(t, running, contract)
		assertSessionIdentity(t, observation.session)
		assertSessionState(t, observation.session, contract.Require.OutboundState)
		assertWritableWait(t, observation.session, contract)

		got := sendServerMessage(t, running, observation.session, contract.Reply, contract)
		contract.AssertMessage(t, got, contract.Reply)

		done := core.SessionDone(observation.session)
		requireCapability(t, "core.SessionLifecycle", done != nil, contract.Require.SessionLifecycle)
		if err := observation.session.Close(); err != nil {
			t.Fatalf("Session.Close() error = %v", err)
		}
		if err := observation.session.Close(); err != nil {
			t.Fatalf("second Session.Close() error = %v", err)
		}
		if done != nil {
			awaitClosed(t, done, contract.Timeout, "session Done")
		}
		if err := observation.session.Send(contract.Reply); !errors.Is(err, core.ErrClosed) {
			t.Fatalf("Session.Send() after Close error = %v, want %v", err, core.ErrClosed)
		}
		if _, supported, err := core.SessionSendWithReceipt(observation.session, contract.Reply); supported && !errors.Is(err, core.ErrClosed) {
			t.Fatalf("SessionSendWithReceipt() after Close error = %v, want %v", err, core.ErrClosed)
		}
	})

	t.Run("session_outbound_drain", func(t *testing.T) {
		running := startMessageHarness(t, contract)
		observation := establishSession(t, running, contract)
		_, supported := observation.session.(core.SessionOutboundDrainer)
		requireCapability(t, "core.SessionOutboundDrainer", supported, contract.Require.SessionDrain)
		if !supported {
			t.Skip("session does not expose outbound drain")
		}

		receiveCtx, cancelReceive := context.WithTimeout(context.Background(), contract.Timeout)
		defer cancelReceive()
		received := receiveAsync(running.peer, receiveCtx)
		receipt, receiptSupported, err := core.SessionSendWithReceipt(observation.session, contract.Reply)
		if err != nil {
			t.Fatalf("SessionSendWithReceipt() error = %v", err)
		}
		requireCapability(t, "core.WriteReceiptSender", receiptSupported, contract.Require.WriteReceipt)
		if !receiptSupported {
			if err := observation.session.Send(contract.Reply); err != nil {
				t.Fatalf("Session.Send() error = %v", err)
			}
		}
		drainCtx, cancelDrain := context.WithTimeout(context.Background(), contract.Timeout)
		defer cancelDrain()
		if supported, err := core.SessionDrainOutbound(drainCtx, observation.session); !supported || err != nil {
			t.Fatalf("SessionDrainOutbound() = (%v, %v), want (true, nil)", supported, err)
		}
		if receipt != nil {
			assertReceiptCompleted(t, receipt, contract.Timeout)
		}
		got := awaitPeerMessage(t, received, contract.Timeout)
		contract.AssertMessage(t, got, contract.Reply)
		if supported, err := core.SessionDrainOutbound(drainCtx, observation.session); !supported || err != nil {
			t.Fatalf("second SessionDrainOutbound() = (%v, %v), want (true, nil)", supported, err)
		}
		if err := observation.session.Send(contract.Reply); !errors.Is(err, core.ErrClosed) {
			t.Fatalf("Session.Send() after drain error = %v, want %v", err, core.ErrClosed)
		}
		if state, ok := core.SessionOutboundState(observation.session); ok && (!state.Closed || state.Writable) {
			t.Fatalf("outbound state after drain = %+v, want closed and not writable", state)
		}
	})

	t.Run("quiesce_then_transport_drain", func(t *testing.T) {
		running := startMessageHarness(t, contract)
		observation := establishSession(t, running, contract)
		quiescer, hasQuiesce := running.harness.Server.(transport.Quiescer)
		drainer, hasDrain := running.harness.Server.(transport.OutboundDrainer)
		requireCapability(t, "transport.Quiescer", hasQuiesce, contract.Require.Quiesce)
		requireCapability(t, "transport.OutboundDrainer", hasDrain, contract.Require.TransportDrain)
		if !hasQuiesce || !hasDrain {
			t.Skip("transport does not expose both quiesce and outbound drain")
		}

		ctx, cancel := context.WithTimeout(context.Background(), contract.Timeout)
		defer cancel()
		if err := quiescer.Quiesce(ctx); err != nil {
			t.Fatalf("Quiesce() error = %v", err)
		}
		got := sendServerMessage(t, running, observation.session, contract.Reply, contract)
		contract.AssertMessage(t, got, contract.Reply)
		if err := drainer.DrainOutbound(ctx); err != nil {
			t.Fatalf("DrainOutbound() error = %v", err)
		}
		if err := observation.session.Send(contract.Reply); !errors.Is(err, core.ErrClosed) {
			t.Fatalf("Session.Send() after transport drain error = %v, want %v", err, core.ErrClosed)
		}
		if err := quiescer.Quiesce(ctx); err != nil {
			t.Fatalf("second Quiesce() error = %v", err)
		}
		if err := drainer.DrainOutbound(ctx); err != nil {
			t.Fatalf("second DrainOutbound() error = %v", err)
		}
	})

	t.Run("outbound_snapshot", func(t *testing.T) {
		running := startMessageHarness(t, contract)
		observation := establishSession(t, running, contract)
		before, supported := transport.ComponentOutboundSnapshot(running.harness.Server)
		requireCapability(t, "transport.OutboundSnapshotter", supported, contract.Require.OutboundSnapshot)
		if !supported {
			t.Skip("transport does not expose outbound snapshots")
		}
		assertOutboundSnapshot(t, before)
		if before.Sessions < 1 {
			t.Fatalf("OutboundSnapshot().Sessions = %d, want at least 1", before.Sessions)
		}

		got := sendServerMessage(t, running, observation.session, contract.Reply, contract)
		contract.AssertMessage(t, got, contract.Reply)
		after, _ := transport.ComponentOutboundSnapshot(running.harness.Server)
		assertOutboundSnapshot(t, after)
		if after.AcceptedWrites != before.AcceptedWrites+1 {
			t.Fatalf("AcceptedWrites delta = %d, want 1", after.AcceptedWrites-before.AcceptedWrites)
		}
		if after.SucceededWrites != before.SucceededWrites+1 {
			t.Fatalf("SucceededWrites delta = %d, want 1", after.SucceededWrites-before.SucceededWrites)
		}
		if after.AcceptedBytes <= before.AcceptedBytes || after.SucceededBytes <= before.SucceededBytes {
			t.Fatalf("outbound byte counters did not advance: before=%+v after=%+v", before, after)
		}
		if after.FailedWrites != before.FailedWrites || after.DroppedWrites != before.DroppedWrites {
			t.Fatalf("successful write changed failure counters: before=%+v after=%+v", before, after)
		}
		if after.PendingWrites != 0 || after.PendingBytes != 0 {
			t.Fatalf("completed write remains pending: %+v", after)
		}

		if err := observation.session.Close(); err != nil {
			t.Fatalf("Session.Close() error = %v", err)
		}
		closed, _ := transport.ComponentOutboundSnapshot(running.harness.Server)
		if closed.Sessions != after.Sessions-1 {
			t.Fatalf("Sessions after close = %d, want %d", closed.Sessions, after.Sessions-1)
		}
		if closed.AcceptedWrites != after.AcceptedWrites || closed.SucceededWrites != after.SucceededWrites {
			t.Fatalf("process-lifetime counters changed after session close: before=%+v after=%+v", after, closed)
		}
		if drainer, ok := running.harness.Server.(transport.OutboundDrainer); ok {
			ctx, cancel := context.WithTimeout(context.Background(), contract.Timeout)
			defer cancel()
			if err := drainer.DrainOutbound(ctx); err != nil {
				t.Fatalf("DrainOutbound() error = %v", err)
			}
			drained, _ := transport.ComponentOutboundSnapshot(running.harness.Server)
			if drained.DrainSucceeded != closed.DrainSucceeded+1 {
				t.Fatalf("DrainSucceeded delta = %d, want 1", drained.DrainSucceeded-closed.DrainSucceeded)
			}
		}
	})

	t.Run("shutdown_before_start", func(t *testing.T) {
		harness := newMessageHarness(t, contract)
		ctx, cancel := context.WithTimeout(context.Background(), contract.Timeout)
		defer cancel()
		if err := harness.Server.Shutdown(ctx); err != nil {
			t.Fatalf("Shutdown() before start error = %v", err)
		}
		if err := harness.Server.Shutdown(ctx); err != nil {
			t.Fatalf("second Shutdown() before start error = %v", err)
		}
		result := make(chan error, 1)
		go func() {
			result <- harness.Server.ListenAndServe(ctx, func(core.Context) error { return nil })
		}()
		select {
		case err := <-result:
			if err == nil {
				t.Fatal("ListenAndServe() after Shutdown returned nil, want rejection")
			}
		case <-ctx.Done():
			t.Fatal("ListenAndServe() after Shutdown did not return promptly")
		}
	})

	t.Run("duplicate_start", func(t *testing.T) {
		running := startMessageHarness(t, contract)
		ctx, cancel := context.WithTimeout(context.Background(), contract.Timeout)
		defer cancel()
		result := make(chan error, 1)
		go func() {
			result <- running.harness.Server.ListenAndServe(ctx, func(core.Context) error { return nil })
		}()
		select {
		case err := <-result:
			if !errors.Is(err, core.ErrAlreadyStarted) {
				t.Fatalf("second ListenAndServe() error = %v, want %v", err, core.ErrAlreadyStarted)
			}
		case <-ctx.Done():
			t.Fatal("second ListenAndServe() did not return promptly")
		}
	})
}

type observedMessage struct {
	session core.Session
	message core.Message
}

type peerResult struct {
	message core.Message
	err     error
}

type runningMessageHarness struct {
	harness       MessageHarness
	peer          MessagePeer
	observed      chan observedMessage
	cancel        context.CancelFunc
	serveDone     chan error
	serveReturned bool
	timeout       time.Duration
	stopOnce      sync.Once
	shutdownErr   error
	serveErr      error
}

func normalizeMessageContract(t *testing.T, contract MessageContract) MessageContract {
	t.Helper()
	if contract.New == nil {
		t.Fatal("MessageContract.New is required")
	}
	if contract.Timeout <= 0 {
		contract.Timeout = defaultTimeout
	}
	if contract.Probe.ID == 0 {
		contract.Probe = core.Message{Flags: 1, ID: 0x7fff0001, Seq: 1, Data: []byte("transport-contract-probe")}
	}
	if contract.Reply.ID == 0 {
		contract.Reply = core.Message{Flags: 2, ID: 0x7fff0002, Seq: 2, Data: []byte("transport-contract-reply")}
	}
	if contract.AssertMessage == nil {
		contract.AssertMessage = assertDefaultMessage
	}
	return contract
}

func newMessageHarness(t *testing.T, contract MessageContract) MessageHarness {
	t.Helper()
	harness := contract.New(t)
	if harness.Server == nil {
		t.Fatal("MessageHarness.Server is required")
	}
	if harness.Connect == nil {
		t.Fatal("MessageHarness.Connect is required")
	}
	return harness
}

func startMessageHarness(t *testing.T, contract MessageContract) *runningMessageHarness {
	t.Helper()
	harness := newMessageHarness(t, contract)
	ctx, cancel := context.WithCancel(context.Background())
	running := &runningMessageHarness{
		harness:   harness,
		observed:  make(chan observedMessage, 8),
		cancel:    cancel,
		serveDone: make(chan error, 1),
		timeout:   contract.Timeout,
	}
	t.Cleanup(func() {
		shutdownErr, serveErr := running.stop()
		if shutdownErr != nil {
			t.Errorf("contract cleanup Shutdown() error = %v", shutdownErr)
		}
		if serveErr != nil {
			t.Errorf("contract cleanup ListenAndServe() error = %v", serveErr)
		}
	})
	handler := func(messageCtx core.Context) error {
		cloned := core.CloneContext(messageCtx)
		select {
		case running.observed <- observedMessage{session: messageCtx.Session(), message: cloned.Message()}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	go func() {
		running.serveDone <- harness.Server.ListenAndServe(ctx, handler)
	}()

	ready, hasReadiness := harness.Server.(transport.ReadinessProvider)
	requireCapability(t, "transport.ReadinessProvider", hasReadiness, contract.Require.Readiness)
	if hasReadiness {
		channel := ready.Ready()
		if channel == nil {
			t.Fatal("ReadinessProvider.Ready() returned nil")
		}
		select {
		case <-channel:
		case err := <-running.serveDone:
			running.serveErr = err
			running.serveReturned = true
			t.Fatalf("ListenAndServe() returned before readiness: %v", err)
		case <-time.After(contract.Timeout):
			t.Fatal("Ready() did not close before timeout")
		}
	}

	connectCtx, cancelConnect := context.WithTimeout(context.Background(), contract.Timeout)
	defer cancelConnect()
	peer, err := harness.Connect(connectCtx)
	if err != nil {
		t.Fatalf("MessageHarness.Connect() error = %v", err)
	}
	if peer == nil {
		t.Fatal("MessageHarness.Connect() returned a nil peer")
	}
	running.peer = peer
	return running
}

func (r *runningMessageHarness) stop() (error, error) {
	r.stopOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
		r.shutdownErr = r.harness.Server.Shutdown(ctx)
		cancel()
		if r.peer != nil {
			_ = r.peer.Close()
		}
		r.cancel()
		if !r.serveReturned {
			select {
			case r.serveErr = <-r.serveDone:
				r.serveReturned = true
			case <-time.After(r.timeout):
				r.serveErr = fmt.Errorf("ListenAndServe did not stop within %s", r.timeout)
			}
		}
	})
	return r.shutdownErr, r.serveErr
}

func establishSession(t *testing.T, running *runningMessageHarness, contract MessageContract) observedMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), contract.Timeout)
	defer cancel()
	if err := running.peer.Send(ctx, contract.Probe); err != nil {
		t.Fatalf("peer Send(probe) error = %v", err)
	}
	select {
	case observation := <-running.observed:
		if observation.session == nil {
			t.Fatal("handler observed a nil session")
		}
		contract.AssertMessage(t, observation.message, contract.Probe)
		return observation
	case err := <-running.serveDone:
		running.serveErr = err
		running.serveReturned = true
		t.Fatalf("ListenAndServe() returned before probe dispatch: %v", err)
	case <-ctx.Done():
		t.Fatalf("probe was not dispatched: %v", ctx.Err())
	}
	return observedMessage{}
}

func sendServerMessage(t *testing.T, running *runningMessageHarness, session core.Session, message core.Message, contract MessageContract) core.Message {
	t.Helper()
	receiveCtx, cancelReceive := context.WithTimeout(context.Background(), contract.Timeout)
	defer cancelReceive()
	received := receiveAsync(running.peer, receiveCtx)
	receipt, supported, err := core.SessionSendWithReceipt(session, message)
	requireCapability(t, "core.WriteReceiptSender", supported, contract.Require.WriteReceipt)
	if supported {
		if err != nil {
			t.Fatalf("SessionSendWithReceipt() error = %v", err)
		}
		if receipt == nil {
			t.Fatal("SessionSendWithReceipt() returned a nil receipt without error")
		}
		assertReceiptCompleted(t, receipt, contract.Timeout)
	} else if err := session.Send(message); err != nil {
		t.Fatalf("Session.Send() error = %v", err)
	}
	return awaitPeerMessage(t, received, contract.Timeout)
}

func receiveAsync(peer MessagePeer, ctx context.Context) <-chan peerResult {
	result := make(chan peerResult, 1)
	go func() {
		message, err := peer.Receive(ctx)
		result <- peerResult{message: message, err: err}
	}()
	return result
}

func awaitPeerMessage(t *testing.T, result <-chan peerResult, timeout time.Duration) core.Message {
	t.Helper()
	select {
	case received := <-result:
		if received.err != nil {
			t.Fatalf("peer Receive() error = %v", received.err)
		}
		return received.message
	case <-time.After(timeout):
		t.Fatal("peer Receive() did not complete before timeout")
	}
	return core.Message{}
}

func assertReceiptCompleted(t *testing.T, receipt *core.WriteReceipt, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := receipt.Wait(ctx); err != nil {
		t.Fatalf("WriteReceipt.Wait() error = %v", err)
	}
	firstErr, completed := receipt.Result()
	if !completed || firstErr != nil {
		t.Fatalf("WriteReceipt.Result() = (%v, %v), want (nil, true)", firstErr, completed)
	}
	secondErr, secondCompleted := receipt.Result()
	if secondCompleted != completed || !errors.Is(secondErr, firstErr) {
		t.Fatalf("WriteReceipt.Result() is unstable: first=(%v, %v) second=(%v, %v)", firstErr, completed, secondErr, secondCompleted)
	}
}

func assertSessionIdentity(t *testing.T, session core.Session) {
	t.Helper()
	if session.ID() == 0 {
		t.Error("Session.ID() = 0, want a stable non-zero identifier")
	}
	if session.Transport() == "" {
		t.Error("Session.Transport() is empty")
	}
}

func assertSessionState(t *testing.T, session core.Session, required bool) {
	t.Helper()
	state, supported := core.SessionOutboundState(session)
	requireCapability(t, "core.OutboundStateProvider", supported, required)
	if !supported {
		return
	}
	if state.PendingWrites < 0 || state.PendingBytes < 0 || state.WaitingWriters < 0 {
		t.Fatalf("OutboundState() has negative current values: %+v", state)
	}
	if state.MaxPendingWrites < 0 || state.MaxPendingBytes < 0 || state.MaxWaitingWriters < 0 {
		t.Fatalf("OutboundState() has negative limits: %+v", state)
	}
	if state.MaxPendingWrites > 0 && state.PendingWrites > state.MaxPendingWrites {
		t.Fatalf("OutboundState() exceeds write limit: %+v", state)
	}
	if state.MaxPendingBytes > 0 && state.PendingBytes > state.MaxPendingBytes {
		t.Fatalf("OutboundState() exceeds byte limit: %+v", state)
	}
	if state.WaitingWriters > state.MaxWaitingWriters && state.MaxWaitingWriters > 0 {
		t.Fatalf("OutboundState() exceeds waiter limit: %+v", state)
	}
	if state.LowWatermarkBytes < 0 || state.HighWatermarkBytes < 0 ||
		(state.HighWatermarkBytes > 0 && state.LowWatermarkBytes > state.HighWatermarkBytes) {
		t.Fatalf("OutboundState() has invalid watermarks: %+v", state)
	}
	if state.Closed && state.Writable {
		t.Fatalf("OutboundState() is both closed and writable: %+v", state)
	}
}

func assertWritableWait(t *testing.T, session core.Session, contract MessageContract) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), contract.Timeout)
	defer cancel()
	supported, err := core.SessionWaitOutboundWritable(ctx, session)
	requireCapability(t, "core.OutboundWritableWaiter", supported, contract.Require.WritableWait)
	if supported && err != nil {
		t.Fatalf("SessionWaitOutboundWritable() on an idle session error = %v", err)
	}
}

func assertOutboundSnapshot(t *testing.T, snapshot transport.OutboundSnapshot) {
	t.Helper()
	if snapshot.Sessions < 0 || snapshot.PendingWrites < 0 || snapshot.PendingBytes < 0 ||
		snapshot.WaitingWriters < 0 || snapshot.SaturatedSessions < 0 {
		t.Fatalf("OutboundSnapshot() has negative gauges: %+v", snapshot)
	}
	if snapshot.SaturatedSessions > snapshot.Sessions {
		t.Fatalf("OutboundSnapshot() saturated sessions exceed sessions: %+v", snapshot)
	}
	if snapshot.SucceededWrites+snapshot.FailedWrites+snapshot.DroppedWrites > snapshot.AcceptedWrites {
		t.Fatalf("OutboundSnapshot() terminal writes exceed accepted writes: %+v", snapshot)
	}
	if snapshot.SucceededBytes+snapshot.FailedBytes+snapshot.DroppedBytes > snapshot.AcceptedBytes {
		t.Fatalf("OutboundSnapshot() terminal bytes exceed accepted bytes: %+v", snapshot)
	}
	if snapshot.DrainDurationTotal < 0 || snapshot.LastDrainDuration < 0 || snapshot.MaxDrainDuration < 0 ||
		snapshot.LastDrainDuration > snapshot.MaxDrainDuration || snapshot.MaxDrainDuration > snapshot.DrainDurationTotal {
		t.Fatalf("OutboundSnapshot() has invalid drain durations: %+v", snapshot)
	}
}

func assertDefaultMessage(t *testing.T, got, want core.Message) {
	t.Helper()
	if got.Flags != want.Flags || got.ID != want.ID || got.Seq != want.Seq || !reflect.DeepEqual(got.Data, want.Data) {
		t.Fatalf("message = {Flags:%d ID:%d Seq:%d Data:%q}, want {Flags:%d ID:%d Seq:%d Data:%q}",
			got.Flags, got.ID, got.Seq, got.Data, want.Flags, want.ID, want.Seq, want.Data)
	}
}

func requireCapability(t *testing.T, name string, supported, required bool) {
	t.Helper()
	if required && !supported {
		t.Fatalf("required capability %s is not implemented", name)
	}
}

func awaitClosed(t *testing.T, done <-chan struct{}, timeout time.Duration, name string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatalf("%s did not close before timeout", name)
	}
}

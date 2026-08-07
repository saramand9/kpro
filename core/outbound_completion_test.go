package core

import (
	"context"
	"errors"
	"testing"
	"time"
)

type completionSession struct {
	*fakeSession
	receipt  *WriteReceipt
	sendErr  error
	drainErr error
}

func (s *completionSession) SendWithReceipt(Message) (*WriteReceipt, error) {
	return s.receipt, s.sendErr
}

func (s *completionSession) DrainOutbound(ctx context.Context) error {
	if ctx == nil {
		panic("nil drain context")
	}
	return s.drainErr
}

func TestWriteReceiptCompletion(t *testing.T) {
	wantErr := errors.New("write failed")
	receipt := NewWriteReceipt()
	if err, completed := receipt.Result(); completed || err != nil {
		t.Fatalf("Result() before completion = (%v, %v), want (nil, false)", err, completed)
	}
	if !receipt.Complete(wantErr) {
		t.Fatal("first Complete() = false, want true")
	}
	if receipt.Complete(nil) {
		t.Fatal("second Complete() = true, want false")
	}
	if err := receipt.Wait(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("Wait() error = %v, want %v", err, wantErr)
	}
	if err, completed := receipt.Result(); !completed || !errors.Is(err, wantErr) {
		t.Fatalf("Result() = (%v, %v), want (%v, true)", err, completed, wantErr)
	}
}

func TestWriteReceiptZeroValueAndCancellation(t *testing.T) {
	var receipt WriteReceipt
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := receipt.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() error = %v, want %v", err, context.Canceled)
	}
	if !receipt.Complete(nil) {
		t.Fatal("zero-value Complete() = false, want true")
	}
	if err := receipt.Wait(ctx); err != nil {
		t.Fatalf("completed receipt lost cancellation race: %v", err)
	}

	var nilReceipt *WriteReceipt
	if err := nilReceipt.Wait(context.Background()); !errors.Is(err, ErrInvalidWriteReceipt) {
		t.Fatalf("nil Wait() error = %v, want %v", err, ErrInvalidWriteReceipt)
	}
	if nilReceipt.Done() != nil {
		t.Fatal("nil Done() must return nil")
	}
}

func TestWriteReceiptWaitBlocksUntilCompletion(t *testing.T) {
	receipt := NewWriteReceipt()
	result := make(chan error, 1)
	go func() {
		result <- receipt.Wait(context.Background())
	}()
	select {
	case err := <-result:
		t.Fatalf("Wait() returned before completion: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	receipt.Complete(nil)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Wait() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait() did not observe completion")
	}
}

func TestSessionCompletionHelpersUseOptionalCapabilities(t *testing.T) {
	wantSendErr := errors.New("send failed")
	wantDrainErr := errors.New("drain failed")
	session := &completionSession{
		fakeSession: newFakeSession(1),
		receipt:     NewWriteReceipt(),
		sendErr:     wantSendErr,
		drainErr:    wantDrainErr,
	}
	receipt, supported, err := SessionSendWithReceipt(session, Message{ID: 1})
	if !supported || receipt != session.receipt || !errors.Is(err, wantSendErr) {
		t.Fatalf("SessionSendWithReceipt() = (%p, %v, %v)", receipt, supported, err)
	}
	supported, err = SessionDrainOutbound(nil, session)
	if !supported || !errors.Is(err, wantDrainErr) {
		t.Fatalf("SessionDrainOutbound() = (%v, %v)", supported, err)
	}

	plain := newFakeSession(2)
	if receipt, supported, err := SessionSendWithReceipt(plain, Message{}); receipt != nil || supported || err != nil {
		t.Fatalf("unsupported send = (%p, %v, %v)", receipt, supported, err)
	}
	if supported, err := SessionDrainOutbound(context.Background(), plain); supported || err != nil {
		t.Fatalf("unsupported drain = (%v, %v)", supported, err)
	}
}

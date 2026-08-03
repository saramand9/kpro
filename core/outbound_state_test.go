package core

import (
	"context"
	"errors"
	"testing"
)

type outboundStateSession struct {
	*fakeSession
	state OutboundState
}

func (s *outboundStateSession) OutboundState() OutboundState { return s.state }

type outboundWaitSession struct {
	*fakeSession
	err        error
	sawContext bool
}

func (s *outboundWaitSession) WaitOutboundWritable(ctx context.Context) error {
	s.sawContext = ctx != nil
	return s.err
}

func TestSessionOutboundStateUsesOptionalCapability(t *testing.T) {
	want := OutboundState{
		Writable:           true,
		PendingWrites:      2,
		MaxPendingWrites:   8,
		PendingBytes:       128,
		MaxPendingBytes:    1024,
		LowWatermarkBytes:  512,
		HighWatermarkBytes: 768,
	}
	session := &outboundStateSession{fakeSession: newFakeSession(1), state: want}
	got, ok := SessionOutboundState(session)
	if !ok || got != want {
		t.Fatalf("SessionOutboundState() = (%+v, %v), want (%+v, true)", got, ok, want)
	}

	if got, ok := SessionOutboundState(newFakeSession(2)); ok || got != (OutboundState{}) {
		t.Fatalf("session without capability = (%+v, %v), want zero, false", got, ok)
	}
	if got, ok := SessionOutboundState(nil); ok || got != (OutboundState{}) {
		t.Fatalf("nil session = (%+v, %v), want zero, false", got, ok)
	}
}

func TestSessionWaitOutboundWritableUsesOptionalCapability(t *testing.T) {
	wantErr := errors.New("wait failed")
	session := &outboundWaitSession{fakeSession: newFakeSession(1), err: wantErr}
	supported, err := SessionWaitOutboundWritable(nil, session)
	if !supported || !errors.Is(err, wantErr) || !session.sawContext {
		t.Fatalf("SessionWaitOutboundWritable() = (%v, %v), saw context = %v", supported, err, session.sawContext)
	}

	if supported, err := SessionWaitOutboundWritable(context.Background(), newFakeSession(2)); supported || err != nil {
		t.Fatalf("session without capability = (%v, %v), want false, nil", supported, err)
	}
	if supported, err := SessionWaitOutboundWritable(context.Background(), nil); supported || err != nil {
		t.Fatalf("nil session = (%v, %v), want false, nil", supported, err)
	}
}

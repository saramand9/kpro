package websocket

import (
	"errors"
	"testing"

	"github.com/saramand9/kpro/core"
)

func TestSendAfterCloseNeverQueues(t *testing.T) {
	for attempt := 0; attempt < 128; attempt++ {
		done := make(chan struct{})
		close(done)
		session := &session{done: done, sendQueue: make(chan wsFrame, 1)}
		if err := session.Send(core.Message{}); !errors.Is(err, core.ErrClosed) {
			t.Fatalf("attempt %d: Send() error = %v, want %v", attempt, err, core.ErrClosed)
		}
		if queued := len(session.sendQueue); queued != 0 {
			t.Fatalf("attempt %d: queued frames = %d, want 0", attempt, queued)
		}
	}
}

func TestSendQueueFullReturnsOverloadBeforeClosedState(t *testing.T) {
	session := &session{sendQueue: make(chan wsFrame), done: make(chan struct{})}
	session.closeOnce.Do(func() {})
	err := session.enqueue(wsFrame{opcode: opBinary})
	if !errors.Is(err, core.ErrSendQueueFull) {
		t.Fatalf("enqueue() error = %v, want %v", err, core.ErrSendQueueFull)
	}
	if errors.Is(err, core.ErrClosed) {
		t.Fatalf("enqueue() error = %v, must report overload rather than prior closed state", err)
	}
}

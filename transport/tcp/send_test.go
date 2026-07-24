package tcp

import (
	"errors"
	"testing"

	"github.com/saramand9/kpro/core"
)

func TestSendAfterCloseNeverQueues(t *testing.T) {
	for attempt := 0; attempt < 128; attempt++ {
		done := make(chan struct{})
		close(done)
		session := &session{done: done, sendQueue: make(chan []byte, 1)}
		if err := session.Send(core.Message{}); !errors.Is(err, core.ErrClosed) {
			t.Fatalf("attempt %d: Send() error = %v, want %v", attempt, err, core.ErrClosed)
		}
		if queued := len(session.sendQueue); queued != 0 {
			t.Fatalf("attempt %d: queued frames = %d, want 0", attempt, queued)
		}
	}
}

func TestSendQueueFullReturnsOverloadBeforeClosedState(t *testing.T) {
	server := New(Config{})
	session := &session{server: server, sendQueue: make(chan []byte), done: make(chan struct{})}
	session.closeOnce.Do(func() {})
	err := session.Send(core.Message{})
	if !errors.Is(err, core.ErrSendQueueFull) {
		t.Fatalf("Send() error = %v, want %v", err, core.ErrSendQueueFull)
	}
	if errors.Is(err, core.ErrClosed) {
		t.Fatalf("Send() error = %v, must report overload rather than prior closed state", err)
	}
}

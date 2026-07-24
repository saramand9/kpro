package kcp

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

package tcp

import (
	"context"
	"testing"
	"time"

	"github.com/saramand9/kpro/core"
)

func TestTCPBatchDispatchKeepsBuffersUntilRouterCompletion(t *testing.T) {
	server := New(Config{
		SendQueueSize:          4,
		MaxRetainedBufferBytes: 1024,
	})
	session := &session{
		id:          1,
		server:      server,
		payloadPool: server.payloadPool.Shard(1),
		done:        make(chan struct{}),
	}
	router := core.NewRouter(core.RouterConfig{
		SyncQueueSize:         4,
		AsyncWorkers:          1,
		AsyncQueueSize:        1,
		MaxInflightPerSession: 4,
		MaxInflight:           4,
		SessionIdleTimeout:    time.Minute,
	})
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	handled := make(chan string, 2)
	router.HandleSync(10, func(ctx core.Context) error {
		if ctx.Message().Seq == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		handled <- string(ctx.Message().Data)
		return nil
	})
	for index, value := range []string{"first", "second"} {
		buffer := session.payloadPool.GetUninitialized(len(value))
		copy(buffer, value)
		session.readFrames[index] = inboundFrame{
			message: core.Message{ID: 10, Seq: uint64(index + 1), Data: buffer},
			buffer:  buffer,
		}
	}
	single := func(ctx core.Context) error {
		_ = router.Dispatch(ctx)
		return nil
	}
	if !session.dispatchInboundFrames(single, router.TryDispatchSyncBatch, 2) {
		t.Fatal("batch dispatch stopped the session")
	}
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first Router handler did not start")
	}
	if got := server.payloadPool.RetainedBytes(); got != 0 {
		t.Fatalf("buffers returned while Router owned contexts: %d", got)
	}
	close(releaseFirst)
	for _, want := range []string{"first", "second"} {
		select {
		case got := <-handled:
			if got != want {
				t.Fatalf("handled payload = %q, want %q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for payload %q", want)
		}
	}
	if err := router.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := server.payloadPool.RetainedBytes(); got != 32 {
		t.Fatalf("retained bytes after Router completion = %d, want 32", got)
	}
}

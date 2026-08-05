package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/transport"
)

func TestEndpointUsesOptionalBatchTransportCapability(t *testing.T) {
	batchTransport := &endpointBatchTransport{}
	endpoint := &Endpoint{
		transport: batchTransport,
		router: core.NewRouter(core.RouterConfig{
			SyncQueueSize:         4,
			MaxInflightPerSession: 4,
			MaxInflight:           4,
		}),
	}
	handled := make(chan uint64, 2)
	endpoint.HandleSync(10, func(ctx core.Context) error {
		handled <- ctx.Message().Seq
		return nil
	})
	if err := endpoint.ListenAndServe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !batchTransport.batchCalled || batchTransport.legacyCalled {
		t.Fatalf("transport calls = batch %v, legacy %v", batchTransport.batchCalled, batchTransport.legacyCalled)
	}
	for sequence := uint64(1); sequence <= 2; sequence++ {
		select {
		case got := <-handled:
			if got != sequence {
				t.Fatalf("handled sequence = %d, want %d", got, sequence)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for sequence %d", sequence)
		}
	}
	if err := endpoint.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type endpointBatchTransport struct {
	batchCalled  bool
	legacyCalled bool
}

func (*endpointBatchTransport) Name() string { return "batch-test" }

func (t *endpointBatchTransport) ListenAndServe(context.Context, core.Handler) error {
	t.legacyCalled = true
	return errors.New("legacy ListenAndServe was called")
}

func (t *endpointBatchTransport) ListenAndServeBatch(
	_ context.Context,
	_ core.Handler,
	handler transport.BatchHandler,
) error {
	t.batchCalled = true
	session := &fakeSession{id: 1, done: make(chan struct{})}
	contexts := []core.Context{
		core.AcquireContext(session, core.Message{ID: 10, Seq: 1}),
		core.AcquireContext(session, core.Message{ID: 10, Seq: 2}),
	}
	if !handler(contexts) {
		return errors.New("batch handler fell back")
	}
	return nil
}

func (*endpointBatchTransport) Shutdown(context.Context) error { return nil }

var _ transport.BatchMessageServer = (*endpointBatchTransport)(nil)

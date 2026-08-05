// File: core/context_pool_test.go
// Purpose: Verifies transport context pooling and reset behavior.

package core

import (
	"context"
	"testing"
)

func TestReleaseContextClearsPooledBaseContext(t *testing.T) {
	session := newFakeSession(1)
	ctx := AcquireContext(session, Message{
		ID:      10,
		Seq:     20,
		Data:    []byte("payload"),
		Headers: map[string]string{"trace": "abc"},
		Meta:    map[string]any{"attempt": 1},
	})
	ctx.Set("key", "value")
	ctx.setContext(context.WithValue(context.Background(), messageContextTestKey{}, "pooled"))

	releaseContext(ctx)

	if ctx.refs.Load() != 0 {
		t.Fatal("released context still marked as checked out")
	}
	if ctx.session != nil {
		t.Fatal("released context retained its session")
	}
	if ctx.message.ID != 0 || ctx.message.Seq != 0 || ctx.message.Data != nil || ctx.message.Headers != nil || ctx.message.Meta != nil {
		t.Fatalf("released context retained message state: %+v", ctx.message)
	}
	if ctx.values != nil {
		t.Fatalf("released context retained handler values: %v", ctx.values)
	}
	if ctx.ctx != nil {
		t.Fatal("released context retained middleware context")
	}
}

func TestReleaseContextDoesNotTakeOwnershipOfNewContext(t *testing.T) {
	session := newFakeSession(1)
	ctx := NewContext(session, Message{ID: 10})

	releaseContext(ctx)

	if ctx.Session() != session || ctx.Message().ID != 10 {
		t.Fatal("releaseContext mutated a context not acquired from the pool")
	}
}

func TestTransportContextWaitsForRouterOwnership(t *testing.T) {
	ctx := AcquireTransportContext(newFakeSession(1), Message{ID: 10})
	retainRouterContext(ctx)
	ReleaseTransportContext(ctx)
	if got := ctx.refs.Load(); got != 1 {
		t.Fatalf("refs after transport release = %d, want router ref", got)
	}
	releaseContext(ctx)
	if got := ctx.refs.Load(); got != 0 {
		t.Fatalf("refs after router release = %d, want 0", got)
	}
}

func TestOwnedTransportContextReleasesBufferAfterFinalReference(t *testing.T) {
	owner := &contextTestBufferOwner{}
	buffer := make([]byte, 16, 64)
	ctx := AcquireOwnedTransportContext(
		newFakeSession(1),
		Message{ID: 10, Data: buffer[:16]},
		buffer,
		owner,
	)
	retainRouterContext(ctx)
	ReleaseTransportContext(ctx)
	if owner.releases != 0 {
		t.Fatalf("buffer releases after transport reference = %d, want 0", owner.releases)
	}

	releaseContext(ctx)
	if owner.releases != 1 {
		t.Fatalf("buffer releases after final reference = %d, want 1", owner.releases)
	}
	if cap(owner.buffer) != cap(buffer) || &owner.buffer[:1][0] != &buffer[:1][0] {
		t.Fatal("buffer owner received a different backing allocation")
	}
	if ctx.transportBuffer != nil || ctx.bufferOwner != nil {
		t.Fatal("released context retained transport buffer ownership")
	}
}

type contextTestBufferOwner struct {
	releases int
	buffer   []byte
}

func (o *contextTestBufferOwner) ReleaseTransportBuffer(buffer []byte) {
	o.releases++
	o.buffer = buffer
}

// File: core/context_pool_test.go
// Purpose: Verifies transport context pooling and reset behavior.

package core

import "testing"

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

	releaseContext(ctx)

	if ctx.pooled {
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
}

func TestReleaseContextDoesNotTakeOwnershipOfNewContext(t *testing.T) {
	session := newFakeSession(1)
	ctx := NewContext(session, Message{ID: 10})

	releaseContext(ctx)

	if ctx.Session() != session || ctx.Message().ID != 10 {
		t.Fatal("releaseContext mutated a context not acquired from the pool")
	}
}

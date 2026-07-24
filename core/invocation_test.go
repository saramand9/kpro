// File: core/invocation_test.go
// Purpose: Defines shared KPro primitives used by transports, routers, and middleware.

package core

import (
	"context"
	"testing"
)

func TestMessageInvocationUsesMessageOperationAndPayload(t *testing.T) {
	msgCtx := NewContext(newFakeSession(7), Message{
		ID:      42,
		Seq:     9,
		Data:    []byte("payload"),
		Headers: map[string]string{"trace": "abc"},
		Meta:    map[string]any{"attempt": 1},
	})

	inv := NewMessageInvocation(context.Background(), msgCtx)

	if got := inv.Operation(); got != "message/42" {
		t.Fatalf("operation = %q", got)
	}
	if inv.Transport() != TransportTCP {
		t.Fatalf("transport = %s", inv.Transport())
	}
	if inv.ConnID() != 7 {
		t.Fatalf("conn id = %d", inv.ConnID())
	}
	payload, ok := inv.Payload().([]byte)
	if !ok || string(payload) != "payload" {
		t.Fatalf("payload = %T %q", inv.Payload(), inv.Payload())
	}
	if inv.Native() != msgCtx {
		t.Fatalf("native = %#v, want message context", inv.Native())
	}

	msg := msgCtx.Message()
	msg.Headers["trace"] = "changed"
	msg.Meta["attempt"] = 2
	if inv.Headers()["trace"] != "abc" {
		t.Fatalf("headers were not isolated: %q", inv.Headers()["trace"])
	}
	if inv.Meta()["attempt"] != 1 {
		t.Fatalf("meta was not isolated: %v", inv.Meta()["attempt"])
	}
}

func TestChainRunsInOrder(t *testing.T) {
	var calls []string
	handler := Chain(
		func(next InvocationHandler) InvocationHandler {
			return func(ctx Invocation) error {
				calls = append(calls, "first-before")
				err := next(ctx)
				calls = append(calls, "first-after")
				return err
			}
		},
		func(next InvocationHandler) InvocationHandler {
			return func(ctx Invocation) error {
				calls = append(calls, "second-before")
				err := next(ctx)
				calls = append(calls, "second-after")
				return err
			}
		},
	)(func(ctx Invocation) error {
		calls = append(calls, "handler")
		ctx.Set("seen", true)
		return nil
	})

	ctx := NewInvocation(InvocationOptions{Operation: "test"})
	if err := handler(ctx); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if value, ok := ctx.Get("seen"); !ok || value != true {
		t.Fatalf("context value missing: value=%v ok=%v", value, ok)
	}

	want := []string{"first-before", "second-before", "handler", "second-after", "first-after"}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("calls = %v, want %v", calls, want)
		}
	}
}

func TestWithInvocationContextPreservesConnectionLifetime(t *testing.T) {
	connectionDone := make(chan struct{})
	requestCtx, cancel := context.WithCancel(context.Background())
	invocation := NewInvocation(InvocationOptions{
		Context:  context.Background(),
		ConnDone: connectionDone,
	})
	wrapped := WithInvocationContext(invocation, requestCtx)

	if got := InvocationConnDone(wrapped); got != connectionDone {
		t.Fatal("connection lifetime was not preserved by context wrapper")
	}
	if got := InvocationDone(wrapped); got != requestCtx.Done() {
		t.Fatal("request lifetime did not follow replacement context")
	}
	cancel()
	select {
	case <-connectionDone:
		t.Fatal("request cancellation closed the connection lifetime")
	default:
	}
	close(connectionDone)
}

func TestMessageInvocationExposesSessionAsConnectionLifetime(t *testing.T) {
	session := newFakeSession(7)
	invocation := NewMessageInvocation(context.Background(), NewContext(session, Message{ID: 42}))
	defer invocation.Release()

	if got := InvocationConnDone(invocation); got != session.Done() {
		t.Fatal("message invocation did not expose session lifetime as connection lifetime")
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close session: %v", err)
	}
}

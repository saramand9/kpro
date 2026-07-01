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

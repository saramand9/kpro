package core

import (
	"context"
	"errors"
	"testing"
	"time"
)

var _ Context = (*legacyMessageContext)(nil)

type legacyMessageContext struct {
	session Session
	message Message
}

func (c *legacyMessageContext) Session() Session               { return c.session }
func (c *legacyMessageContext) Message() Message               { return c.message }
func (c *legacyMessageContext) Transport() Transport           { return c.session.Transport() }
func (c *legacyMessageContext) ConnID() uint64                 { return c.session.ID() }
func (c *legacyMessageContext) RemoteAddr() string             { return "" }
func (c *legacyMessageContext) Reply(uint32, []byte) error     { return nil }
func (c *legacyMessageContext) Error(string) error             { return nil }
func (c *legacyMessageContext) Set(string, interface{})        {}
func (c *legacyMessageContext) Get(string) (interface{}, bool) { return nil, false }

func TestContextOfSupportsLegacyContextImplementations(t *testing.T) {
	ctx := &legacyMessageContext{session: newFakeSession(1)}
	if got := ContextOf(ctx); got == nil || got.Err() != nil {
		t.Fatalf("ContextOf(legacy) = %v", got)
	}
}

type messageContextTestKey struct{}

func TestMessageHandlerReceivesMiddlewareContext(t *testing.T) {
	session := newFakeSession(1)
	defer func() { _ = session.Close() }()
	messageCtx := NewContext(session, Message{ID: 10})

	handler := buildMessageInvocationHandler(func(ctx Context) error {
		if got := ContextOf(ctx).Value(messageContextTestKey{}); got != "middleware" {
			t.Fatalf("context value = %v, want middleware", got)
		}
		if _, ok := ContextOf(ctx).Deadline(); !ok {
			t.Fatal("message handler context did not retain middleware deadline")
		}
		return nil
	}, []Middleware{
		func(next InvocationHandler) InvocationHandler {
			return func(invocation Invocation) error {
				ctx := context.WithValue(invocation.Context(), messageContextTestKey{}, "middleware")
				ctx, cancel := context.WithTimeout(ctx, time.Second)
				defer cancel()
				return next(WithInvocationContext(invocation, ctx))
			}
		},
	})

	invocation := NewMessageInvocation(context.Background(), messageCtx)
	defer invocation.Release()
	if err := handler(invocation); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
}

func TestMessageHandlerContextFollowsSessionLifecycle(t *testing.T) {
	session := newFakeSession(1)
	messageCtx := NewContext(session, Message{ID: 10})
	var handlerCtx context.Context
	handler := buildMessageInvocationHandler(func(ctx Context) error {
		handlerCtx = ContextOf(ctx)
		return nil
	}, nil)

	invocation := NewMessageInvocation(context.Background(), messageCtx)
	defer invocation.Release()
	if err := handler(invocation); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if handlerCtx == nil {
		t.Fatal("handler did not receive a standard context")
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close session: %v", err)
	}
	waitForClosed(t, handlerCtx.Done(), "message handler context did not follow session closure")
	if !errors.Is(handlerCtx.Err(), context.Canceled) {
		t.Fatalf("context error = %v, want context.Canceled", handlerCtx.Err())
	}
}

func TestCloneContextRetainsMiddlewareContext(t *testing.T) {
	base := NewContext(newFakeSession(1), Message{ID: 10})
	base.setContext(context.WithValue(context.Background(), messageContextTestKey{}, "clone"))

	cloned := CloneContext(base)
	if got := cloned.Context().Value(messageContextTestKey{}); got != "clone" {
		t.Fatalf("cloned context value = %v, want clone", got)
	}
}

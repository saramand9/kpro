// File: core/session_lifecycle.go
// Purpose: Defines the optional lifecycle contract shared by message sessions.

package core

import (
	"context"
	"sync"
)

// SessionLifecycle is the optional lifecycle capability implemented by
// sessions that can signal when they are closed.
//
// Session intentionally remains a small compatibility interface. Code that
// needs cancellation should use SessionDone or SessionContext instead of
// repeating an anonymous Done method assertion.
type SessionLifecycle interface {
	Done() <-chan struct{}
}

// SessionDone returns the session close signal when the optional lifecycle
// capability is available. It returns nil for nil sessions, sessions without
// lifecycle support, and implementations that do not expose a signal.
func SessionDone(session Session) <-chan struct{} {
	if session == nil {
		return nil
	}
	lifecycle, ok := session.(SessionLifecycle)
	if !ok {
		return nil
	}
	return lifecycle.Done()
}

// SessionContext adapts the optional session close signal to context.Context.
// The returned context is canceled when the session closes. Sessions without
// lifecycle support return context.Background().
//
// The adapter uses the session's channel directly and does not start a
// goroutine.
func SessionContext(session Session) context.Context {
	return contextForSessionDone(context.Background(), SessionDone(session))
}

type sessionDoneContext struct {
	context.Context
	done <-chan struct{}
}

func (c sessionDoneContext) Done() <-chan struct{} {
	return c.done
}

func (c sessionDoneContext) Err() error {
	select {
	case <-c.done:
		return context.Canceled
	default:
		return nil
	}
}

func contextForSessionDone(parent context.Context, done <-chan struct{}) context.Context {
	if parent == nil {
		parent = context.Background()
	}
	if done == nil {
		return parent
	}
	return sessionDoneContext{Context: parent, done: done}
}

// mergeSessionContext makes both the caller context and session lifecycle
// cancel an invocation. The common message path has a non-cancelable caller
// context and therefore uses the session channel directly, with no callback or
// waiting goroutine.
func mergeSessionContext(parent context.Context, done <-chan struct{}) (context.Context, func()) {
	if parent == nil {
		parent = context.Background()
	}
	if done == nil {
		return parent, nil
	}
	if parent.Done() == nil {
		return contextForSessionDone(parent, done), nil
	}
	if parent.Done() == done {
		return parent, nil
	}

	merged, cancel := context.WithCancelCause(parent)
	stop := context.AfterFunc(sessionBridgeContext(done), func() {
		cancel(ErrClosed)
	})
	return merged, func() {
		stop()
		cancel(context.Canceled)
	}
}

type sessionContextBridge struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
}

var sessionContextBridges sync.Map

// sessionBridgeContext converts a raw Done channel to a standard cancel
// context once per live session. context.AfterFunc can then register callbacks
// without creating a waiting goroutine per invocation.
func sessionBridgeContext(done <-chan struct{}) context.Context {
	select {
	case <-done:
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(ErrClosed)
		return ctx
	default:
	}

	candidate := &sessionContextBridge{}
	candidate.ctx, candidate.cancel = context.WithCancelCause(context.Background())
	actual, loaded := sessionContextBridges.LoadOrStore(done, candidate)
	if loaded {
		return actual.(*sessionContextBridge).ctx
	}

	go func() {
		<-done
		candidate.cancel(ErrClosed)
		sessionContextBridges.CompareAndDelete(done, candidate)
	}()
	return candidate.ctx
}

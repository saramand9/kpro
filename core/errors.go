// File: core/errors.go
// Purpose: Defines shared KPro primitives used by transports, routers, and middleware.

package core

import "errors"

var (
	// ErrAlreadyStarted reports that a one-shot component was started more than once.
	ErrAlreadyStarted = errors.New("component already started")
	// ErrClosed reports that a session or transport is already closed.
	ErrClosed = errors.New("session closed")
	// ErrNoHandler reports that no route was registered for a message id.
	ErrNoHandler = errors.New("no handler registered")
	// ErrRouterClosed reports that dispatch was attempted after router shutdown.
	ErrRouterClosed = errors.New("router closed")
	// ErrDispatchQueueFull reports that a sync or async dispatch queue is full.
	ErrDispatchQueueFull = errors.New("dispatch queue full")
	// ErrSendQueueFull reports that a session send queue cannot accept more messages.
	ErrSendQueueFull = errors.New("send queue full")
	// ErrTooManyInflight reports that a session reached its in-flight limit.
	ErrTooManyInflight = errors.New("too many in-flight messages")
	// ErrGlobalInflightLimit reports that the Router-wide in-flight limit was reached.
	ErrGlobalInflightLimit = errors.New("global in-flight limit reached")
)

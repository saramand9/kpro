package core

import "errors"

var (
	ErrClosed            = errors.New("session closed")
	ErrNoHandler         = errors.New("no handler registered")
	ErrRouterClosed      = errors.New("router closed")
	ErrDispatchQueueFull = errors.New("dispatch queue full")
	ErrTooManyInflight   = errors.New("too many in-flight messages")
)

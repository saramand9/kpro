package legacy

import (
	"errors"

	"github.com/saramand9/bignet/kpro/protocol"
)

var (
	ErrClosed             = errors.New("connection closed")
	ErrFrameTooLarge      = protocol.ErrFrameTooLarge
	ErrBadMagic           = protocol.ErrBadMagic
	ErrBadVersion         = protocol.ErrBadVersion
	ErrSendQueueFull      = errors.New("send queue full")
	ErrWorkerQueueFull    = errors.New("worker queue full")
	ErrTooManyConnections = errors.New("too many connections")
	ErrTooManyIPConns     = errors.New("too many connections from ip")
	ErrTooManyInflight    = errors.New("too many in-flight requests")
	ErrNoHandler          = errors.New("no handler registered")
)

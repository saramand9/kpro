package core

import "errors"

var (
	ErrClosed    = errors.New("session closed")
	ErrNoHandler = errors.New("no handler registered")
)

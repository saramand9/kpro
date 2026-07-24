package redisstore

import "errors"

var (
	ErrInvalidConfig       = errors.New("invalid redis config")
	ErrNotFound            = errors.New("redis key not found")
	ErrLockNotAcquired     = errors.New("redis lock not acquired")
	ErrLockAlreadyAcquired = errors.New("redis lock already acquired")
	ErrLockTokenMismatch   = errors.New("redis lock token mismatch")
)

package lifecycle

import "errors"

var (
	ErrNotFound        = errors.New("data not found")
	ErrConflict        = errors.New("data conflict")
	ErrInvalidArgument = errors.New("invalid data argument")
	ErrUnavailable     = errors.New("data resource unavailable")
	ErrNotImplemented  = errors.New("data capability not implemented")
)

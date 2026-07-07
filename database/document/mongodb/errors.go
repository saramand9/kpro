// Package mongostore adapts MongoDB as a document resource.
package mongostore

import "errors"

var (
	// ErrInvalidConfig reports invalid MongoDB adapter configuration.
	ErrInvalidConfig = errors.New("invalid mongo config")
	// ErrInvalidRequest reports invalid MongoDB collection input.
	ErrInvalidRequest = errors.New("invalid mongo request")
	// ErrNotFound reports that a document does not exist.
	ErrNotFound = errors.New("mongo document not found")
)

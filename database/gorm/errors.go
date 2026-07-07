// Package gormstore adapts GORM as a peer storage framework.
package gormstore

import "errors"

var (
	// ErrInvalidConfig reports an invalid GORM adapter configuration.
	ErrInvalidConfig = errors.New("invalid gorm storage config")
	// ErrInvalidIdentifier reports an unsafe table or column identifier.
	ErrInvalidIdentifier = errors.New("invalid gorm storage identifier")
	// ErrNoColumns reports that a write operation has no mapped columns.
	ErrNoColumns = errors.New("no gorm storage columns")
	// ErrMissingPrimaryKey reports that update or upsert needs a primary key.
	ErrMissingPrimaryKey = errors.New("missing primary key")
	// ErrLastInsertIDUnavailable reports that the model did not expose an inserted id.
	ErrLastInsertIDUnavailable = errors.New("last insert id unavailable")
)

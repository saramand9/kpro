// File: database/dbx/errors.go
// Purpose: Defines shared database writer errors.

package dbx

import "errors"

var (
	// ErrInvalidConfig reports an invalid database configuration.
	ErrInvalidConfig = errors.New("invalid database config")
	// ErrInvalidIdentifier reports an unsafe table or column identifier.
	ErrInvalidIdentifier = errors.New("invalid database identifier")
	// ErrNoColumns reports that a write operation has no columns to write.
	ErrNoColumns = errors.New("no database columns")
	// ErrMissingPrimaryKey reports that update needs a primary key column.
	ErrMissingPrimaryKey = errors.New("missing primary key")
	// ErrUnsupportedRead reports that a generic read cannot map the requested row.
	ErrUnsupportedRead = errors.New("unsupported generic read")
	// ErrAsyncQueueFull reports that an async write could not be queued.
	ErrAsyncQueueFull = errors.New("async database queue full")
	// ErrAsyncClosed reports that an async writer has stopped accepting writes.
	ErrAsyncClosed = errors.New("async database writer closed")
)

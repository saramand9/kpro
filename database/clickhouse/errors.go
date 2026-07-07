// Package clickhousestore adapts ClickHouse as an analytical SQL resource.
package clickhousestore

import "errors"

var (
	// ErrInvalidConfig reports an invalid ClickHouse adapter configuration.
	ErrInvalidConfig = errors.New("invalid clickhouse config")
	// ErrInvalidWriter reports a table view without a writer function.
	ErrInvalidWriter = errors.New("invalid clickhouse writer")
)

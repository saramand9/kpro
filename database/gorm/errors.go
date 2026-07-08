// Package gormstore exposes GORM database resources for KPro applications.
package gormstore

import "errors"

var (
	// ErrInvalidConfig reports an invalid GORM adapter configuration.
	ErrInvalidConfig = errors.New("invalid gorm config")
	// ErrInvalidMigration reports an invalid migration declaration.
	ErrInvalidMigration = errors.New("invalid gorm migration")
	// ErrUnsupportedDialect reports a migration without SQL for the active dialect.
	ErrUnsupportedDialect = errors.New("unsupported gorm dialect")
)

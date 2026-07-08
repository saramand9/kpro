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
	// ErrMigrationLock reports a failure to acquire the migration lock.
	ErrMigrationLock = errors.New("gorm migration lock unavailable")
	// ErrMigrationChecksum reports that an applied migration was modified.
	ErrMigrationChecksum = errors.New("gorm migration checksum mismatch")
	// ErrDirtyMigration reports a migration left in dirty or failed state.
	ErrDirtyMigration = errors.New("gorm migration is dirty")
)

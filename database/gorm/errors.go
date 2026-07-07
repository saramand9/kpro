// Package gormstore exposes GORM database resources for KPro applications.
package gormstore

import "errors"

var (
	// ErrInvalidConfig reports an invalid GORM adapter configuration.
	ErrInvalidConfig = errors.New("invalid gorm config")
)

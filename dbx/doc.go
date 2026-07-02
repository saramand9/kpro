// File: dbx/doc.go
// Purpose: Documents the optional database writer package.

// Package dbx provides optional database/sql helpers for KPro applications.
//
// The package is intentionally small: database/sql remains the bottom layer,
// while dbx adds generated writes, transaction helpers, and bounded async
// write workers. It is designed to sit behind application Store or Repository
// types instead of replacing application business logic.
package dbx

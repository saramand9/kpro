// File: transport/validation.go
// Purpose: Defines optional startup validation for transport components.

package transport

// Validator is an optional component capability used by server orchestration
// to reject invalid configuration before any serving goroutine starts.
type Validator interface {
	Validate() error
}

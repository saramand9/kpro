// File: internal/modules/login/service/dto.go
// Purpose: Defines service-level DTOs for login operations.

package service

// LoginInput is the service input for authenticating a principal.
type LoginInput struct {
	Username string
	Password string
}

// Session is the service output for a successful login.
type Session struct {
	Username  string `json:"username"`
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
	ExpiresIn int64  `json:"expires_in"`
}

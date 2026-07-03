// File: internal/modules/login/service/service.go
// Purpose: Provides login business operations for protocol handlers.

package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

const defaultTokenTTL = time.Hour

// ErrInvalidCredentials reports invalid login credentials.
var ErrInvalidCredentials = errors.New("invalid login credentials")

// Config controls the example login service.
type Config struct {
	Username string
	Password string
	TokenTTL time.Duration
	Now      func() time.Time
}

// Service owns login business operations and hides authentication details from handlers.
type Service struct {
	username string
	password string
	tokenTTL time.Duration
	now      func() time.Time
}

// New creates a login service.
func New(config Config) *Service {
	if config.Username == "" {
		config.Username = "admin"
	}
	if config.Password == "" {
		config.Password = "secret"
	}
	if config.TokenTTL <= 0 {
		config.TokenTTL = defaultTokenTTL
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Service{
		username: config.Username,
		password: config.Password,
		tokenTTL: config.TokenTTL,
		now:      config.Now,
	}
}

// Login validates credentials and returns an example session token.
func (s *Service) Login(ctx context.Context, input LoginInput) (Session, error) {
	select {
	case <-ctx.Done():
		return Session{}, ctx.Err()
	default:
	}
	username := strings.TrimSpace(input.Username)
	password := strings.TrimSpace(input.Password)
	if username == "" || password == "" {
		return Session{}, ErrInvalidCredentials
	}
	if username != s.username || password != s.password {
		return Session{}, ErrInvalidCredentials
	}
	now := s.now().UTC()
	expiresAt := now.Add(s.tokenTTL)
	return Session{
		Username:  username,
		Token:     issueExampleToken(username, now),
		ExpiresAt: expiresAt.Format(time.RFC3339),
		ExpiresIn: int64(s.tokenTTL.Seconds()),
	}, nil
}

func issueExampleToken(username string, now time.Time) string {
	// This token is intentionally simple because the module demonstrates protocol wiring.
	raw := fmt.Sprintf("%s:%d", username, now.Unix())
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

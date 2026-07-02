// File: transport/transport.go
// Purpose: Defines shared transport interfaces for KPro servers.

package transport

import (
	"context"

	"github.com/saramand9/bignet/kpro/core"
)

// Server is implemented by message transports that dispatch core.Context handlers.
type Server interface {
	// Name returns the transport identifier used in diagnostics.
	Name() string
	// ListenAndServe starts the transport and dispatches decoded messages to handler.
	ListenAndServe(context.Context, core.Handler) error
	// Shutdown stops the transport and releases active sessions.
	Shutdown(context.Context) error
}

// Runner is implemented by native protocol endpoints managed by server.Server.
type Runner interface {
	// Name returns the runner identifier used in diagnostics.
	Name() string
	// ListenAndServe starts the native endpoint until the context stops or serving fails.
	ListenAndServe(context.Context) error
	// Shutdown gracefully stops the native endpoint.
	Shutdown(context.Context) error
}

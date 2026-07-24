// File: transport/transport.go
// Purpose: Defines shared transport interfaces for KPro servers.

package transport

import (
	"context"

	"github.com/saramand9/kpro/core"
)

// Component is the common lifecycle surface shared by message and native
// transports. Startup remains mode-specific so implementations do not need to
// erase their handler type behind any.
type Component interface {
	// Name returns the component identifier used in diagnostics.
	Name() string
	// Shutdown gracefully stops the component.
	Shutdown(context.Context) error
}

// MessageServer is implemented by message transports that dispatch
// core.Context handlers.
type MessageServer interface {
	Component
	// Name returns the transport identifier used in diagnostics.
	// ListenAndServe starts the transport and dispatches decoded messages to handler.
	ListenAndServe(context.Context, core.Handler) error
}

// Server is kept as a source-compatible alias for MessageServer.
type Server = MessageServer

// NativeRunner is implemented by native protocol endpoints managed by
// server.Server.
type NativeRunner interface {
	Component
	// Name returns the runner identifier used in diagnostics.
	// ListenAndServe starts the native endpoint until the context stops or serving fails.
	ListenAndServe(context.Context) error
}

// Runner is kept as a source-compatible alias for NativeRunner.
type Runner = NativeRunner

// MiddlewareTarget is the optional capability implemented by transports that
// accept protocol-neutral invocation middleware.
type MiddlewareTarget interface {
	ApplyMiddleware(...core.Middleware)
}

// ReadinessProvider is implemented by transports that can report when their
// listener has been bound and the component is ready to accept work. Server
// orchestration falls back to the legacy "ListenAndServe entered" signal for
// components that do not implement this optional capability. Implementations
// must close Ready only after successful listener publication; startup failure
// is returned by ListenAndServe and must not close the readiness channel.
type ReadinessProvider interface {
	Ready() <-chan struct{}
}

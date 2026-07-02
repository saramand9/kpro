// File: legacy/context.go
// Purpose: Implements or tests the legacy TCP validation server.

package legacy

import "github.com/saramand9/bignet/kpro/core"

// Handler aliases the shared core handler type for legacy callers.
type Handler = core.Handler

// Context aliases the shared core message context type.
type Context = core.Context

// Invocation aliases the shared middleware invocation type.
type Invocation = core.Invocation

// InvocationHandler aliases the shared middleware handler type.
type InvocationHandler = core.InvocationHandler

// Middleware aliases the shared middleware type.
type Middleware = core.Middleware

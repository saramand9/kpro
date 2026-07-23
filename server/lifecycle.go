// File: server/lifecycle.go
// Purpose: Exposes the high-level server lifecycle API.

package server

import (
	"errors"

	"github.com/saramand9/bignet/kpro/core"
)

// State is the observable one-shot Server lifecycle state.
type State = core.LifecycleState

const (
	StateCreated  = core.StateCreated
	StateStarting = core.StateStarting
	StateRunning  = core.StateRunning
	StateDraining = core.StateDraining
	StateStopped  = core.StateStopped
)

var (
	// ErrServerAlreadyStarted reports a concurrent or repeated start attempt.
	ErrServerAlreadyStarted = errors.New("server already started")
	// ErrServerStopped reports a start attempt after draining has begun.
	ErrServerStopped = errors.New("server stopped")
	// ErrConfigurationLocked reports a configuration mutation after startup or
	// shutdown has claimed the one-shot lifecycle.
	ErrConfigurationLocked = errors.New("server configuration locked")
	// ErrConfigurationBusy reports a concurrent or reentrant mutation that
	// would make middleware application order ambiguous.
	ErrConfigurationBusy = errors.New("server configuration busy")
)

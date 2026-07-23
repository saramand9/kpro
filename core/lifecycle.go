// File: core/lifecycle.go
// Purpose: Defines observable lifecycle states shared by core and server layers.

package core

import "fmt"

// LifecycleState describes the one-way lifecycle of a Router or managed server.
//
// Valid transitions are:
//
//	Created -> Starting -> Running -> Draining -> Stopped
//
// A component may move directly from Created to Draining when it is shut down
// before it starts. Stopped components are not restartable.
type LifecycleState uint32

const (
	// StateCreated means the component accepts configuration but has not started.
	StateCreated LifecycleState = iota
	// StateStarting means startup won ownership but is not yet fully published.
	StateStarting
	// StateRunning means the component accepts work.
	StateRunning
	// StateDraining means new work is rejected while accepted work is finishing.
	StateDraining
	// StateStopped means the one-shot lifecycle has completed.
	StateStopped
)

// String returns a stable human-readable lifecycle name.
func (s LifecycleState) String() string {
	switch s {
	case StateCreated:
		return "created"
	case StateStarting:
		return "starting"
	case StateRunning:
		return "running"
	case StateDraining:
		return "draining"
	case StateStopped:
		return "stopped"
	default:
		return fmt.Sprintf("lifecycle(%d)", uint32(s))
	}
}

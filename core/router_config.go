// File: core/router_config.go
// Purpose: Provides strict opt-in validation for Router configuration.

package core

import (
	"errors"
	"fmt"
)

// Validate checks strict Router configuration invariants. Zero values are
// valid and select defaults; negative capacities and durations are invalid.
// NewRouter remains permissive for source compatibility.
func (c RouterConfig) Validate() error {
	var validationErr error
	validationErr = errors.Join(
		validationErr,
		nonNegativeInt("sync queue size", c.SyncQueueSize),
		nonNegativeInt("async workers", c.AsyncWorkers),
		nonNegativeInt("async queue size", c.AsyncQueueSize),
		nonNegativeInt("max in-flight per session", c.MaxInflightPerSession),
		nonNegativeInt("max in-flight", c.MaxInflight),
	)
	if c.SessionIdleTimeout < 0 {
		validationErr = errors.Join(validationErr, errors.New("session idle timeout cannot be negative"))
	}
	if c.ShutdownTimeout < 0 {
		validationErr = errors.Join(validationErr, errors.New("router shutdown timeout cannot be negative"))
	}
	if c.Backpressure != BackpressureReject && c.Backpressure != BackpressureWait {
		validationErr = errors.Join(validationErr, fmt.Errorf(
			"backpressure policy %d is invalid",
			c.Backpressure,
		))
	}
	return validationErr
}

func nonNegativeInt(name string, value int) error {
	if value < 0 {
		return fmt.Errorf("%s cannot be negative", name)
	}
	return nil
}

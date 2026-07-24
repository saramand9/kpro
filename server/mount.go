package server

import (
	"fmt"

	"github.com/saramand9/kpro/transport"
)

// MountNative mounts a native protocol runner and returns it with its concrete
// type preserved. It panics on the same configuration errors as
// Server.MountRunner; use MountNativeChecked when mounting is data-driven or
// may race server startup.
func MountNative[T transport.Runner](app *Server, runner T) T {
	mounted, err := MountNativeChecked(app, runner)
	if err != nil {
		panic(err)
	}
	return mounted
}

// MountNativeChecked mounts a native protocol runner while preserving its
// concrete type. It returns the zero value of T when mounting fails.
func MountNativeChecked[T transport.Runner](app *Server, runner T) (T, error) {
	var zero T
	if app == nil {
		return zero, fmt.Errorf("%w: server is nil", ErrInvalidConfiguration)
	}
	if _, err := app.MountRunnerChecked(runner); err != nil {
		return zero, err
	}
	return runner, nil
}

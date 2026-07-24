package server

import (
	"fmt"

	"github.com/saramand9/kpro/transport"
)

// ComponentError identifies the mounted component and lifecycle phase that
// produced an error while preserving errors.Is/errors.As compatibility.
type ComponentError struct {
	Component string
	Phase     string
	Err       error
}

func (e *ComponentError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("component %q %s: %v", e.Component, e.Phase, e.Err)
}

func (e *ComponentError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func wrapComponentError(component transport.Component, phase string, err error) error {
	if err == nil {
		return nil
	}
	name := ""
	if component != nil {
		name = component.Name()
	}
	if name == "" {
		name = fmt.Sprintf("%T", component)
	}
	return wrapNamedComponentError(name, phase, err)
}

func wrapNamedComponentError(name, phase string, err error) error {
	if err == nil {
		return nil
	}
	return &ComponentError{Component: name, Phase: phase, Err: err}
}

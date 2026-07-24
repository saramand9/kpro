package projector

import (
	"fmt"
	"strings"
)

// Registry owns an explicit list of projectors.
type Registry struct {
	projectors []Projector
	names      map[string]struct{}
}

// NewRegistry creates an empty projector registry.
func NewRegistry() *Registry {
	return &Registry{names: make(map[string]struct{})}
}

// Register appends projectors and rejects duplicate names.
func (r *Registry) Register(projectors ...Projector) error {
	if r == nil {
		return fmt.Errorf("projector registry is nil")
	}
	if r.names == nil {
		r.names = make(map[string]struct{})
	}
	seen := make(map[string]struct{}, len(r.names)+len(projectors))
	for name := range r.names {
		seen[name] = struct{}{}
	}
	names := make([]string, 0, len(projectors))
	for _, projector := range projectors {
		if projector == nil {
			return fmt.Errorf("projector is nil")
		}
		name := strings.TrimSpace(projector.Name())
		if name == "" {
			return fmt.Errorf("projector name is required")
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("projector %q already registered", name)
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	for i, projector := range projectors {
		name := names[i]
		r.names[name] = struct{}{}
		r.projectors = append(r.projectors, projector)
	}
	return nil
}

// All returns a copy of the registered projectors.
func (r *Registry) All() []Projector {
	if r == nil {
		return nil
	}
	return append([]Projector(nil), r.projectors...)
}

package projector

import "fmt"

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
	if r.names == nil {
		r.names = make(map[string]struct{})
	}
	for _, projector := range projectors {
		if projector == nil {
			continue
		}
		name := projector.Name()
		if name == "" {
			return fmt.Errorf("projector name is required")
		}
		if _, exists := r.names[name]; exists {
			return fmt.Errorf("projector %q already registered", name)
		}
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

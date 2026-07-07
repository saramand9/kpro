package projections

import "fmt"

// Registry owns an explicit list of projections.
type Registry struct {
	projections []Projection
	names       map[string]struct{}
}

// NewRegistry creates an empty projection registry.
func NewRegistry() *Registry {
	return &Registry{names: make(map[string]struct{})}
}

// Register appends projections and rejects duplicate names.
func (r *Registry) Register(projections ...Projection) error {
	if r.names == nil {
		r.names = make(map[string]struct{})
	}
	for _, projection := range projections {
		if projection == nil {
			continue
		}
		name := projection.Name()
		if name == "" {
			return fmt.Errorf("projection name is required")
		}
		if _, exists := r.names[name]; exists {
			return fmt.Errorf("projection %q already registered", name)
		}
		r.names[name] = struct{}{}
		r.projections = append(r.projections, projection)
	}
	return nil
}

// All returns a copy of the registered projections.
func (r *Registry) All() []Projection {
	if r == nil {
		return nil
	}
	return append([]Projection(nil), r.projections...)
}

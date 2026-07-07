package facts

import "fmt"

// Decoder converts an encoded payload into a typed fact.
type Decoder func([]byte) (any, error)

// Registry maps fact type names to decoders.
type Registry struct {
	decoders map[string]Decoder
}

// NewRegistry creates an empty fact registry.
func NewRegistry() *Registry {
	return &Registry{decoders: make(map[string]Decoder)}
}

// Register adds one fact decoder.
func (r *Registry) Register(factType string, decoder Decoder) error {
	if factType == "" {
		return fmt.Errorf("fact type is required")
	}
	if decoder == nil {
		return fmt.Errorf("decoder is required for fact type %q", factType)
	}
	if r.decoders == nil {
		r.decoders = make(map[string]Decoder)
	}
	if _, exists := r.decoders[factType]; exists {
		return fmt.Errorf("fact type %q already registered", factType)
	}
	r.decoders[factType] = decoder
	return nil
}

// Decode converts an envelope payload using the registered decoder.
func (r *Registry) Decode(envelope Envelope) (any, error) {
	if r == nil {
		return nil, fmt.Errorf("fact registry is nil")
	}
	decoder, ok := r.decoders[envelope.Type]
	if !ok {
		return nil, fmt.Errorf("fact type %q is not registered", envelope.Type)
	}
	return decoder(envelope.Payload)
}

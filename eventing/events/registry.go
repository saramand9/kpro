package events

import "fmt"

// Decoder converts an encoded payload into a typed event.
type Decoder func([]byte) (any, error)

// Registry maps event type names to decoders.
type Registry struct {
	decoders map[string]Decoder
}

// NewRegistry creates an empty event registry.
func NewRegistry() *Registry {
	return &Registry{decoders: make(map[string]Decoder)}
}

// Register adds one event decoder.
func (r *Registry) Register(eventType string, decoder Decoder) error {
	if eventType == "" {
		return fmt.Errorf("event type is required")
	}
	if decoder == nil {
		return fmt.Errorf("decoder is required for event type %q", eventType)
	}
	if r.decoders == nil {
		r.decoders = make(map[string]Decoder)
	}
	if _, exists := r.decoders[eventType]; exists {
		return fmt.Errorf("event type %q already registered", eventType)
	}
	r.decoders[eventType] = decoder
	return nil
}

// Decode converts an envelope payload using the registered decoder.
func (r *Registry) Decode(envelope Envelope) (any, error) {
	if r == nil {
		return nil, fmt.Errorf("event registry is nil")
	}
	decoder, ok := r.decoders[envelope.Type]
	if !ok {
		return nil, fmt.Errorf("event type %q is not registered", envelope.Type)
	}
	return decoder(envelope.Payload)
}

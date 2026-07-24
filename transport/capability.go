package transport

import (
	"fmt"
	"reflect"

	"github.com/saramand9/kpro/core"
)

// Mode describes which startup contract a transport uses.
type Mode string

const (
	// ModeMessage dispatches decoded core.Context values through MessageServer.
	ModeMessage Mode = "message"
	// ModeNative owns its protocol request model and runs through NativeRunner.
	ModeNative Mode = "native"
)

// Capability is a bit set describing optional protocol behavior.
type Capability uint32

const (
	// CapabilitySessions exposes stable physical or virtual peer sessions.
	CapabilitySessions Capability = 1 << iota
	// CapabilityStream uses an ordered byte/message stream transport.
	CapabilityStream
	// CapabilityDatagram uses independent datagrams.
	CapabilityDatagram
	// CapabilityRequestResponse supports request/reply interaction.
	CapabilityRequestResponse
	// CapabilityServerStreaming supports a server-produced stream.
	CapabilityServerStreaming
	// CapabilityBidirectionalStreaming supports concurrent two-way streams.
	CapabilityBidirectionalStreaming
	// CapabilityTLS is always enabled for this configured component.
	CapabilityTLS
	// CapabilityMiddleware exposes protocol-neutral invocation middleware,
	// directly for native runners or through the standard Router for message
	// servers.
	CapabilityMiddleware
)

// Has reports whether all requested flags are present.
func (c Capability) Has(requested Capability) bool {
	return c&requested == requested
}

// Descriptor is the stable, dependency-free description used by diagnostics,
// validation, tooling, and future module discovery.
type Descriptor struct {
	Name         string
	Protocol     core.Transport
	Mode         Mode
	Capabilities Capability
}

// Validate checks invariants expected by server orchestration and tooling.
func (d Descriptor) Validate() error {
	if d.Name == "" {
		return fmt.Errorf("transport descriptor name is required")
	}
	if d.Protocol == "" {
		return fmt.Errorf("transport %q protocol is required", d.Name)
	}
	if d.Mode != ModeMessage && d.Mode != ModeNative {
		return fmt.Errorf("transport %q has invalid mode %q", d.Name, d.Mode)
	}
	if d.Capabilities.Has(CapabilityStream | CapabilityDatagram) {
		return fmt.Errorf("transport %q cannot be both stream and datagram", d.Name)
	}
	return nil
}

// CapabilityProvider lets built-in and third-party transports advertise their
// behavior without expanding the startup interfaces.
type CapabilityProvider interface {
	Descriptor() Descriptor
}

// Describe returns an advertised descriptor. Older custom transports that do
// not yet implement CapabilityProvider receive a conservative descriptor based
// on their lifecycle interface and Name.
func Describe(component Component) Descriptor {
	if component == nil || isNilComponent(component) {
		return Descriptor{}
	}
	if provider, ok := component.(CapabilityProvider); ok {
		return provider.Descriptor()
	}
	descriptor := Descriptor{
		Name:     component.Name(),
		Protocol: core.Transport(component.Name()),
	}
	if _, ok := component.(MessageServer); ok {
		descriptor.Mode = ModeMessage
	} else if _, ok := component.(NativeRunner); ok {
		descriptor.Mode = ModeNative
	}
	if _, ok := component.(MiddlewareTarget); ok {
		descriptor.Capabilities |= CapabilityMiddleware
	}
	return descriptor
}

func isNilComponent(component Component) bool {
	value := reflect.ValueOf(component)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

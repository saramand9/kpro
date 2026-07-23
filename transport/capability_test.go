package transport

import (
	"context"
	"testing"

	"github.com/saramand9/bignet/kpro/core"
)

func TestDescribeFallsBackForLegacyMessageServer(t *testing.T) {
	descriptor := Describe(legacyMessageServer{})
	if descriptor.Name != "legacy" || descriptor.Protocol != core.Transport("legacy") {
		t.Fatalf("descriptor identity = %#v", descriptor)
	}
	if descriptor.Mode != ModeMessage {
		t.Fatalf("descriptor mode = %q, want %q", descriptor.Mode, ModeMessage)
	}
	if err := descriptor.Validate(); err != nil {
		t.Fatalf("validate fallback descriptor: %v", err)
	}
}

func TestDescriptorValidateRejectsConflictingWireModes(t *testing.T) {
	descriptor := Descriptor{
		Name:         "invalid",
		Protocol:     core.TransportTCP,
		Mode:         ModeMessage,
		Capabilities: CapabilityStream | CapabilityDatagram,
	}
	if err := descriptor.Validate(); err == nil {
		t.Fatal("Validate accepted stream and datagram capabilities together")
	}
}

func TestDescribeAcceptsTypedNilComponent(t *testing.T) {
	var component *nilMessageServer
	if descriptor := Describe(component); descriptor != (Descriptor{}) {
		t.Fatalf("typed nil descriptor = %#v", descriptor)
	}
}

type legacyMessageServer struct{}

func (legacyMessageServer) Name() string                                       { return "legacy" }
func (legacyMessageServer) ListenAndServe(context.Context, core.Handler) error { return nil }
func (legacyMessageServer) Shutdown(context.Context) error                     { return nil }

type nilMessageServer struct{}

func (*nilMessageServer) Name() string                                       { return "nil" }
func (*nilMessageServer) ListenAndServe(context.Context, core.Handler) error { return nil }
func (*nilMessageServer) Shutdown(context.Context) error                     { return nil }

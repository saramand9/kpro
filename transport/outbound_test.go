package transport

import (
	"context"
	"testing"
	"time"
)

type outboundSnapshotComponent struct {
	snapshot OutboundSnapshot
}

func (*outboundSnapshotComponent) Name() string                   { return "snapshot" }
func (*outboundSnapshotComponent) Shutdown(context.Context) error { return nil }
func (c *outboundSnapshotComponent) OutboundSnapshot() OutboundSnapshot {
	return c.snapshot
}

func TestComponentOutboundSnapshotUsesOptionalCapability(t *testing.T) {
	want := OutboundSnapshot{
		Sessions:           2,
		PendingWrites:      3,
		PendingBytes:       4096,
		AcceptedWrites:     8,
		SucceededWrites:    5,
		FailedWrites:       1,
		DroppedWrites:      1,
		DrainSucceeded:     2,
		DrainDurationTotal: 20 * time.Millisecond,
	}
	component := &outboundSnapshotComponent{snapshot: want}
	if got, supported := ComponentOutboundSnapshot(component); !supported || got != want {
		t.Fatalf("ComponentOutboundSnapshot() = (%+v, %v), want (%+v, true)", got, supported, want)
	}
	if got, supported := ComponentOutboundSnapshot(nil); supported || got != (OutboundSnapshot{}) {
		t.Fatalf("nil ComponentOutboundSnapshot() = (%+v, %v)", got, supported)
	}
	var typedNil *outboundSnapshotComponent
	if got, supported := ComponentOutboundSnapshot(typedNil); supported || got != (OutboundSnapshot{}) {
		t.Fatalf("typed-nil ComponentOutboundSnapshot() = (%+v, %v)", got, supported)
	}
}

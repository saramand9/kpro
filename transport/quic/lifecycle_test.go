package quic

import (
	"context"
	"sync/atomic"
	"testing"
)

func TestShutdownRejectsPendingConnectionAndStreamRegistration(t *testing.T) {
	server := New(Config{MaxStreams: 1})
	if !server.tryAcquireStreamSlot() {
		t.Fatal("failed to reserve pending stream slot")
	}
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if server.registerConnection(nil) {
		t.Fatal("registered a QUIC connection after shutdown")
	}
	pending := &session{id: 1, server: server}
	if server.registerSession(pending, nil) {
		t.Fatal("registered a QUIC stream after shutdown")
	}
	server.releaseStreamSlot()
	if active := atomic.LoadInt64(&server.active); active != 0 {
		t.Fatalf("active streams = %d, want 0", active)
	}
}

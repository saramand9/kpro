package tcp

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/saramand9/kpro/core"
)

func TestConnectionLimitIsAtomic(t *testing.T) {
	server := New(Config{MaxConnections: 1})
	const contenders = 128
	start := make(chan struct{})
	results := make(chan bool, contenders)
	var group sync.WaitGroup
	for index := 0; index < contenders; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			results <- server.tryAcquireConnectionSlot()
		}()
	}
	close(start)
	group.Wait()
	close(results)

	acquired := 0
	for result := range results {
		if result {
			acquired++
		}
	}
	if acquired != 1 {
		t.Fatalf("acquired slots = %d, want 1", acquired)
	}
	server.releaseConnectionSlot()
	if active := atomic.LoadInt64(&server.active); active != 0 {
		t.Fatalf("active connections = %d, want 0", active)
	}
}

func TestShutdownRejectsPendingSessionRegistration(t *testing.T) {
	server := New(Config{MaxConnections: 1})
	if !server.tryAcquireConnectionSlot() {
		t.Fatal("failed to reserve pending connection slot")
	}
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	pending := &session{id: 1, server: server}
	if server.registerSession(pending, nil) {
		t.Fatal("registered a TCP session after shutdown")
	}
	server.releaseConnectionSlot()
	if active := atomic.LoadInt64(&server.active); active != 0 {
		t.Fatalf("active connections = %d, want 0", active)
	}
}

func TestShutdownBeforeListenRejectsListenerPublication(t *testing.T) {
	server := New(Config{Addr: freeTCPTestAddr(t)})
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if err := server.ListenAndServe(context.Background(), nil); !errors.Is(err, core.ErrClosed) {
		t.Fatalf("ListenAndServe() error = %v, want %v", err, core.ErrClosed)
	}
}

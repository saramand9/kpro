package sse

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStreamAdmissionIsAtomic(t *testing.T) {
	endpoint := New(Config{MaxStreams: 1})
	const contenders = 128
	start := make(chan struct{})
	results := make(chan bool, contenders)
	var wg sync.WaitGroup
	for index := 0; index < contenders; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- endpoint.tryAcquireStreamSlot()
		}()
	}
	close(start)
	wg.Wait()
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
	if active := atomic.LoadInt64(&endpoint.active); active != 1 {
		t.Fatalf("active streams = %d, want 1", active)
	}
	endpoint.releaseStreamSlot()
}

func TestServeRejectsStreamOverLimit(t *testing.T) {
	endpoint := New(Config{MaxStreams: 1})
	if !endpoint.tryAcquireStreamSlot() {
		t.Fatal("failed to reserve initial stream slot")
	}
	defer endpoint.releaseStreamSlot()

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://example.test/events", nil)
	endpoint.serve(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	if !bytes.Contains(response.Body.Bytes(), []byte(ErrTooManyStreams.Error())) {
		t.Fatalf("body = %q, want stream limit error", response.Body.String())
	}
}

func TestHTTPServerUsesHandshakeAndIdleTimeouts(t *testing.T) {
	endpoint := New(Config{HandshakeTimeout: 3 * time.Second, IdleTimeout: 7 * time.Second})
	server := endpoint.buildHTTPServer()
	if server.ReadHeaderTimeout != 3*time.Second {
		t.Fatalf("ReadHeaderTimeout = %v, want 3s", server.ReadHeaderTimeout)
	}
	if server.IdleTimeout != 7*time.Second {
		t.Fatalf("IdleTimeout = %v, want 7s", server.IdleTimeout)
	}
}

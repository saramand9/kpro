package middleware

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/saramand9/bignet/kpro/core"
)

func TestMetricsRecordsSuccessAndError(t *testing.T) {
	metrics := NewMetrics()
	ctx := core.NewInvocation(core.InvocationOptions{Operation: core.MessageOperation(testMsgEcho)})

	success := metrics.Middleware()(func(core.Invocation) error {
		time.Sleep(time.Millisecond)
		return nil
	})
	if err := success(ctx); err != nil {
		t.Fatalf("success handler returned %v", err)
	}

	wantErr := errors.New("handler failed")
	failure := metrics.Middleware()(func(core.Invocation) error {
		return wantErr
	})
	if err := failure(ctx); !errors.Is(err, wantErr) {
		t.Fatalf("failure handler returned %v, want %v", err, wantErr)
	}

	snapshot := metrics.Snapshot()
	if snapshot.Started != 2 || snapshot.Completed != 2 {
		t.Fatalf("started/completed = %d/%d, want 2/2", snapshot.Started, snapshot.Completed)
	}
	if snapshot.Errors != 1 {
		t.Fatalf("errors = %d, want 1", snapshot.Errors)
	}
	if snapshot.Panics != 0 {
		t.Fatalf("panics = %d, want 0", snapshot.Panics)
	}
	if snapshot.Inflight != 0 {
		t.Fatalf("inflight = %d, want 0", snapshot.Inflight)
	}
	if snapshot.TotalLatency <= 0 || snapshot.AverageLatency <= 0 || snapshot.MaxLatency <= 0 {
		t.Fatalf("latencies were not recorded: %+v", snapshot)
	}
}

func TestMetricsRecordsAndRethrowsPanic(t *testing.T) {
	metrics := NewMetrics()
	ctx := core.NewInvocation(core.InvocationOptions{Operation: core.MessageOperation(testMsgEcho)})
	handler := metrics.Middleware()(func(core.Invocation) error {
		panic("boom")
	})

	recovered := recoverPanic(func() {
		_ = handler(ctx)
	})
	if recovered != "boom" {
		t.Fatalf("recovered = %v, want boom", recovered)
	}

	snapshot := metrics.Snapshot()
	if snapshot.Started != 1 || snapshot.Completed != 1 {
		t.Fatalf("started/completed = %d/%d, want 1/1", snapshot.Started, snapshot.Completed)
	}
	if snapshot.Errors != 1 || snapshot.Panics != 1 {
		t.Fatalf("errors/panics = %d/%d, want 1/1", snapshot.Errors, snapshot.Panics)
	}
	if snapshot.Inflight != 0 {
		t.Fatalf("inflight = %d, want 0", snapshot.Inflight)
	}
}

func TestMetricsServeHTTP(t *testing.T) {
	metrics := NewMetrics()
	ctx := core.NewInvocation(core.InvocationOptions{Operation: core.MessageOperation(testMsgEcho)})
	handler := metrics.Middleware()(func(core.Invocation) error {
		return nil
	})
	if err := handler(ctx); err != nil {
		t.Fatalf("handler returned %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	resp := httptest.NewRecorder()
	metrics.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
	if got := resp.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("content type = %q", got)
	}
	var snapshot MetricsSnapshot
	if err := json.Unmarshal(resp.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode metrics response: %v", err)
	}
	if snapshot.Started != 1 || snapshot.Completed != 1 {
		t.Fatalf("snapshot = %+v, want one completed call", snapshot)
	}
}

func recoverPanic(fn func()) (recovered interface{}) {
	defer func() {
		recovered = recover()
	}()
	fn()
	return nil
}

package healthx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHandlerFailsWhenAnyCheckFails(t *testing.T) {
	handler := MustNew([]Check{
		{Name: "database", Run: func(context.Context) error { return errors.New("down") }},
		{Name: "runtime", Run: func(context.Context) error { return nil }},
	}, Config{ExposeErrors: true})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), `"name":"database","status":"fail","error":"down"`) {
		t.Fatalf("response = %s", recorder.Body.String())
	}
}

func TestHandlerBoundsChecksThatHonorCancellation(t *testing.T) {
	handler := MustNew([]Check{{Name: "slow", Run: func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}}}, Config{Timeout: 10 * time.Millisecond})
	started := time.Now()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", recorder.Code)
	}
	if time.Since(started) > time.Second {
		t.Fatal("health handler did not honor its timeout")
	}
}

func TestNewRejectsDuplicateCheckNames(t *testing.T) {
	check := Check{Name: "same", Run: func(context.Context) error { return nil }}
	if _, err := New([]Check{check, check}); err == nil {
		t.Fatal("duplicate health check names were accepted")
	}
}

func TestHandlerDoesNotStartOverlappingStuckChecks(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	handler := MustNew([]Check{{Name: "stuck", Run: func(context.Context) error {
		calls.Add(1)
		<-release
		return nil
	}}}, Config{Timeout: 5 * time.Millisecond})
	for index := 0; index < 10; index++ {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("probe %d status = %d", index, recorder.Code)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("stuck check calls = %d, want 1", got)
	}
	close(release)
}

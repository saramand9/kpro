package core

import (
	"errors"
	"testing"
)

func TestNewRouterRejectsMultipleConfigs(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("NewRouter accepted multiple configs")
		}
	}()
	_ = NewRouter(RouterConfig{}, RouterConfig{})
}

func TestRouterCheckedRegistrationReportsInvalidAndLateHandlers(t *testing.T) {
	router := NewRouter()
	if err := router.HandleChecked(1, nil); !errors.Is(err, ErrNilHandler) {
		t.Fatalf("nil handler error = %v, want ErrNilHandler", err)
	}
	handler := func(Context) error { return nil }
	if err := router.HandleChecked(1, handler); err != nil {
		t.Fatalf("HandleChecked: %v", err)
	}
	if err := router.HandleSyncChecked(2, handler); err != nil {
		t.Fatalf("HandleSyncChecked: %v", err)
	}
	if err := router.HandleAsyncChecked(3, handler); err != nil {
		t.Fatalf("HandleAsyncChecked: %v", err)
	}

	router.Close()
	if err := router.HandleChecked(4, handler); !errors.Is(err, ErrRouterClosed) {
		t.Fatalf("late registration error = %v, want ErrRouterClosed", err)
	}
}

func TestRouterCheckedRegistrationRejectsDuplicateWithoutReplacingHandler(t *testing.T) {
	router := NewRouter()
	firstCalled := false
	first := func(Context) error {
		firstCalled = true
		return nil
	}
	if err := router.HandleChecked(7, first); err != nil {
		t.Fatalf("first HandleChecked: %v", err)
	}
	if err := router.HandleChecked(7, func(Context) error {
		t.Fatal("duplicate handler replaced the original")
		return nil
	}); !errors.Is(err, ErrHandlerAlreadyRegistered) {
		t.Fatalf("duplicate HandleChecked error = %v, want %v", err, ErrHandlerAlreadyRegistered)
	}

	router.mu.RLock()
	registered := router.routes[7].handler
	router.mu.RUnlock()
	if err := registered(nil); err != nil {
		t.Fatalf("original handler returned error: %v", err)
	}
	if !firstCalled {
		t.Fatal("original handler was not preserved")
	}
}

func TestRouterDuplicateRegistrationDoesNotRebuildMiddlewareChain(t *testing.T) {
	router := NewRouter()
	builds := 0
	router.Use(func(next InvocationHandler) InvocationHandler {
		builds++
		return next
	})
	if err := router.HandleChecked(8, func(Context) error { return nil }); err != nil {
		t.Fatalf("first HandleChecked: %v", err)
	}
	if builds != 1 {
		t.Fatalf("middleware builds after first registration = %d, want 1", builds)
	}
	if err := router.HandleChecked(8, func(Context) error { return nil }); !errors.Is(err, ErrHandlerAlreadyRegistered) {
		t.Fatalf("duplicate HandleChecked error = %v, want %v", err, ErrHandlerAlreadyRegistered)
	}
	if builds != 1 {
		t.Fatalf("duplicate registration rebuilt middleware chain: builds = %d, want 1", builds)
	}
}

func TestRouterCompatibilityRegistrationPanicsOnFailure(t *testing.T) {
	router := NewRouter()
	router.Close()
	defer func() {
		recovered := recover()
		err, ok := recovered.(error)
		if !ok || !errors.Is(err, ErrRouterClosed) {
			t.Fatalf("panic = %v, want ErrRouterClosed", recovered)
		}
	}()
	router.Handle(1, func(Context) error { return nil })
}

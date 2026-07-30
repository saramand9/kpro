package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/saramand9/kpro/core"
)

func TestEndpointCheckedRegistrationForwardsToRouter(t *testing.T) {
	endpoint := &Endpoint{router: core.NewRouter()}
	defer shutdownCheckedRegistrationRouter(t, endpoint.router)

	called := make(chan uint32, 3)
	handler := func(ctx core.Context) error {
		called <- ctx.Message().ID
		return nil
	}
	if err := endpoint.HandleChecked(11, handler); err != nil {
		t.Fatalf("HandleChecked: %v", err)
	}
	if err := endpoint.HandleSyncChecked(12, handler); err != nil {
		t.Fatalf("HandleSyncChecked: %v", err)
	}
	if err := endpoint.HandleAsyncChecked(13, handler); err != nil {
		t.Fatalf("HandleAsyncChecked: %v", err)
	}

	session := &fakeSession{id: 1, done: make(chan struct{})}
	for _, msgID := range []uint32{11, 12, 13} {
		if err := endpoint.dispatch(core.NewContext(session, core.Message{ID: msgID})); err != nil {
			t.Fatalf("dispatch message %d: %v", msgID, err)
		}
	}

	seen := make(map[uint32]bool, 3)
	for range 3 {
		select {
		case msgID := <-called:
			seen[msgID] = true
		case <-time.After(time.Second):
			t.Fatal("registered handler was not called")
		}
	}
	for _, msgID := range []uint32{11, 12, 13} {
		if !seen[msgID] {
			t.Errorf("handler for message %d was not called", msgID)
		}
	}
}

func TestEndpointCheckedRegistrationReturnsRouterErrors(t *testing.T) {
	endpoint := &Endpoint{router: core.NewRouter()}
	defer shutdownCheckedRegistrationRouter(t, endpoint.router)
	handler := func(core.Context) error { return nil }

	if err := endpoint.HandleChecked(21, nil); !errors.Is(err, core.ErrNilHandler) {
		t.Fatalf("nil handler error = %v, want %v", err, core.ErrNilHandler)
	}
	if err := endpoint.HandleChecked(21, handler); err != nil {
		t.Fatalf("initial registration: %v", err)
	}
	if err := endpoint.HandleAsyncChecked(21, handler); !errors.Is(err, core.ErrHandlerAlreadyRegistered) {
		t.Fatalf("duplicate error = %v, want %v", err, core.ErrHandlerAlreadyRegistered)
	}

	endpoint.router.Close()
	if err := endpoint.HandleSyncChecked(22, handler); !errors.Is(err, core.ErrRouterClosed) {
		t.Fatalf("closed router error = %v, want %v", err, core.ErrRouterClosed)
	}
}

func TestEndpointCheckedRegistrationRejectsMissingRouter(t *testing.T) {
	handler := func(core.Context) error { return nil }

	var nilEndpoint *Endpoint
	if err := nilEndpoint.HandleChecked(31, handler); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("nil endpoint error = %v, want %v", err, ErrInvalidConfiguration)
	}

	endpoint := &Endpoint{}
	if err := endpoint.HandleSyncChecked(31, handler); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("nil router sync error = %v, want %v", err, ErrInvalidConfiguration)
	}
	if err := endpoint.HandleAsyncChecked(31, handler); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("nil router async error = %v, want %v", err, ErrInvalidConfiguration)
	}
}

func TestEndpointFluentRegistrationStillChains(t *testing.T) {
	endpoint := &Endpoint{router: core.NewRouter()}
	defer shutdownCheckedRegistrationRouter(t, endpoint.router)

	called := make(chan uint32, 3)
	handler := func(ctx core.Context) error {
		called <- ctx.Message().ID
		return nil
	}
	if got := endpoint.Handle(41, handler); got != endpoint {
		t.Fatal("Handle did not return the endpoint")
	}
	if got := endpoint.HandleSync(42, handler); got != endpoint {
		t.Fatal("HandleSync did not return the endpoint")
	}
	if got := endpoint.HandleAsync(43, handler); got != endpoint {
		t.Fatal("HandleAsync did not return the endpoint")
	}

	session := &fakeSession{id: 2, done: make(chan struct{})}
	for _, msgID := range []uint32{41, 42, 43} {
		if err := endpoint.dispatch(core.NewContext(session, core.Message{ID: msgID})); err != nil {
			t.Fatalf("dispatch message %d: %v", msgID, err)
		}
	}
	for range 3 {
		select {
		case <-called:
		case <-time.After(time.Second):
			t.Fatal("fluent handler was not called")
		}
	}
}

func TestEndpointFluentRegistrationStillPanicsOnRouterError(t *testing.T) {
	endpoint := &Endpoint{router: core.NewRouter()}
	defer shutdownCheckedRegistrationRouter(t, endpoint.router)
	endpoint.Handle(51, func(core.Context) error { return nil })

	defer func() {
		recovered := recover()
		err, ok := recovered.(error)
		if !ok || !errors.Is(err, core.ErrHandlerAlreadyRegistered) {
			t.Fatalf("panic = %v, want %v", recovered, core.ErrHandlerAlreadyRegistered)
		}
	}()
	endpoint.Handle(51, func(core.Context) error { return nil })
}

func shutdownCheckedRegistrationRouter(t *testing.T, router *core.Router) {
	t.Helper()
	if err := router.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown router: %v", err)
	}
}

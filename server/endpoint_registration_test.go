package server

import (
	"context"
	"errors"
	"sync"
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

func TestEndpointShutdownKeepsTransportOpenWhileAcceptedHandlerDrains(t *testing.T) {
	transport := &shutdownOrderingTransport{shutdownCalled: make(chan struct{})}
	endpoint := &Endpoint{
		transport: transport,
		router: core.NewRouter(core.RouterConfig{
			SyncQueueSize:         1,
			MaxInflightPerSession: 1,
			ShutdownTimeout:       time.Second,
		}),
	}

	started := make(chan struct{})
	release := make(chan struct{})
	endpoint.HandleSync(61, func(core.Context) error {
		close(started)
		<-release
		select {
		case <-transport.shutdownCalled:
			return errors.New("transport closed before accepted handler completed")
		default:
			return nil
		}
	})

	session := &fakeSession{id: 61, done: make(chan struct{})}
	if err := endpoint.dispatch(core.NewContext(session, core.Message{ID: 61})); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- endpoint.Shutdown(context.Background()) }()
	select {
	case <-transport.shutdownCalled:
		t.Fatal("transport shut down while an accepted handler was still running")
	case <-time.After(25 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("Shutdown() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not complete after handler drain")
	}
	select {
	case <-transport.shutdownCalled:
	default:
		t.Fatal("transport was not shut down after handler drain")
	}
}

func TestEndpointShutdownQuiescesBeforeRouterDrainAndTransportClose(t *testing.T) {
	transport := &quiesceOrderingTransport{
		quiesceCalled:  make(chan struct{}),
		drainCalled:    make(chan struct{}),
		shutdownCalled: make(chan struct{}),
	}
	endpoint := &Endpoint{
		transport: transport,
		router: core.NewRouter(core.RouterConfig{
			SyncQueueSize:         1,
			MaxInflightPerSession: 1,
			ShutdownTimeout:       time.Second,
		}),
	}

	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	endpoint.HandleSync(63, func(core.Context) error {
		close(handlerStarted)
		<-releaseHandler
		select {
		case <-transport.quiesceCalled:
			return nil
		default:
			return errors.New("router drained before transport quiesced")
		}
	})
	session := &fakeSession{id: 63, done: make(chan struct{})}
	if err := endpoint.dispatch(core.NewContext(session, core.Message{ID: 63})); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	select {
	case <-handlerStarted:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- endpoint.Shutdown(context.Background()) }()
	select {
	case <-transport.quiesceCalled:
	case <-time.After(time.Second):
		t.Fatal("transport was not quiesced before Router drain")
	}
	select {
	case <-transport.drainCalled:
		t.Fatal("outbound drained while an accepted handler was running")
	case <-transport.shutdownCalled:
		t.Fatal("transport shut down while accepted handler was running")
	case <-time.After(25 * time.Millisecond):
	}

	close(releaseHandler)
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("Shutdown() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not complete after handler drain")
	}
	select {
	case <-transport.drainCalled:
	default:
		t.Fatal("outbound was not drained after Router work")
	}
	select {
	case <-transport.shutdownCalled:
	default:
		t.Fatal("transport was not closed after Router drain")
	}
}

func TestEndpointShutdownJoinsQuiesceAndTransportErrors(t *testing.T) {
	quiesceErr := errors.New("quiesce failed")
	drainErr := errors.New("outbound drain failed")
	transportErr := errors.New("transport shutdown failed")
	transport := &quiesceOrderingTransport{
		quiesceCalled:  make(chan struct{}),
		drainCalled:    make(chan struct{}),
		shutdownCalled: make(chan struct{}),
		quiesceErr:     quiesceErr,
		drainErr:       drainErr,
		shutdownErr:    transportErr,
	}
	endpoint := &Endpoint{transport: transport, router: core.NewRouter()}
	err := endpoint.Shutdown(context.Background())
	if !errors.Is(err, quiesceErr) || !errors.Is(err, drainErr) || !errors.Is(err, transportErr) {
		t.Fatalf("Shutdown() error = %v, want joined quiesce, drain, and transport errors", err)
	}
}

func TestEndpointServeCancellationDrainsAcceptedHandlerBeforeTransport(t *testing.T) {
	transport := &cancellationOrderingTransport{
		serveStarted:         make(chan struct{}),
		serveContextCanceled: make(chan struct{}),
		quiesceCalled:        make(chan struct{}),
		shutdownCalled:       make(chan struct{}),
	}
	endpoint := &Endpoint{
		transport: transport,
		router: core.NewRouter(core.RouterConfig{
			SyncQueueSize:         1,
			MaxInflightPerSession: 1,
			ShutdownTimeout:       time.Second,
		}),
	}

	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	endpoint.HandleSync(62, func(core.Context) error {
		close(handlerStarted)
		<-releaseHandler
		return nil
	})

	serveCtx, cancelServe := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- endpoint.ListenAndServe(serveCtx) }()
	select {
	case <-transport.serveStarted:
	case <-time.After(time.Second):
		t.Fatal("transport serve did not start")
	}

	session := &fakeSession{id: 62, done: make(chan struct{})}
	if err := endpoint.dispatch(core.NewContext(session, core.Message{ID: 62})); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	select {
	case <-handlerStarted:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}

	cancelServe()
	select {
	case <-transport.quiesceCalled:
	case <-time.After(time.Second):
		t.Fatal("parent cancellation did not quiesce transport")
	}
	select {
	case <-transport.shutdownCalled:
		t.Fatal("transport shut down while an accepted handler was running")
	case <-transport.serveContextCanceled:
		t.Fatal("parent cancellation reached the transport before Router drain")
	case <-time.After(25 * time.Millisecond):
	}

	close(releaseHandler)
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("ListenAndServe() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("serve did not stop after accepted handler drained")
	}
	select {
	case <-transport.shutdownCalled:
	default:
		t.Fatal("transport shutdown was not called after Router drain")
	}
}

type shutdownOrderingTransport struct {
	shutdownCalled chan struct{}
}

func (*shutdownOrderingTransport) Name() string { return "shutdown-ordering" }

func (*shutdownOrderingTransport) ListenAndServe(context.Context, core.Handler) error { return nil }

func (t *shutdownOrderingTransport) Shutdown(context.Context) error {
	close(t.shutdownCalled)
	return nil
}

type quiesceOrderingTransport struct {
	quiesceCalled  chan struct{}
	drainCalled    chan struct{}
	shutdownCalled chan struct{}
	quiesceErr     error
	drainErr       error
	shutdownErr    error
	quiesceOnce    sync.Once
	drainOnce      sync.Once
	shutdownOnce   sync.Once
}

func (*quiesceOrderingTransport) Name() string { return "quiesce-ordering" }

func (*quiesceOrderingTransport) ListenAndServe(context.Context, core.Handler) error {
	return nil
}

func (t *quiesceOrderingTransport) Quiesce(context.Context) error {
	t.quiesceOnce.Do(func() { close(t.quiesceCalled) })
	return t.quiesceErr
}

func (t *quiesceOrderingTransport) DrainOutbound(context.Context) error {
	if t.drainCalled != nil {
		t.drainOnce.Do(func() { close(t.drainCalled) })
	}
	return t.drainErr
}

func (t *quiesceOrderingTransport) Shutdown(context.Context) error {
	if t.drainCalled != nil {
		select {
		case <-t.drainCalled:
		default:
			return errors.New("transport shutdown ran before outbound drain")
		}
	}
	t.shutdownOnce.Do(func() { close(t.shutdownCalled) })
	return t.shutdownErr
}

type cancellationOrderingTransport struct {
	serveStarted         chan struct{}
	serveContextCanceled chan struct{}
	quiesceCalled        chan struct{}
	shutdownCalled       chan struct{}
	quiesceOnce          sync.Once
	shutdownOnce         sync.Once
}

func (*cancellationOrderingTransport) Name() string { return "cancellation-ordering" }

func (t *cancellationOrderingTransport) ListenAndServe(ctx context.Context, _ core.Handler) error {
	close(t.serveStarted)
	select {
	case <-ctx.Done():
		close(t.serveContextCanceled)
		return nil
	case <-t.quiesceCalled:
		return nil
	case <-t.shutdownCalled:
		return nil
	}
}

func (t *cancellationOrderingTransport) Quiesce(context.Context) error {
	t.quiesceOnce.Do(func() { close(t.quiesceCalled) })
	return nil
}

func (t *cancellationOrderingTransport) Shutdown(context.Context) error {
	t.shutdownOnce.Do(func() { close(t.shutdownCalled) })
	return nil
}

func shutdownCheckedRegistrationRouter(t *testing.T, router *core.Router) {
	t.Helper()
	if err := router.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown router: %v", err)
	}
}

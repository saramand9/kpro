package core

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRouterErrorObserverReceivesHandlerErrorSnapshot(t *testing.T) {
	handlerErr := errors.New("handler failed")
	events := make(chan RouterErrorEvent, 2)
	router := NewRouter(RouterConfig{
		ErrorObserver: func(event RouterErrorEvent) {
			events <- event
		},
	})
	cleanupRouter(t, router)
	router.HandleSync(77, func(Context) error {
		return handlerErr
	})

	session := newFakeSession(41)
	if err := router.Dispatch(NewContext(session, Message{
		ID:   77,
		Seq:  9,
		Data: []byte("must not appear in the event"),
	})); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	response := session.waitForMessage(t)
	if response.ID != MsgError || response.Seq != 9 {
		t.Fatalf("response = %#v, want MsgError sequence 9", response)
	}
	event := waitForRouterErrorEvent(t, events)
	if event.Stage != RouterErrorStageHandler {
		t.Fatalf("stage = %q, want %q", event.Stage, RouterErrorStageHandler)
	}
	if event.Transport != TransportTCP || event.ConnID != 41 || event.RemoteAddr != "" {
		t.Fatalf("connection snapshot = %q/%d/%q", event.Transport, event.ConnID, event.RemoteAddr)
	}
	if event.MessageID != 77 || event.Sequence != 9 {
		t.Fatalf("message snapshot = %d/%d, want 77/9", event.MessageID, event.Sequence)
	}
	if !errors.Is(event.Err, handlerErr) {
		t.Fatalf("event error = %v, want handler error", event.Err)
	}
	if event.ResponseErr != nil || len(event.Stack) != 0 {
		t.Fatalf("response error/stack = %v/%d, want nil/0", event.ResponseErr, len(event.Stack))
	}

	shutdownRouter(t, router)
	assertNoRouterErrorEvent(t, events)
}

func TestRouterErrorObserverReportsPanicAndResponseFailureOnce(t *testing.T) {
	panicErr := errors.New("panic cause")
	responseErr := errors.New("response failed")
	events := make(chan RouterErrorEvent, 2)
	router := NewRouter(RouterConfig{
		ErrorObserver: func(event RouterErrorEvent) {
			events <- event
		},
	})
	cleanupRouter(t, router)
	router.HandleSync(88, func(Context) error {
		panic(panicErr)
	})

	session := newFakeSession(42)
	messageContext := &routerErrorResponseContext{
		Context: NewContext(session, Message{ID: 88, Seq: 10}),
		err:     responseErr,
	}
	if err := router.Dispatch(messageContext); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	event := waitForRouterErrorEvent(t, events)
	if event.Stage != RouterErrorStagePanic {
		t.Fatalf("stage = %q, want %q", event.Stage, RouterErrorStagePanic)
	}
	if !errors.Is(event.Err, ErrRecovered) || !errors.Is(event.Err, panicErr) {
		t.Fatalf("panic error = %v, want ErrRecovered and original panic", event.Err)
	}
	if !errors.Is(event.ResponseErr, responseErr) {
		t.Fatalf("response error = %v, want %v", event.ResponseErr, responseErr)
	}
	if len(event.Stack) == 0 {
		t.Fatal("panic event did not include a stack")
	}
	if event.Transport != TransportTCP || event.ConnID != 42 || event.MessageID != 88 || event.Sequence != 10 {
		t.Fatalf("panic snapshot = %#v", event)
	}

	shutdownRouter(t, router)
	assertNoRouterErrorEvent(t, events)
}

func TestRouterRejectReturnsAndObservesResponseFailure(t *testing.T) {
	responseErr := errors.New("response failed")
	events := make(chan RouterErrorEvent, 1)
	router := NewRouter(RouterConfig{
		ErrorObserver: func(event RouterErrorEvent) {
			events <- event
		},
	})
	cleanupRouter(t, router)

	session := newFakeSession(43)
	messageContext := &routerErrorResponseContext{
		Context: NewContext(session, Message{ID: 99, Seq: 11}),
		err:     responseErr,
	}
	err := router.Dispatch(messageContext)
	if !errors.Is(err, ErrNoHandler) || !errors.Is(err, responseErr) {
		t.Fatalf("dispatch error = %v, want ErrNoHandler and response error", err)
	}
	event := waitForRouterErrorEvent(t, events)
	if event.Stage != RouterErrorStageResponse || !errors.Is(event.Err, ErrNoHandler) {
		t.Fatalf("response event = %#v, want response/ErrNoHandler", event)
	}
	if !errors.Is(event.ResponseErr, responseErr) {
		t.Fatalf("response error = %v, want %v", event.ResponseErr, responseErr)
	}
	if event.Transport != TransportTCP || event.ConnID != 43 || event.MessageID != 99 || event.Sequence != 11 {
		t.Fatalf("response snapshot = %#v", event)
	}
}

func TestRouterDrainedDispatchObservesResponseFailure(t *testing.T) {
	responseErr := errors.New("response failed")
	events := make(chan RouterErrorEvent, 1)
	router := NewRouter(RouterConfig{
		SyncQueueSize:         2,
		MaxInflightPerSession: 3,
		ErrorObserver: func(event RouterErrorEvent) {
			events <- event
		},
	})
	cleanupRouter(t, router)
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHandler := func() {
		releaseOnce.Do(func() { close(release) })
	}
	t.Cleanup(releaseHandler)
	router.HandleSync(103, func(Context) error {
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		return nil
	})

	session := newFakeSession(48)
	if err := router.Dispatch(NewContext(session, Message{ID: 103, Seq: 1})); err != nil {
		t.Fatalf("dispatch active: %v", err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for active handler")
	}
	queued := &routerErrorResponseContext{
		Context: NewContext(session, Message{ID: 103, Seq: 2}),
		err:     responseErr,
	}
	if err := router.Dispatch(queued); err != nil {
		t.Fatalf("dispatch queued: %v", err)
	}

	value, ok := router.sessions.Load(sessionExecutorKey{id: session.ID(), done: session.Done()})
	if !ok {
		t.Fatal("session executor not found")
	}
	value.(*sessionExecutor).close()
	event := waitForRouterErrorEvent(t, events)
	if event.Stage != RouterErrorStageResponse || !errors.Is(event.Err, ErrRouterClosed) {
		t.Fatalf("drain event = %#v, want response/ErrRouterClosed", event)
	}
	if !errors.Is(event.ResponseErr, responseErr) || event.ConnID != 48 || event.Sequence != 2 {
		t.Fatalf("drain response snapshot = %#v", event)
	}
	releaseHandler()
	shutdownRouter(t, router)
}

func TestRouterErrorObserverPanicIsIsolated(t *testing.T) {
	var calls atomic.Int32
	router := NewRouter(RouterConfig{
		ErrorObserver: func(RouterErrorEvent) {
			calls.Add(1)
			panic("observer failed")
		},
	})
	cleanupRouter(t, router)
	router.HandleSync(100, func(Context) error {
		return errors.New("handler failed")
	})

	session := newFakeSession(44)
	for sequence := uint64(1); sequence <= 2; sequence++ {
		if err := router.Dispatch(NewContext(session, Message{ID: 100, Seq: sequence})); err != nil {
			t.Fatalf("dispatch %d: %v", sequence, err)
		}
		response := session.waitForMessage(t)
		if response.ID != MsgError || response.Seq != sequence {
			t.Fatalf("response = %#v, want MsgError sequence %d", response, sequence)
		}
	}
	shutdownRouter(t, router)
	if got := calls.Load(); got != 2 {
		t.Fatalf("observer calls = %d, want 2", got)
	}
}

func TestRouterErrorObserverMayRunConcurrently(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseObservers := func() {
		releaseOnce.Do(func() { close(release) })
	}

	router := NewRouter(RouterConfig{
		AsyncWorkers:          2,
		AsyncQueueSize:        2,
		MaxInflightPerSession: 2,
		MaxInflight:           2,
		ErrorObserver: func(RouterErrorEvent) {
			entered <- struct{}{}
			<-release
		},
	})
	cleanupRouter(t, router)
	t.Cleanup(releaseObservers)
	router.HandleAsync(101, func(Context) error {
		return errors.New("handler failed")
	})

	first := newFakeSession(45)
	second := newFakeSession(46)
	if err := router.Dispatch(NewContext(first, Message{ID: 101, Seq: 1})); err != nil {
		t.Fatalf("dispatch first: %v", err)
	}
	if err := router.Dispatch(NewContext(second, Message{ID: 101, Seq: 2})); err != nil {
		t.Fatalf("dispatch second: %v", err)
	}
	waitForRouterErrorObserverEntry(t, entered)
	waitForRouterErrorObserverEntry(t, entered)
	first.waitForMessage(t)
	second.waitForMessage(t)
	releaseObservers()
	shutdownRouter(t, router)
}

func TestRouterObservesErrorResponsePanic(t *testing.T) {
	handlerErr := errors.New("handler failed")
	responsePanic := errors.New("response panic")
	events := make(chan RouterErrorEvent, 1)
	router := NewRouter(RouterConfig{
		ErrorObserver: func(event RouterErrorEvent) {
			events <- event
		},
	})
	cleanupRouter(t, router)
	router.HandleSync(102, func(Context) error {
		return handlerErr
	})

	session := newFakeSession(47)
	messageContext := &routerErrorResponseContext{
		Context:    NewContext(session, Message{ID: 102, Seq: 12}),
		panicValue: responsePanic,
	}
	if err := router.Dispatch(messageContext); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	event := waitForRouterErrorEvent(t, events)
	if !errors.Is(event.Err, handlerErr) || !errors.Is(event.ResponseErr, responsePanic) {
		t.Fatalf("event errors = %v/%v", event.Err, event.ResponseErr)
	}
	shutdownRouter(t, router)
}

func TestDefaultRouterErrorObserverLogsResponseFailure(t *testing.T) {
	var output bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	previousPrefix := log.Prefix()
	log.SetOutput(&output)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
	})

	router := NewRouter()
	cleanupRouter(t, router)
	started := make(chan struct{})
	router.HandleSync(104, func(Context) error {
		close(started)
		return errors.New("handler failed")
	})

	session := newFakeSession(49)
	messageContext := &routerErrorResponseContext{
		Context: NewContext(session, Message{ID: 104, Seq: 13}),
		err:     errors.New("response failed"),
	}
	if err := router.Dispatch(messageContext); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for handler")
	}
	shutdownRouter(t, router)

	logged := output.String()
	for _, want := range []string{
		"message handler error: handler failed",
		"message error response failed:",
		"transport=tcp",
		"conn=49",
		"message=104",
		"sequence=13",
		"response failed",
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("default Router log = %q, want substring %q", logged, want)
		}
	}
}

type routerErrorResponseContext struct {
	Context
	err        error
	panicValue any
}

func (c *routerErrorResponseContext) Error(string) error {
	if c.panicValue != nil {
		panic(c.panicValue)
	}
	return c.err
}

func cleanupRouter(t *testing.T, router *Router) {
	t.Helper()
	t.Cleanup(func() {
		if err := router.Shutdown(context.Background()); err != nil {
			t.Errorf("cleanup router: %v", err)
		}
	})
}

func shutdownRouter(t *testing.T, router *Router) {
	t.Helper()
	if err := router.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown router: %v", err)
	}
}

func waitForRouterErrorEvent(t *testing.T, events <-chan RouterErrorEvent) RouterErrorEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Router error event")
		return RouterErrorEvent{}
	}
}

func assertNoRouterErrorEvent(t *testing.T, events <-chan RouterErrorEvent) {
	t.Helper()
	select {
	case event := <-events:
		t.Fatalf("unexpected extra Router error event: %#v", event)
	default:
	}
}

func waitForRouterErrorObserverEntry(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for concurrent Router error observer")
	}
}

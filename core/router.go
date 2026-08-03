// File: core/router.go
// Purpose: Defines shared KPro primitives used by transports, routers, and middleware.

package core

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// DefaultSyncQueueSize is the default per-session sync dispatch queue size.
	DefaultSyncQueueSize = 64
	// DefaultAsyncWorkers lets the router derive worker count from GOMAXPROCS.
	DefaultAsyncWorkers = 0
	// DefaultAsyncQueueSize is the default shared async dispatch queue size.
	DefaultAsyncQueueSize = 1024
	// DefaultMaxInflightPerSession limits outstanding work per session.
	DefaultMaxInflightPerSession = 128
	// DefaultSessionIdleTimeout removes idle session executors.
	DefaultSessionIdleTimeout = time.Minute
	// DefaultRouterShutdownTimeout bounds graceful router shutdown.
	DefaultRouterShutdownTimeout = 5 * time.Second
)

// DispatchMode controls whether a route is serialized per session or run by async workers.
type DispatchMode uint8

const (
	// DispatchSync serializes handler execution per session.
	DispatchSync DispatchMode = iota
	// DispatchAsync runs handler execution through shared async workers.
	DispatchAsync
)

// RouterErrorStage identifies the Router operation that produced an observed
// error.
type RouterErrorStage string

const (
	// RouterErrorStageHandler reports an error returned by an accepted handler.
	RouterErrorStageHandler RouterErrorStage = "handler"
	// RouterErrorStagePanic reports a panic recovered while executing an
	// accepted handler.
	RouterErrorStagePanic RouterErrorStage = "panic"
	// RouterErrorStageResponse reports a standalone failure to deliver a
	// public error response. Handler and panic response failures remain on
	// their owning event through ResponseErr.
	RouterErrorStageResponse RouterErrorStage = "response"
)

// RouterErrorEvent is a stable, payload-free snapshot of one Router error.
// Err is the original handler error, a recovered-panic error, or the dispatch
// error that required a response. ResponseErr reports a failure from
// Context.Error and retains its errors.Is/errors.As chain. Stack is populated
// only for a recovered panic and is safe for the observer to retain.
type RouterErrorEvent struct {
	Stage       RouterErrorStage
	Transport   Transport
	ConnID      uint64
	RemoteAddr  string
	MessageID   uint32
	Sequence    uint64
	Err         error
	ResponseErr error
	Stack       []byte
}

// RouterErrorObserver observes Router handler, panic, and error-response
// failures. It is called synchronously on the dispatch caller or Router worker,
// and calls may run concurrently for different messages. Implementations must
// return promptly and be concurrency-safe. Observer panics are isolated.
type RouterErrorObserver func(RouterErrorEvent)

// RouterConfig tunes message dispatch queues, worker counts, and shutdown behavior.
type RouterConfig struct {
	// SyncQueueSize is the per-session queue length for sync handlers.
	SyncQueueSize int
	// AsyncWorkers is the shared worker count for async handlers. Values <= 0 use a runtime-derived default.
	AsyncWorkers int
	// AsyncQueueSize is the shared queue length for async handler tasks.
	AsyncQueueSize int
	// MaxInflightPerSession caps queued or running work for one session.
	MaxInflightPerSession int
	// MaxInflight caps accepted work across every session. Values <= 0 use
	// DefaultMaxInflight.
	MaxInflight int
	// MaxWaitingDispatches caps callers blocked by BackpressureWait across all
	// admission stages. Values <= 0 use DefaultMaxWaitingDispatches, capped by
	// MaxInflight.
	MaxWaitingDispatches int
	// MaxWaitingDispatchesPerSession prevents one session from monopolizing the
	// Router-wide wait allowance. Values <= 0 use
	// DefaultMaxWaitingDispatchesPerSession, capped by
	// MaxInflightPerSession.
	MaxWaitingDispatchesPerSession int
	// Backpressure selects reject or context-cancelable wait behavior when an
	// inflight or queue limit is reached. The zero value preserves rejection.
	// Handler-initiated reentrant dispatch under Wait must use DispatchContext
	// with a deadline because the current handler may itself own needed capacity.
	Backpressure BackpressurePolicy
	// SessionIdleTimeout removes a session executor after it becomes idle.
	SessionIdleTimeout time.Duration
	// ShutdownTimeout is used when Shutdown receives a context without deadline.
	ShutdownTimeout time.Duration
	// ErrorObserver receives structured Router failures. Nil preserves the
	// compatibility default of logging handler errors and recovered panics and
	// additionally logs error-response delivery failures.
	ErrorObserver RouterErrorObserver
}

type route struct {
	handler           Handler
	invocationHandler InvocationHandler
	operation         string
	mode              DispatchMode
}

type dispatchTask struct {
	ctx       Context
	handler   InvocationHandler
	operation string
	session   *sessionExecutor
}

// Router maps message ids to handlers and owns dispatch scheduling.
type Router struct {
	mu            sync.RWMutex
	routes        map[uint32]route
	middlewares   []Middleware
	revision      uint64
	config        RouterConfig
	errorObserver RouterErrorObserver

	done      chan struct{}
	closeOnce sync.Once
	graceful  atomic.Bool
	wg        sync.WaitGroup
	drainOnce sync.Once
	drained   chan struct{}
	state     atomic.Uint32

	asyncQueue     chan dispatchTask
	asyncSlots     chan struct{}
	asyncStartOnce sync.Once
	asyncMu        sync.Mutex

	globalSlots    chan struct{}
	waiters        *waiterRegistry
	globalInflight atomic.Int64
	syncQueued     atomic.Int64
	asyncQueued    atomic.Int64
	accepted       atomic.Uint64
	completed      atomic.Uint64
	dropped        atomic.Uint64
	rejected       atomic.Uint64
	waited         atomic.Uint64

	sessionsCreateMu sync.Mutex
	sessions         sync.Map
}

// NewRouter creates a Router with normalized defaults.
func NewRouter(config ...RouterConfig) *Router {
	cfg := normalizeRouterConfig(firstRouterConfig(config))
	errorObserver := cfg.ErrorObserver
	if errorObserver == nil {
		errorObserver = logRouterError
	}
	r := &Router{
		routes:        make(map[uint32]route),
		config:        cfg,
		errorObserver: errorObserver,
		done:          make(chan struct{}),
		drained:       make(chan struct{}),
		asyncQueue:    make(chan dispatchTask, cfg.AsyncQueueSize),
		asyncSlots:    newCapacity(cfg.AsyncQueueSize),
		globalSlots:   newCapacity(cfg.MaxInflight),
		waiters:       newWaiterRegistry(cfg.MaxWaitingDispatches, cfg.MaxWaitingDispatchesPerSession),
	}
	return r
}

// Use appends middleware that wraps all future dispatches through the router.
func (r *Router) Use(middlewares ...Middleware) {
	if len(middlewares) == 0 {
		return
	}
	r.mu.Lock()
	r.middlewares = append(r.middlewares, middlewares...)
	r.revision++
	r.mu.Unlock()
	r.rebuildMessageInvocationHandlers()
}

// Handle registers a sync handler for msgID.
func (r *Router) Handle(msgID uint32, h Handler) {
	if err := r.HandleChecked(msgID, h); err != nil {
		panic(err)
	}
}

// HandleSync registers a handler that is serialized per session.
func (r *Router) HandleSync(msgID uint32, h Handler) {
	if err := r.HandleSyncChecked(msgID, h); err != nil {
		panic(err)
	}
}

// HandleAsync registers a handler that is run by shared async workers.
func (r *Router) HandleAsync(msgID uint32, h Handler) {
	if err := r.HandleAsyncChecked(msgID, h); err != nil {
		panic(err)
	}
}

// HandleChecked registers a sync handler and reports invalid, duplicate, or late registration.
func (r *Router) HandleChecked(msgID uint32, h Handler) error {
	return r.HandleSyncChecked(msgID, h)
}

// HandleSyncChecked registers a serialized handler and reports invalid or late registration.
func (r *Router) HandleSyncChecked(msgID uint32, h Handler) error {
	return r.handle(msgID, h, DispatchSync)
}

// HandleAsyncChecked registers an async handler and reports invalid or late registration.
func (r *Router) HandleAsyncChecked(msgID uint32, h Handler) error {
	return r.handle(msgID, h, DispatchAsync)
}

// Dispatch submits a decoded message context using the configured
// Backpressure policy. With BackpressureWait, use DispatchContext when the
// caller needs to bound the wait; this compatibility method waits until the
// Router or Session closes.
func (r *Router) Dispatch(ctx Context) error {
	return r.DispatchContext(context.Background(), ctx)
}

// DispatchContext submits a decoded message context. Reject mode returns
// immediately when a global, per-session, or queue limit is reached. Wait mode
// waits for capacity until waitCtx, the Router, or the Session is canceled.
// The Router owns one ctx reference after this call and releases it on every
// success or failure path. Transport-managed contexts retain their caller
// reference until ReleaseTransportContext is called.
func (r *Router) DispatchContext(waitCtx context.Context, ctx Context) error {
	if waitCtx == nil {
		waitCtx = context.Background()
	}
	retainRouterContext(ctx)
	if err := r.ensureRunning(); err != nil {
		return r.rejectDispatch(ctx, err, false)
	}

	msg := ctx.Message()
	r.mu.RLock()
	rt := r.routes[msg.ID]
	r.mu.RUnlock()
	if rt.handler == nil {
		return r.rejectDispatch(ctx, ErrNoHandler, false)
	}

	sourceSession := ctx.Session()
	sessionDone := SessionDone(sourceSession)
	waiter := newDispatchWaiter(
		r.waiters,
		sessionExecutorKey{id: sourceSession.ID(), done: sessionDone},
	)
	defer waiter.release()

	waited, err := r.acquireGlobal(waitCtx, sessionDone, &waiter)
	if err != nil {
		return r.rejectDispatch(ctx, err, waited)
	}

	session, sessionWaited, err := r.sessionForContext(waitCtx, sourceSession, &waiter)
	waited = waited || sessionWaited
	if err != nil {
		r.releaseGlobal()
		return r.rejectDispatch(ctx, err, waited)
	}

	task := dispatchTask{
		ctx:       ctx,
		handler:   rt.invocationHandler,
		operation: rt.operation,
		session:   session,
	}
	var queueWaited bool
	switch rt.mode {
	case DispatchAsync:
		queueWaited, err = r.submitAsync(waitCtx, task, &waiter)
	default:
		queueWaited, err = task.session.enqueueReserved(waitCtx, task, r.config.Backpressure, &waiter)
	}
	waited = waited || queueWaited
	if err != nil {
		task.session.release()
		r.releaseGlobal()
		if r.closed() && (err == errSessionExecutorClosed || errors.Is(err, ErrClosed)) {
			err = ErrRouterClosed
		} else if err == errSessionExecutorClosed {
			err = ErrClosed
		}
		return r.rejectDispatch(ctx, err, waited)
	}

	if waited {
		r.waited.Add(1)
	}
	r.accepted.Add(1)
	return nil
}

func (r *Router) rejectDispatch(ctx Context, err error, waited bool) error {
	if waited {
		r.waited.Add(1)
	}
	r.rejected.Add(1)
	event := newRouterErrorEvent(RouterErrorStageResponse, ctx, err)
	responseErr := sendRouterErrorResponse(ctx, err)
	releaseContext(ctx)
	if responseErr != nil {
		event.ResponseErr = responseErr
		r.notifyError(event)
		return errors.Join(err, fmt.Errorf("send message error response: %w", responseErr))
	}
	return err
}

// Close stops admission and takes the immediate shutdown path. Sync work still
// queued when an executor observes closure is released instead of deliberately
// drained; a task already selected by an executor may still begin.
func (r *Router) Close() {
	r.close(false)
}

func (r *Router) close(graceful bool) {
	r.closeOnce.Do(func() {
		r.graceful.Store(graceful)
		// Publish the selected close mode before Draining becomes observable.
		// Session.Done may race shutdown and must never mistake graceful drain
		// for the immediate Close path.
		r.beginDraining()
		close(r.done)
		r.asyncMu.Lock()
		close(r.asyncQueue)
		r.asyncMu.Unlock()

		r.sessionsCreateMu.Lock()
		sessions := make([]*sessionExecutor, 0)
		r.sessions.Range(func(_, value any) bool {
			sessions = append(sessions, value.(*sessionExecutor))
			return true
		})
		r.sessionsCreateMu.Unlock()

		for _, session := range sessions {
			if graceful {
				session.beginDrain()
			} else {
				session.close()
			}
		}
	})
	r.drainDone()
}

// Shutdown stops admission, drains accepted sync and async handlers, and waits
// for their completion or for ctx to expire. Close remains the best-effort
// immediate path and does not deliberately drain queued sync work.
func (r *Router) Shutdown(ctx context.Context) error {
	r.close(true)
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok && r.config.ShutdownTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.config.ShutdownTimeout)
		defer cancel()
	}

	select {
	case <-r.drainDone():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Router) handle(msgID uint32, h Handler, mode DispatchMode) error {
	if h == nil {
		return ErrNilHandler
	}
	if r.closed() {
		return ErrRouterClosed
	}
	for {
		r.mu.RLock()
		if _, exists := r.routes[msgID]; exists {
			r.mu.RUnlock()
			return fmt.Errorf("%w: message id %d", ErrHandlerAlreadyRegistered, msgID)
		}
		revision := r.revision
		middlewares := append([]Middleware(nil), r.middlewares...)
		r.mu.RUnlock()

		invocationHandler := buildMessageInvocationHandler(h, middlewares)

		r.mu.Lock()
		if revision != r.revision && !r.closed() {
			r.mu.Unlock()
			continue
		}
		if r.closed() {
			r.mu.Unlock()
			return ErrRouterClosed
		}
		if _, exists := r.routes[msgID]; exists {
			r.mu.Unlock()
			return fmt.Errorf("%w: message id %d", ErrHandlerAlreadyRegistered, msgID)
		}
		r.routes[msgID] = route{
			handler:           h,
			invocationHandler: invocationHandler,
			operation:         MessageOperation(msgID),
			mode:              mode,
		}
		r.revision++
		r.mu.Unlock()
		return nil
	}
}

func (r *Router) rebuildMessageInvocationHandlers() {
	for {
		r.mu.RLock()
		revision := r.revision
		middlewares := append([]Middleware(nil), r.middlewares...)
		routes := make(map[uint32]route, len(r.routes))
		for msgID, registered := range r.routes {
			routes[msgID] = registered
		}
		r.mu.RUnlock()

		compiled := make(map[uint32]InvocationHandler, len(routes))
		for msgID, registered := range routes {
			compiled[msgID] = buildMessageInvocationHandler(registered.handler, middlewares)
		}

		r.mu.Lock()
		if revision != r.revision {
			r.mu.Unlock()
			continue
		}
		for msgID, invocationHandler := range compiled {
			registered := r.routes[msgID]
			registered.invocationHandler = invocationHandler
			r.routes[msgID] = registered
		}
		r.mu.Unlock()
		return
	}
}

func buildMessageInvocationHandler(handler Handler, middlewares []Middleware) InvocationHandler {
	if handler == nil {
		return nil
	}
	invocationHandler := InvocationHandler(func(invocation Invocation) error {
		ctx, ok := invocation.Native().(Context)
		if !ok {
			return fmt.Errorf("message invocation native context has type %T", invocation.Native())
		}
		return handler(withMessageContext(ctx, invocation.Context()))
	})
	if len(middlewares) > 0 {
		invocationHandler = Chain(middlewares...)(invocationHandler)
	}
	return invocationHandler
}

func (r *Router) submitAsync(ctx context.Context, task dispatchTask, waiter *dispatchWaiter) (bool, error) {
	if !r.startAsyncWorkers() {
		return false, ErrRouterClosed
	}
	waited, err := acquireCapacity(
		ctx,
		r.asyncSlots,
		r.config.Backpressure,
		ErrDispatchQueueFull,
		r.done,
		SessionDone(task.ctx.Session()),
		nil,
		nil,
		waiter,
	)
	if err != nil {
		return waited, err
	}
	r.asyncMu.Lock()
	defer r.asyncMu.Unlock()
	if r.closed() {
		releaseCapacity(r.asyncSlots)
		return waited, ErrRouterClosed
	}
	if channelClosed(SessionDone(task.ctx.Session())) {
		releaseCapacity(r.asyncSlots)
		return waited, ErrClosed
	}
	r.asyncQueued.Add(1)
	select {
	case r.asyncQueue <- task:
		return waited, nil
	default:
		r.asyncQueued.Add(-1)
		releaseCapacity(r.asyncSlots)
		return waited, ErrDispatchQueueFull
	}
}

func (r *Router) drainDone() <-chan struct{} {
	r.drainOnce.Do(func() {
		go func() {
			r.wg.Wait()
			r.state.Store(uint32(StateStopped))
			close(r.drained)
		}()
	})
	return r.drained
}

// State returns the current observable Router lifecycle state.
func (r *Router) State() LifecycleState {
	return LifecycleState(r.state.Load())
}

// Snapshot returns lightweight admission, queue, and completion statistics.
func (r *Router) Snapshot() RouterStats {
	sessions := 0
	r.sessions.Range(func(_, _ any) bool {
		sessions++
		return true
	})
	waiting, globalWaitRejected, sessionWaitRejected := r.waiters.snapshot()
	return RouterStats{
		State:                          r.State(),
		Backpressure:                   r.config.Backpressure,
		Inflight:                       r.globalInflight.Load(),
		MaxInflight:                    r.config.MaxInflight,
		MaxInflightPerSession:          r.config.MaxInflightPerSession,
		WaitingDispatches:              waiting,
		MaxWaitingDispatches:           r.config.MaxWaitingDispatches,
		MaxWaitingDispatchesPerSession: r.config.MaxWaitingDispatchesPerSession,
		Sessions:                       sessions,
		SyncQueueDepth:                 r.syncQueued.Load(),
		SyncQueueCapacityPerSession:    r.config.SyncQueueSize,
		AsyncQueueDepth:                r.asyncQueued.Load(),
		AsyncQueueCapacity:             r.config.AsyncQueueSize,
		Accepted:                       r.accepted.Load(),
		Completed:                      r.completed.Load(),
		Dropped:                        r.dropped.Load(),
		Rejected:                       r.rejected.Load(),
		Waited:                         r.waited.Load(),
		GlobalWaitRejected:             globalWaitRejected,
		SessionWaitRejected:            sessionWaitRejected,
	}
}

func (r *Router) ensureRunning() error {
	for {
		switch state := r.State(); state {
		case StateCreated:
			if r.state.CompareAndSwap(uint32(StateCreated), uint32(StateStarting)) {
				return r.publishRunning()
			}
		case StateStarting:
			runtime.Gosched()
		case StateRunning:
			return nil
		case StateDraining, StateStopped:
			return ErrRouterClosed
		default:
			return ErrRouterClosed
		}
	}
}

func (r *Router) publishRunning() error {
	// Close may claim Starting and move the Router to Draining. A conditional
	// transition prevents startup from reviving a Router after draining began.
	if r.state.CompareAndSwap(uint32(StateStarting), uint32(StateRunning)) {
		return nil
	}
	return ErrRouterClosed
}

func (r *Router) beginDraining() {
	for {
		state := r.State()
		switch state {
		case StateCreated, StateStarting, StateRunning:
			if r.state.CompareAndSwap(uint32(state), uint32(StateDraining)) {
				return
			}
		case StateDraining, StateStopped:
			return
		default:
			return
		}
	}
}

func (r *Router) startAsyncWorkers() bool {
	r.asyncMu.Lock()
	defer r.asyncMu.Unlock()
	if r.closed() {
		return false
	}
	r.asyncStartOnce.Do(func() {
		for i := 0; i < r.config.AsyncWorkers; i++ {
			r.wg.Add(1)
			go r.asyncWorker()
		}
	})
	return true
}

func (r *Router) asyncWorker() {
	defer r.wg.Done()
	for task := range r.asyncQueue {
		r.asyncQueued.Add(-1)
		releaseCapacity(r.asyncSlots)
		r.execute(task)
	}
}

func (r *Router) execute(task dispatchTask) {
	// Completion is the final publication for an accepted task: observers that
	// see it must also see the context and all admission capacity released.
	defer r.completed.Add(1)
	defer releaseContext(task.ctx)
	defer r.releaseGlobal()
	defer task.session.release()
	defer func() {
		if recovered := recover(); recovered != nil {
			handlerErr := recoveredHandlerError(recovered)
			event := newRouterErrorEvent(RouterErrorStagePanic, task.ctx, handlerErr)
			event.Stack = debug.Stack()
			event.ResponseErr = sendRouterErrorResponse(task.ctx, handlerErr)
			r.notifyError(event)
		}
	}()
	inv := acquireMessageInvocation(context.Background(), task.ctx, task.operation)
	defer releaseMessageInvocation(inv)
	if err := task.handler(inv); err != nil {
		event := newRouterErrorEvent(RouterErrorStageHandler, task.ctx, err)
		event.ResponseErr = sendRouterErrorResponse(task.ctx, err)
		r.notifyError(event)
	}
}

func newRouterErrorEvent(stage RouterErrorStage, ctx Context, err error) RouterErrorEvent {
	event := RouterErrorEvent{
		Stage: stage,
		Err:   err,
	}
	if ctx == nil {
		return event
	}
	message := ctx.Message()
	event.Transport = ctx.Transport()
	event.ConnID = ctx.ConnID()
	event.RemoteAddr = ctx.RemoteAddr()
	event.MessageID = message.ID
	event.Sequence = message.Seq
	return event
}

func recoveredHandlerError(recovered any) error {
	if recoveredErr, ok := recovered.(error); ok {
		return fmt.Errorf("%w: %w", ErrRecovered, recoveredErr)
	}
	return fmt.Errorf("%w: %v", ErrRecovered, recovered)
}

func sendRouterErrorResponse(ctx Context, cause error) (responseErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if recoveredErr, ok := recovered.(error); ok {
				responseErr = fmt.Errorf("message error response panic: %w", recoveredErr)
				return
			}
			responseErr = fmt.Errorf("message error response panic: %v", recovered)
		}
	}()
	return ctx.Error(PublicErrorMessage(cause))
}

func (r *Router) notifyError(event RouterErrorEvent) {
	if r == nil || r.errorObserver == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	r.errorObserver(event)
}

func logRouterError(event RouterErrorEvent) {
	switch event.Stage {
	case RouterErrorStageHandler:
		log.Printf("message handler error: %v", event.Err)
	case RouterErrorStagePanic:
		log.Printf("message handler panic: %v\n%s", event.Err, event.Stack)
	}
	if event.ResponseErr != nil {
		log.Printf(
			"message error response failed: transport=%s conn=%d message=%d sequence=%d cause=%v: %v",
			event.Transport,
			event.ConnID,
			event.MessageID,
			event.Sequence,
			event.Err,
			event.ResponseErr,
		)
	}
}

func (r *Router) sessionFor(session Session) (*sessionExecutor, error) {
	key := sessionExecutorKey{id: session.ID(), done: SessionDone(session)}
	waiter := newDispatchWaiter(r.waiters, key)
	defer waiter.release()
	executor, _, err := r.sessionForContext(context.Background(), session, &waiter)
	return executor, err
}

func (r *Router) sessionForContext(
	ctx context.Context,
	session Session,
	waiter *dispatchWaiter,
) (*sessionExecutor, bool, error) {
	done := SessionDone(session)
	key := sessionExecutorKey{id: session.ID(), done: done}
	waited := false

	for {
		if channelClosed(done) {
			return nil, waited, ErrClosed
		}
		if value, ok := r.sessions.Load(key); ok {
			existing := value.(*sessionExecutor)
			acquireWaited, err := existing.acquireContext(ctx, r.config.Backpressure, waiter)
			waited = waited || acquireWaited
			if err == nil {
				return existing, waited, nil
			} else if err != errSessionExecutorClosed {
				return nil, waited, err
			}
			if r.closed() {
				return nil, waited, ErrRouterClosed
			}
			r.sessions.CompareAndDelete(key, existing)
			existing.close()
			continue
		}

		r.sessionsCreateMu.Lock()
		if value, ok := r.sessions.Load(key); ok {
			existing := value.(*sessionExecutor)
			// Capacity waits execute caller-controlled cancellation paths and may
			// block. Never hold the global creation lock while waiting on one hot
			// session, otherwise unrelated sessions cannot be created.
			r.sessionsCreateMu.Unlock()
			acquireWaited, err := existing.acquireContext(ctx, r.config.Backpressure, waiter)
			waited = waited || acquireWaited
			if err == nil {
				return existing, waited, nil
			} else if err != errSessionExecutorClosed {
				return nil, waited, err
			}
			if r.closed() {
				return nil, waited, ErrRouterClosed
			}
			r.sessions.CompareAndDelete(key, existing)
			existing.close()
			continue
		}
		if r.closed() {
			r.sessionsCreateMu.Unlock()
			return nil, waited, ErrRouterClosed
		}
		if channelClosed(done) {
			r.sessionsCreateMu.Unlock()
			return nil, waited, ErrClosed
		}

		executor := newSessionExecutor(r, key)
		acquireWaited, err := executor.acquireContext(ctx, r.config.Backpressure, waiter)
		waited = waited || acquireWaited
		if err != nil {
			r.sessionsCreateMu.Unlock()
			return nil, waited, err
		}
		r.sessions.Store(key, executor)
		r.wg.Add(1)
		go executor.run()
		r.sessionsCreateMu.Unlock()
		return executor, waited, nil
	}
}

func (r *Router) removeSession(session *sessionExecutor) {
	r.sessions.CompareAndDelete(session.key, session)
}

type sessionExecutorKey struct {
	id   uint64
	done <-chan struct{}
}

type sessionExecutor struct {
	router        *Router
	key           sessionExecutorKey
	done          <-chan struct{}
	queue         chan dispatchTask
	closedCh      chan struct{}
	inflightSlots chan struct{}
	queueSlots    chan struct{}

	mu       sync.Mutex
	closed   atomic.Bool
	inflight int
}

var errSessionExecutorClosed = errors.New("session executor closed")

func newSessionExecutor(router *Router, key sessionExecutorKey) *sessionExecutor {
	return &sessionExecutor{
		router:        router,
		key:           key,
		done:          key.done,
		queue:         make(chan dispatchTask, router.config.SyncQueueSize),
		closedCh:      make(chan struct{}),
		inflightSlots: newCapacity(router.config.MaxInflightPerSession),
		queueSlots:    newCapacity(router.config.SyncQueueSize),
	}
}

func (s *sessionExecutor) enqueueReserved(
	ctx context.Context,
	task dispatchTask,
	policy BackpressurePolicy,
	waiter *dispatchWaiter,
) (bool, error) {
	waited, err := acquireCapacity(
		ctx,
		s.queueSlots,
		policy,
		ErrDispatchQueueFull,
		s.router.done,
		s.done,
		s.closedCh,
		errSessionExecutorClosed,
		waiter,
	)
	if err != nil {
		return waited, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.router.closed() {
		releaseCapacity(s.queueSlots)
		return waited, ErrRouterClosed
	}
	if s.closed.Load() {
		releaseCapacity(s.queueSlots)
		return waited, errSessionExecutorClosed
	}
	if channelClosed(s.done) {
		releaseCapacity(s.queueSlots)
		return waited, ErrClosed
	}
	s.router.syncQueued.Add(1)
	select {
	case s.queue <- task:
		return waited, nil
	default:
		s.router.syncQueued.Add(-1)
		releaseCapacity(s.queueSlots)
		return waited, ErrDispatchQueueFull
	}
}

func (s *sessionExecutor) acquire() error {
	waiter := newDispatchWaiter(s.router.waiters, s.key)
	defer waiter.release()
	_, err := s.acquireContext(context.Background(), s.router.config.Backpressure, &waiter)
	return err
}

func (s *sessionExecutor) acquireContext(
	ctx context.Context,
	policy BackpressurePolicy,
	waiter *dispatchWaiter,
) (bool, error) {
	waited, err := acquireCapacity(
		ctx,
		s.inflightSlots,
		policy,
		ErrTooManyInflight,
		s.router.done,
		s.done,
		s.closedCh,
		errSessionExecutorClosed,
		waiter,
	)
	if err != nil {
		return waited, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.router.closed() {
		releaseCapacity(s.inflightSlots)
		return waited, ErrRouterClosed
	}
	if s.closed.Load() {
		releaseCapacity(s.inflightSlots)
		return waited, errSessionExecutorClosed
	}
	if channelClosed(s.done) {
		releaseCapacity(s.inflightSlots)
		return waited, ErrClosed
	}
	s.inflight++
	return waited, nil
}

func (s *sessionExecutor) release() {
	released := false
	s.mu.Lock()
	if s.inflight > 0 {
		s.inflight--
		released = true
	}
	s.mu.Unlock()
	if released {
		releaseCapacity(s.inflightSlots)
	}
}

func (s *sessionExecutor) isClosed() bool {
	return s.closed.Load()
}

func (s *sessionExecutor) run() {
	defer s.router.wg.Done()
	timer := time.NewTimer(s.router.config.SessionIdleTimeout)
	defer timer.Stop()

	for {
		select {
		case task := <-s.queue:
			s.router.syncQueued.Add(-1)
			releaseCapacity(s.queueSlots)
			resetTimer(timer, s.router.config.SessionIdleTimeout)
			s.router.execute(task)
		case <-s.done:
			s.stopOnSessionClose()
			return
		case <-s.router.done:
			if s.router.graceful.Load() {
				s.drainAccepted()
			} else {
				s.close()
			}
			return
		case <-timer.C:
			if s.closeIfIdle() {
				return
			}
			timer.Reset(s.router.config.SessionIdleTimeout)
		}
	}
}

func (s *sessionExecutor) stopOnSessionClose() {
	if s.router.graceful.Load() {
		s.drainAccepted()
		return
	}
	s.close()
}

func (s *sessionExecutor) beginDrain() {
	s.mu.Lock()
	s.markClosedLocked()
	s.mu.Unlock()
}

func (s *sessionExecutor) drainAccepted() {
	// Taking the session lock closes admission after every enqueue already in
	// its commit section. Once beginDrain returns there can be no late producer,
	// so observing an empty queue is a stable drain boundary.
	s.beginDrain()
	for {
		select {
		case task := <-s.queue:
			s.router.syncQueued.Add(-1)
			releaseCapacity(s.queueSlots)
			s.router.execute(task)
		default:
			s.router.removeSession(s)
			return
		}
	}
}

func (s *sessionExecutor) closeIfIdle() bool {
	s.mu.Lock()
	if s.closed.Load() || len(s.queue) > 0 || s.inflight > 0 {
		s.mu.Unlock()
		return false
	}
	s.markClosedLocked()
	s.mu.Unlock()
	s.router.removeSession(s)
	return true
}

func (s *sessionExecutor) close() {
	s.mu.Lock()
	s.markClosedLocked()
	drained := make([]dispatchTask, 0, len(s.queue))
	for {
		select {
		case task := <-s.queue:
			if s.inflight > 0 {
				s.inflight--
				releaseCapacity(s.inflightSlots)
			}
			s.router.syncQueued.Add(-1)
			releaseCapacity(s.queueSlots)
			drained = append(drained, task)
		default:
			s.mu.Unlock()
			s.router.removeSession(s)
			for _, task := range drained {
				s.router.releaseGlobal()
				s.router.dropped.Add(1)
				event := newRouterErrorEvent(RouterErrorStageResponse, task.ctx, ErrRouterClosed)
				event.ResponseErr = sendRouterErrorResponse(task.ctx, ErrRouterClosed)
				releaseContext(task.ctx)
				if event.ResponseErr != nil {
					s.router.notifyError(event)
				}
			}
			return
		}
	}
}

func (s *sessionExecutor) markClosedLocked() {
	if s.closed.CompareAndSwap(false, true) {
		close(s.closedCh)
	}
}

func (r *Router) closed() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

func (r *Router) acquireGlobal(
	ctx context.Context,
	sessionDone <-chan struct{},
	waiter *dispatchWaiter,
) (bool, error) {
	waited, err := acquireCapacity(
		ctx,
		r.globalSlots,
		r.config.Backpressure,
		ErrGlobalInflightLimit,
		r.done,
		sessionDone,
		nil,
		nil,
		waiter,
	)
	if err != nil {
		return waited, err
	}
	if r.closed() {
		releaseCapacity(r.globalSlots)
		return waited, ErrRouterClosed
	}
	if channelClosed(sessionDone) {
		releaseCapacity(r.globalSlots)
		return waited, ErrClosed
	}
	r.globalInflight.Add(1)
	return waited, nil
}

func (r *Router) releaseGlobal() {
	r.globalInflight.Add(-1)
	releaseCapacity(r.globalSlots)
}

func newCapacity(size int) chan struct{} {
	return make(chan struct{}, size)
}

type waiterRegistry struct {
	mu sync.Mutex

	maxGlobal     int
	maxPerSession int
	current       int
	perSession    map[sessionExecutorKey]int

	globalRejected  uint64
	sessionRejected uint64
}

func newWaiterRegistry(maxGlobal, maxPerSession int) *waiterRegistry {
	return &waiterRegistry{
		maxGlobal:     maxGlobal,
		maxPerSession: maxPerSession,
		perSession:    make(map[sessionExecutorKey]int),
	}
}

func (w *waiterRegistry) acquire(key sessionExecutorKey) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.perSession[key] >= w.maxPerSession {
		w.sessionRejected++
		return ErrSessionWaitingDispatchLimit
	}
	if w.current >= w.maxGlobal {
		w.globalRejected++
		return ErrGlobalWaitingDispatchLimit
	}
	w.current++
	w.perSession[key]++
	return nil
}

func (w *waiterRegistry) release(key sessionExecutorKey) {
	w.mu.Lock()
	defer w.mu.Unlock()
	sessionWaiters := w.perSession[key]
	if w.current <= 0 || sessionWaiters <= 0 {
		panic("core: waiting-dispatch capacity released without acquisition")
	}
	w.current--
	if sessionWaiters == 1 {
		delete(w.perSession, key)
	} else {
		w.perSession[key] = sessionWaiters - 1
	}
}

func (w *waiterRegistry) snapshot() (current int, globalRejected, sessionRejected uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.current, w.globalRejected, w.sessionRejected
}

type dispatchWaiter struct {
	registry *waiterRegistry
	key      sessionExecutorKey
	acquired bool
}

func newDispatchWaiter(registry *waiterRegistry, key sessionExecutorKey) dispatchWaiter {
	return dispatchWaiter{registry: registry, key: key}
}

func (w *dispatchWaiter) acquire() error {
	if w.acquired {
		return nil
	}
	if w.registry == nil {
		return ErrGlobalWaitingDispatchLimit
	}
	if err := w.registry.acquire(w.key); err != nil {
		return err
	}
	w.acquired = true
	return nil
}

func (w *dispatchWaiter) release() {
	if w == nil || !w.acquired {
		return
	}
	w.registry.release(w.key)
	w.acquired = false
}

func acquireCapacity(
	ctx context.Context,
	slots chan struct{},
	policy BackpressurePolicy,
	fullErr error,
	routerDone <-chan struct{},
	sessionDone <-chan struct{},
	ownerDone <-chan struct{},
	ownerErr error,
	waiter *dispatchWaiter,
) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if channelClosed(routerDone) {
		return false, ErrRouterClosed
	}
	if channelClosed(sessionDone) {
		return false, ErrClosed
	}
	if channelClosed(ownerDone) {
		return false, ownerErr
	}

	select {
	case slots <- struct{}{}:
		if err := ctx.Err(); err != nil {
			releaseCapacity(slots)
			return false, err
		}
		return false, nil
	default:
	}
	if policy != BackpressureWait {
		return false, fullErr
	}
	if waiter == nil {
		return false, fullErr
	}
	if err := waiter.acquire(); err != nil {
		return false, err
	}

	select {
	case slots <- struct{}{}:
		if err := ctx.Err(); err != nil {
			releaseCapacity(slots)
			return true, err
		}
		return true, nil
	case <-ctx.Done():
		return true, ctx.Err()
	case <-routerDone:
		return true, ErrRouterClosed
	case <-sessionDone:
		return true, ErrClosed
	case <-ownerDone:
		return true, ownerErr
	}
}

func releaseCapacity(slots <-chan struct{}) {
	select {
	case <-slots:
	default:
		panic("core: capacity released without matching acquisition")
	}
}

func channelClosed(done <-chan struct{}) bool {
	if done == nil {
		return false
	}
	select {
	case <-done:
		return true
	default:
		return false
	}
}

func resetTimer(timer *time.Timer, d time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(d)
}

func firstRouterConfig(config []RouterConfig) RouterConfig {
	if len(config) == 0 {
		return RouterConfig{}
	}
	if len(config) > 1 {
		panic("core.NewRouter accepts at most one RouterConfig")
	}
	return config[0]
}

func normalizeRouterConfig(config RouterConfig) RouterConfig {
	if config.SyncQueueSize <= 0 {
		config.SyncQueueSize = DefaultSyncQueueSize
	}
	if config.AsyncWorkers <= 0 {
		config.AsyncWorkers = runtime.GOMAXPROCS(0) * 4
		if config.AsyncWorkers < 4 {
			config.AsyncWorkers = 4
		}
	}
	if config.AsyncQueueSize <= 0 {
		config.AsyncQueueSize = DefaultAsyncQueueSize
	}
	if config.MaxInflightPerSession <= 0 {
		config.MaxInflightPerSession = DefaultMaxInflightPerSession
	}
	if config.MaxInflight <= 0 {
		config.MaxInflight = DefaultMaxInflight
	}
	if config.MaxWaitingDispatches <= 0 {
		config.MaxWaitingDispatches = min(DefaultMaxWaitingDispatches, config.MaxInflight)
	}
	if config.MaxWaitingDispatchesPerSession <= 0 {
		config.MaxWaitingDispatchesPerSession = min(
			DefaultMaxWaitingDispatchesPerSession,
			config.MaxInflightPerSession,
			config.MaxWaitingDispatches,
		)
	}
	if config.Backpressure != BackpressureWait {
		config.Backpressure = BackpressureReject
	}
	if config.SessionIdleTimeout <= 0 {
		config.SessionIdleTimeout = DefaultSessionIdleTimeout
	}
	if config.ShutdownTimeout <= 0 {
		config.ShutdownTimeout = DefaultRouterShutdownTimeout
	}
	return config
}
